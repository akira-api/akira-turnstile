package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"projek/internal/browser"
	"projek/internal/config"
	"projek/internal/logger"
	"projek/internal/server"
	"projek/internal/xvfb"
)

/**
 * Turnstile solver - solves Cloudflare challenges via Chrome DevTools Protocol
 */
func main() {
	logger.Init()

	if err := os.MkdirAll("debug", 0755); err != nil {
		logger.Debugf("warning: failed to create debug directory: %v", err)
	}

	cfg := config.Load()
	logger.Debugf("startup config: port=%s pool_size=%d tabs_per_browser=%d gcra_limit=%d gcra_period=%s gcra_retry_after=%s "+
		"browser_max_age=%s browser_max_solves=%d solve_timeout=%s proxy_configured=%t goos=%s gomaxprocs=%d existing_display=%q",
		cfg.Port, cfg.PoolSize, cfg.TabsPerBrowser, cfg.GCRALimit, cfg.GCRAPeriod, cfg.GCRARetryAfter,
		cfg.BrowserMaxAge, cfg.BrowserMaxSolves, cfg.SolveTimeout, cfg.ProxyConfigured(),
		runtime.GOOS, runtime.GOMAXPROCS(0), os.Getenv("DISPLAY"))

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	xv, err := xvfb.Start(cfg, rootCtx)
	if err != nil {
		log.Fatal(err)
	}
	defer xv.Stop()
	logger.Debugf("xvfb ready: display=%q", xv.Display)

	pool, err := browser.NewPool(rootCtx, cfg, xv.Display)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	logger.Debugf("browser pool initialized: workers=%d display=%q", pool.Workers(), pool.Display())

	if err := server.Listen(rootCtx, cfg, pool); err != nil {
		log.Fatal(err)
	}
}
