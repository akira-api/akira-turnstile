package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/gin-gonic/gin"

	"projek/internal/helpers"
	"projek/internal/logger"
	"projek/internal/model"
	"projek/internal/monitor"
)

type solveJob struct {
	ID       string
	Req      model.SolveReq
	Ctx      context.Context
	Reply    chan model.SolveResult
	Enqueued time.Time
}

type solveUAMJob struct {
	ID       string
	URL      string
	Ctx      context.Context
	Reply    chan model.SolveUAMResp
	Enqueued time.Time
}

func (w *worker) runSolve(job *solveJob, mon *monitor.Hub) model.SolveResult {
	mon.RecordActiveDelta(1)
	defer mon.RecordActiveDelta(-1)
	mon.Publish("turnstile_start", gin.H{"worker_id": w.id, "url": job.Req.URL, "request_id": job.ID})

	tab, err := newTab(w.instance.rootCtx)
	if err != nil {
		return model.SolveResult{Err: err}
	}
	defer tab.cancel()
	ctx := tab.ctx
	bootMS := tab.bootMS

	pausedCh := make(chan *fetch.EventRequestPaused, model.PausedBufSize)
	tokenCh := make(chan string, 1)
	errCh := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)

	listenCtx, listenCancel := context.WithCancel(ctx)
	defer listenCancel()

	chromedp.ListenTarget(listenCtx, func(ev any) {
		switch ev := ev.(type) {
		case *fetch.EventRequestPaused:
			if ev == nil {
				return
			}
			select {
			case <-done:
				return
			case <-listenCtx.Done():
				return
			case pausedCh <- ev:
			default:
			}
		case *cdpruntime.EventBindingCalled:
			if ev == nil || ev.Name != model.TurnstileBinding {
				return
			}
			payload := strings.TrimSpace(ev.Payload)
			if payload == "" {
				return
			}
			select {
			case tokenCh <- payload:
			default:
			}
		case *cdpruntime.EventConsoleAPICalled:
			if ev == nil {
				return
			}
			logger.Debugf("job %s worker %d console.%s: %s", job.ID, w.id, ev.Type.String(), helpers.SummarizeObjs(ev.Args))
		case *cdpruntime.EventExceptionThrown:
			if ev == nil {
				return
			}
			logger.Debugf("job %s worker %d runtime exception: %s", job.ID, w.id, helpers.SummarizeExc(ev.ExceptionDetails))
		}
	})

	fakeHTML := strings.ReplaceAll(model.FakePage, "<site-key>", job.Req.SiteKey)

	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(actx context.Context) error {
		ec, err := helpers.TargetExec(actx)
		if err != nil {
			return err
		}
		logger.Debugf("job %s worker %d enabling fetch interception", job.ID, w.id)
		go interceptLoop(ec, pausedCh, done, errCh, job.Req.URL, fakeHTML)
		patterns := []*fetch.RequestPattern{{URLPattern: "*", ResourceType: network.ResourceTypeDocument, RequestStage: fetch.RequestStageRequest}}
		return fetch.Enable().WithPatterns(patterns).Do(ec)
	})); err != nil {
		return model.SolveResult{Err: err}
	}

	execCtx, err := helpers.TargetExec(ctx)
	if err != nil {
		return model.SolveResult{Err: err}
	}

	solveStart := time.Now()
	if _, _, errText, _, err := page.Navigate(job.Req.URL).Do(execCtx); err != nil {
		return model.SolveResult{Err: err}
	} else if errText != "" {
		return model.SolveResult{Err: fmt.Errorf("page navigate: %s", errText)}
	}
	navMS := time.Since(solveStart).Milliseconds()

	var auditMu sync.Mutex
	var firstHitAt, lastHitAt time.Time
	var hitCount int

	go func() {
		ticker := time.NewTicker(800 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-job.Ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				if w.checkClick(ctx, job.ID) {
					auditMu.Lock()
					if firstHitAt.IsZero() {
						firstHitAt = time.Now()
					}
					lastHitAt = time.Now()
					hitCount++
					auditMu.Unlock()
				}
			}
		}
	}()

	token, err := waitToken(job.Ctx, tokenCh, errCh)
	_ = fetch.Disable().Do(execCtx)
	if err != nil {
		return model.SolveResult{Err: err}
	}
	w.instance.solveCount++

	auditMu.Lock()
	finalHits := hitCount
	finalFirst := firstHitAt
	finalLast := lastHitAt
	auditMu.Unlock()

	solveMS := time.Since(solveStart).Milliseconds()
	detectMS := int64(0)
	if !finalFirst.IsZero() {
		detectMS = finalFirst.Sub(solveStart).Milliseconds()
	}

	logger.Infof("job %s done solve_ms=%d nav_ms=%d hits=%d", job.ID, solveMS, navMS, finalHits)
	return model.SolveResult{
		Token: token, BootMS: bootMS, NavMS: navMS, DetectMS: detectMS,
		LastHitMS: finalLast.UnixMilli(), HitCount: finalHits, SolveMS: solveMS,
	}
}

