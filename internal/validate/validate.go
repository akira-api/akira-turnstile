/**
 * Package validate provides URL and IP address validation for solver requests.
 */
package validate

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

/**
 * URL validates and normalizes a target URL.
 * Checks for valid scheme, non-empty host, blocked IP ranges, and DNS resolution (if no proxy).
 */
func URL(ctx context.Context, raw, proxyServer string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("target url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("invalid target url")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("target url must use http or https")
	}
	if strings.TrimSpace(u.Host) == "" {
		return "", errors.New("target url host is required")
	}
	hostname := strings.ToLower(u.Hostname())
	if hostname == "" {
		return "", errors.New("target url host is required")
	}
	if blockedHost(hostname) {
		return "", fmt.Errorf("target host %q is not allowed", hostname)
	}
	if strings.TrimSpace(proxyServer) == "" {
		lc, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		addrs, err := net.DefaultResolver.LookupNetIP(lc, "ip", hostname)
		if err != nil {
			return "", fmt.Errorf("resolve target host %q: %w", hostname, err)
		}
		for _, addr := range addrs {
			if blockedIP(net.IP(addr.AsSlice())) {
				return "", fmt.Errorf("target host %q resolved to blocked address", hostname)
			}
		}
	}
	if u.Path == "" {
		u.Path = "/"
	}
	u.Fragment = ""
	return u.String(), nil
}

/** blockedHost checks if a hostname is in the blocked list. */
func blockedHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if host == "metadata.google.internal" || strings.HasSuffix(host, ".internal") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	if err == nil {
		return blockedIP(net.IP(addr.AsSlice()))
	}
	return false
}

/** blockedIP checks if an IP is in reserved or private ranges. */
func blockedIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	prefixes := []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("::/128"),
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("fc00::/7"),
		netip.MustParsePrefix("fe80::/10"),
	}
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return addr.IsLoopback() || addr.IsLinkLocalUnicast()
}
