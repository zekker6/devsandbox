package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKitty_Registered(t *testing.T) {
	tool := Get("kitty")
	if tool == nil {
		t.Fatal("expected kitty tool to be registered")
	}
	if tool.Name() != "kitty" {
		t.Errorf("expected name 'kitty', got %q", tool.Name())
	}
}

func TestKitty_Description(t *testing.T) {
	k := &Kitty{}
	if k.Description() == "" {
		t.Error("expected non-empty description")
	}
}

func TestKitty_Available_NoEnvVar(t *testing.T) {
	k := &Kitty{}
	t.Setenv("KITTY_LISTEN_ON", "")
	if k.Available("/home/user") {
		t.Error("expected Available=false when KITTY_LISTEN_ON is empty")
	}
}

func TestKitty_Available_NoBinary(t *testing.T) {
	k := &Kitty{}
	t.Setenv("KITTY_LISTEN_ON", "unix:/tmp/kitty-12345")
	t.Setenv("PATH", t.TempDir())
	if k.Available("/home/user") {
		t.Error("expected Available=false when kitty binary is missing")
	}
}

func TestKittySocketPath(t *testing.T) {
	tests := []struct {
		listenOn string
		want     string
	}{
		{"unix:/tmp/kitty-12345", "/tmp/kitty-12345"},
		{"/tmp/kitty-12345", "/tmp/kitty-12345"},
		{"unix:/run/user/1000/kitty-99", "/run/user/1000/kitty-99"},
		{"", ""},
	}
	for _, tt := range tests {
		got := kittySocketPath(tt.listenOn)
		if got != tt.want {
			t.Errorf("kittySocketPath(%q) = %q, want %q", tt.listenOn, got, tt.want)
		}
	}
}

func TestKitty_Bindings(t *testing.T) {
	k := &Kitty{}
	t.Setenv("KITTY_LISTEN_ON", "unix:/tmp/kitty-12345")

	bindings := k.Bindings("/home/user", "/sandbox/home")

	if len(bindings) < 1 {
		t.Fatal("expected at least 1 binding (socket)")
	}

	sock := bindings[0]
	if sock.Source != "/tmp/kitty-12345" {
		t.Errorf("expected socket source /tmp/kitty-12345, got %q", sock.Source)
	}
	if sock.Dest != "/tmp/kitty-12345" {
		t.Errorf("expected socket dest /tmp/kitty-12345, got %q", sock.Dest)
	}
	if sock.ReadOnly {
		t.Error("expected socket binding to be read-write")
	}
	if sock.Category != CategoryRuntime {
		t.Errorf("expected CategoryRuntime, got %q", sock.Category)
	}
	if sock.Type != MountBind {
		t.Errorf("expected MountBind (sockets cannot use overlay), got %q", sock.Type)
	}
}

func TestKitty_Bindings_NoEnv(t *testing.T) {
	k := &Kitty{}
	t.Setenv("KITTY_LISTEN_ON", "")

	bindings := k.Bindings("/home/user", "/sandbox/home")
	if len(bindings) != 0 {
		t.Errorf("expected 0 bindings when KITTY_LISTEN_ON is empty, got %d", len(bindings))
	}
}

func TestKitty_Environment(t *testing.T) {
	k := &Kitty{}
	env := k.Environment("/home/user", "/sandbox/home")

	expected := map[string]bool{
		"KITTY_LISTEN_ON": false,
		"KITTY_WINDOW_ID": false,
		"KITTY_PID":       false,
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

func TestKitty_Check_NoBinary(t *testing.T) {
	k := &Kitty{}
	t.Setenv("PATH", t.TempDir())

	result := k.Check("/home/user")
	if result.Available {
		t.Error("expected Available=false when binary not found")
	}
	if result.BinaryName != "kitty" {
		t.Errorf("expected BinaryName 'kitty', got %q", result.BinaryName)
	}
	if result.InstallHint == "" {
		t.Error("expected non-empty InstallHint")
	}
}

func TestKitty_Check_NoListenOn(t *testing.T) {
	k := &Kitty{}

	// Create fake kitty binary so CheckBinary passes
	dir := t.TempDir()
	fake := filepath.Join(dir, "kitty")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("KITTY_LISTEN_ON", "")

	result := k.Check("/home/user")
	if result.Available {
		t.Error("expected Available=false when KITTY_LISTEN_ON is empty")
	}
	hasIssue := false
	for _, issue := range result.Issues {
		if strings.Contains(issue, "KITTY_LISTEN_ON") {
			hasIssue = true
		}
	}
	if !hasIssue {
		t.Error("expected issue mentioning KITTY_LISTEN_ON")
	}
}

func TestKitty_Check_SocketMissing(t *testing.T) {
	k := &Kitty{}

	dir := t.TempDir()
	fake := filepath.Join(dir, "kitty")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("KITTY_LISTEN_ON", "unix:/tmp/nonexistent-kitty-socket-12345")

	result := k.Check("/home/user")
	if result.Available {
		t.Error("expected Available=false when socket does not exist")
	}
	hasIssue := false
	for _, issue := range result.Issues {
		if strings.Contains(issue, "socket not found") {
			hasIssue = true
		}
	}
	if !hasIssue {
		t.Error("expected issue about missing socket")
	}
}
