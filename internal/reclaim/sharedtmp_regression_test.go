package reclaim

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"devsandbox/internal/sandbox"
	"devsandbox/internal/sandbox/tools"
)

func TestOrphanSharedTmpPreservesPreviousBase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	oldRoot := filepath.Join(home, "old-base", "project")
	handle, err := sandbox.AcquireSession(oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := handle.Release(); err != nil {
			t.Error(err)
		}
	})
	oldHome := sandbox.SandboxHomePath(oldRoot)
	if err := os.MkdirAll(oldHome, 0o700); err != nil {
		t.Fatal(err)
	}
	tmp := tools.SharedTmpPath(home, oldHome)
	writeFile(t, filepath.Join(tmp, "scratch"), 1)
	backdate(t, tmp, time.Now().Add(-8*24*time.Hour))
	base := filepath.Join(home, "new-base")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	n, err := locationNamed(t, "orphaned shared temp").Run(Target{HomeDir: home, SandboxBase: base})
	if err != nil || n != 0 {
		t.Fatalf("sweep = (%d, %v), want no removal", n, err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "scratch")); err != nil {
		t.Fatalf("live session's temporary file removed: %v", err)
	}
	if !sandbox.IsSessionActive(oldRoot) {
		t.Fatal("session lock was not held during sweep")
	}
}
