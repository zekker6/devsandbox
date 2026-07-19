package tools

import (
	"os/exec"
	"strings"
	"testing"
)

func TestMise_CacheMounts(t *testing.T) {
	m := &Mise{}

	mounts := m.CacheMounts()

	// Only the download cache is a dedicated cache mount. MISE_DATA_DIR is
	// deliberately not one: the container isolator points it at the persistent
	// sandbox home so installed tools persist across runs there.
	if len(mounts) != 1 {
		t.Fatalf("CacheMounts() returned %d mounts, want 1", len(mounts))
	}
	if mounts[0].Name != "mise/cache" {
		t.Errorf("mounts[0].Name = %q, want %q", mounts[0].Name, "mise/cache")
	}
	if mounts[0].EnvVar != "MISE_CACHE_DIR" {
		t.Errorf("mounts[0].EnvVar = %q, want %q", mounts[0].EnvVar, "MISE_CACHE_DIR")
	}
	for _, mnt := range mounts {
		if mnt.EnvVar == "MISE_DATA_DIR" {
			t.Error("MISE_DATA_DIR must not be a cache mount (the isolator points it at the persistent sandbox home)")
		}
	}
}

func TestMise_ImplementsToolWithCache(t *testing.T) {
	var _ ToolWithCache = (*Mise)(nil)
}

func TestMise_ImplementsToolWithConfig(t *testing.T) {
	var _ ToolWithConfig = (*Mise)(nil)
}

func TestMise_IgnoreGlobalConfig(t *testing.T) {
	const key = "MISE_GLOBAL_CONFIG_FILE"

	findEnv := func(envs []EnvVar, name string) (EnvVar, bool) {
		for _, e := range envs {
			if e.Name == name {
				return e, true
			}
		}
		return EnvVar{}, false
	}

	tests := []struct {
		name       string
		toolCfg    map[string]any
		wantIgnore bool
	}{
		{"default (no config) respects global config", nil, false},
		{"explicit false respects global config", map[string]any{"ignore_global_config": false}, false},
		{"true ignores global config", map[string]any{"ignore_global_config": true}, true},
		{"unrelated keys leave default", map[string]any{"mount_mode": "overlay"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Mise{}
			m.Configure(GlobalConfig{}, tt.toolCfg)
			env := m.Environment("/home/testuser", "/tmp/sandbox")

			got, present := findEnv(env, key)
			if tt.wantIgnore {
				if !present {
					t.Fatalf("expected %s to be set when ignore_global_config=true", key)
				}
				if got.Value != "/dev/null" {
					t.Errorf("%s = %q, want /dev/null", key, got.Value)
				}
			} else if present {
				t.Errorf("%s should not be set when respecting the global config, got %q", key, got.Value)
			}
		})
	}
}

// TestMise_ConfigureResetsBetweenRuns guards against the shared tool singleton
// leaking a prior run's ignore_global_config into a run that does not set it.
func TestMise_ConfigureResetsBetweenRuns(t *testing.T) {
	m := &Mise{}
	m.Configure(GlobalConfig{}, map[string]any{"ignore_global_config": true})
	if len(m.Environment("/h", "/s")) == 0 {
		t.Fatal("precondition: ignore_global_config=true should set env")
	}
	m.Configure(GlobalConfig{}, nil) // next run has no mise config
	if env := m.Environment("/h", "/s"); len(env) != 0 {
		t.Errorf("ignore_global_config leaked across Configure calls: %+v", env)
	}
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

func TestHostMiseInstallsMount(t *testing.T) {
	m := hostMiseInstallsMount("/home/testuser", "linux")
	if m == nil {
		t.Fatal("hostMiseInstallsMount() = nil on linux, want a mount")
	}
	if m.Source != "/home/testuser/.local/share/mise/installs" {
		t.Errorf("Source = %q, want host mise installs dir", m.Source)
	}
	if m.Dest != hostMiseInstallsDest {
		t.Errorf("Dest = %q, want %q", m.Dest, hostMiseInstallsDest)
	}
	if !m.ReadOnly {
		t.Error("host installs mount must be read-only")
	}

	// The guest is always Linux; a darwin host's binaries cannot run in it, so
	// no mount must be produced there.
	if m := hostMiseInstallsMount("/home/testuser", "darwin"); m != nil {
		t.Errorf("hostMiseInstallsMount() on darwin = %+v, want nil", m)
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
