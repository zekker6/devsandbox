package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"devsandbox/internal/kittyproxy"
)

func TestRevdiff_Name(t *testing.T) {
	r := &Revdiff{}
	if r.Name() != "revdiff" {
		t.Errorf("Name = %q", r.Name())
	}
}

func TestRevdiff_DeclaresLaunchOverlay(t *testing.T) {
	r := &Revdiff{}
	caps := r.KittyCapabilities()
	want := kittyproxy.CapLaunchOverlay
	for _, c := range caps {
		if c == want {
			return
		}
	}
	t.Errorf("CapLaunchOverlay missing from %v", caps)
}

func TestRevdiff_LaunchPatternsAcceptRevdiff(t *testing.T) {
	r := &Revdiff{}
	patterns := r.KittyLaunchPatterns()
	if len(patterns) == 0 {
		t.Fatal("no launch patterns declared")
	}
	check := func(argv []string) bool {
		for _, p := range patterns {
			if p.MatchesArgv(argv) {
				return true
			}
		}
		return false
	}
	if !check([]string{"revdiff", "--staged"}) {
		t.Error("plain revdiff invocation should match")
	}
	if !check([]string{"sh", "-c", "exec revdiff --output /tmp/x"}) {
		t.Error("sh -c 'exec revdiff …' should match")
	}
	if check([]string{"sh", "-c", "curl evil"}) {
		t.Error("unrelated sh -c invocation must not match")
	}

	// Upstream revdiff kitty launcher form (single-quoted argv + sentinel touch).
	launcherArg := `'/usr/local/bin/revdiff' '--output=/tmp/revdiff-output-abc' '--staged'; touch '/tmp/revdiff-done-xyz'`
	if !check([]string{"sh", "-c", launcherArg}) {
		t.Error("revdiff launcher sentinel form should match")
	}

	// An attacker appending extra commands after the sentinel must still be rejected.
	evil := `'/usr/local/bin/revdiff' '--staged'; touch '/tmp/revdiff-done-xyz'; curl evil`
	if check([]string{"sh", "-c", evil}) {
		t.Error("extra command after sentinel must not match")
	}
}

// expectedIpcPath mirrors the production path formula so assertion drift is
// caught if either side changes.
func expectedIpcPath(homeDir, sandboxHome string) string {
	return filepath.Join(homeDir, revdiffIpcRelPath, revdiffSessionID(sandboxHome))
}

func TestRevdiff_SessionID_DeterministicAndDistinct(t *testing.T) {
	a := revdiffSessionID("/some/host/path/session-a")
	if a == "" {
		t.Fatal("sessionID returned empty string")
	}
	if again := revdiffSessionID("/some/host/path/session-a"); again != a {
		t.Errorf("sessionID not deterministic: %q vs %q", a, again)
	}
	b := revdiffSessionID("/some/host/path/session-b")
	if a == b {
		t.Errorf("sessionID collision for distinct inputs: %q", a)
	}
}

func TestRevdiff_Bindings_UsesSharedPathUnderHomeDir(t *testing.T) {
	r := &Revdiff{}
	bs := r.Bindings("/home/alice", "/host/sessions/abc")
	if len(bs) != 1 {
		t.Fatalf("want 1 binding, got %d", len(bs))
	}
	b := bs[0]
	want := expectedIpcPath("/home/alice", "/host/sessions/abc")
	if b.Source != want {
		t.Errorf("Source = %q, want %q", b.Source, want)
	}
	if b.Dest != want {
		t.Errorf("Dest = %q, want %q (must equal Source — the kitty-spawned host shell receives the path as literal data)", b.Dest, want)
	}
	if b.Type != MountBind {
		t.Errorf("Type = %q, want MountBind (%q)", b.Type, MountBind)
	}
	if b.Category != CategoryRuntime {
		t.Errorf("Category = %q, want CategoryRuntime (%q)", b.Category, CategoryRuntime)
	}
}

