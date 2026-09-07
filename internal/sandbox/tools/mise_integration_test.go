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
	// Must fully unset (not set to empty) - mise checks var presence, not value.
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

// miseTrustShow runs `mise trust --show` in projectDir with the given extra
// environment and returns the config paths it reports as trusted and untrusted.
// Only stdout is parsed: mise prints update notices on stderr.
func miseTrustShow(t *testing.T, projectDir string, extraEnv []string) (trusted, untrusted []string) {
	t.Helper()
	cmd := exec.Command("mise", "trust", "--show")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("mise trust --show in %s: %v\n%s", projectDir, err, stderr)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		switch {
		case strings.HasSuffix(line, ": trusted"):
			trusted = append(trusted, line)
		case strings.HasSuffix(line, ": untrusted"):
			untrusted = append(untrusted, line)
		}
	}
	return trusted, untrusted
}

// TestMise_TrustedConfigPathsTrustsProject runs real mise against the
// environment Mise.Environment emits. The project config must load as trusted
// with no host-side `mise trust` - and it must still do so when the project
// directory is spelled through a symlink, because os.Getwd hands devsandbox
// $PWD verbatim, so that spelling is the one MISE_TRUSTED_CONFIG_PATHS carries
// while mise itself sees the resolved cwd.
func TestMise_TrustedConfigPathsTrustsProject(t *testing.T) {
	if _, err := exec.LookPath("mise"); err != nil {
		t.Skip("mise not installed")
	}
	isolateMiseConfig(t)

	realDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(realDir, ".mise.toml"), []byte("[tools]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}

	// Without the export the same config is untrusted, so the trusted
	// assertions below are not vacuous.
	if trusted, untrusted := miseTrustShow(t, realDir, nil); len(untrusted) == 0 || len(trusted) != 0 {
		t.Fatalf("without MISE_TRUSTED_CONFIG_PATHS: trusted = %v, untrusted = %v, want only untrusted", trusted, untrusted)
	}

	tests := []struct {
		name       string
		projectDir string
	}{
		{name: "real path", projectDir: realDir},
		{name: "through a symlink", projectDir: link},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Mise{}
			m.Configure(GlobalConfig{ProjectDir: tt.projectDir}, nil)

			env := []string{"PWD=" + tt.projectDir}
			for _, v := range m.Environment("", "") {
				env = append(env, v.Name+"="+v.Value)
			}

			trusted, untrusted := miseTrustShow(t, tt.projectDir, env)
			if len(trusted) == 0 || len(untrusted) != 0 {
				t.Errorf("with MISE_TRUSTED_CONFIG_PATHS=%s: trusted = %v, untrusted = %v, want the project config trusted",
					tt.projectDir, trusted, untrusted)
			}
		})
	}
}
