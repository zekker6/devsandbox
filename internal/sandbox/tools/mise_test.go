package tools

import (
	"os/exec"
	"strings"
	"testing"
)

func TestMise_CacheMounts(t *testing.T) {
	m := &Mise{}

	mounts := m.CacheMounts()

	if len(mounts) != 2 {
		t.Fatalf("CacheMounts() returned %d mounts, want 2", len(mounts))
	}

	// Check mise data dir
	if mounts[0].Name != "mise" {
		t.Errorf("mounts[0].Name = %q, want %q", mounts[0].Name, "mise")
	}
	if mounts[0].EnvVar != "MISE_DATA_DIR" {
		t.Errorf("mounts[0].EnvVar = %q, want %q", mounts[0].EnvVar, "MISE_DATA_DIR")
	}

	// Check mise cache dir
	if mounts[1].Name != "mise/cache" {
		t.Errorf("mounts[1].Name = %q, want %q", mounts[1].Name, "mise/cache")
	}
	if mounts[1].EnvVar != "MISE_CACHE_DIR" {
		t.Errorf("mounts[1].EnvVar = %q, want %q", mounts[1].EnvVar, "MISE_CACHE_DIR")
	}
}

func TestMise_ImplementsToolWithCache(t *testing.T) {
	var _ ToolWithCache = (*Mise)(nil)
}

func TestMise_DockerBindings_NoCacheDirs(t *testing.T) {
	m := &Mise{}

	mounts := m.DockerBindings("/home/testuser", "/tmp/sandbox")

	// Should only have config and bin mounts, NOT data/cache/state
	for _, mount := range mounts {
		if strings.Contains(mount.Dest, "share/mise") {
			t.Errorf("DockerBindings() should not mount share/mise (use CacheMounts): %s", mount.Dest)
		}
		if strings.Contains(mount.Dest, "cache/mise") {
			t.Errorf("DockerBindings() should not mount cache/mise (use CacheMounts): %s", mount.Dest)
		}
	}

	// Should have config mount
	foundConfig := false
	foundBin := false
	for _, mount := range mounts {
		if strings.Contains(mount.Dest, ".config/mise") {
			foundConfig = true
		}
		if strings.Contains(mount.Dest, ".local/bin") {
			foundBin = true
		}
	}

	if !foundConfig {
		t.Error("DockerBindings() missing .config/mise mount")
	}
	if !foundBin {
		t.Error("DockerBindings() missing .local/bin mount")
	}
}

func TestMise_Bindings_Categories(t *testing.T) {
	m := &Mise{}
	bindings := m.Bindings("/home/test", "/tmp/sandbox")

	// Category-based bindings follow the split-mode mount policy.
	// ~/.local/bin is intentionally NOT in this list — it is a read-only
	// bind mount (see TestMise_Bindings_LocalBinReadOnly).
	expected := map[string]BindingCategory{
		"/home/test/.config/mise":      CategoryConfig,
		"/home/test/.local/share/mise": CategoryData,
		"/home/test/.cache/mise":       CategoryCache,
		"/home/test/.local/state/mise": CategoryState,
	}

	for _, b := range bindings {
		// ~/.local/bin is covered by a separate test.
		if b.Source == "/home/test/.local/bin" {
			continue
		}
		want, ok := expected[b.Source]
		if !ok {
			t.Errorf("unexpected binding source: %s", b.Source)
			continue
		}
		if b.Category != want {
			t.Errorf("binding %s: Category = %q, want %q", b.Source, b.Category, want)
		}
		if b.Type != "" {
			t.Errorf("binding %s: Type should be empty, got %q", b.Source, b.Type)
		}
		delete(expected, b.Source)
	}
	for src := range expected {
		t.Errorf("missing binding for %s", src)
	}
}

// TestMise_Bindings_LocalBinReadOnly verifies ~/.local/bin is mounted as a
// read-only bind, not a category-based overlay. Persistent overlays on this
// path accumulate stale writes from tool self-updaters (e.g. claude's own
// updater writing to versions/), which can leave 0-byte shadow files that
// break execution across sessions. Read-only bind mounts also close the H6
// persistent PATH hijack finding in docs/security-assessment-2026-04-04.md.
func TestMise_Bindings_LocalBinReadOnly(t *testing.T) {
	m := &Mise{}
	bindings := m.Bindings("/home/test", "/tmp/sandbox")

	var localBin *Binding
	for i := range bindings {
		if bindings[i].Source == "/home/test/.local/bin" {
			localBin = &bindings[i]
			break
		}
	}

	if localBin == nil {
		t.Fatal("Bindings() missing ~/.local/bin")
	}
	if localBin.Type != MountBind {
		t.Errorf("~/.local/bin Type = %q, want %q", localBin.Type, MountBind)
	}
	if !localBin.ReadOnly {
		t.Error("~/.local/bin must be ReadOnly to prevent overlay shadowing of host binaries")
	}
	if localBin.Category != "" {
		t.Errorf("~/.local/bin Category should be empty (explicit Type takes precedence), got %q", localBin.Category)
	}
	if !localBin.Optional {
		t.Error("~/.local/bin should be Optional (user may not have one)")
	}
}

func TestCheckMiseTrust_NoMise(t *testing.T) {
	// If mise is not installed, CheckMiseTrust should return nil
	if _, err := exec.LookPath("mise"); err != nil {
		statuses, err := CheckMiseTrust(t.TempDir())
		if err != nil {
			t.Fatalf("CheckMiseTrust() error = %v, want nil", err)
		}
		if statuses != nil {
			t.Errorf("CheckMiseTrust() = %v, want nil when mise not available", statuses)
		}
	}
}

func TestCheckMiseTrust_NoConfig(t *testing.T) {
	if _, err := exec.LookPath("mise"); err != nil {
		t.Skip("mise not installed")
	}

	dir := t.TempDir()
	statuses, err := CheckMiseTrust(dir)
	if err != nil {
		t.Fatalf("CheckMiseTrust() error = %v", err)
	}
	// No config files means no statuses (or all trusted global configs)
	for _, s := range statuses {
		if !s.Trusted {
			t.Errorf("unexpected untrusted status for %s", s.Path)
		}
	}
}
