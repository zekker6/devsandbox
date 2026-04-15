package overlay

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocateUppers_PrimaryOnly(t *testing.T) {
	sandboxHome := t.TempDir()
	primary := filepath.Join(sandboxHome, "overlay", "home_zekker_.claude_projects", "upper")
	if err := os.MkdirAll(primary, 0o755); err != nil {
		t.Fatal(err)
	}

	sources, err := LocateUppers(sandboxHome, "/home/zekker/.claude/projects", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 {
		t.Fatalf("want 1 source, got %d: %+v", len(sources), sources)
	}
	if sources[0].Kind != UpperPrimary {
		t.Errorf("want UpperPrimary, got %v", sources[0].Kind)
	}
	if sources[0].Path != primary {
		t.Errorf("want %q, got %q", primary, sources[0].Path)
	}
}

func TestLocateUppers_PrimaryAndSessions_MtimeOrder(t *testing.T) {
	sandboxHome := t.TempDir()
	safe := "home_zekker_.claude_projects"

	primary := filepath.Join(sandboxHome, "overlay", safe, "upper")
	sessA := filepath.Join(sandboxHome, "overlay", "sessions", "aaa", safe, "upper")
	sessB := filepath.Join(sandboxHome, "overlay", "sessions", "bbb", safe, "upper")
	for _, p := range []string{primary, sessA, sessB} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Set session dir mtimes so bbb is newer than aaa.
	older := time.Now().Add(-1 * time.Hour)
	newer := time.Now()
	if err := os.Chtimes(filepath.Dir(sessA), older, older); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Dir(sessB), newer, newer); err != nil {
		t.Fatal(err)
	}

	sources, err := LocateUppers(sandboxHome, "/home/zekker/.claude/projects", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 3 {
		t.Fatalf("want 3 sources, got %d", len(sources))
	}
	if sources[0].Kind != UpperPrimary {
		t.Errorf("first must be primary, got %v", sources[0].Kind)
	}
	if sources[1].Path != sessA || sources[2].Path != sessB {
		t.Errorf("session order wrong: got %q then %q", sources[1].Path, sources[2].Path)
	}
}

func TestLocateUppers_PrimaryOnlyFlag(t *testing.T) {
	sandboxHome := t.TempDir()
	safe := "home_x"
	if err := os.MkdirAll(filepath.Join(sandboxHome, "overlay", safe, "upper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sandboxHome, "overlay", "sessions", "s1", safe, "upper"), 0o755); err != nil {
		t.Fatal(err)
	}

	sources, err := LocateUppers(sandboxHome, "/home/x", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Kind != UpperPrimary {
		t.Fatalf("--primary-only should yield exactly primary, got %+v", sources)
	}
}

func TestLocateUppers_NoUppers(t *testing.T) {
	sandboxHome := t.TempDir()
	sources, err := LocateUppers(sandboxHome, "/home/never/created", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 {
		t.Fatalf("want empty, got %+v", sources)
	}
}
