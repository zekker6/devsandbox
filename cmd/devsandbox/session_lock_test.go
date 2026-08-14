package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devsandbox/internal/notice"
	"devsandbox/internal/sandbox"
)

// captureNotices routes notice output into a buffer for the duration of a test.
func captureNotices(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	if err := notice.Setup("", false, &buf); err != nil {
		t.Fatalf("notice.Setup: %v", err)
	}
	t.Cleanup(func() { _ = notice.Setup("", false, io.Discard) })
	return &buf
}

// seedSandboxState creates a sandbox root holding a marker of persistent state.
func seedSandboxState(t *testing.T) (root, statePath string) {
	t.Helper()

	root = filepath.Join(t.TempDir(), "sandbox")
	statePath = filepath.Join(root, "home", "overlay", "home_user_.cache", "upper")
	if err := os.MkdirAll(statePath, 0o755); err != nil {
		t.Fatalf("seed sandbox state: %v", err)
	}
	return root, statePath
}

// TestRemoveSandboxOnExit_KeepsStateWhileAnotherSessionIsLive covers the --rm
// path: a concurrent session that started after this launch is still holding
// the sandbox at exit, and its overlay lower layers are the state --rm removes.
func TestRemoveSandboxOnExit_KeepsStateWhileAnotherSessionIsLive(t *testing.T) {
	buf := captureNotices(t)
	root, statePath := seedSandboxState(t)

	primary, err := sandbox.AcquireSession(root)
	if err != nil {
		t.Fatalf("AcquireSession failed: %v", err)
	}
	if !primary.IsPrimary() {
		t.Fatal("first launch is not primary")
	}

	concurrent, err := sandbox.AcquireSession(root)
	if err != nil {
		t.Fatalf("concurrent AcquireSession failed: %v", err)
	}
	defer func() { _ = concurrent.Release() }()

	removeSandboxOnExit(primary, root)

	if _, err := os.Stat(statePath); err != nil {
		t.Errorf("sandbox state removed while a session was live: %v", err)
	}
	if !strings.Contains(buf.String(), "--rm skipped") {
		t.Errorf("no notice explaining the skipped removal, got: %q", buf.String())
	}
}

func TestRemoveSandboxOnExit_RemovesStateWhenNoOtherSessionRemains(t *testing.T) {
	buf := captureNotices(t)
	root, _ := seedSandboxState(t)

	handle, err := sandbox.AcquireSession(root)
	if err != nil {
		t.Fatalf("AcquireSession failed: %v", err)
	}

	removeSandboxOnExit(handle, root)

	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("sandbox root still present after --rm: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected notice output on a clean removal: %q", buf.String())
	}
	// The launch's own deferred release runs after this and must not error.
	if err := handle.Release(); err != nil {
		t.Errorf("deferred release after removal failed: %v", err)
	}
}
