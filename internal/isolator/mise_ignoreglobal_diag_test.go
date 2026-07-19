package isolator

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"os"
)

// TestDiag_MiseIgnoreGlobalConfigEnv verifies the ignore_global_config option
// results in MISE_GLOBAL_CONFIG_FILE being injected via the real getToolBindings path.
func TestDiag_MiseIgnoreGlobalConfigEnv(t *testing.T) {
	if _, err := exec.LookPath("mise"); err != nil {
		t.Skip("mise not on PATH; Mise tool would be excluded from Available()")
	}
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home", "testuser")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sandboxHome := filepath.Join(tmpDir, "sandbox")
	if err := os.MkdirAll(sandboxHome, 0o755); err != nil {
		t.Fatal(err)
	}

	iso := NewKrunIsolator(DockerConfig{})
	cfg := &Config{
		ProjectDir:  filepath.Join(tmpDir, "project"),
		SandboxHome: sandboxHome,
		HomeDir:     homeDir,
		Shell:       "fish",
		ToolsConfig: map[string]any{
			"mise": map[string]any{"ignore_global_config": true},
		},
	}

	_, envVars, _ := iso.getToolBindings(cfg)
	found := ""
	for _, e := range envVars {
		if strings.HasPrefix(e, "MISE_GLOBAL_CONFIG_FILE=") {
			found = e
		}
	}
	t.Logf("envVars = %v", envVars)
	if found == "" {
		t.Fatalf("BUG REPRODUCED: MISE_GLOBAL_CONFIG_FILE not injected despite ignore_global_config=true")
	}
	t.Logf("OK: injected %s", found)
}
