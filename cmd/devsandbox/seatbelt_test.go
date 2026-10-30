package main

import (
	"os"
	"path/filepath"
	"testing"

	"devsandbox/internal/sandbox"
)

func TestCleanupPrimaryOverlaysSkipsNativeHome(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	victim := filepath.Join(base, "host-data")
	if err := os.MkdirAll(filepath.Join(home, "overlay"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(victim, "important"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(victim, "important", "keep")
	if err := os.WriteFile(file, []byte("host data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(home, "overlay", "sessions")); err != nil {
		t.Fatal(err)
	}
	cleanupPrimaryOverlays(&sandbox.Config{Isolation: sandbox.IsolationSeatbelt, SandboxHome: home}, true)
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "host data" {
		t.Fatalf("native startup followed an overlay symlink: %q, %v", data, err)
	}
}

func TestCleanupPrimaryOverlaysRequiresDesignation(t *testing.T) {
	home := t.TempDir()
	session := filepath.Join(home, "overlay", "sessions", "live")
	if err := os.MkdirAll(session, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &sandbox.Config{Isolation: sandbox.IsolationDocker, SandboxHome: home}
	cleanupPrimaryOverlays(cfg, false)
	if _, err := os.Stat(session); err != nil {
		t.Fatalf("non-primary removed a session: %v", err)
	}
	cleanupPrimaryOverlays(cfg, true)
	if _, err := os.Stat(session); !os.IsNotExist(err) {
		t.Fatalf("primary did not remove stale session: %v", err)
	}
}
