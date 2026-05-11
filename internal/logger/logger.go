// Package logger provides toggleable debug logging.
// When DEBUG not set: only Info-level messages appear (request in/out, errors).
// When DEBUG=1: all Debugf messages are also printed.
package logger

import (
	"log"
	"os"
	"strings"
)

var active bool

// Init must be called once at startup before any logging.
func Init() {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DEBUG")))
	active = v == "1" || v == "true" || v == "yes" || v == "on"
}

// Debugf logs only when debug mode is active.
func Debugf(format string, args ...any) {
	if active {
		log.Printf("[debug] "+format, args...)
	}
}

// Infof always logs. Use for request in/out, errors, and startup messages.
func Infof(format string, args ...any) {
	log.Printf(format, args...)
}

// Enabled returns true if debug mode is on.
func Enabled() bool {
	return active
}
