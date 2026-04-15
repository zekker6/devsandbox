package overlay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runSetToolMode(t *testing.T, initial, tool, mode string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, ".devsandbox.toml")
	if err := os.WriteFile(p, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetToolMode(p, tool, mode); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSetToolMode_ReplacesExistingMount(t *testing.T) {
	got := runSetToolMode(t, `# project config

[tools.claude]
mount_mode = "split"
`, "claude", "readwrite")
	if !strings.Contains(got, `mount_mode = "readwrite"`) {
		t.Errorf("mount_mode not updated:\n%s", got)
	}
	if !strings.Contains(got, "# project config") {
		t.Error("comment was stripped")
	}
	if strings.Count(got, `mount_mode = `) != 1 {
		t.Errorf("expected exactly one mount_mode line:\n%s", got)
	}
}

func TestSetToolMode_InsertsIntoExistingSection(t *testing.T) {
	got := runSetToolMode(t, `[tools.claude]
other = "value"
`, "claude", "readwrite")
	if !strings.Contains(got, `mount_mode = "readwrite"`) {
		t.Errorf("mount_mode not inserted:\n%s", got)
	}
	if !strings.Contains(got, `other = "value"`) {
		t.Errorf("sibling field lost:\n%s", got)
	}
}

func TestSetToolMode_AppendsNewSection(t *testing.T) {
	got := runSetToolMode(t, `[tools.other]
mount_mode = "readwrite"
`, "claude", "readwrite")
	if !strings.Contains(got, `[tools.claude]`) {
		t.Errorf("section not appended:\n%s", got)
	}
	if !strings.Contains(got, `[tools.other]`) {
		t.Error("existing section lost")
	}
}

func TestSetToolMode_CreatesFileIfMissing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".devsandbox.toml")
	if err := SetToolMode(p, "claude", "readwrite"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `[tools.claude]`) {
		t.Errorf("expected new section:\n%s", string(b))
	}
}

func TestSetToolMode_RejectsInvalidMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".devsandbox.toml")
	if err := SetToolMode(p, "claude", "nonsense"); err == nil {
		t.Error("expected error for invalid mode")
	}
}
