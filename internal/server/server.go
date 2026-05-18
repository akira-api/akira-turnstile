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
	"projek/internal/logger"
	"projek/internal/model"
	"projek/internal/validate"
)

/**
 * Listen starts the HTTP server with API endpoints.
 */
func Listen(parent context.Context, cfg config.Config, pool *browser.Pool) error {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	/** Health check endpoint. */
	r.GET("/api/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	/** Turnstile solver endpoint. */
	r.POST("/api/solve", func(c *gin.Context) {
		reqID := helpers.NextID("solve")
		logger.HTTPSf("method=POST path=/api/solve phase=in ip=%s req=%s", c.ClientIP(), reqID)

		var req model.SolveReq
		if err := c.ShouldBindJSON(&req); err != nil {
			logger.HTTPSf("method=POST path=/api/solve phase=out req=%s status=400 err=bind", reqID)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		normalizedURL, err := validate.URL(c.Request.Context(), req.URL, cfg.ProxyServer)
		if err != nil {
			logger.HTTPSf("method=POST path=/api/solve phase=out req=%s status=400 err=url", reqID)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		req.URL = normalizedURL

		res, err := pool.Submit(c.Request.Context(), req, cfg.SolveTimeout)
		if err != nil {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				status = http.StatusGatewayTimeout
			case errors.Is(err, context.Canceled):
				status = http.StatusRequestTimeout
			}
			logger.HTTPSf("method=POST path=/api/solve phase=out req=%s status=%d err=%v", reqID, status, err)
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}

		logger.HTTPSf("method=POST path=/api/solve phase=out req=%s status=200 solve_ms=%d", reqID, res.SolveMS)
		c.JSON(http.StatusOK, model.SolveResp{
			Token: res.Token, BootMS: res.BootMS, NavMS: res.NavMS,
			DetectMS: res.DetectMS, HitCount: res.HitCount,
			CFDelayMS: 0, SolveMS: res.SolveMS,
		})
	})

	/** Cloudflare UAM solver endpoint. */
	r.POST("/api/solve/uam", func(c *gin.Context) {
		reqID := helpers.NextID("uam")
		logger.HTTPSf("method=POST path=/api/solve/uam phase=in ip=%s req=%s", c.ClientIP(), reqID)

		var req model.SolveUAMReq
		if err := c.ShouldBindJSON(&req); err != nil {
			logger.HTTPSf("method=POST path=/api/solve/uam phase=out req=%s status=400 err=bind", reqID)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		normalizedURL, err := validate.URL(c.Request.Context(), req.URL, cfg.ProxyServer)
		if err != nil {
			logger.HTTPSf("method=POST path=/api/solve/uam phase=out req=%s status=400 err=url", reqID)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		res, err := pool.SubmitUAM(c.Request.Context(), normalizedURL, cfg.SolveTimeout)
		if err != nil {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				status = http.StatusGatewayTimeout
			case errors.Is(err, context.Canceled):
				status = http.StatusRequestTimeout
			}
			logger.HTTPSf("method=POST path=/api/solve/uam phase=out req=%s status=%d err=%v", reqID, status, err)
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}

		logger.HTTPSf("method=POST path=/api/solve/uam phase=out req=%s status=200 solve_ms=%d", reqID, res.SolveMS)
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
