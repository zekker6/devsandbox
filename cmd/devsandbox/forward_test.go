package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"devsandbox/internal/session"
)

func TestParsePortSpec(t *testing.T) {
	tests := []struct {
		input       string
		sandboxPort int
		hostPort    int
		wantErr     bool
	}{
		{"3000", 3000, 3000, false},
		{"3000:8080", 3000, 8080, false},
		{"0", 0, 0, true},
		{"99999", 0, 0, true},
		{"abc", 0, 0, true},
		{"3000:", 0, 0, true},
		{":8080", 0, 0, true},
		{"3000:8080:9090", 0, 0, true},
	}
	for _, tt := range tests {
		sandboxPort, hostPort, err := parsePortSpec(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parsePortSpec(%q): err = %v, wantErr = %v", tt.input, err, tt.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if sandboxPort != tt.sandboxPort {
			t.Errorf("parsePortSpec(%q): sandboxPort = %d, want %d", tt.input, sandboxPort, tt.sandboxPort)
		}
		if hostPort != tt.hostPort {
			t.Errorf("parsePortSpec(%q): hostPort = %d, want %d", tt.input, hostPort, tt.hostPort)
		}
	}
}

func TestResolveSession_ExplicitName(t *testing.T) {
	store := newForwardTestStore(t)
	registerLive(t, store, "alpha", "/work/alpha")
	registerLive(t, store, "beta", "/work/beta")

	sess, err := resolveSession(store, "beta", "/work/beta")
	if err != nil {
		t.Fatalf("resolveSession: %v", err)
	}
	if sess.Name != "beta" {
		t.Fatalf("got %q, want beta", sess.Name)
	}
}

func TestResolveSession_ExplicitNameMissing(t *testing.T) {
	store := newForwardTestStore(t)

	_, err := resolveSession(store, "ghost", "/work/anything")
	if err == nil {
		t.Fatal("expected error for missing name, got nil")
	}
}

func TestResolveSession_SingleCWDMatch(t *testing.T) {
	store := newForwardTestStore(t)
	registerLive(t, store, "alpha", "/work/alpha")
	registerLive(t, store, "beta", "/work/beta")

	sess, err := resolveSession(store, "", "/work/alpha")
	if err != nil {
		t.Fatalf("resolveSession: %v", err)
	}
	if sess.Name != "alpha" {
		t.Fatalf("got %q, want alpha", sess.Name)
	}
}

func TestResolveSession_NoCWDMatch(t *testing.T) {
	store := newForwardTestStore(t)
	registerLive(t, store, "alpha", "/work/alpha")
	registerLive(t, store, "beta", "/work/beta")

	_, err := resolveSession(store, "", "/work/nowhere")
	if err == nil {
		t.Fatal("expected error for no match, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "/work/nowhere") {
		t.Errorf("error should mention CWD, got: %v", err)
	}
	if !strings.Contains(msg, "alpha") || !strings.Contains(msg, "beta") {
		t.Errorf("error should hint at live sessions, got: %v", err)
	}
}

func TestResolveSession_MultipleCWDMatches(t *testing.T) {
	store := newForwardTestStore(t)
	registerLive(t, store, "alpha", "/work/shared")
	registerLive(t, store, "beta", "/work/shared")

	_, err := resolveSession(store, "", "/work/shared")
	if err == nil {
		t.Fatal("expected error for multiple matches, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "--name") {
		t.Errorf("error should suggest --name, got: %v", err)
	}
	if !strings.Contains(msg, "alpha") || !strings.Contains(msg, "beta") {
		t.Errorf("error should list candidates, got: %v", err)
	}
}

// Helpers

func newForwardTestStore(t *testing.T) *session.Store {
	t.Helper()
	return session.NewStore(t.TempDir())
}

func registerLive(t *testing.T, store *session.Store, name, workDir string) {
	t.Helper()
	sess := &session.Session{
		Name:      name,
		PID:       os.Getpid(),
		NetworkNS: "/proc/self/ns/net",
		StartedAt: time.Now().UTC().Truncate(time.Second),
		WorkDir:   workDir,
	}
	if err := store.Register(sess); err != nil {
		t.Fatalf("Register %s: %v", name, err)
	}
}
