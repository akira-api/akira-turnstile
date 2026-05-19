package xvfb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
		busy, err := reclaimDisplay(displayNum)
		if err != nil {
			logger.Debugf("startXvfb reclaim failed for %s: %v", display, err)
			continue
		}
		if busy {
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

func reclaimDisplay(displayNum int) (bool, error) {
	lockPath := filepath.Join("/tmp", fmt.Sprintf(".X%d-lock", displayNum))
	socketPath := filepath.Join("/tmp/.X11-unix", fmt.Sprintf("X%d", displayNum))

	pid, err := lockPID(lockPath)
	if err == nil && pid > 0 {
		if processAlive(pid) {
			logger.Debugf("startXvfb stopping stale Xvfb: display=:%d pid=%d", displayNum, pid)
			if err := signalProcess(pid, syscall.SIGTERM); err != nil {
				return true, err
			}
			if err := waitForProcessExit(pid, 2*time.Second); err != nil {
				_ = signalProcess(pid, syscall.SIGKILL)
				if err := waitForProcessExit(pid, time.Second); err != nil {
					return true, err
				}
			}
		}
		_ = os.Remove(lockPath)
		_ = os.Remove(socketPath)
		return false, nil
	}
	if _, err := os.Stat(lockPath); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return true, err
	}
	if _, err := os.Stat(socketPath); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return true, err
	}
	return false, nil
}

func lockPID(lockPath string) (int, error) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return 0, err
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return 0, fmt.Errorf("empty lock file")
	}
	parts := strings.FieldsFunc(text, func(r rune) bool {
		return r < '0' || r > '9'
	})
	if len(parts) == 0 {
		return 0, fmt.Errorf("no pid in lock file")
	}
	pid, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, err
	}
	return pid, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return signalProcess(pid, syscall.Signal(0)) == nil
}

func signalProcess(pid int, sig syscall.Signal) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(sig)
}

func waitForProcessExit(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("pid %d did not exit in time", pid)
}

func waitReady(cmd *exec.Cmd, displayNum int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	socketPath := filepath.Join("/tmp/.X11-unix", fmt.Sprintf("X%d", displayNum))
	lockPath := filepath.Join("/tmp", fmt.Sprintf(".X%d-lock", displayNum))
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			if _, err := os.Stat(lockPath); err == nil {
				time.Sleep(100 * time.Millisecond)
				return nil
			}
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
