package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devsandbox/internal/agentid"
	"devsandbox/internal/herdrstate"
	"devsandbox/internal/sandbox"
)

func TestHerdrPaneRecordSkipLogic(t *testing.T) {
	cfg := &sandbox.Config{
		ProjectDir:  "/projects/demo",
		SandboxRoot: "/state/demo-1234abcd",
	}

	tests := []struct {
		name    string
		paneID  string
		command []string
		want    bool
	}{
		{name: "pane and agent present", paneID: "w1F:p1C", command: []string{"claude"}, want: true},
		{name: "pane id unset", paneID: "", command: []string{"claude"}, want: false},
		{name: "agent not known", paneID: "w1F:p1C", command: []string{"bash"}, want: false},
		{name: "no command at all", paneID: "w1F:p1C", command: nil, want: false},
		{name: "neither anchor", paneID: "", command: []string{"bash"}, want: false},
		{name: "agent named in a later argument", paneID: "w1F:p1C", command: []string{"sh", "-c", "claude"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HERDR_PANE_ID", tt.paneID)
			cfg.LaunchedAgent = agentid.CanonicalAgent(tt.command)

			rec, ok := herdrPaneRecord(cfg)
			if ok != tt.want {
				t.Fatalf("herdrPaneRecord ok = %v, want %v", ok, tt.want)
			}
			if !ok {
				if rec != (herdrstate.Record{}) {
					t.Errorf("expected zero record when skipping, got %+v", rec)
				}
				return
			}
			if rec.PaneID != tt.paneID {
				t.Errorf("PaneID = %q, want %q", rec.PaneID, tt.paneID)
			}
			if rec.Agent != "claude" {
				t.Errorf("Agent = %q, want claude", rec.Agent)
			}
			if rec.ProjectDir != cfg.ProjectDir {
				t.Errorf("ProjectDir = %q, want %q", rec.ProjectDir, cfg.ProjectDir)
			}
			if rec.SandboxRoot != cfg.SandboxRoot {
				t.Errorf("SandboxRoot = %q, want %q", rec.SandboxRoot, cfg.SandboxRoot)
			}
		})
	}
}

func TestRecordHerdrPaneMappingWritesRecord(t *testing.T) {
	stateHome := t.TempDir()
	projectDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("HERDR_PANE_ID", "w1F:p1C")

	cfg := &sandbox.Config{
		ProjectDir:  projectDir,
		SandboxRoot: filepath.Join(t.TempDir(), sandbox.GenerateSandboxName(projectDir)),
	}

	cfg.LaunchedAgent = agentid.CanonicalAgent([]string{"claude", "--resume", "abc"})
	if err := recordHerdrPaneMapping(cfg); err != nil {
		t.Fatalf("recordHerdrPaneMapping: %v", err)
	}

	store := herdrstate.NewStore(filepath.Join(stateHome, "devsandbox", "herdr-panes"))
	rec, err := store.Load("w1F:p1C")
	if err != nil {
		t.Fatalf("load record: %v", err)
	}
	if err := herdrstate.Validate(rec, "w1F:p1C", "claude"); err != nil {
		t.Fatalf("validate record: %v", err)
	}
	if rec.ProjectDir != projectDir {
		t.Errorf("ProjectDir = %q, want %q", rec.ProjectDir, projectDir)
	}
	if rec.SandboxRoot != cfg.SandboxRoot {
		t.Errorf("SandboxRoot = %q, want %q", rec.SandboxRoot, cfg.SandboxRoot)
	}
	if !herdrstate.DerivesSameSandboxRoot(rec, projectDir) {
		t.Error("a plain launch should derive the same sandbox root from its project dir")
	}
}

func TestRecordHerdrPaneMappingSkipsWithoutAnchors(t *testing.T) {
	tests := []struct {
		name    string
		paneID  string
		command []string
	}{
		{name: "no pane id", paneID: "", command: []string{"claude"}},
		{name: "unknown agent", paneID: "w1F:p1C", command: []string{"bash"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stateHome := t.TempDir()
			t.Setenv("XDG_STATE_HOME", stateHome)
			t.Setenv("HERDR_PANE_ID", tt.paneID)

			cfg := &sandbox.Config{ProjectDir: t.TempDir(), SandboxRoot: t.TempDir()}
			cfg.LaunchedAgent = agentid.CanonicalAgent(tt.command)
			if err := recordHerdrPaneMapping(cfg); err != nil {
				t.Fatalf("recordHerdrPaneMapping: %v", err)
			}

			dir := filepath.Join(stateHome, "devsandbox", "herdr-panes")
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("store directory should not be created when skipping, stat err = %v", err)
			}
		})
	}
}

func TestRecordHerdrPaneMappingReportsWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory write permissions")
	}

	base := t.TempDir()
	stateHome := filepath.Join(base, "state")
	if err := os.Mkdir(stateHome, 0o500); err != nil {
		t.Fatalf("create read-only state home: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateHome, 0o700) })

	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("HERDR_PANE_ID", "w1F:p1C")

	cfg := &sandbox.Config{ProjectDir: t.TempDir(), SandboxRoot: t.TempDir()}
	cfg.LaunchedAgent = "claude"
	err := recordHerdrPaneMapping(cfg)
	if err == nil {
		t.Fatal("expected an error when the state dir cannot be written")
	}
	if !strings.Contains(err.Error(), "record herdr pane mapping") {
		t.Errorf("error %q should name the failing operation", err)
	}
}

// A --worktree launch re-derives SandboxRoot from the repo root while
// ProjectDir becomes the worktree path (main.go's worktree branch). The
// recorded root must be the repo-root-derived one, which is what lets the
// restore-time guard detect that a re-entry from the worktree would open a
// different synthetic home.
func TestRecordHerdrPaneMappingWorktreeRoot(t *testing.T) {
	stateHome := t.TempDir()
	sandboxBase := t.TempDir()
	repoRoot := t.TempDir()
	worktreePath := t.TempDir()

	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("HERDR_PANE_ID", "w1F:p2C")

	cfg := &sandbox.Config{
		ProjectDir:  worktreePath,
		SandboxRoot: filepath.Join(sandboxBase, sandbox.GenerateSandboxName(repoRoot)),
	}

	cfg.LaunchedAgent = "claude"
	if err := recordHerdrPaneMapping(cfg); err != nil {
		t.Fatalf("recordHerdrPaneMapping: %v", err)
	}

	store := herdrstate.NewStore(filepath.Join(stateHome, "devsandbox", "herdr-panes"))
	rec, err := store.Load("w1F:p2C")
	if err != nil {
		t.Fatalf("load record: %v", err)
	}
	if err := herdrstate.Validate(rec, "w1F:p2C", "claude"); err != nil {
		t.Fatalf("validate record: %v", err)
	}
	if herdrstate.DerivesSameSandboxRoot(rec, worktreePath) {
		t.Error("a worktree launch must not derive its sandbox root from the worktree path")
	}
	if !herdrstate.DerivesSameSandboxRoot(rec, repoRoot) {
		t.Error("the recorded sandbox root should be the repo-root-derived one")
	}
}
