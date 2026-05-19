package browser

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/chromedp"

	"projek/internal/config"
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
	return &browserInst{id: id, allocCtx: allocCtx, allocCancel: allocCancel, rootCtx: rootCtx, rootCancel: rootCancel, createdAt: time.Now()}, nil
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
