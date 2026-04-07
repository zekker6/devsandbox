package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"devsandbox/internal/sandbox"
)

func TestNewScratchpadCmd(t *testing.T) {
	cmd := newScratchpadCmd()

	if cmd.Use != "scratchpad [name] [command...]" {
		t.Errorf("unexpected Use: %q", cmd.Use)
	}
	if cmd.Annotations["scratchpad"] != "true" {
		t.Error("expected cmd.Annotations[\"scratchpad\"] = \"true\"")
	}

	// "sp" alias for shorter invocation.
	if !slices.Contains(cmd.Aliases, "sp") {
		t.Errorf("expected %q alias, got %v", "sp", cmd.Aliases)
	}

	// Sandbox flags should be inherited.
	for _, name := range []string{"proxy", "rm", "isolation", "git-mode", "filter-default", "info", "no-mitm", "no-hide-env"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("expected flag --%s on scratchpad subcommand", name)
		}
	}

	// Nested subcommands exist.
	for _, sub := range []string{"list", "rm"} {
		if _, _, err := cmd.Find([]string{sub}); err != nil {
			t.Errorf("subcommand %q not found: %v", sub, err)
		}
	}
}

func TestScratchpadParseArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantName string
		wantCmd  []string
		wantErr  bool
	}{
		{name: "no args", args: nil, wantName: "default", wantCmd: nil},
		{name: "named only", args: []string{"foo"}, wantName: "foo", wantCmd: nil},
		{name: "named with cmd", args: []string{"foo", "npm", "install"}, wantName: "foo", wantCmd: []string{"npm", "install"}},
		{name: "explicit default with cmd", args: []string{"default", "npm", "install"}, wantName: "default", wantCmd: []string{"npm", "install"}},
		{name: "valid name two-arg", args: []string{"npm", "install"}, wantName: "npm", wantCmd: []string{"install"}},
		{name: "invalid name falls through", args: []string{"../escape"}, wantName: "default", wantCmd: []string{"../escape"}},
		{name: "dash-prefix falls through", args: []string{"-not-a-name"}, wantName: "default", wantCmd: []string{"-not-a-name"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotCmd, err := parseScratchpadArgs(tc.args)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotName != tc.wantName {
				t.Errorf("name = %q, want %q", gotName, tc.wantName)
			}
			if len(gotCmd) != len(tc.wantCmd) {
				t.Fatalf("cmd len = %d, want %d (%v vs %v)", len(gotCmd), len(tc.wantCmd), gotCmd, tc.wantCmd)
			}
			for i := range gotCmd {
				if gotCmd[i] != tc.wantCmd[i] {
					t.Errorf("cmd[%d] = %q, want %q", i, gotCmd[i], tc.wantCmd[i])
				}
			}
		})
	}
}

func TestCollectScratchpads(t *testing.T) {
	// Build a fake HOME with scratchpads and a matching sandbox state dir.
	fakeHome := t.TempDir()
	baseDir := filepath.Join(fakeHome, ".local", "share", "devsandbox-scratchpads")
	if err := os.MkdirAll(filepath.Join(baseDir, "scratchpad-default"), 0o700); err != nil {
		t.Fatalf("mkdir scratchpad-default: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(baseDir, "scratchpad-foo"), 0o700); err != nil {
		t.Fatalf("mkdir scratchpad-foo: %v", err)
	}
	// A non-scratchpad dir should be ignored.
	if err := os.MkdirAll(filepath.Join(baseDir, "other"), 0o700); err != nil {
		t.Fatalf("mkdir other: %v", err)
	}

	// Pre-create sandbox state for "default" so HasState=true; "foo" has none.
	defaultWorkDir := filepath.Join(baseDir, "scratchpad-default")
	defaultSandbox := sandbox.GenerateSandboxName(defaultWorkDir)
	sandboxStateDir := filepath.Join(fakeHome, ".local", "share", "devsandbox", defaultSandbox)
	if err := os.MkdirAll(sandboxStateDir, 0o700); err != nil {
		t.Fatalf("mkdir sandbox state: %v", err)
	}

	got, err := collectScratchpads(fakeHome)
	if err != nil {
		t.Fatalf("collectScratchpads: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d scratchpads, want 2: %+v", len(got), got)
	}

	byName := map[string]ScratchpadInfo{}
	for _, s := range got {
		byName[s.Name] = s
	}

	if !byName["default"].HasState {
		t.Error("default: HasState = false, want true")
	}
	if byName["foo"].HasState {
		t.Error("foo: HasState = true, want false")
	}
}

func TestCollectScratchpads_MissingBaseDir(t *testing.T) {
	fakeHome := t.TempDir() // no scratchpad base dir created
	got, err := collectScratchpads(fakeHome)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d scratchpads, want 0", len(got))
	}
}

func TestScratchpadRmCmd_FlagsAndSubcommand(t *testing.T) {
	rm := newScratchpadRmCmd()
	if rm.Use != "rm <name>" {
		t.Errorf("unexpected Use: %q", rm.Use)
	}
	for _, name := range []string{"all", "keep-state", "force"} {
		if rm.Flags().Lookup(name) == nil {
			t.Errorf("missing flag --%s", name)
		}
	}
}
