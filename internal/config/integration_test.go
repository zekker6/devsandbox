// internal/config/integration_test.go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFullConfigLoad_Integration(t *testing.T) {
	// Set up directory structure
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".config", "devsandbox")
	workDir := filepath.Join(tmpDir, "work", "project")
	personalDir := filepath.Join(tmpDir, "personal", "project")

	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("failed to create work dir: %v", err)
	}
	if err := os.MkdirAll(personalDir, 0755); err != nil {
		t.Fatalf("failed to create personal dir: %v", err)
	}

	// Global config with includes
	globalConfig := `
[proxy]
port = 8080

[tools.git]
mode = "readonly"

[[include]]
if = "dir:` + filepath.Join(tmpDir, "work") + `/**"
path = "` + filepath.Join(configDir, "work.toml") + `"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(globalConfig), 0644); err != nil {
		t.Fatalf("failed to write global config: %v", err)
	}

	// Work include
	workConfig := `
[proxy]
enabled = true
port = 9090

[tools.git]
mode = "readwrite"
`
	if err := os.WriteFile(filepath.Join(configDir, "work.toml"), []byte(workConfig), 0644); err != nil {
		t.Fatalf("failed to write work config: %v", err)
	}

	// Local config in work project
	localConfig := `
[proxy]
port = 7070

[[proxy.filter.rules]]
pattern = "*.internal.com"
action = "allow"
`
	if err := os.WriteFile(filepath.Join(workDir, ".devsandbox.toml"), []byte(localConfig), 0644); err != nil {
		t.Fatalf("failed to write local config: %v", err)
	}

	// Create trust store with pre-trusted work project
	trustStore := &TrustStore{}
	hash, err := HashFile(filepath.Join(workDir, ".devsandbox.toml"))
	if err != nil {
		t.Fatalf("failed to hash local config: %v", err)
	}
	trustStore.AddTrust(workDir, hash)

	// Test 1: Work project with local config
	cfg, err := LoadWithProjectDir(
		filepath.Join(configDir, "config.toml"),
		workDir,
		&LoadOptions{TrustStore: trustStore},
	)
	if err != nil {
		t.Fatalf("failed to load work config: %v", err)
	}

	// Should have: global -> work include -> local
	if cfg.Proxy.Port != 7070 {
		t.Errorf("expected port 7070 from local, got %d", cfg.Proxy.Port)
	}
	if !cfg.Proxy.IsEnabled() {
		t.Error("expected proxy enabled from work include")
	}
	if cfg.GetToolConfig("git")["mode"] != "readwrite" {
		t.Error("expected git readwrite from work include")
	}
	if len(cfg.Proxy.Filter.Rules) != 1 {
		t.Error("expected 1 filter rule from local config")
	}

	// Test 2: Personal project (no include match, no local config)
	cfg2, err := LoadWithProjectDir(
		filepath.Join(configDir, "config.toml"),
		personalDir,
		&LoadOptions{TrustStore: trustStore},
	)
	if err != nil {
		t.Fatalf("failed to load personal config: %v", err)
	}

	// Should only have global settings
	if cfg2.Proxy.Port != 8080 {
		t.Errorf("expected port 8080 from global, got %d", cfg2.Proxy.Port)
	}
	if cfg2.Proxy.IsEnabled() {
		t.Error("expected proxy disabled (global default)")
	}
	if cfg2.GetToolConfig("git")["mode"] != "readonly" {
		t.Error("expected git readonly from global")
	}
}

func TestUntrustedLocalConfig_Integration(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Local config exists
	if err := os.WriteFile(filepath.Join(projectDir, ".devsandbox.toml"), []byte(`
[proxy]
enabled = true
`), 0644); err != nil {
		t.Fatalf("failed to write local config: %v", err)
	}

	// Empty trust store (nothing trusted)
	trustStore := &TrustStore{}

	// Track if prompt was called
	promptCalled := false

	cfg, err := LoadWithProjectDir(
		"", // No global config
		projectDir,
		&LoadOptions{
			TrustStore: trustStore,
			OnLocalConfigPrompt: func(dir, content string, changed bool) (bool, error) {
				promptCalled = true
				return false, nil // Deny trust
			},
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !promptCalled {
		t.Error("expected prompt to be called for untrusted config")
	}

	// Should not have local config applied (denied)
	if cfg.Proxy.IsEnabled() {
		t.Error("expected proxy disabled (local config denied)")
	}
}

func TestTrustApproval_Integration(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	trustStorePath := filepath.Join(tmpDir, "trusted-configs.toml")

	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Local config
	if err := os.WriteFile(filepath.Join(projectDir, ".devsandbox.toml"), []byte(`
