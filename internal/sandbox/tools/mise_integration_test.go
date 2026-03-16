//go:build integration

package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// isolateMiseConfig points mise at empty config/data/state directories and unsets
// trust-related env vars so tests are not affected by host settings.
func isolateMiseConfig(t *testing.T) {
	t.Helper()
	t.Setenv("MISE_CONFIG_DIR", t.TempDir())
	t.Setenv("MISE_DATA_DIR", t.TempDir())
	t.Setenv("MISE_STATE_DIR", t.TempDir())
	// Must fully unset (not set to empty) — mise checks var presence, not value.
	unsetForTest(t, "MISE_TRUSTED_CONFIG_PATHS")
	unsetForTest(t, "MISE_YES")
}

// unsetForTest removes an env var for the duration of the test, restoring it on cleanup.
func unsetForTest(t *testing.T, key string) {
	t.Helper()
	if orig, ok := os.LookupEnv(key); ok {
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("failed to unset %s: %v", key, err)
		}
		t.Cleanup(func() {
			if err := os.Setenv(key, orig); err != nil {
				t.Errorf("failed to restore %s: %v", key, err)
			}
		})
	} else {
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("failed to unset %s: %v", key, err)
		}
	}
}

func TestCheckMiseTrust_UntrustedConfig(t *testing.T) {
	if _, err := exec.LookPath("mise"); err != nil {
		t.Skip("mise not installed")
	}

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

	isolateMiseConfig(t)

	dir := t.TempDir()
	configPath := filepath.Join(dir, ".mise.toml")
	if err := os.WriteFile(configPath, []byte("[tools]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := TrustMiseConfig(dir); err != nil {
		t.Fatalf("TrustMiseConfig() error = %v", err)
	}

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
