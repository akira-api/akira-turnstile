/**
 * Package config loads and holds all application configuration
 * from environment variables and .env file.
 */
package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

/** Default values (overridable via environment). */
const (
	DefaultPort            = "4557"
	DefaultPoolSize        = 1
	DefaultTabsPerBrowser  = 1
	DefaultBrowserMaxAge   = 30 * time.Minute
	DefaultBrowserMaxSolve = int64(50)
	DefaultSolveTimeout    = 60 * time.Second
	XvfbDisplayBase        = 99
	XvfbDisplayAttempts    = 64
	PausedEventBuffer      = 128
)

/** Config holds all runtime configuration. */
type Config struct {
	Port              string
	PoolSize          int
	TabsPerBrowser    int
	BrowserMaxAge     time.Duration
	BrowserMaxSolves  int64
	SolveTimeout      time.Duration
	ProxyServer       string
	APIKey            string
	AllowNoSandbox    bool
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	ReadHeaderTimeout time.Duration
}

/** Load reads configuration from environment and computes dynamic values. */
func Load() Config {
	loadDotEnv(".env")

	poolSize, tabsPerBrowser := DetectConcurrency()
	allowNoSandbox := getenvBool("ALLOW_NO_SANDBOX", false)
	if os.Geteuid() == 0 {
		allowNoSandbox = true
	}

	return Config{
		Port:             getenvString("PORT", DefaultPort),
		PoolSize:         poolSize,
		TabsPerBrowser:   tabsPerBrowser,
		BrowserMaxAge:    time.Duration(getenvInt("BROWSER_MAX_AGE_MIN", int(DefaultBrowserMaxAge/time.Minute))) * time.Minute,
		BrowserMaxSolves: int64(getenvInt("BROWSER_MAX_SOLVES", int(DefaultBrowserMaxSolve))),
		SolveTimeout:     time.Duration(getenvInt("SOLVE_TIMEOUT_SEC", int(DefaultSolveTimeout/time.Second))) * time.Second,
		ProxyServer:      normalizeProxyServer(getenvString("PROXY_SERVER", "")),
		APIKey:           getenvString("API_KEY", ""),
		AllowNoSandbox:   allowNoSandbox,
		ReadTimeout:      15 * time.Second,
		WriteTimeout:     75 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

/** ProxyConfigured returns true if a proxy server has been set. */
func (c Config) ProxyConfigured() bool {
	return strings.TrimSpace(c.ProxyServer) != ""
}

/** normalizeProxyServer ensures proxy URL has a scheme. */
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

/** loadDotEnv reads environment variables from a .env file. */
func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		return
	}
	for _, raw := range strings.Split(string(data), "\n") {
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
	}
}

/** getenvString retrieves a string environment variable with fallback. */
func getenvString(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

/** getenvInt retrieves an integer environment variable with fallback. */
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

/** getenvBool retrieves a boolean environment variable with fallback. */
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

/** DetectConcurrency returns (poolSize, tabsPerBrowser) based on env or system resources. */
func DetectConcurrency() (int, int) {
	if poolRaw := strings.TrimSpace(os.Getenv("POOL_SIZE")); poolRaw != "" {
		poolSize := getenvInt("POOL_SIZE", DefaultPoolSize)
		tabsPerBrowser := getenvInt("TABS_PER_BROWSER", DefaultTabsPerBrowser)
		return clampInt(poolSize, 1, 16), clampInt(tabsPerBrowser, 1, 4)
	}
	return DefaultPoolSize, DefaultTabsPerBrowser
}

/** clampInt constrains v between lo and hi. */
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

