package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZellij_Registered(t *testing.T) {
	tool := Get("zellij")
	if tool == nil {
		t.Fatal("expected zellij tool to be registered")
	}
	if tool.Name() != "zellij" {
		t.Errorf("expected name 'zellij', got %q", tool.Name())
	}
}

func TestZellij_Description(t *testing.T) {
	z := &Zellij{}
	if z.Description() == "" {
		t.Error("expected non-empty description")
	}
}

func TestZellij_Available_NoEnvVar(t *testing.T) {
	z := &Zellij{}
	t.Setenv("ZELLIJ", "")
	if z.Available("/home/user") {
		t.Error("expected Available=false when ZELLIJ is empty")
	}
}

func TestZellij_Available_NoBinary(t *testing.T) {
	z := &Zellij{}
	t.Setenv("ZELLIJ", "0")
	t.Setenv("PATH", t.TempDir())
	if z.Available("/home/user") {
		t.Error("expected Available=false when zellij binary is missing")
	}
}

func TestZellijSocketDirs(t *testing.T) {
	t.Run("ZELLIJ_SOCKET_DIR overrides everything", func(t *testing.T) {
		t.Setenv("ZELLIJ_SOCKET_DIR", "/custom/sock/dir")
		t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
		got := zellijSocketDirs()
		if len(got) != 1 || got[0] != "/custom/sock/dir" {
			t.Errorf("expected [/custom/sock/dir], got %v", got)
		}
	})

	t.Run("XDG_RUNTIME_DIR + /tmp fallback", func(t *testing.T) {
		t.Setenv("ZELLIJ_SOCKET_DIR", "")
		t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
		got := zellijSocketDirs()
		wantXDG := "/run/user/1000/zellij"
		wantTmp := fmt.Sprintf("/tmp/zellij-%d", os.Getuid())
		if len(got) != 2 || got[0] != wantXDG || got[1] != wantTmp {
			t.Errorf("expected [%q, %q], got %v", wantXDG, wantTmp, got)
		}
	})

	t.Run("no XDG_RUNTIME_DIR", func(t *testing.T) {
		t.Setenv("ZELLIJ_SOCKET_DIR", "")
		t.Setenv("XDG_RUNTIME_DIR", "")
		got := zellijSocketDirs()
		want := fmt.Sprintf("/tmp/zellij-%d", os.Getuid())
		if len(got) != 1 || got[0] != want {
			t.Errorf("expected [%q], got %v", want, got)
		}
	})
}

func TestZellij_Bindings(t *testing.T) {
	z := &Zellij{}

	// Create a fake socket directory.
	sockDir := t.TempDir()
	t.Setenv("ZELLIJ", "0")
	t.Setenv("ZELLIJ_SOCKET_DIR", sockDir)

	bindings := z.Bindings("/home/user", "/sandbox/home")

	if len(bindings) < 1 {
		t.Fatal("expected at least 1 binding (socket dir)")
	}

	sock := bindings[0]
	if sock.Source != sockDir {
		t.Errorf("expected socket dir source %q, got %q", sockDir, sock.Source)
	}
	if sock.Dest != sockDir {
		t.Errorf("expected socket dir dest %q, got %q", sockDir, sock.Dest)
	}
	if sock.ReadOnly {
		t.Error("expected socket dir binding to be read-write")
	}
	if sock.Category != CategoryRuntime {
		t.Errorf("expected CategoryRuntime, got %q", sock.Category)
	}
	if sock.Type != MountBind {
		t.Errorf("expected MountBind (sockets cannot use overlay), got %q", sock.Type)
	}
}