func (w *worker) runSolveUAM(job *solveUAMJob, mon *monitor.Hub) (model.SolveUAMResp, error) {
	mon.RecordActiveDelta(1)
	defer mon.RecordActiveDelta(-1)
	mon.Publish("uam_start", gin.H{"worker_id": w.id, "url": job.URL, "request_id": job.ID})

	debugPort := 9222 + w.id
	sess, err := newRawSession(job.Ctx, debugPort)
	if err != nil {
		return model.SolveUAMResp{}, fmt.Errorf("raw session: %w", err)
	}
	defer sess.close()

	// Inject anti-detection scripts (no Runtime.enable needed)
	antiDetect := `(() => {
		try {
			const navProto = Object.getPrototypeOf(navigator);
			Object.defineProperty(navProto, 'webdriver', {
				get: () => undefined, set: () => {}, configurable: true,
			});
		} catch(e) {}
		try {
			if (window.chrome) {
				window.chrome.runtime = undefined;
				window.chrome.loadTimes = undefined;
				window.chrome.csi = undefined;
				window.chrome.app = undefined;
			}
		} catch(e) {}
	})()`
	if err := sess.Execute(job.Ctx, "Page.addScriptToEvaluateOnNewDocument",
		map[string]any{"source": antiDetect, "runImmediately": true}, nil); err != nil {
		return model.SolveUAMResp{}, fmt.Errorf("inject scripts: %w", err)
	}

	// Navigate to target
	solveStart := time.Now()
	logger.Infof("[UAM] job %s navigating to %q", job.ID, job.URL)
	if err := sess.Execute(job.Ctx, "Page.navigate",
		map[string]any{"url": job.URL}, nil); err != nil {
		return model.SolveUAMResp{}, fmt.Errorf("navigate: %w", err)
	}
	// Wait briefly for page to load
	time.Sleep(3 * time.Second)

	// Click loop + clearance poll
	ticker := time.NewTicker(800 * time.Millisecond)
	defer ticker.Stop()

	clearance := ""
	cookies := []model.CookieInfo{}

	for {
		select {
		case <-job.Ctx.Done():
			return model.SolveUAMResp{}, job.Ctx.Err()
		default:
		}

		// Check for cf_clearance cookie
		var cookieResult struct {
			Cookies []struct {
				Name   string `json:"name"`
				Value  string `json:"value"`
				Domain string `json:"domain"`
				Path   string `json:"path"`
			} `json:"cookies"`
		}
		if err := sess.Execute(job.Ctx, "Network.getCookies",
			map[string]any{"urls": []string{job.URL}}, &cookieResult); err == nil {
			for _, c := range cookieResult.Cookies {
				cookies = append(cookies, model.CookieInfo{
					Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
				})
				if c.Name == "cf_clearance" && c.Value != "" {
					clearance = c.Value
				}
			}
		}

		if clearance != "" {
			break
		}
		cookies = nil // reset if not found on this poll

		// Check for challenge checkbox via JS
		var evalResult struct {
			Result struct {
				Value any `json:"value"`
			} `json:"result"`
		}
		clickScript := `(() => {
			const el = document.querySelector('[name="cf-turnstile-response"]');
			if (el && el.parentElement) {
				const r = el.parentElement.getBoundingClientRect();
				return JSON.stringify({x:r.left+30, y:r.top+r.height/2, w:r.width, h:r.height});
			}
			const div = Array.from(document.querySelectorAll('div')).find(d => {
				const r = d.getBoundingClientRect();
				return r.width>290 && r.width<310 && !d.querySelector('*');
			});
			if (div) {
				const r = div.getBoundingClientRect();
				return JSON.stringify({x:r.left+15, y:r.top+r.height/2, w:r.width, h:r.height});
			}
			return null;
		})()`
		if err := sess.Execute(job.Ctx, "Runtime.evaluate",
			map[string]any{"expression": clickScript, "returnByValue": true}, &evalResult); err == nil {
			if evalResult.Result.Value != nil {
				if coords, ok := evalResult.Result.Value.(string); ok && coords != "" && coords != "null" {
					var coord struct{ X, Y, W, H float64 }
					if json.Unmarshal([]byte(coords), &coord) == nil && coord.W > 0 {
						// Click with ghost cursor movement
						steps := 10
						for i := 0; i <= steps; i++ {
							t := float64(i) / float64(steps)
							ix := coord.X * t
							iy := coord.Y * t
							if i == 0 {
								ix = 50 + float64(i)*3
								iy = 50 + float64(i)*3
							}
							_ = sess.Execute(job.Ctx, "Input.dispatchMouseEvent",
								map[string]any{"type": "mouseMoved", "x": ix, "y": iy}, nil)
							time.Sleep(10 * time.Millisecond)
						}
						time.Sleep(50 * time.Millisecond)
						_ = sess.Execute(job.Ctx, "Input.dispatchMouseEvent",
							map[string]any{"type": "mousePressed", "x": coord.X, "y": coord.Y, "button": "left", "clickCount": 1}, nil)
						time.Sleep(100 * time.Millisecond)
						_ = sess.Execute(job.Ctx, "Input.dispatchMouseEvent",
							map[string]any{"type": "mouseReleased", "x": coord.X, "y": coord.Y, "button": "left", "clickCount": 1}, nil)
						// Cooldown after click
						select {
						case <-job.Ctx.Done():
							return model.SolveUAMResp{}, job.Ctx.Err()
						case <-time.After(3 * time.Second):
						}
					}
				}
			}
		}

		select {
		case <-job.Ctx.Done():
			return model.SolveUAMResp{}, job.Ctx.Err()
		case <-ticker.C:
		}
	}

	solveMS := time.Since(solveStart).Milliseconds()
	w.instance.solveCount++

	logger.Infof("[UAM] job %s success solve_ms=%d clearance_len=%d cookies=%d", job.ID, solveMS, len(clearance), len(cookies))
	mon.Publish("uam_success", gin.H{"worker_id": w.id, "solve_ms": solveMS, "request_id": job.ID})

	return model.SolveUAMResp{
		CFClearance: clearance,
		UserAgent:   "",
		Cookies:     cookies,
		BootMS:      0,
		SolveMS:     solveMS,
	}, nil
}

