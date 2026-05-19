package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"projek/internal/logger"
	"projek/internal/model"
)

type solveDirectJob struct {
	ID       string
	URL      string
	Ctx      context.Context
	Enqueued time.Time
}

func (w *worker) runSolveDirect(job *solveDirectJob) (model.SolveDirectResp, error) {

	debugPort := 9222 + w.id
	sess, err := newRawSession(job.Ctx, debugPort)
	if err != nil {
		return model.SolveDirectResp{}, fmt.Errorf("raw session: %w", err)
	}
	defer sess.close()

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
		return model.SolveDirectResp{}, fmt.Errorf("inject scripts: %w", err)
	}

	solveStart := time.Now()
	
	// Clear cookies to ensure fresh start for each job
	if err := sess.Execute(job.Ctx, "Network.clearBrowserCookies", nil, nil); err != nil {
		logger.Debugf("[DIRECT] job %s clearing cookies failed: %v", job.ID, err)
	}
	
	logger.Infof("[DIRECT] job %s navigating to %q", job.ID, job.URL)
	if err := sess.Execute(job.Ctx, "Page.navigate",
		map[string]any{"url": job.URL}, nil); err != nil {
		return model.SolveDirectResp{}, fmt.Errorf("navigate: %w", err)
	}
	time.Sleep(1 * time.Second)

	noCheckboxAfterClickCount := 0
	lastClickTime := time.Time{}
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	cookies := ""

	for {
		select {
		case <-job.Ctx.Done():
			logger.Infof("[DIRECT] job %s timeout, returning cookies", job.ID)
			solveMS := time.Since(solveStart).Milliseconds()
			w.instance.solveCount++
			return model.SolveDirectResp{Cookies: cookies, SolveMS: solveMS}, nil
		default:
		}

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
		checkboxFound := false
		if err := sess.Execute(job.Ctx, "Runtime.evaluate",
			map[string]any{"expression": clickScript, "returnByValue": true}, &evalResult); err == nil {
			if evalResult.Result.Value != nil {
				if coords, ok := evalResult.Result.Value.(string); ok && coords != "" && coords != "null" {
					var coord struct{ X, Y, W, H float64 }
					if json.Unmarshal([]byte(coords), &coord) == nil && coord.W > 0 {
						checkboxFound = true
						logger.Debugf("[DIRECT] job %s checkbox found, clicking", job.ID)
						lastClickTime = time.Now()
						noCheckboxAfterClickCount = 0

						steps := 10
						for i := 0; i <= steps; i++ {
							t := float64(i) / float64(steps)
							ix := coord.X * t
							iy := coord.Y * t
							if i == 0 {
								ix = 50 + float64(i)*3
								iy = 50 + float64(i)*3
							}
							_ = sess.Execute(job.Ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": ix, "y": iy}, nil)
							time.Sleep(10 * time.Millisecond)
						}
						time.Sleep(50 * time.Millisecond)
						_ = sess.Execute(job.Ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": coord.X, "y": coord.Y, "button": "left", "clickCount": 1}, nil)
						time.Sleep(100 * time.Millisecond)
						_ = sess.Execute(job.Ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": coord.X, "y": coord.Y, "button": "left", "clickCount": 1}, nil)

						select {
						case <-job.Ctx.Done():
							solveMS := time.Since(solveStart).Milliseconds()
							w.instance.solveCount++
							return model.SolveDirectResp{Cookies: cookies, SolveMS: solveMS}, nil
					case <-time.After(1 * time.Second):
						}
						noCheckboxAfterClickCount = 0
					}
				}
			}
		}

		var storageResult struct {
			Cookies []map[string]any `json:"cookies"`
		}
		if err := sess.Execute(job.Ctx, "Storage.getCookies", nil, &storageResult); err == nil {
			var parts []string
			for _, c := range storageResult.Cookies {
				if name, ok := c["name"].(string); ok {
					if value, ok := c["value"].(string); ok {
						parts = append(parts, name+"="+value)
					}
				}
			}
			if len(parts) > 0 {
				cookies = strings.Join(parts, "; ")
			}
		}

		if !checkboxFound {
			if !lastClickTime.IsZero() {
				noCheckboxAfterClickCount++
				if noCheckboxAfterClickCount >= 2 {
					logger.Infof("[DIRECT] job %s Turnstile auto-solved, exiting", job.ID)
					break
				}
			}
		} else {
			noCheckboxAfterClickCount = 0
		}

		select {
		case <-job.Ctx.Done():
			logger.Infof("[DIRECT] job %s timeout, returning cookies", job.ID)
			solveMS := time.Since(solveStart).Milliseconds()
			w.instance.solveCount++
			return model.SolveDirectResp{Cookies: cookies, SolveMS: solveMS}, nil
		case <-ticker.C:
		}
	}

	solveMS := time.Since(solveStart).Milliseconds()
	w.instance.solveCount++
	logger.Infof("[DIRECT] job %s success solve_ms=%d cookies_len=%d", job.ID, solveMS, len(cookies))

	return model.SolveDirectResp{Cookies: cookies, SolveMS: solveMS}, nil
}