func TestZellij_Bindings_XDGAndTmp(t *testing.T) {
	z := &Zellij{}

	xdgRuntime := t.TempDir()
	xdgZellij := filepath.Join(xdgRuntime, "zellij")
	if err := os.Mkdir(xdgZellij, 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ZELLIJ", "0")
	t.Setenv("ZELLIJ_SOCKET_DIR", "")
	t.Setenv("XDG_RUNTIME_DIR", xdgRuntime)

	bindings := z.Bindings("/home/user", "/sandbox/home")

	var sawXDG bool
	for _, b := range bindings {
		if b.Source == xdgZellij {
			sawXDG = true
			if b.Type != MountBind {
				t.Errorf("XDG socket dir must be MountBind, got %q", b.Type)
			}
		}
	}
	if !sawXDG {
		t.Errorf("expected XDG zellij dir (%s) to be mounted, got bindings: %+v", xdgZellij, bindings)
	}
}

func TestZellij_Bindings_NoEnv(t *testing.T) {
	z := &Zellij{}
	t.Setenv("ZELLIJ", "")

	bindings := z.Bindings("/home/user", "/sandbox/home")
	if len(bindings) != 0 {
		t.Errorf("expected 0 bindings when ZELLIJ is empty, got %d", len(bindings))
	}
}

func TestZellij_Bindings_NoSocketDir(t *testing.T) {
	z := &Zellij{}
	t.Setenv("ZELLIJ", "0")
	t.Setenv("ZELLIJ_SOCKET_DIR", "/nonexistent/zellij/sock/dir")

	bindings := z.Bindings("/home/user", "/sandbox/home")
	// Should have no socket dir binding, but may still have binary binding.
	for _, b := range bindings {
		if b.Source == "/nonexistent/zellij/sock/dir" {
			t.Error("should not bind nonexistent socket directory")
		}
	}
}

func TestZellij_Environment(t *testing.T) {
	z := &Zellij{}
	env := z.Environment("/home/user", "/sandbox/home")

	expected := map[string]bool{
		"ZELLIJ":              false,
		"ZELLIJ_SESSION_NAME": false,
		"ZELLIJ_PANE_ID":      false,
	}

	for _, e := range env {
		if _, ok := expected[e.Name]; ok {
			if !e.FromHost {
				t.Errorf("expected %s to be FromHost=true", e.Name)
			}
			expected[e.Name] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("expected env var %s not found", name)
		}
	}
}

func TestZellij_Check_NoBinary(t *testing.T) {
	z := &Zellij{}
	t.Setenv("PATH", t.TempDir())

	result := z.Check("/home/user")
	if result.Available {
		t.Error("expected Available=false when binary not found")
	}
	if result.BinaryName != "zellij" {
		t.Errorf("expected BinaryName 'zellij', got %q", result.BinaryName)
	}
	if result.InstallHint == "" {
		t.Error("expected non-empty InstallHint")
	}
}

func TestZellij_Check_NoSession(t *testing.T) {
	z := &Zellij{}

	dir := t.TempDir()
	fake := filepath.Join(dir, "zellij")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("ZELLIJ", "")

	result := z.Check("/home/user")
	if result.Available {
		t.Error("expected Available=false when ZELLIJ is empty")
	}
	hasIssue := false
	for _, issue := range result.Issues {
		if strings.Contains(issue, "ZELLIJ not set") {
			hasIssue = true
		}
	}
	if !hasIssue {
		t.Error("expected issue mentioning ZELLIJ not set")
	}
}

func TestZellij_Check_NoSocketDir(t *testing.T) {
	z := &Zellij{}

	dir := t.TempDir()
	fake := filepath.Join(dir, "zellij")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("ZELLIJ", "0")
	t.Setenv("ZELLIJ_SOCKET_DIR", "/nonexistent-zellij-sock-12345")
	t.Setenv("XDG_RUNTIME_DIR", "")

	result := z.Check("/home/user")
	if result.Available {
		t.Error("expected Available=false when socket dir does not exist")
	}
	hasIssue := false
	for _, issue := range result.Issues {
		if strings.Contains(issue, "socket directory not found") {
			hasIssue = true
		}
	}
	if !hasIssue {
		t.Error("expected issue about missing socket directory")
	}
}

func TestZellij_Check_OK(t *testing.T) {
	z := &Zellij{}

	dir := t.TempDir()
	fake := filepath.Join(dir, "zellij")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	sockDir := t.TempDir()

	t.Setenv("PATH", dir)
	t.Setenv("ZELLIJ", "0")
	t.Setenv("ZELLIJ_SOCKET_DIR", sockDir)
	t.Setenv("ZELLIJ_SESSION_NAME", "test-session")

	result := z.Check("/home/user")
	if !result.Available {
		t.Errorf("expected Available=true, issues: %v", result.Issues)
	}

	hasSessionInfo := false
	for _, info := range result.Info {
		if strings.Contains(info, "test-session") {
			hasSessionInfo = true
		}
	}
	if !hasSessionInfo {
		t.Error("expected info about session name")
	}
}
