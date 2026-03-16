package tools

import (
	"os"
	"os/exec"
	"path/filepath"
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

// isolateMiseConfig points mise at empty config/data directories so tests
// are not affected by host or CI mise settings (e.g. trusted_config_paths).
func isolateMiseConfig(t *testing.T) {
	t.Helper()
	t.Setenv("MISE_CONFIG_DIR", t.TempDir())
	t.Setenv("MISE_DATA_DIR", t.TempDir())
	t.Setenv("MISE_TRUSTED_CONFIG_PATHS", "")
	t.Setenv("MISE_YES", "")
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

func TestCheckMiseTrust_UntrustedConfig(t *testing.T) {
	if _, err := exec.LookPath("mise"); err != nil {
		t.Skip("mise not installed")
	}

	// Isolate mise from host config to get consistent trust behavior in CI.
	// jdx/mise-action may configure trusted_config_paths in mise's settings file.
	isolateMiseConfig(t)

	dir := t.TempDir()
	configPath := filepath.Join(dir, ".mise.toml")
	if err := os.WriteFile(configPath, []byte("[tools]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	statuses, err := CheckMiseTrust(dir)
	if err != nil {
		t.Fatalf("CheckMiseTrust() error = %v", err)
	}

	// Should have at least one untrusted entry for the temp dir
	foundUntrusted := false
	for _, s := range statuses {
		if !s.Trusted && strings.Contains(s.Path, dir) {
			foundUntrusted = true
		}
	}
	if !foundUntrusted {
		t.Errorf("expected untrusted status for %s, got: %v", dir, statuses)
	}
}

func TestTrustMiseConfig(t *testing.T) {
	if _, err := exec.LookPath("mise"); err != nil {
		t.Skip("mise not installed")
	}

	// Isolate mise from host config to get consistent trust behavior in CI.
	isolateMiseConfig(t)

	dir := t.TempDir()
	configPath := filepath.Join(dir, ".mise.toml")
	if err := os.WriteFile(configPath, []byte("[tools]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Trust the config
	if err := TrustMiseConfig(dir); err != nil {
		t.Fatalf("TrustMiseConfig() error = %v", err)
	}

	// Verify it's now trusted
	statuses, err := CheckMiseTrust(dir)
	if err != nil {
		t.Fatalf("CheckMiseTrust() error = %v", err)
	}

	for _, s := range statuses {
		if strings.Contains(s.Path, dir) && !s.Trusted {
			t.Errorf("expected trusted status for %s after TrustMiseConfig()", s.Path)
		}
	}
}
