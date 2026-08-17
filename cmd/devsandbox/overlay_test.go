package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"devsandbox/internal/sandbox"
)

func TestOverlayMigrate_RequiresScopeFlag(t *testing.T) {
	cmd := newOverlayCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"migrate", "--path", "/home/x"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error for missing --sandbox / --all-sandboxes")
	}
}

func TestOverlayMigrate_RequiresSelectionFlag(t *testing.T) {
	cmd := newOverlayCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"migrate", "--sandbox", "foo"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error for missing --path / --tool")
	}
}

func TestOverlayMigrate_MutualExclusion_SandboxAndAll(t *testing.T) {
	cmd := newOverlayCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"migrate", "--sandbox", "foo", "--all-sandboxes", "--path", "/home/x"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("want mutual exclusion error, got %v", err)
	}
}

func TestOverlayMigrate_SetMode_RequiresTool(t *testing.T) {
	cmd := newOverlayCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"migrate", "--sandbox", "foo", "--path", "/home/x", "--set-mode", "readwrite"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--set-mode requires --tool") {
		t.Errorf("want --set-mode requires --tool, got %v", err)
	}
}

func TestOverlayMigrate_Tool_UnknownReturnsError(t *testing.T) {
	cmd := newOverlayCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"migrate", "--sandbox", "foo", "--tool", "not-a-tool"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("want unknown-tool error, got %v", err)
	}
}

func TestOverlayMigrate_AllSandboxes_EmptyReportsNone(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cmd := newOverlayCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"migrate", "--all-sandboxes", "--path", "/home/x"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "No sandboxes to migrate") {
		t.Errorf("want 'No sandboxes to migrate', got:\n%s", buf.String())
	}
}

func TestResolveSandboxes_AllSkipsRemovalStaging(t *testing.T) {
	home := t.TempDir()
	base := sandbox.SandboxBasePath(home)
	for _, name := range []string{"alpha", filepath.Join(".removing", "beta-1234"), "gamma"} {
		if err := os.MkdirAll(filepath.Join(base, name, "home"), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}

	names, err := resolveSandboxes(home, &overlayMigrateFlags{allSandboxes: true})
	if err != nil {
		t.Fatalf("resolveSandboxes: %v", err)
	}

	want := []string{"alpha", "gamma"}
	if !slices.Equal(names, want) {
		t.Errorf("resolveSandboxes() = %v, want %v (a sandbox staged for removal must not be migrated)", names, want)
	}
}

func TestOverlayMigrate_MutualExclusion_PathAndTool(t *testing.T) {
	cmd := newOverlayCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"migrate", "--sandbox", "foo", "--path", "/home/x", "--tool", "claude"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("want mutual exclusion error, got %v", err)
	}
}
