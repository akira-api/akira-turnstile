package xvfb

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"projek/internal/config"
	"projek/internal/logger"
)

const (
	x11UnixDir     = "/tmp/.X11-unix"
	x11LockPattern = "/tmp/.X%d-lock"
	x11SockPattern = x11UnixDir + "/X%d"
	readyInterval  = 50 * time.Millisecond
	killTimeout    = time.Second
)

/**
 * Manager manages a single Xvfb virtual framebuffer instance.
 * Holds the process handle, active display string, and the display
 * that was set before launch so it can be restored on Stop.
 */
type Manager struct {
	Cmd     *exec.Cmd
	Display string

	prevDisplay string
	mu          sync.Mutex
	stopped     bool
}

/**
 * Start launches an Xvfb instance on the first available display number
 * starting from config.XvfbDisplayBase.
 * On non-Linux systems it returns a no-op Manager pointing at the existing $DISPLAY.
 * Returns an error if Xvfb is not in PATH, the socket directory cannot be created,
 * or no free display is found within config.XvfbDisplayAttempts tries.
 */
func Start(cfg config.Config, parent context.Context) (*Manager, error) {
	if runtime.GOOS != "linux" {
		display := os.Getenv("DISPLAY")
		logger.Debugf("startXvfb skipped: non-linux goos=%s display=%q", runtime.GOOS, display)
		return &Manager{Display: display}, nil
	}

	if _, err := exec.LookPath("Xvfb"); err != nil {
		return nil, fmt.Errorf("Xvfb not found in PATH: %w", err)
	}

	if err := ensureX11Dir(); err != nil {
		return nil, fmt.Errorf("ensure X11 socket dir: %w", err)
	}

	prevDisplay := os.Getenv("DISPLAY")
	base := config.XvfbDisplayBase

	startIdx := 0
	if n, ok := parseDisplayNum(prevDisplay); ok && n >= base {
		startIdx = (n - base) + 1
	}

	logger.Debugf("startXvfb searching display base=%d startIdx=%d previous_display=%q",
		base, startIdx, prevDisplay)

	for i := startIdx; i < config.XvfbDisplayAttempts; i++ {
		displayNum := base + i
		display := fmt.Sprintf(":%d", displayNum)

		busy, err := reclaimDisplay(displayNum)
		if err != nil {
			logger.Debugf("startXvfb reclaim error %s: %v", display, err)
			continue
		}
		if busy {
			logger.Debugf("startXvfb display busy: %s", display)
			continue
		}

		m, err := launchXvfb(parent, display, prevDisplay)
		if err != nil {
			logger.Debugf("startXvfb launch failed %s: %v", display, err)
			continue
		}

		return m, nil
	}

	return nil, fmt.Errorf("no free display found after %d attempts from :%d",
		config.XvfbDisplayAttempts, base)
}

/**
 * launchXvfb starts Xvfb on the given display string and waits until it is ready.
 * Stderr is captured via an OS pipe and filtered through filterXvfbStderr so that
 * benign non-root ownership warnings never reach the terminal.
 * $DISPLAY is only updated in the environment after waitReady confirms the process
 * is healthy, preventing callers from seeing a half-started display.
 */
func launchXvfb(parent context.Context, display, prevDisplay string) (*Manager, error) {
	displayNum, ok := parseDisplayNum(display)
	if !ok {
		return nil, fmt.Errorf("invalid display string %q", display)
	}

	cmd := exec.CommandContext(parent, "Xvfb", display,
		"-screen", "0", "1920x1080x24",
		"-ac",              // disable access control (safe in sandbox)
		"-nolisten", "tcp", // harden: no TCP port
	)
	cmd.Stdout = os.Stdout

	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("os.Pipe: %w", err)
	}
	cmd.Stderr = pw
	cmd.Env = envWithoutDisplay()

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return nil, fmt.Errorf("exec.Start: %w", err)
	}

	// Close the write-end in the parent; the child keeps its own copy.
	// filterXvfbStderr will see EOF once Xvfb exits.
	_ = pw.Close()
	go filterXvfbStderr(pr)

	if err := waitReady(cmd, displayNum, 3*time.Second); err != nil {
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
		return nil, fmt.Errorf("waitReady: %w", err)
	}

	logger.Debugf("startXvfb ready: display=%q pid=%d", display, cmd.Process.Pid)

	if err := os.Setenv("DISPLAY", display); err != nil {
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
		return nil, fmt.Errorf("setenv DISPLAY: %w", err)
	}

	return &Manager{
		Cmd:         cmd,
		Display:     display,
		prevDisplay: prevDisplay,
	}, nil
}

