package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// emptyPath points PATH at an empty directory so exec.LookPath cannot find a
// host-installed rtk and the config/data detection paths are the ones tested.
func emptyPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

func TestRTK_Bindings(t *testing.T) {
	r := &RTK{}
	bindings := r.Bindings("/home/test", "/tmp/sandbox")

	// rtk resolves both through XDG, which the sandbox points back at the host
	// home path, so these are the in-sandbox paths too.
	want := map[string]BindingCategory{
		"/home/test/.config/rtk":      CategoryConfig,
		"/home/test/.local/share/rtk": CategoryData,
	}

	if len(bindings) != len(want) {
		t.Fatalf("Bindings() returned %d bindings, want %d: %+v", len(bindings), len(want), bindings)
	}

	for _, b := range bindings {
		category, ok := want[b.Source]
		if !ok {
			t.Errorf("unexpected binding source %q", b.Source)
			continue
		}
		delete(want, b.Source)

		if b.Category != category {
			t.Errorf("%s: category = %q, want %q", b.Source, b.Category, category)
		}
		if !b.Optional {
			t.Errorf("%s: binding should be optional", b.Source)
		}
		// Leaving Type empty is what lets the mount mode policy resolve config
		// to tmpoverlay and data to a persistent overlay.
		if b.Type != "" {
			t.Errorf("%s: Type = %q, want empty so the mount policy applies", b.Source, b.Type)
		}
		if b.ReadOnly {
			t.Errorf("%s: binding must not be read-only, rtk writes its tracking database", b.Source)
		}
	}

	for src := range want {
		t.Errorf("missing binding %s", src)
	}
}

func TestRTK_Available(t *testing.T) {
	tests := []struct {
		name string
		dirs []string
		want bool
	}{
		{name: "no binary, no directories", want: false},
		{name: "config directory only", dirs: []string{".config/rtk"}, want: true},
		{name: "data directory only", dirs: []string{".local/share/rtk"}, want: true},
		{name: "both directories", dirs: []string{".config/rtk", ".local/share/rtk"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			emptyPath(t)

			home := t.TempDir()
			for _, dir := range tt.dirs {
				if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			r := &RTK{}
			if got := r.Available(home); got != tt.want {
				t.Errorf("Available() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRTK_Check_ReportsExistingDirectories(t *testing.T) {
	emptyPath(t)

	home := t.TempDir()
	configDir := filepath.Join(home, ".config", "rtk")
	dataDir := filepath.Join(home, ".local", "share", "rtk")
	for _, dir := range []string{configDir, dataDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	r := &RTK{}
	result := r.Check(home)

	if !result.Available {
		t.Error("Check() should report available when rtk directories exist")
	}
	if len(result.Issues) != 0 {
		t.Errorf("Check() reported issues: %v", result.Issues)
	}

	seen := make(map[string]bool, len(result.ConfigPaths))
	for _, p := range result.ConfigPaths {
		seen[p] = true
	}
	for _, want := range []string{configDir, dataDir} {
		if !seen[want] {
			t.Errorf("Check() config paths %v missing %s", result.ConfigPaths, want)
		}
	}
}

func TestRTK_Check_UnavailableReportsIssue(t *testing.T) {
	emptyPath(t)

	r := &RTK{}
	result := r.Check(t.TempDir())

	if result.Available {
		t.Error("Check() should report unavailable with no binary and no directories")
	}
	if len(result.Issues) == 0 {
		t.Error("Check() should report an issue when unavailable")
	}
	if result.InstallHint == "" {
		t.Error("Check() should provide an install hint")
	}
}

func TestRTK_Registered(t *testing.T) {
	tool := Get("rtk")
	if tool == nil {
		t.Fatal("rtk is not registered")
	}
	if _, ok := tool.(*RTK); !ok {
		t.Fatalf("registered rtk tool has type %T, want *RTK", tool)
	}
}
