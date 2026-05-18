package logger

/**
 * Package logger provides colored, prefixed logging.
 * DEBUG accepts only true/false and controls debug output.
 * App logs use [level] [app], request logs use [level] [https].
 */

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

var active bool
var jakartaLocation *time.Location

const (
	colorReset   = "\x1b[0m"
	colorGray    = "\x1b[90m"
	colorCyan    = "\x1b[36m"
	colorBlue    = "\x1b[34m"
	colorGreen   = "\x1b[32m"
	colorMagenta = "\x1b[35m"
)

/** Init must be called once at startup before any logging. */
func Init() {
	log.SetFlags(0)
	jakartaLocation = time.FixedZone("Asia/Jakarta", 7*60*60)
	if loc, err := time.LoadLocation("Asia/Jakarta"); err == nil {
		jakartaLocation = loc
	}
	active = parseBoolStrict(os.Getenv("DEBUG"))
}

/** Debugf logs only when debug mode is active. */
func Debugf(format string, args ...any) {
	if active {
		logLine("debug", "app", colorMagenta, colorBlue, format, args...)
	}
}

/** Infof logs application messages. */
func Infof(format string, args ...any) {
	logLine("info", "app", colorGreen, colorBlue, format, args...)
}

/** HTTPSf logs HTTP access-style messages. Accepts a single message string. */
func HTTPSf(msg string) {
	logLine("info", "https", colorGreen, colorCyan, "%s", msg)
}

func logLine(level, component, levelColor, componentColor, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	timestamp := time.Now().In(jakartaLocation).Format("2006-01-02 15:04:05")
	log.Printf("%s%s%s %s[%s]%s %s[%s]%s %s", colorGray, timestamp, colorReset, levelColor, level, colorReset, componentColor, component, colorReset, message)
}

func parseBoolStrict(raw string) bool {
	v := strings.ToLower(strings.TrimSpace(raw))
	return v == "true"
}
