/**
 * Package limiter provides a GCRA (Generic Cell Rate Algorithm) rate limiter
 * suitable for HTTP middleware.
 */
package limiter

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

/**
 * GCRALimiter implements token-bucket-style rate limiting.
 */
type GCRALimiter struct {
	mu               sync.Mutex
	emissionInterval time.Duration
	burst            time.Duration
	retryAfter       time.Duration
	entries          map[string]time.Time
	nextCleanup      time.Time
}

/**
 * New creates a new GCRA rate limiter.
 * Example: New(10, 3*time.Second, 2*time.Second) allows 10 requests per 3 seconds.
 */
func New(limit int, period, retryAfter time.Duration) *GCRALimiter {
	return &GCRALimiter{
		emissionInterval: period / time.Duration(limit),
		burst:            period,
		retryAfter:       retryAfter,
		entries:          make(map[string]time.Time),
		nextCleanup:      time.Now().Add(period),
	}
}

/**
 * Middleware returns a Gin handler that enforces rate limits per caller.
 */
func (l *GCRALimiter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		key := path + "|" + callerIdentity(c.Request)
		now := time.Now()
		if !l.allow(key, now) {
			c.Header("Retry-After", strconv.Itoa(int(l.retryAfter.Seconds())))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limited"})
			return
		}
		c.Next()
	}
}

/** Allow checks if a request should be allowed. */
func (l *GCRALimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.After(l.nextCleanup) {
		l.cleanupExpiredLocked(now)
		l.nextCleanup = now.Add(l.burst)
	}
	tat := l.entries[key]
	if tat.IsZero() {
		l.entries[key] = now.Add(l.emissionInterval)
		return true
	}
	allowAt := tat.Add(-l.burst)
	if now.Before(allowAt) {
		return false
	}
	if now.After(tat) {
		l.entries[key] = now.Add(l.emissionInterval)
	} else {
		l.entries[key] = tat.Add(l.emissionInterval)
	}
	return true
}

/** cleanupExpiredLocked removes expired entries (should be called under lock). */
func (l *GCRALimiter) cleanupExpiredLocked(now time.Time) {
	for key, tat := range l.entries {
		if now.After(tat.Add(l.burst)) {
			delete(l.entries, key)
		}
	}
}

/** callerIdentity extracts the caller IP from request. */
func callerIdentity(r *http.Request) string {
	remoteAddr := strings.TrimSpace(r.RemoteAddr)
	if remoteAddr == "" {
		return "unknown"
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	if strings.TrimSpace(host) == "" {
		return remoteAddr
	}
	return host
}
