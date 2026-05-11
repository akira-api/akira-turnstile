package xvfb

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"projek/internal/config"
	"projek/internal/logger"
)

// Manager manages an Xvfb virtual framebuffer instance.
type Manager struct {
	Cmd         *exec.Cmd
	Display     string
	prevDisplay string
	mu          sync.Mutex
	stopped     bool
}

// Start launches an Xvfb instance on an available display.
// On non-Linux systems it returns a manager pointing at $DISPLAY.
func Start(cfg config.Config, parent context.Context) (*Manager, error) {
	if runtime.GOOS != "linux" {
		logger.Debugf("startXvfb skipped: non-linux goos=%s display=%q", runtime.GOOS, os.Getenv("DISPLAY"))
		return &Manager{Display: os.Getenv("DISPLAY")}, nil
	}
	if _, err := exec.LookPath("Xvfb"); err != nil {
		logger.Debugf("startXvfb failed: Xvfb not found in PATH")
		return nil, fmt.Errorf("Xvfb not found: %w", err)
	}
	prevDisplay := os.Getenv("DISPLAY")
	base := config.XvfbDisplayBase
	logger.Debugf("startXvfb searching display from base=%d previous_display=%q", base, prevDisplay)
	for i := range config.XvfbDisplayAttempts {
		displayNum := base + i
		display := fmt.Sprintf(":%d", displayNum)
		if displayBusy(displayNum) {
			logger.Debugf("startXvfb display busy: %s", display)
			continue
		}
		cmd := exec.CommandContext(parent, "Xvfb", display, "-screen", "0", "1920x1080x24", "-ac")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		logger.Debugf("startXvfb attempting launch: %s", display)
		if err := cmd.Start(); err != nil {
			logger.Debugf("startXvfb launch failed for %s: %v", display, err)
			continue
		}
		if err := waitReady(cmd, displayNum, 2*time.Second); err != nil {
			logger.Debugf("startXvfb wait ready failed for %s: %v", display, err)
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			continue
		}
		if err := os.Setenv("DISPLAY", display); err != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			return nil, fmt.Errorf("set DISPLAY: %w", err)
		}
		logger.Debugf("startXvfb launched successfully: display=%q pid=%d", display, cmd.Process.Pid)
		return &Manager{Cmd: cmd, Display: display, prevDisplay: prevDisplay}, nil
	}
	logger.Debugf("startXvfb exhausted display search from :%d", base)
	return nil, fmt.Errorf("start Xvfb: no free display found after %d attempts from :%d", config.XvfbDisplayAttempts, base)
}

func displayBusy(displayNum int) bool {
	lockPath := filepath.Join("/tmp", fmt.Sprintf(".X%d-lock", displayNum))
	if _, err := os.Stat(lockPath); err == nil {
		return true
	}
	socketPath := filepath.Join("/tmp/.X11-unix", fmt.Sprintf("X%d", displayNum))
	if _, err := os.Stat(socketPath); err == nil {
		return true
	}
	return false
}

func waitReady(cmd *exec.Cmd, displayNum int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	socketPath := filepath.Join("/tmp/.X11-unix", fmt.Sprintf("X%d", displayNum))
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			return nil
		}
		if cmd.Process == nil {
			return fmt.Errorf("Xvfb process missing")
		}
		if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
			return fmt.Errorf("Xvfb exited before ready: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("Xvfb did not become ready in time")
}

// Stop terminates the Xvfb process and restores the previous $DISPLAY.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return
	}
	m.stopped = true
	if m.prevDisplay == "" {
		_ = os.Unsetenv("DISPLAY")
	} else {
		_ = os.Setenv("DISPLAY", m.prevDisplay)
	}
	if m.Cmd != nil && m.Cmd.Process != nil {
		logger.Debugf("stopping Xvfb: pid=%d display=%q", m.Cmd.Process.Pid, m.Display)
		_ = m.Cmd.Process.Signal(syscall.SIGTERM)
		_, _ = m.Cmd.Process.Wait()
	}
}