func waitClearance(ctx context.Context, execCtx context.Context, rawURL string, candidateCh <-chan struct{}, errCh <-chan error) (string, []model.CookieInfo, error) {
	for {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case err := <-errCh:
			return "", nil, err
		case <-candidateCh:
			cookies, err := network.GetCookies().WithURLs([]string{rawURL}).Do(execCtx)
			if err != nil {
				return "", nil, err
			}
			mapped := make([]model.CookieInfo, 0, len(cookies))
			clearance := ""
			for _, cookie := range cookies {
				if cookie == nil {
					continue
				}
				mapped = append(mapped, model.CookieInfo{
					Name: cookie.Name, Value: cookie.Value,
					Domain: cookie.Domain, Path: cookie.Path,
				})
				if cookie.Name == "cf_clearance" && cookie.Value != "" {
					clearance = cookie.Value
				}
			}
			if clearance != "" {
				return clearance, mapped, nil
			}
		}
	}
}

func waitToken(ctx context.Context, tokenCh <-chan string, errCh <-chan error) (string, error) {
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case err := <-errCh:
			return "", err
		case token := <-tokenCh:
			token = strings.TrimSpace(token)
			if len(token) >= 10 {
				return token, nil
			}
		}
	}
}

func interceptLoop(ctx context.Context, pausedCh <-chan *fetch.EventRequestPaused, done <-chan struct{}, errCh chan<- error, targetURL, fakeHTML string) {
	for {
		select {
		case <-done:
			return
		case paused := <-pausedCh:
			if paused == nil || paused.Request == nil {
				continue
			}
			if shouldFulfill(paused, targetURL) {
				logger.Debugf("interceptLoop fulfilling document request: %q", paused.Request.URL)
				err := fetch.FulfillRequest(paused.RequestID, 200).
					WithResponseHeaders([]*fetch.HeaderEntry{{Name: "Content-Type", Value: "text/html; charset=UTF-8"}}).
					WithBody(base64.StdEncoding.EncodeToString([]byte(fakeHTML))).
					Do(ctx)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
			} else {
				_ = fetch.ContinueRequest(paused.RequestID).Do(ctx)
			}
		}
	}
}

func shouldFulfill(paused *fetch.EventRequestPaused, target string) bool {
	if paused.ResourceType != network.ResourceTypeDocument {
		return false
	}
	return normURL(paused.Request.URL) == normURL(target)
}

func normURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Fragment = ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}