/**
 * Stop sends SIGTERM to the Xvfb process and waits up to 3 seconds for a clean exit.
 * If the process does not exit in time, SIGKILL is sent.
 * $DISPLAY is restored to its pre-launch value (or unset if there was none).
 * Safe to call multiple times; subsequent calls are no-ops.
 */
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

	if m.Cmd == nil || m.Cmd.Process == nil {
		return
	}

	pid := m.Cmd.Process.Pid
	logger.Debugf("stopping Xvfb: pid=%d display=%q", pid, m.Display)

	_ = m.Cmd.Process.Signal(syscall.SIGTERM)

	done := make(chan struct{})
	go func() {
		_, _ = m.Cmd.Process.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		logger.Debugf("Xvfb pid=%d did not exit after SIGTERM; sending SIGKILL", pid)
		_ = m.Cmd.Process.Signal(syscall.SIGKILL)
		<-done
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

/**
 * ensureX11Dir creates /tmp/.X11-unix with sticky-bit permissions (0o1777)
 * if it does not already exist.
 * Xvfb prints a warning and silently fails to create its socket when this
 * directory is missing and the process is running as non-root.
 */
func ensureX11Dir() error {
	if err := os.MkdirAll(x11UnixDir, 0o1777); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

/**
 * reclaimDisplay inspects the lock file and socket for displayNum and determines
 * whether the display is free for use.
 *
 * If a lock file exists but the recorded PID is no longer alive, the stale lock
 * and socket are removed so the display can be reused.
 * If an orphaned socket exists without a lock file, the socket is removed.
 *
 * Returns (true, nil)  — display is actively in use, skip it.
 * Returns (false, nil) — display is free.
 * Returns (true, err)  — unrecoverable error, skip and log.
 */
func reclaimDisplay(displayNum int) (busy bool, _ error) {
	lockPath := fmt.Sprintf(x11LockPattern, displayNum)
	sockPath := fmt.Sprintf(x11SockPattern, displayNum)

	pid, err := lockPID(lockPath)
	switch {
	case err == nil && pid > 0:
		if processAlive(pid) {
			return true, nil
		}
		logger.Debugf("reclaimDisplay: removing stale lock for display :%d (pid=%d)", displayNum, pid)
		if err := killAndWait(pid, 2*time.Second); err != nil {
			return true, fmt.Errorf("kill stale pid %d: %w", pid, err)
		}
		_ = os.Remove(lockPath)
		_ = os.Remove(sockPath)
		return false, nil

	case errors.Is(err, os.ErrNotExist):
		// No lock file — fall through to orphaned-socket check.

	default:
		if err != nil {
			return true, fmt.Errorf("read lock %s: %w", lockPath, err)
		}
	}

	if _, serr := os.Stat(sockPath); serr == nil {
		logger.Debugf("reclaimDisplay: removing orphaned socket %s", sockPath)
		_ = os.Remove(sockPath)
	} else if !errors.Is(serr, os.ErrNotExist) {
		return true, fmt.Errorf("stat socket %s: %w", sockPath, serr)
	}

	return false, nil
}

/**
 * waitReady polls the filesystem until both the UNIX socket and the lock file
 * for displayNum exist, which indicates Xvfb has finished initialisation.
 * The process is liveness-checked on every iteration so a crash is detected
 * immediately rather than waiting for the full timeout.
 * An extra 100 ms sleep is added after the files appear to let Xvfb complete
 * any internal setup that follows file creation.
 */
func waitReady(cmd *exec.Cmd, displayNum int, timeout time.Duration) error {
	sockPath := fmt.Sprintf(x11SockPattern, displayNum)
	lockPath := fmt.Sprintf(x11LockPattern, displayNum)
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			return fmt.Errorf("Xvfb exited prematurely (code %d)", cmd.ProcessState.ExitCode())
		}
		if cmd.Process != nil {
			if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
				return fmt.Errorf("Xvfb process gone: %w", err)
			}
		}

		if fileExists(sockPath) && fileExists(lockPath) {
			time.Sleep(100 * time.Millisecond)
			return nil
		}

		time.Sleep(readyInterval)
	}

	return fmt.Errorf("Xvfb :%d not ready after %s (socket=%v lock=%v)",
		displayNum, timeout, fileExists(sockPath), fileExists(lockPath))
}

