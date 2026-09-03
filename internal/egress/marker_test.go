package egress

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// reapedPID returns the pid of a process that has exited and been waited for,
// so the kernel no longer knows it.
func reapedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for helper process: %v", err)
	}
	return pid
}

// pidAnsweringEPERM returns pid 1 once it has confirmed that a signal-0 probe
// of it answers EPERM. Gating on the euid alone is not enough: inside a PID
// namespace pid 1 is the sandbox's own init, owned by the test user, so the
// probe succeeds and the test would pass without exercising the EPERM path.
func pidAnsweringEPERM(t *testing.T) int {
	t.Helper()
	if err := syscall.Kill(1, 0); !errors.Is(err, syscall.EPERM) {
		t.Skipf("pid 1 does not answer EPERM here (kill(1, 0) = %v): root, or own pid namespace", err)
	}
	return 1
}

// writeMarker creates a marker directory under root holding a ready file, the
// shape a launch leaves behind, and dates every mtime in it to at when at is
// set. The file is dated before the directory, because creating the file is
// what moves the directory's mtime.
func writeMarker(t *testing.T, root, name string, at time.Time) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	file := filepath.Join(dir, "applied")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	if !at.IsZero() {
		for _, p := range []string{file, dir} {
			if err := os.Chtimes(p, at, at); err != nil {
				t.Fatalf("chtimes %s: %v", p, err)
			}
		}
	}
	return dir
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Sets process environment, so it must not call t.Parallel().
func TestMarkerRoot(t *testing.T) {
	t.Run("honors XDG_STATE_HOME", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		if got, want := MarkerRoot("/home/other"), filepath.Join(state, "devsandbox", "egress"); got != want {
			t.Errorf("MarkerRoot() = %q, want %q", got, want)
		}
	})

	t.Run("falls back to ~/.local/state", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", "")
		home := t.TempDir()
		if got, want := MarkerRoot(home), filepath.Join(home, ".local", "state", "devsandbox", "egress"); got != want {
			t.Errorf("MarkerRoot() = %q, want %q", got, want)
		}
	})

	t.Run("does not create the directory", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", "")
		root := MarkerRoot(t.TempDir())
		if exists(root) {
			t.Errorf("MarkerRoot() created %s", root)
		}
	})
}

func TestNewMarkerDir(t *testing.T) {
	t.Run("creates a 0700 directory named by the pid under a root it creates", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "state", "devsandbox", "egress")

		dir, err := NewMarkerDir(root)
		if err != nil {
			t.Fatalf("NewMarkerDir: %v", err)
		}
		if want := filepath.Join(root, strconv.Itoa(os.Getpid())); dir != want {
			t.Errorf("NewMarkerDir() = %q, want %q", dir, want)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("marker mode = %o, want 0700", perm)
		}
		rootInfo, err := os.Stat(root)
		if err != nil {
			t.Fatalf("stat %s: %v", root, err)
		}
		if perm := rootInfo.Mode().Perm(); perm != 0o700 {
			t.Errorf("root mode = %o, want 0700", perm)
		}
	})

	t.Run("replaces a leftover under its own pid", func(t *testing.T) {
		root := t.TempDir()
		stale := writeMarker(t, root, strconv.Itoa(os.Getpid()), time.Time{})
		if !LockdownApplied(filepath.Join(stale, "applied")) {
			t.Fatal("fixture: the leftover ready file does not read as applied")
		}

		dir, err := NewMarkerDir(root)
		if err != nil {
			t.Fatalf("NewMarkerDir: %v", err)
		}
		if dir != stale {
			t.Errorf("NewMarkerDir() = %q, want the same path %q", dir, stale)
		}
		if LockdownApplied(filepath.Join(dir, "applied")) {
			t.Error("a ready file from a previous launch survived into the new marker; LockdownApplied would answer before the prologue ran")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		if len(entries) != 0 {
			t.Errorf("new marker holds %d entries, want an empty directory", len(entries))
		}
	})
}

func TestSweepMarkers(t *testing.T) {
	old := time.Now().Add(-markerStaleAge - time.Hour)
	tests := []struct {
		name        string
		marker      string
		at          time.Time
		wantRemoved bool
	}{
		{"pid-named marker whose pid is dead", strconv.Itoa(reapedPID(t)), time.Time{}, true},
		{"pid-named marker whose pid is alive (self)", strconv.Itoa(os.Getpid()), time.Time{}, false},
		{"pid-named marker whose pid is alive is kept regardless of age", strconv.Itoa(os.Getpid()), old, false},
		{"legacy marker older than the age bound", "lockdown-2853441234", old, true},
		{"legacy marker younger than the age bound", "lockdown-2853441234", time.Time{}, false},
		{"negative number", "-1", old, false},
		{"zero", "0", old, false},
		{"signed number", "+7", old, false},
		{"leading zero", "007", old, false},
		{"word", "notes", old, false},
		{"legacy prefix on a pid-shaped suffix is still legacy", "lockdown-" + strconv.Itoa(os.Getpid()), old, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dir := writeMarker(t, root, tt.marker, tt.at)

			n, err := SweepMarkers(root)
			if err != nil {
				t.Fatalf("SweepMarkers: %v", err)
			}
			if exists(dir) == tt.wantRemoved {
				t.Errorf("marker %q exists = %v, want removed = %v", tt.marker, exists(dir), tt.wantRemoved)
			}
			wantN := 0
			if tt.wantRemoved {
				wantN = 1
			}
			if n != wantN {
				t.Errorf("removed = %d, want %d", n, wantN)
			}
		})
	}
}