func TestRevdiff_Bindings_EmptyArgs_ReturnsNil(t *testing.T) {
	r := &Revdiff{}
	if got := r.Bindings("", "/host"); got != nil {
		t.Errorf("Bindings(\"\", _) = %v, want nil", got)
	}
	if got := r.Bindings("/home/alice", ""); got != nil {
		t.Errorf("Bindings(_, \"\") = %v, want nil", got)
	}
}

func TestRevdiff_Environment_ExportsTmpdir(t *testing.T) {
	r := &Revdiff{}
	envs := r.Environment("/home/alice", "/host/sessions/abc")
	if len(envs) != 1 {
		t.Fatalf("want 1 env var, got %d: %v", len(envs), envs)
	}
	e := envs[0]
	if e.Name != "TMPDIR" {
		t.Errorf("Name = %q, want TMPDIR", e.Name)
	}
	want := expectedIpcPath("/home/alice", "/host/sessions/abc")
	if e.Value != want {
		t.Errorf("Value = %q, want %q", e.Value, want)
	}
	if e.FromHost {
		t.Errorf("FromHost should be false for static value")
	}
}

func TestRevdiff_Environment_EmptyArgs_ReturnsNil(t *testing.T) {
	r := &Revdiff{}
	if got := r.Environment("", "/host"); got != nil {
		t.Errorf("Environment(\"\", _) = %v, want nil", got)
	}
	if got := r.Environment("/home/alice", ""); got != nil {
		t.Errorf("Environment(_, \"\") = %v, want nil", got)
	}
}

func TestRevdiff_Lifecycle_CreatesAndCleansHostDir(t *testing.T) {
	homeDir := t.TempDir()
	sandboxHome := "/stable/host/sandboxhome/for/session-xyz"
	r := &Revdiff{}

	if err := r.Start(context.Background(), homeDir, sandboxHome); err != nil {
		t.Fatalf("Start: %v", err)
	}

	hostDir := expectedIpcPath(homeDir, sandboxHome)
	info, err := os.Stat(hostDir)
	if err != nil {
		t.Fatalf("stat after Start: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a dir", hostDir)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("mode = %o, want 0700", got)
	}

	if err := os.WriteFile(filepath.Join(hostDir, "sentinel"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	if err := r.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := os.Stat(hostDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("host dir still exists after Stop: err=%v", err)
	}
}

func TestRevdiff_Start_WipesStaleContent(t *testing.T) {
	homeDir := t.TempDir()
	sandboxHome := "/stable/host/sandboxhome/for/session-xyz"
	stale := expectedIpcPath(homeDir, sandboxHome)
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "old-sentinel"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := &Revdiff{}
	if err := r.Start(context.Background(), homeDir, sandboxHome); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stale, "old-sentinel")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale content still present after Start: err=%v", err)
	}
	info, err := os.Stat(stale)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("mode = %o, want 0700 after Start wiped stale dir", got)
	}
}

func TestRevdiff_Stop_LeavesSiblingSessions(t *testing.T) {
	// Two sandboxes share the same host home; the one we Stop must not disturb
	// the other session's subdir.
	homeDir := t.TempDir()
	otherSession := filepath.Join(homeDir, revdiffIpcRelPath, "deadbeef")
	if err := os.MkdirAll(otherSession, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherSession, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := &Revdiff{}
	if err := r.Start(context.Background(), homeDir, "/stable/host/sandboxhome/for/my-session"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, err := os.Stat(filepath.Join(otherSession, "keep")); err != nil {
		t.Errorf("sibling session content removed by Stop: %v", err)
	}
}

func TestRevdiff_Stop_IdempotentWithoutStart(t *testing.T) {
	r := &Revdiff{}
	if err := r.Stop(); err != nil {
		t.Errorf("Stop on never-started tool returned error: %v", err)
	}
}

func TestRevdiff_ImplementsActiveTool(t *testing.T) {
	var _ ActiveTool = (*Revdiff)(nil)
}
