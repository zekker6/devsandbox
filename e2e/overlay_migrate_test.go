package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestOverlayMigrate_Help verifies the overlay subcommand and its migrate
// subcommand are wired into the binary.
func TestOverlayMigrate_Help(t *testing.T) {
	cmd := exec.Command(binaryPath, "overlay", "migrate", "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("overlay migrate --help failed: %v\nOutput: %s", err, output)
	}
	out := string(output)
	for _, want := range []string{
		"--sandbox",
		"--all-sandboxes",
		"--path",
		"--tool",
		"--apply",
		"--set-mode",
		"--primary-only",
		"--force",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("overlay migrate --help missing flag %q. Output:\n%s", want, out)
		}
	}
}

// TestOverlayMigrate_EndToEnd_SyntheticUppers sets up a fake sandbox on disk
// with an overlay upper containing a file, then runs `overlay migrate --apply`
// and verifies the file lands at the host path. Does not actually launch a
// bwrap sandbox — just exercises the migration code path against synthetic
// directory structure.
func TestOverlayMigrate_EndToEnd_SyntheticUppers(t *testing.T) {
	tmpHome := t.TempDir()

	sandboxName := "fake-sandbox-e2e"
	// Sandbox metadata + layout.
	sandboxBase := filepath.Join(tmpHome, ".local", "share", "devsandbox")
	sandboxRoot := filepath.Join(sandboxBase, sandboxName)
	sandboxHome := filepath.Join(sandboxRoot, "home")
	if err := os.MkdirAll(sandboxHome, 0o755); err != nil {
		t.Fatal(err)
	}

	// Overlay upper for ~/.claude/projects — safePath munging strips the
	// leading / and replaces remaining / with _.
	hostPath := filepath.Join(tmpHome, ".claude", "projects")
	safePath := strings.ReplaceAll(strings.TrimPrefix(hostPath, "/"), "/", "_")
	upperDir := filepath.Join(sandboxHome, "overlay", safePath, "upper")
	if err := os.MkdirAll(upperDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Drop a file in the upper.
	payload := filepath.Join(upperDir, "session-e2e.jsonl")
	if err := os.WriteFile(payload, []byte("e2e-content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The target path must not exist on host yet (no pre-existing file).
	targetHostFile := filepath.Join(hostPath, "session-e2e.jsonl")
	if _, err := os.Stat(targetHostFile); !os.IsNotExist(err) {
		t.Fatalf("unexpected: host file %s already exists", targetHostFile)
	}

	cmd := exec.Command(binaryPath,
		"overlay", "migrate",
		"--sandbox", sandboxName,
		"--path", hostPath,
		"--apply",
	)
	cmd.Env = append(os.Environ(), "HOME="+tmpHome)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("overlay migrate failed: %v\nOutput: %s", err, output)
	}
	if !strings.Contains(string(output), "Apply succeeded") {
		t.Fatalf("migrate didn't report success:\n%s", output)
	}

	// Verify the file landed on host.
	content, err := os.ReadFile(targetHostFile)
	if err != nil {
		t.Fatalf("migrated file missing: %v\nOutput was:\n%s", err, output)
	}
	if string(content) != "e2e-content" {
		t.Errorf("unexpected content: %q", string(content))
	}
}

// TestOverlayMigrate_DryRunDoesNotWrite verifies the default dry-run mode
// produces a preview without touching the host.
func TestOverlayMigrate_DryRunDoesNotWrite(t *testing.T) {
	tmpHome := t.TempDir()
	sandboxName := "fake-sandbox-dryrun"
	sandboxBase := filepath.Join(tmpHome, ".local", "share", "devsandbox")
	sandboxHome := filepath.Join(sandboxBase, sandboxName, "home")
	if err := os.MkdirAll(sandboxHome, 0o755); err != nil {
		t.Fatal(err)
	}

	hostPath := filepath.Join(tmpHome, ".claude", "projects")
	safePath := strings.ReplaceAll(strings.TrimPrefix(hostPath, "/"), "/", "_")
	upperDir := filepath.Join(sandboxHome, "overlay", safePath, "upper")
	if err := os.MkdirAll(upperDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upperDir, "would-be-new.jsonl"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binaryPath,
		"overlay", "migrate",
		"--sandbox", sandboxName,
		"--path", hostPath,
		// No --apply
	)
	cmd.Env = append(os.Environ(), "HOME="+tmpHome)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("overlay migrate dry-run failed: %v\nOutput: %s", err, output)
	}
	if !strings.Contains(string(output), "DRY RUN") {
		t.Errorf("dry-run label missing:\n%s", output)
	}
	// The host file must NOT exist.
	if _, err := os.Stat(filepath.Join(hostPath, "would-be-new.jsonl")); !os.IsNotExist(err) {
		t.Errorf("dry-run wrote to host (err=%v)", err)
	}
}
