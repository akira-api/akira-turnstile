package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
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
	authMiddleware := newAPIKeyAuth(cfg.APIKey)

	/** Health check endpoint. */
	r.GET("/api/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	protected := r.Group("/", authMiddleware)

	/** Turnstile solver endpoint. */
	protected.POST("/api/solve", func(c *gin.Context) {
		startedAt := time.Now()
		reqID := helpers.NextID("solve")

		var req model.SolveReq
		if err := c.ShouldBindJSON(&req); err != nil {
			logHTTPSAccess(c.Request.Method, "/api/solve", reqID, c.ClientIP(), http.StatusBadRequest, startedAt, "error=bind")
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		normalizedURL, err := validate.URL(c.Request.Context(), req.URL, cfg.ProxyServer)
		if err != nil {
			logHTTPSAccess(c.Request.Method, "/api/solve", reqID, c.ClientIP(), http.StatusBadRequest, startedAt, "error=url")
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
			logHTTPSAccess(c.Request.Method, "/api/solve", reqID, c.ClientIP(), status, startedAt, "error="+err.Error())
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}

		logHTTPSAccess(c.Request.Method, "/api/solve", reqID, c.ClientIP(), http.StatusOK, startedAt, fmt.Sprintf("solve_ms=%d", res.SolveMS))
		c.JSON(http.StatusOK, model.SolveResp{
			Token: res.Token, BootMS: res.BootMS, NavMS: res.NavMS,
			DetectMS: res.DetectMS, HitCount: res.HitCount,
			CFDelayMS: 0, SolveMS: res.SolveMS,
		})
	})

	/** Cloudflare UAM solver endpoint. */
	protected.POST("/api/solve/uam", func(c *gin.Context) {
		startedAt := time.Now()
		reqID := helpers.NextID("uam")

		var req model.SolveUAMReq
		if err := c.ShouldBindJSON(&req); err != nil {
			logHTTPSAccess(c.Request.Method, "/api/solve/uam", reqID, c.ClientIP(), http.StatusBadRequest, startedAt, "error=bind")
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		normalizedURL, err := validate.URL(c.Request.Context(), req.URL, cfg.ProxyServer)
		if err != nil {
			logHTTPSAccess(c.Request.Method, "/api/solve/uam", reqID, c.ClientIP(), http.StatusBadRequest, startedAt, "error=url")
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
			logHTTPSAccess(c.Request.Method, "/api/solve/uam", reqID, c.ClientIP(), status, startedAt, "error="+err.Error())
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}

		logHTTPSAccess(c.Request.Method, "/api/solve/uam", reqID, c.ClientIP(), http.StatusOK, startedAt, fmt.Sprintf("solve_ms=%d", res.SolveMS))
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

func newAPIKeyAuth(expected string) gin.HandlerFunc {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return func(c *gin.Context) {
			c.Next()
		}
	}
	return func(c *gin.Context) {
		provided := strings.TrimSpace(c.GetHeader("apikey"))
		if provided == "" {
			provided = strings.TrimSpace(c.GetHeader("X-API-Key"))
		}
		if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}

func logHTTPSAccess(method, path, reqID, ip string, status int, startedAt time.Time, extra string) {
	fields := []string{
		"method=" + method,
		"path=" + path,
		"status=" + fmt.Sprintf("%d", status),
		"duration_ms=" + fmt.Sprintf("%d", time.Since(startedAt).Milliseconds()),
	}
	if reqID != "" {
		fields = append(fields, "req_id="+reqID)
	}
	if ip != "" {
		fields = append(fields, "ip="+ip)
	}
	if extra != "" {
		fields = append(fields, extra)
	}
	logger.HTTPSf(strings.Join(fields, " "))
}