/**
 * lockPID reads the PID integer from an X11 lock file at lockPath.
 * Lock files normally contain just a decimal PID, optionally space-padded.
 * Falls back to extracting the first digit sequence if direct Atoi fails.
 * Returns os.ErrNotExist (wrapped) when the file does not exist.
 */
func lockPID(lockPath string) (int, error) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return 0, err
	}
	text := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(text)
	if err != nil {
		parts := strings.FieldsFunc(text, func(r rune) bool { return r < '0' || r > '9' })
		if len(parts) == 0 {
			return 0, fmt.Errorf("no pid found in %s", lockPath)
		}
		return strconv.Atoi(parts[0])
	}
	return pid, nil
}

/**
 * processAlive returns true if a process with the given pid exists and is
 * reachable by the current user (signal 0 probe).
 */
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

/**
 * killAndWait sends SIGTERM to pid and polls until the process exits or timeout
 * is reached, then escalates to SIGKILL and waits a further killTimeout.
 * Returns nil immediately if the process is already gone.
 */
func killAndWait(pid int, timeout time.Duration) error {
	if !processAlive(pid) {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	_ = p.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = p.Signal(syscall.SIGKILL)
	return waitForExit(pid, killTimeout)
}

/**
 * waitForExit polls processAlive until pid is gone or timeout expires.
 */
func waitForExit(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("pid %d did not exit within %s", pid, timeout)
}

/**
 * fileExists returns true if path exists and is stat-able.
 */
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

/**
 * parseDisplayNum extracts the display number from a string like ":99".
 * Returns (n, true) on success, (0, false) if the string is not a valid display.
 */
func parseDisplayNum(display string) (int, bool) {
	s := strings.TrimPrefix(display, ":")
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

/**
 * envWithoutDisplay returns a copy of the current process environment with any
 * DISPLAY=... entry removed, so that child processes start with a clean slate.
 */
func envWithoutDisplay() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "DISPLAY=") {
			out = append(out, e)
		}
	}
	return out
}

// ignoredStderr lists substrings of Xvfb stderr lines that are safe to suppress.
// These warnings appear when the process runs as non-root inside a container and
// /tmp/.X11-unix is not owned by root — they are harmless.
var ignoredStderr = []string{
	"_XSERVTransmkdir: Owner of /tmp/.X11-unix should be set to root",
	"_XSERVTransmkdir: ERROR: euid != 0",
}

/**
 * filterXvfbStderr reads Xvfb's stderr line-by-line via an OS pipe, silently
 * drops lines that match ignoredStderr, and writes the rest to os.Stderr.
 * Using an OS pipe (rather than an io.Writer shim) guarantees that all bytes
 * are intercepted at the kernel level before any reach the terminal — even
 * output written by Xvfb before Go's runtime has fully initialised the child.
 * Closes r when the pipe reaches EOF (i.e. when Xvfb exits).
 */
func filterXvfbStderr(r *os.File) {
	defer r.Close()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		suppress := false
		for _, substr := range ignoredStderr {
			if strings.Contains(line, substr) {
				suppress = true
				break
			}
		}
		if !suppress {
			_, _ = fmt.Fprintln(os.Stderr, line)
		}
	}
}