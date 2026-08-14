package overlay

import (
	"errors"
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

// TestSetToolMode_RefusesSymlink pins the fix for a host-side write through a
// path the sandbox controls. The project directory is bind-mounted read-write,
// and the protective /dev/null bind is applied only when .devsandbox.toml
// already exists at launch - so a project without one lets the sandbox create
// that name as a symlink to any host file, which `overlay migrate --set-mode`
// would then read, rewrite as TOML and hand back, skipping the confirmation
// prompt entirely when only one config is affected.
func TestSetToolMode_RefusesSymlink(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "victim")
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, ".devsandbox.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	err := SetToolMode(link, "claude", "readonly")
	if err == nil {
		t.Fatal("SetToolMode followed a symlink, want error")
	}
	if !errors.Is(err, ErrNotRegularFile) {
		t.Errorf("error = %v, want ErrNotRegularFile", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original\n" {
		t.Errorf("symlink target was rewritten: %q", got)
	}
}

// TestSetToolMode_ReplacesWithoutFollowing writes a real config twice, so the
// refusal above cannot pass by breaking the ordinary path, and confirms the
// replacement lands on the name itself.
func TestSetToolMode_ReplacesWithoutFollowing(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".devsandbox.toml")

	if err := SetToolMode(path, "claude", "readonly"); err != nil {
		t.Fatal(err)
	}
	if err := SetToolMode(path, "claude", "overlay"); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.Mode().IsRegular() {
		t.Fatalf("config is not a regular file: %v", fi.Mode())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `mount_mode = "overlay"`) {
		t.Errorf("config = %q, want mount_mode = \"overlay\"", got)
	}
}
