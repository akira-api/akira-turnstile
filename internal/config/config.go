// Package config loads and holds all application configuration
// from environment variables and .env file.
package config

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Default values (overridable via environment).
const (
	DefaultPort                  = "4557"
	DefaultPoolSize              = 2
	DefaultTabsPerBrowser        = 2
	DefaultGCRALimit             = 10
	DefaultBrowserMaxAge         = 30 * time.Minute
	DefaultBrowserMaxSolve       = int64(50)
	DefaultSolveTimeout          = 60 * time.Second
	DefaultGCRAPeriod            = 3 * time.Second
	DefaultGCRARetryAfter        = 2 * time.Second
	XvfbDisplayBase              = 99
	XvfbDisplayAttempts          = 64
	WsWriteTimeout               = 5 * time.Second
	WsReadTimeout                = 60 * time.Second
	WsPingInterval               = 25 * time.Second
	WsReadLimit            int64 = 1024
	PausedEventBuffer            = 128
)

// Config holds all runtime configuration.
type Config struct {
	Port              string
	PoolSize          int
	TabsPerBrowser    int
	GCRALimit         int
	GCRAPeriod        time.Duration
	GCRARetryAfter    time.Duration
	BrowserMaxAge     time.Duration
	BrowserMaxSolves  int64
	SolveTimeout      time.Duration
	ProxyServer       string
	AllowNoSandbox    bool
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	ReadHeaderTimeout time.Duration
}

// Load reads configuration from environment and computes dynamic values.
func Load() Config {
	loadDotEnv(".env")

	poolSize, tabsPerBrowser := DetectConcurrency()
	gcraLimit, gcraPeriod, gcraRetryAfter := DetectGCRA(poolSize, tabsPerBrowser)
	allowNoSandbox := getenvBool("ALLOW_NO_SANDBOX", false)
	if os.Geteuid() == 0 {
		allowNoSandbox = true
	}

	return Config{
		Port:              getenvString("PORT", DefaultPort),
		PoolSize:          poolSize,
		TabsPerBrowser:    tabsPerBrowser,
		GCRALimit:         gcraLimit,
		GCRAPeriod:        gcraPeriod,
		GCRARetryAfter:    gcraRetryAfter,
		BrowserMaxAge:     time.Duration(getenvInt("BROWSER_MAX_AGE_MIN", int(DefaultBrowserMaxAge/time.Minute))) * time.Minute,
		BrowserMaxSolves:  int64(getenvInt("BROWSER_MAX_SOLVES", int(DefaultBrowserMaxSolve))),
		SolveTimeout:      time.Duration(getenvInt("SOLVE_TIMEOUT_SEC", int(DefaultSolveTimeout/time.Second))) * time.Second,
		ProxyServer:       normalizeProxyServer(getenvString("PROXY_SERVER", "")),
		AllowNoSandbox:    allowNoSandbox,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      75 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// ProxyConfigured returns true if a proxy server has been set.
func (c Config) ProxyConfigured() bool {
	return strings.TrimSpace(c.ProxyServer) != ""
}

// ---------------------------------------------------------------------------
// .env file loader
// ---------------------------------------------------------------------------

func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		return // silently ignore other errors
	}
	for lineNo, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, value)
		_ = lineNo // unused but kept for potential error messages
	}
}

// ---------------------------------------------------------------------------
// Proxy normalization
// ---------------------------------------------------------------------------

func normalizeProxyServer(raw string) string {
	proxy := strings.TrimSpace(raw)
	if proxy == "" {
		return ""
	}
	if strings.Contains(proxy, "://") {
		return proxy
	}
	return "socks5://" + proxy
}

// ---------------------------------------------------------------------------
// Environment helpers
// ---------------------------------------------------------------------------

func getenvString(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func getenvBool(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

// ---------------------------------------------------------------------------
// Concurrency / resource detection
// ---------------------------------------------------------------------------

// DetectConcurrency returns (poolSize, tabsPerBrowser) based on env or system resources.
func DetectConcurrency() (int, int) {
	if poolRaw := strings.TrimSpace(os.Getenv("POOL_SIZE")); poolRaw != "" {
		poolSize := getenvInt("POOL_SIZE", DefaultPoolSize)
		tabsPerBrowser := getenvInt("TABS_PER_BROWSER", DefaultTabsPerBrowser)
		return clampInt(poolSize, 1, 16), clampInt(tabsPerBrowser, 1, 4)
	}

	cpus := runtime.GOMAXPROCS(0)
	memoryGB := detectMemoryGiB()

	poolByCPU := maxInt(1, cpus/2)
	poolByMem := maxInt(1, memoryGB/4)
	poolSize := clampInt(minInt(poolByCPU, poolByMem), 1, 16)

	if cpus >= 8 && memoryGB >= 12 {
		return 4, 2
	}
	if poolSize >= 4 {
		return poolSize, 2
	}
	if poolSize == 3 {
		return poolSize, 3
	}
	if poolSize == 2 {
		if cpus >= 6 && memoryGB >= 12 {
			return poolSize, 3
		}
		return poolSize, 2
	}
	if cpus >= 4 && memoryGB >= 8 {
		return 1, 2
	}
	return 1, 1
}

// DetectGCRA returns (limit, period, retryAfter) for rate limiting.
func DetectGCRA(poolSize, tabsPerBrowser int) (int, time.Duration, time.Duration) {
	period := DefaultGCRAPeriod
	retryAfter := DefaultGCRARetryAfter
	if raw := strings.TrimSpace(os.Getenv("GCRA_PERIOD_MS")); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
			period = time.Duration(ms) * time.Millisecond
		}
	}
	if raw := strings.TrimSpace(os.Getenv("GCRA_RETRY_AFTER_MS")); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
			retryAfter = time.Duration(ms) * time.Millisecond
		}
	}
	if raw := strings.TrimSpace(os.Getenv("GCRA_LIMIT")); raw != "" {
		limit := getenvInt("GCRA_LIMIT", DefaultGCRALimit)
		return clampInt(limit, 1, 1000), period, retryAfter
	}
	totalSlots := maxInt(1, poolSize*tabsPerBrowser)
	limit := minInt(DefaultGCRALimit, totalSlots)
	return clampInt(limit, 1, 1000), period, retryAfter
}

func detectMemoryGiB() int {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 4
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			break
		}
		kb, err := strconv.Atoi(fields[1])
		if err != nil || kb <= 0 {
			break
		}
		gib := kb / (1024 * 1024)
		if gib < 1 {
			return 1
		}
		return gib
	}
	return 4
}

// ---------------------------------------------------------------------------
// Integer helpers
// ---------------------------------------------------------------------------

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
