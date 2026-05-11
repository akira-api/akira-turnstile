package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"projek/internal/browser"
	"projek/internal/config"
	"projek/internal/helpers"
	"projek/internal/limiter"
	"projek/internal/logger"
	"projek/internal/model"
	"projek/internal/monitor"
	"projek/internal/validate"
)

func Listen(parent context.Context, cfg config.Config, pool *browser.Pool, mon *monitor.Hub) error {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	solveLim := limiter.New(cfg.GCRALimit, cfg.GCRAPeriod, cfg.GCRARetryAfter)
	wsLim := limiter.New(10, 30*time.Second, 5*time.Second)
	if cfg.GCRALimit > 10 {
		wsLim = limiter.New(cfg.GCRALimit, 30*time.Second, 5*time.Second)
	}

	r.GET("/", func(c *gin.Context) {
		c.File("/root/byp/projek/index.html")
	})
	r.GET("/ws", wsLim.Middleware(), mon.HandleWS)
	r.GET("/api/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.POST("/api/solve", solveLim.Middleware(), func(c *gin.Context) {
		reqID := helpers.NextID("solve")
		started := time.Now()
		logger.Infof("[IN] /solve ip=%s req=%s", c.ClientIP(), reqID)
		mon.Publish("request_received", gin.H{"path": "/solve", "ip": c.ClientIP(), "request_id": reqID})

		var req model.SolveReq
		if err := c.ShouldBindJSON(&req); err != nil {
			logger.Infof("[OUT] /solve req=%s status=400 err=bind", reqID)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		normalizedURL, err := validate.URL(c.Request.Context(), req.URL, cfg.ProxyServer)
		if err != nil {
			logger.Infof("[OUT] /solve req=%s status=400 err=url", reqID)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		req.URL = normalizedURL
		res, err := pool.Submit(c.Request.Context(), req, cfg.SolveTimeout, mon)
		if err != nil {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				status = http.StatusGatewayTimeout
			case errors.Is(err, context.Canceled):
				status = http.StatusRequestTimeout
			}
			logger.Infof("[OUT] /solve req=%s status=%d err=%v", reqID, status, err)
			mon.Publish("request_failed", gin.H{"path": "/solve", "error": err.Error(), "request_id": reqID})
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}

		cfDelay := int64(0)
		if res.LastHitMS > 0 {
			cfDelay = (started.Add(time.Duration(res.SolveMS) * time.Millisecond)).UnixMilli() - res.LastHitMS
			if cfDelay < 0 {
				cfDelay = 0
			}
		}

		logger.Infof("[OUT] /solve req=%s status=200 solve_ms=%d hits=%d", reqID, res.SolveMS, res.HitCount)
		mon.RecordSuccess(res.SolveMS, gin.H{"path": "/solve", "solve_ms": res.SolveMS, "boot_ms": res.BootMS, "nav_ms": res.NavMS, "detect_ms": res.DetectMS, "hits": res.HitCount, "cf_delay_ms": cfDelay, "request_id": reqID})
		c.JSON(http.StatusOK, model.SolveResp{
			Token: res.Token, BootMS: res.BootMS, NavMS: res.NavMS,
			DetectMS: res.DetectMS, HitCount: res.HitCount,
			CFDelayMS: cfDelay, SolveMS: res.SolveMS,
		})
	})

	r.POST("/api/solve/uam", solveLim.Middleware(), func(c *gin.Context) {
		reqID := helpers.NextID("uam")
		logger.Infof("[IN] /uam ip=%s req=%s", c.ClientIP(), reqID)
		mon.Publish("request_received", gin.H{"path": "/solve/uam", "ip": c.ClientIP(), "request_id": reqID})

		var req model.SolveUAMReq
		if err := c.ShouldBindJSON(&req); err != nil {
			logger.Infof("[OUT] /uam req=%s status=400 err=bind", reqID)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		normalizedURL, err := validate.URL(c.Request.Context(), req.URL, cfg.ProxyServer)
		if err != nil {
			logger.Infof("[OUT] /uam req=%s status=400 err=url", reqID)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		res, err := pool.SubmitUAM(c.Request.Context(), normalizedURL, cfg.SolveTimeout, mon)
		if err != nil {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				status = http.StatusGatewayTimeout
			case errors.Is(err, context.Canceled):
				status = http.StatusRequestTimeout
			}
			logger.Infof("[OUT] /uam req=%s status=%d err=%v", reqID, status, err)
			mon.Publish("request_failed", gin.H{"path": "/solve/uam", "error": err.Error(), "request_id": reqID})
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
		logger.Infof("[OUT] /uam req=%s status=200 solve_ms=%d cookies=%d", reqID, res.SolveMS, len(res.Cookies))
		mon.RecordSuccess(res.SolveMS, gin.H{"path": "/solve/uam", "solve_ms": res.SolveMS, "boot_ms": res.BootMS, "request_id": reqID})
		c.JSON(http.StatusOK, res)
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           r,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
	}

	shutdownErr := make(chan error, 1)
	go func() {
		<-parent.Done()
		sCtx, sCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer sCancel()
		shutdownErr <- srv.Shutdown(sCtx)
	}()

	logger.Infof("listening on :%s", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if parent.Err() != nil {
		if err := <-shutdownErr; err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("shutdown: %w", err)
		}
	}
	return nil
}
