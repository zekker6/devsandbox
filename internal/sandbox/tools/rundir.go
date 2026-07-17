package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
)

// runDirName is the per-sandbox root holding one socket directory per running
// devsandbox instance.
const runDirName = ".run"

// runDir returns the socket directory owned by this devsandbox process.
//
// Sandbox home is keyed on the project, so concurrent sessions for the same
// project share it. A socket at a fixed path there belongs to whichever session
// started last: the next session's start unlinks it and its exit deletes it,
// leaving live sessions pointing at a path that no longer exists. Keying the
// directory on the owning PID gives every instance a private path.
//
// Pass sandboxHome for the host-side path, or homeDir for the same directory as
// seen from inside the sandbox, where sandboxHome is mounted at homeDir.
func runDir(root string) string {
	return filepath.Join(root, runDirName, strconv.Itoa(os.Getpid()))
}

// ensureRunDir creates this process's socket directory under sandboxHome.
func ensureRunDir(sandboxHome string) (string, error) {
	dir := runDir(sandboxHome)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create run dir: %w", err)
	}
	return dir, nil
}

// maxUnixSocketPath returns the longest path a unix socket can bind to:
// sockaddr_un.sun_path holds 108 bytes on Linux and 104 on macOS, including the
// terminating NUL.
func maxUnixSocketPath() int {
	if runtime.GOOS == "darwin" {
		return 103
	}
	return 107
}

// checkSocketPath rejects a socket path the kernel cannot bind. Sandbox home is
// already long, so overshooting is reachable with a long project directory name;
// bind(2) reports only "invalid argument", which says nothing about the cause.
func checkSocketPath(path string) error {
	if max := maxUnixSocketPath(); len(path) > max {
		return fmt.Errorf("socket path %q is %d bytes, over this platform's %d-byte unix socket limit: use a shorter project directory name", path, len(path), max)
	}
	return nil
}

// cleanupStaleRunDirs removes socket directories left behind by devsandbox
// processes that are no longer running, and this process's own directory if a
// previous run reused the PID. It must run before any tool creates its sockets.
// Returns the number of directories removed.
func cleanupStaleRunDirs(sandboxHome string) (int, error) {
	root := filepath.Join(sandboxHome, runDirName)

	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read run dir: %w", err)
	}

	var removed int
	var errs []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not ours to reclaim
		}
		// Our own PID can only be a leftover from a previous run: this runs
		// before we create anything.
		if pid != os.Getpid() && processAlive(pid) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}

	return removed, errors.Join(errs...)
}

// processAlive reports whether a process with the given PID exists. A signal-0
// probe answers EPERM for a live process owned by another user, which counts as
// alive; anything else unexpected is also treated as alive so that an uncertain
// probe never deletes a directory in use.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH)
}