[proxy]
enabled = true
port = 5555
`), 0644); err != nil {
		t.Fatalf("failed to write local config: %v", err)
	}

	// Empty trust store (use LoadTrustStore to set path)
	trustStore, err := LoadTrustStore(trustStorePath)
	if err != nil {
		t.Fatalf("failed to create trust store: %v", err)
	}

	// Approve trust via callback
	cfg, err := LoadWithProjectDir(
		"", // No global config
		projectDir,
		&LoadOptions{
			TrustStore: trustStore,
			OnLocalConfigPrompt: func(dir, content string, changed bool) (bool, error) {
				if changed {
					t.Error("expected changed=false for new config")
				}
				return true, nil // Approve
			},
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Local config should be applied
	if !cfg.Proxy.IsEnabled() {
		t.Error("expected proxy enabled from local config")
	}
	if cfg.Proxy.Port != 5555 {
		t.Errorf("expected port 5555 from local config, got %d", cfg.Proxy.Port)
	}

	// Trust store should be updated
	if len(trustStore.Trusted) != 1 {
		t.Error("expected trust store to have entry")
	}

	// Save and reload trust store to verify persistence works
	if err := trustStore.Save(); err != nil {
		t.Fatalf("failed to save trust store: %v", err)
	}

	reloaded, err := LoadTrustStore(trustStorePath)
	if err != nil {
		t.Fatalf("failed to reload trust store: %v", err)
	}
	if len(reloaded.Trusted) != 1 {
		t.Error("expected reloaded trust store to have entry")
	}
}

func TestExplicitFalseDisablesProxy_Integration(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")
	projectDir := filepath.Join(tmpDir, "project")

	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Global config enables proxy
	globalConfig := `
[proxy]
enabled = true
port = 8080
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(globalConfig), 0644); err != nil {
		t.Fatalf("failed to write global config: %v", err)
	}

	// Local config explicitly disables proxy
	localConfig := `
[proxy]
enabled = false
`
	if err := os.WriteFile(filepath.Join(projectDir, ".devsandbox.toml"), []byte(localConfig), 0644); err != nil {
		t.Fatalf("failed to write local config: %v", err)
	}

	// Pre-trust the local config
	trustStore := &TrustStore{}
	hash, _ := HashFile(filepath.Join(projectDir, ".devsandbox.toml"))
	trustStore.AddTrust(projectDir, hash)

	cfg, err := LoadWithProjectDir(
		filepath.Join(configDir, "config.toml"),
		projectDir,
		&LoadOptions{TrustStore: trustStore},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Local explicit false should override global true
	if cfg.Proxy.IsEnabled() {
		t.Error("expected proxy disabled by local config explicit false")
	}
	// Port should remain from global
	if cfg.Proxy.Port != 8080 {
		t.Errorf("expected port 8080 from global, got %d", cfg.Proxy.Port)
	}
}

func TestChangedConfigPrompt_Integration(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	trustStorePath := filepath.Join(tmpDir, "trusted-configs.toml")

	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Initial local config
	if err := os.WriteFile(filepath.Join(projectDir, ".devsandbox.toml"), []byte(`
[proxy]
port = 1111
`), 0644); err != nil {
		t.Fatalf("failed to write local config: %v", err)
	}

	// Trust store with old hash (use LoadTrustStore to set path)
	trustStore, err := LoadTrustStore(trustStorePath)
	if err != nil {
		t.Fatalf("failed to create trust store: %v", err)
	}
	trustStore.AddTrust(projectDir, "old-hash-that-wont-match")

	// Track prompt details
	var gotChanged bool
	cfg, err := LoadWithProjectDir(
		"",
		projectDir,
		&LoadOptions{
			TrustStore: trustStore,
			OnLocalConfigPrompt: func(dir, content string, changed bool) (bool, error) {
				gotChanged = changed
				return true, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !gotChanged {
		t.Error("expected changed=true for modified config")
	}
	if cfg.Proxy.Port != 1111 {
		t.Errorf("expected port 1111 from local config, got %d", cfg.Proxy.Port)
	}
}
