package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopilot_Bindings(t *testing.T) {
	c := &Copilot{}
	bindings := c.Bindings("/home/test", "/tmp/sandbox")

	// The standalone CLI home must be persistent (CategoryData) so auth carries
	// in and `copilot --resume` finds sessions from an earlier run. The gh
	// extension dirs keep their config/cache categories.
	expected := map[string]BindingCategory{
		"/home/test/.copilot":               CategoryData,
		"/home/test/.config/github-copilot": CategoryConfig,
		"/home/test/.cache/github-copilot":  CategoryCache,
	}

	seen := make(map[string]bool)
	for _, b := range bindings {
		want, ok := expected[b.Source]
		if !ok {
			t.Errorf("unexpected binding %s", b.Source)
			continue
		}
		seen[b.Source] = true
		if b.Category != want {
			t.Errorf("%s: want category %q, got %q", b.Source, want, b.Category)
		}
		if !b.Optional {
			t.Errorf("%s: binding should be optional (host dir may not exist)", b.Source)
		}
	}

	for src := range expected {
		if !seen[src] {
			t.Errorf("missing binding %s", src)
		}
	}
}

func TestCopilot_AvailableDetectsStandaloneHome(t *testing.T) {
	home := t.TempDir()
	c := &Copilot{}

	if c.Available(home) {
		t.Fatal("Available should be false with no copilot directories present")
	}

	if err := os.MkdirAll(filepath.Join(home, ".copilot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !c.Available(home) {
		t.Error("Available should be true once ~/.copilot exists")
	}
}
