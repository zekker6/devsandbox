package main

import (
	"encoding/json"
	"io"
	"os"
	"reflect"
	"testing"

	"devsandbox/internal/sandbox/tools"
)

// Check reports the host files a tool reads its configuration from - for git,
// the global config the sandbox copy is generated from plus the ignore and
// attributes files readonly mode carries in. Computing them and dropping them
// on the way to the CLI leaves the command reporting less than it resolved.
func TestToolsCheck_ReportsConfigPaths(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	checker, ok := tools.Get("git").(tools.ToolWithCheck)
	if !ok {
		t.Fatal("git tool no longer implements ToolWithCheck")
	}
	want := checker.Check(homeDir).ConfigPaths
	if len(want) == 0 {
		t.Skip("host has no git configuration, so there is nothing to report")
	}

	var results []struct {
		Name        string   `json:"name"`
		ConfigPaths []string `json:"config_paths"`
	}
	if err := json.Unmarshal(runToolsCheckJSON(t, "git"), &results); err != nil {
		t.Fatalf("decode --json output: %v", err)
	}
	if len(results) != 1 || results[0].Name != "git" {
		t.Fatalf("results = %+v, want one entry for git", results)
	}
	if !reflect.DeepEqual(results[0].ConfigPaths, want) {
		t.Errorf("config_paths = %v, want %v", results[0].ConfigPaths, want)
	}
}

func runToolsCheckJSON(t *testing.T, args ...string) []byte {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	cmd := newToolsCheckCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append(args, "--json"))
	runErr := cmd.Execute()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatalf("tools check: %v", runErr)
	}
	return out
}
