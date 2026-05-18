package browser

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"projek/internal/config"
	"projek/internal/helpers"
	"projek/internal/logger"
	"projek/internal/model"
)

type browserInst struct {
	id          int
	allocCtx    context.Context
	allocCancel context.CancelFunc
	rootCtx     context.Context
	rootCancel  context.CancelFunc
	createdAt   time.Time
	solveCount  int64
}

func (b *browserInst) needsReplace(maxAge time.Duration, maxSolves int64) bool {
	if maxAge > 0 && time.Since(b.createdAt) > maxAge {
		return true
	}
	if maxSolves > 0 && b.solveCount >= maxSolves {
		return true
	}
	return false
}

func (b *browserInst) close() {
	b.rootCancel()
	b.allocCancel()
}

type solveSession struct {
	ctx    context.Context
	cancel context.CancelFunc
	bootMS int64
}

func newBrowserInst(parent context.Context, cfg config.Config, display string, id int) (*browserInst, error) {
	if os.Geteuid() == 0 && !cfg.AllowNoSandbox {
		return nil, errors.New("refusing to launch browser as root without sandbox; run as non-root or set ALLOW_NO_SANDBOX=true")
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(parent, browserOpts(display, cfg.ProxyServer, cfg.AllowNoSandbox, 9222+id)...)
	rootCtx, rootCancel := chromedp.NewContext(allocCtx)
	if err := chromedp.Run(rootCtx); err != nil {
		rootCancel()
		allocCancel()
		return nil, err
	}
	if err := initSession(rootCtx); err != nil {
		rootCancel()
		allocCancel()
		return nil, err
	}
	if err := prewarm(rootCtx); err != nil {
		logger.Debugf("worker %d prewarm failed: %v", id, err)
	}
	return &browserInst{id: id, allocCtx: allocCtx, allocCancel: allocCancel, rootCtx: rootCtx, rootCancel: rootCancel, createdAt: time.Now()}, nil
}

func newTab(parent context.Context) (*solveSession, error) {
	return newTabWithInit(parent, initSession)
}

func newTabUAM(parent context.Context) (*solveSession, error) {
	return newTabWithInit(parent, initSessionUAM)
}

func newTabWithInit(parent context.Context, initFn func(context.Context) error) (*solveSession, error) {
	start := time.Now()
	tp, tpc := context.WithCancel(parent)
	ctx, cc := chromedp.NewContext(tp)
	cancel := func() {
		cc()
		tpc()
	}
	if err := chromedp.Run(ctx); err != nil {
		cancel()
		return nil, err
	}
	if err := initFn(ctx); err != nil {
		cancel()
		return nil, err
	}
	return &solveSession{ctx: ctx, cancel: cancel, bootMS: time.Since(start).Milliseconds()}, nil
}

func browserOpts(display, proxyServer string, allowNoSandbox bool, debugPort int) []chromedp.ExecAllocatorOption {
	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("headless", false),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-client-side-phishing-detection", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("disable-hang-monitor", true),
		chromedp.Flag("disable-prompt-on-repost", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("remote-debugging-port", strconv.Itoa(debugPort)),
		chromedp.Flag("remote-debugging-address", "127.0.0.1"),
		chromedp.Flag("force-color-profile", "srgb"),
		chromedp.Flag("password-store", "basic"),
		chromedp.Flag("use-mock-keychain", true),
		chromedp.WindowSize(1365, 900),
		chromedp.Env("DISPLAY=" + display),
	}
	if allowNoSandbox {
		opts = append(opts, chromedp.Flag("no-sandbox", true))
	}
	if strings.TrimSpace(proxyServer) != "" {
		opts = append(opts,
			chromedp.ProxyServer(proxyServer),
			chromedp.Flag("proxy-bypass-list", "<-loopback>"),
		)
	}
	return opts
}

func initSessionUAM(ctx context.Context) error {
	execCtx, err := helpers.TargetExec(ctx)
	if err != nil {
		return err
	}
	blocked := []*network.BlockPattern{
		{URLPattern: "*://*.google-analytics.com/*", Block: true},
		{URLPattern: "*://*.googletagmanager.com/*", Block: true},
		{URLPattern: "*://*.doubleclick.net/*", Block: true},
		{URLPattern: "*://fonts.googleapis.com/*", Block: true},
		{URLPattern: "*://fonts.gstatic.com/*", Block: true},
	}
	if err := network.Enable().Do(execCtx); err != nil {
		return err
	}
	if err := network.SetBlockedURLs().WithURLPatterns(blocked).Do(execCtx); err != nil {
		return err
	}
	_, err = page.AddScriptToEvaluateOnNewDocument(`(() => {
		try {
			Object.defineProperty(MouseEvent.prototype, 'screenX', {
				get: function() { return this.clientX + (window.screenX || 0); }
			});
			Object.defineProperty(MouseEvent.prototype, 'screenY', {
				get: function() { return this.clientY + (window.screenY || 0); }
			});
		} catch(e) {}
		try {
			const navProto = Object.getPrototypeOf(navigator);
			Object.defineProperty(navProto, 'webdriver', {
				get: () => undefined,
				set: () => {},
				configurable: true,
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
	})()`).WithRunImmediately(true).Do(execCtx)
	return err
}

func initSession(ctx context.Context) error {
	execCtx, err := helpers.TargetExec(ctx)
	if err != nil {
		return err
	}
	blocked := []*network.BlockPattern{
		{URLPattern: "*://*.google-analytics.com/*", Block: true},
		{URLPattern: "*://*.googletagmanager.com/*", Block: true},
		{URLPattern: "*://*.doubleclick.net/*", Block: true},
		{URLPattern: "*://fonts.googleapis.com/*", Block: true},
		{URLPattern: "*://fonts.gstatic.com/*", Block: true},
	}
	if err := network.Enable().Do(execCtx); err != nil {
		return err
	}
	if err := cdpruntime.Enable().Do(execCtx); err != nil {
		return err
	}
	if err := page.SetLifecycleEventsEnabled(true).Do(execCtx); err != nil {
		return err
	}
	if err := network.SetBlockedURLs().WithURLPatterns(blocked).Do(execCtx); err != nil {
		return err
	}
	if err := cdpruntime.AddBinding(model.TurnstileBinding).Do(execCtx); err != nil {
		return err
	}
	// Inject mouse event coordinate patch (anti-detection)
	if _, err := page.AddScriptToEvaluateOnNewDocument(`(() => {
		Object.defineProperty(MouseEvent.prototype, 'screenX', {
			get: function() { return this.clientX + window.screenX; }
		});
		Object.defineProperty(MouseEvent.prototype, 'screenY', {
			get: function() { return this.clientY + window.screenY; }
		});
	})()`).WithRunImmediately(true).Do(execCtx); err != nil {
		return err
	}
	_, err = page.AddScriptToEvaluateOnNewDocument(`(() => {
		if (!window.` + model.TurnstileBinding + `) { window.` + model.TurnstileBinding + ` = function(t) {}; }
	})()`).WithRunImmediately(true).Do(execCtx)
	return err
}

func prewarm(ctx context.Context) error {
	html := `data:text/html;charset=utf-8,<!doctype html><html><body><script>window.onloadTurnstileCallback=function(){}</script><script src="https://challenges.cloudflare.com/turnstile/v0/api.js?onload=onloadTurnstileCallback"></script></body></html>`
	return chromedp.Run(ctx,
		chromedp.Navigate(html),
		chromedp.ActionFunc(func(actx context.Context) error {
			ec, err := helpers.TargetExec(actx)
			if err != nil {
				return err
			}
			_, _, _, _, err = page.Navigate("about:blank").Do(ec)
			return err
		}),
	)
}
