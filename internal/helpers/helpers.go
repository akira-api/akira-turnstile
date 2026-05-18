package helpers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/cdp"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

var requestSeq atomic.Uint64

/** NextID generates a request ID with the given prefix. */
func NextID(prefix string) string {
	return fmt.Sprintf("%s-%06d", prefix, requestSeq.Add(1))
}

/** TargetExec extracts the executor from chromedp context. */
func TargetExec(ctx context.Context) (context.Context, error) {
	c := chromedp.FromContext(ctx)
	if c == nil || c.Target == nil {
		return nil, errors.New("chromedp target not initialized")
	}
	return cdp.WithExecutor(ctx, c.Target), nil
}

/** SleepCtx sleeps until duration or context cancellation. */
func SleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

/** Mask returns a masked representation of a string for logging. */
func Mask(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "<empty>"
	}
	if len(v) <= 8 {
		return fmt.Sprintf("len=%d:%s", len(v), v)
	}
	return fmt.Sprintf("len=%d:%s...%s", len(v), v[:4], v[len(v)-4:])
}

/** SummarizeObjs converts CDP remote objects to a readable string. */
func SummarizeObjs(args []*cdpruntime.RemoteObject) string {
	if len(args) == 0 {
		return "<no-args>"
	}
	parts := make([]string, 0, len(args))
	for _, a := range args {
		if a == nil {
			parts = append(parts, "<nil>")
			continue
		}
		s := strings.TrimSpace(a.Description)
		if s == "" && len(a.Value) > 0 {
			s = string(a.Value)
		}
		s = strings.Trim(s, `"`)
		if s == "" {
			s = fmt.Sprint(a.Type)
		}
		parts = append(parts, TruncS(s, 200))
	}
	return strings.Join(parts, " | ")
}

/** SummarizeExc converts CDP exception details to a readable string. */
func SummarizeExc(d *cdpruntime.ExceptionDetails) string {
	if d == nil {
		return "<nil>"
	}
	parts := make([]string, 0, 4)
	if d.Text != "" {
		parts = append(parts, TruncS(d.Text, 200))
	}
	if d.Exception != nil {
		if desc := strings.TrimSpace(d.Exception.Description); desc != "" {
			parts = append(parts, TruncS(desc, 200))
		}
	}
	if d.URL != "" {
		parts = append(parts, d.URL)
	}
	if d.LineNumber > 0 || d.ColumnNumber > 0 {
		parts = append(parts, fmt.Sprintf("line=%d col=%d", d.LineNumber, d.ColumnNumber))
	}
	if len(parts) == 0 {
		return "<empty>"
	}
	return strings.Join(parts, " | ")
}

/** TruncS truncates a string to maximum length with ellipsis. */
func TruncS(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

/** DetectMem reads system memory from /proc/meminfo (GiB). */
func DetectMem() int {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 4
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			break
		}
		kb, err := strconv.Atoi(f[1])
		if err != nil || kb <= 0 {
			break
		}
		if gb := kb / (1024 * 1024); gb >= 1 {
			return gb
		}
		return 1
	}
	return 4
}