// A legacy marker's age is judged over everything inside it, not the directory
// alone: a directory's mtime moves only when an entry is added or removed, so
// a ready file written later than that must keep the marker.
func TestSweepMarkers_LegacyAgeCoversTheReadyFile(t *testing.T) {
	old := time.Now().Add(-markerStaleAge - time.Hour)
	root := t.TempDir()
	dir := writeMarker(t, root, "lockdown-1234", old)
	// Rewriting the existing file leaves the directory's mtime where it is.
	if err := os.WriteFile(filepath.Join(dir, "applied"), nil, 0o600); err != nil {
		t.Fatalf("rewrite ready file: %v", err)
	}

	n, err := SweepMarkers(root)
	if err != nil {
		t.Fatalf("SweepMarkers: %v", err)
	}
	if n != 0 || !exists(dir) {
		t.Errorf("removed = %d, exists = %v; a marker with a recent ready file must be kept", n, exists(dir))
	}
}

// A legacy marker whose subtree cannot be read has an unknown age, and unknown
// must not authorize a deletion: reclaiming a live launch's marker makes that
// launch's own exit 78 read as an aborted lockdown.
func TestSweepMarkers_UnknownAgeIsKept(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	old := time.Now().Add(-markerStaleAge - time.Hour)
	root := t.TempDir()
	dir := writeMarker(t, root, "lockdown-9876", old)
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	n, err := SweepMarkers(root)
	if err != nil {
		t.Fatalf("SweepMarkers: %v", err)
	}
	if n != 0 || !exists(dir) {
		t.Errorf("removed = %d, exists = %v; a marker of unknown age must be kept", n, exists(dir))
	}
}

// A pid-named marker whose pid answers EPERM - one recycled by another user's
// process - is not provably dead and is kept, with no age backstop: sweeping it
// under a live session makes that session's own exit 78 read as an aborted
// lockdown, and the leak it prevents is one empty directory.
func TestSweepMarkers_EPERMIsKept(t *testing.T) {
	old := time.Now().Add(-markerStaleAge - time.Hour)
	root := t.TempDir()
	dir := writeMarker(t, root, strconv.Itoa(pidAnsweringEPERM(t)), old)

	n, err := SweepMarkers(root)
	if err != nil {
		t.Fatalf("SweepMarkers: %v", err)
	}
	if n != 0 || !exists(dir) {
		t.Errorf("removed = %d, exists = %v; a marker whose pid answers EPERM must be kept", n, exists(dir))
	}
}

func TestSweepMarkers_MissingRoot(t *testing.T) {
	n, err := SweepMarkers(filepath.Join(t.TempDir(), "never", "created"))
	if err != nil {
		t.Fatalf("SweepMarkers on a missing root: %v", err)
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0", n)
	}
}

// Only directories are markers. A plain file under a stale-looking name is not
// something a launch wrote and is left alone.
func TestSweepMarkers_IgnoresFiles(t *testing.T) {
	old := time.Now().Add(-markerStaleAge - time.Hour)
	root := t.TempDir()
	for _, name := range []string{strconv.Itoa(reapedPID(t)), "lockdown-999"} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}

	n, err := SweepMarkers(root)
	if err != nil {
		t.Fatalf("SweepMarkers: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	if n != 0 || len(entries) != 2 {
		t.Errorf("removed = %d, %d entries left, want 0 removed and both files kept", n, len(entries))
	}
}

func TestSweepMarkers_CountsAndContinuesPastFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	old := time.Now().Add(-markerStaleAge - time.Hour)
	root := t.TempDir()
	first := writeMarker(t, root, strconv.Itoa(reapedPID(t)), time.Time{})
	stuck := writeMarker(t, root, "lockdown-stuck", old)
	last := writeMarker(t, root, strconv.Itoa(reapedPID(t)), time.Time{})
	// A directory that cannot be written keeps its ready file, so RemoveAll
	// fails on this one marker.
	if err := os.Chmod(stuck, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o700) })

	n, err := SweepMarkers(root)
	if err == nil {
		t.Fatal("SweepMarkers returned nil, want the failed removal reported")
	}
	if !strings.Contains(err.Error(), "lockdown-stuck") {
		t.Errorf("error %q does not name the marker that could not be removed", err)
	}
	if n != 2 {
		t.Errorf("removed = %d, want 2: the failure must not stop the sweep", n)
	}
	if exists(first) || exists(last) {
		t.Errorf("stale markers survived: first exists = %v, last exists = %v", exists(first), exists(last))
	}
	if !exists(stuck) {
		t.Error("fixture: the stuck marker was removed, so the failure was not exercised")
	}
}
