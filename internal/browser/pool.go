package browser

import (
	"context"
	"sync"
	"time"

	"projek/internal/config"
	"projek/internal/helpers"
	"projek/internal/logger"
	"projek/internal/model"
)

type Pool struct {
	ctx              context.Context
	cancel           context.CancelFunc
	available        chan *worker
	workers          []*worker
	poolSize         int
	display          string
	browserMaxAge    time.Duration
	browserMaxSolves int64
	cfg              config.Config
}

type worker struct {
	id               int
	display          string
	cfg              config.Config
	pool             *Pool
	browserMaxAge    time.Duration
	browserMaxSolves int64
	serviceCtx       context.Context
	serviceCancel    context.CancelFunc
	mu               sync.Mutex
	instance         *browserInst
	activeTabs       int
	advertisedSlots  int
	tabCap           int
	closing          bool
	draining         bool
	replacing        bool
	cursorX          float64
	cursorY          float64
	cursorSet        bool
}

func NewPool(parent context.Context, cfg config.Config, display string) (*Pool, error) {
	ctx, cancel := context.WithCancel(parent)
	totalSlots := cfg.PoolSize * cfg.TabsPerBrowser
	p := &Pool{
		ctx: ctx, cancel: cancel,
		available:        make(chan *worker, totalSlots),
		poolSize:         cfg.PoolSize,
		display:          display,
		browserMaxAge:    cfg.BrowserMaxAge,
		browserMaxSolves: cfg.BrowserMaxSolves,
		cfg:              cfg,
	}
	logger.Debugf("creating browser pool: pool_size=%d tabs_per_browser=%d total_slots=%d display=%q", cfg.PoolSize, cfg.TabsPerBrowser, totalSlots, display)
	for i := 0; i < cfg.PoolSize; i++ {
		wc, wcancel := context.WithCancel(ctx)
		w := &worker{
			id: i + 1, display: display, cfg: cfg, pool: p,
			browserMaxAge: cfg.BrowserMaxAge, browserMaxSolves: cfg.BrowserMaxSolves,
			serviceCtx: wc, serviceCancel: wcancel, tabCap: cfg.TabsPerBrowser,
		}
		inst, err := newBrowserInst(wc, cfg, display, w.id)
		if err != nil {
			logger.Debugf("creating worker %d browser instance failed: %v", w.id, err)
			cancel()
			return nil, err
		}
		w.instance = inst
		p.workers = append(p.workers, w)
		p.addSlots(w, w.tabCap)
	}
	return p, nil
}

func (p *Pool) Workers() int    { return len(p.workers) }
func (p *Pool) Display() string { return p.display }

func (p *Pool) addSlots(w *worker, n int) {
	for range n {
		w.noteQueued()
		select {
		case <-p.ctx.Done():
			w.noteTaken()
			return
		case p.available <- w:
		}
	}
}

func (p *Pool) Close() {
	logger.Debugf("closing browser pool")
	p.cancel()
	for _, w := range p.workers {
		w.shutdown()
	}
}

func (p *Pool) Submit(parent context.Context, req model.SolveReq, timeout time.Duration) (model.SolveResult, error) {
	jobCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	job := &solveJob{
		ID: helpers.NextID("job-solve"), Req: req, Ctx: jobCtx,
		Reply: make(chan model.SolveResult, 1), Enqueued: time.Now(),
	}
	logger.Debugf("job %s queued: type=turnstile url=%q timeout=%s", job.ID, req.URL, timeout)
	w, err := p.acqWorker(jobCtx)
	if err != nil {
		return model.SolveResult{}, err
	}
	defer p.relWorker(w)
	logger.Debugf("job %s acquired worker %d after %s", job.ID, w.id, time.Since(job.Enqueued))
	res := w.runSolve(job)
	return res, res.Err
}

func (p *Pool) SubmitUAM(parent context.Context, rawURL string, timeout time.Duration) (model.SolveUAMResp, error) {
	jobCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	job := &solveUAMJob{
		ID: helpers.NextID("job-uam"), URL: rawURL, Ctx: jobCtx,
		Reply: make(chan model.SolveUAMResp, 1), Enqueued: time.Now(),
	}
	logger.Debugf("job %s queued: type=uam url=%q timeout=%s", job.ID, rawURL, timeout)
	w, err := p.acqWorker(jobCtx)
	if err != nil {
		return model.SolveUAMResp{}, err
	}
	defer p.relWorker(w)
	logger.Debugf("job %s acquired worker %d after %s", job.ID, w.id, time.Since(job.Enqueued))
	res, err := w.runSolveUAM(job)
	return res, err
}

func (p *Pool) acqWorker(ctx context.Context) (*worker, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case w := <-p.available:
			w.noteTaken()
			if w.tryAcq() {
				return w, nil
			}
		}
	}
}

func (p *Pool) relWorker(w *worker) {
	w.mu.Lock()
	if w.activeTabs > 0 {
		w.activeTabs--
	}
	shouldReplace := w.instance != nil && w.instance.needsReplace(p.browserMaxAge, p.browserMaxSolves)
	if shouldReplace && !w.closing {
		w.draining = true
	}
	if shouldReplace && !w.replacing && !w.closing && w.activeTabs == 0 {
		old := w.instance
		w.replacing = true
		w.draining = true
		w.instance = nil
		logger.Debugf("worker %d scheduling browser replacement", w.id)
		go w.replaceInst(old)
	}
	free := !w.closing && !w.draining && w.instance != nil && w.activeTabs+w.advertisedSlots < w.tabCap
	w.mu.Unlock()
	if !free || !w.canReturn() {
		return
	}
	p.addSlots(w, 1)
}

func (w *worker) tryAcq() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closing || w.draining || w.replacing || w.instance == nil || w.activeTabs >= w.tabCap {
		return false
	}
	w.activeTabs++
	return true
}

func (w *worker) shutdown() {
	w.mu.Lock()
	if w.closing {
		w.mu.Unlock()
		return
	}
	w.closing = true
	at := w.activeTabs
	inst := w.instance
	if at == 0 {
		w.instance = nil
	}
	w.mu.Unlock()
	w.serviceCancel()
	if at == 0 && inst != nil {
		inst.close()
	}
}

func (w *worker) canReturn() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.closing && w.instance != nil
}

func (w *worker) noteQueued() {
	w.mu.Lock()
	w.advertisedSlots++
	w.mu.Unlock()
}

func (w *worker) noteTaken() {
	w.mu.Lock()
	if w.advertisedSlots > 0 {
		w.advertisedSlots--
	}
	w.mu.Unlock()
}

func (w *worker) replaceInst(old *browserInst) {
	closeOld := true
	defer func() {
		if closeOld {
			old.close()
		}
	}()
	logger.Debugf("worker %d replacing browser instance", w.id)
	w.mu.Lock()
	cfg := w.cfg
	disp := w.display
	srvCtx := w.serviceCtx
	w.mu.Unlock()
	inst, err := newBrowserInst(srvCtx, cfg, disp, w.id)
	if err != nil {
		logger.Infof("worker %d replacement failed: %v", w.id, err)
		w.mu.Lock()
		w.replacing = false
		closing := w.closing
		if !w.closing {
			w.draining = false
			w.instance = old
		}
		rem := maxInt(0, w.tabCap-w.activeTabs-w.advertisedSlots)
		w.mu.Unlock()
		if !closing && rem > 0 {
			w.pool.addSlots(w, rem)
		}
		if !closing {
			closeOld = false
		}
		return
	}
	w.mu.Lock()
	if w.closing {
		w.replacing = false
		w.mu.Unlock()
		inst.close()
		return
	}
	w.instance = inst
	w.replacing = false
	w.draining = false
	rem := maxInt(0, w.tabCap-w.activeTabs-w.advertisedSlots)
	w.mu.Unlock()
	if rem > 0 {
		w.pool.addSlots(w, rem)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
