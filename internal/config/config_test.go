package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Proxy.IsEnabled() {
		t.Error("expected proxy to be disabled by default")
	}
	if cfg.Proxy.Port != 8080 {
		t.Errorf("expected default proxy port 8080, got %d", cfg.Proxy.Port)
	}
	if cfg.Sandbox.BasePath != "" {
		t.Error("expected empty base path by default")
	}
	if !cfg.Overlay.IsEnabled() {
		t.Error("expected overlay to be enabled by default")
	}
}

func TestOverlayIsEnabled(t *testing.T) {
	tests := []struct {
		name     string
		enabled  *bool
		expected bool
	}{
		{"nil defaults to true", nil, true},
		{"explicit true", boolPtr(true), true},
		{"explicit false", boolPtr(false), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oc := OverlayConfig{Enabled: tt.enabled}
			if got := oc.IsEnabled(); got != tt.expected {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestSandboxGetConfigVisibility(t *testing.T) {
	tests := []struct {
		name       string
		visibility ConfigVisibility
		expected   ConfigVisibility
	}{
		{"empty defaults to hidden", "", ConfigVisibilityHidden},
		{"explicit hidden", ConfigVisibilityHidden, ConfigVisibilityHidden},
		{"explicit readonly", ConfigVisibilityReadOnly, ConfigVisibilityReadOnly},
		{"explicit readwrite", ConfigVisibilityReadWrite, ConfigVisibilityReadWrite},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := SandboxConfig{ConfigVisibility: tt.visibility}
			if got := sc.GetConfigVisibility(); got != tt.expected {
				t.Errorf("GetConfigVisibility() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func boolPtr(b bool) *bool {
	return &b
}

// emptyTrustStore returns an empty trust store for tests that don't involve local configs.
func emptyTrustStore() *TrustStore {
	return &TrustStore{}
}

func TestLoadFromNonExistent(t *testing.T) {
	cfg, err := LoadFrom("/nonexistent/path/config.toml")
	if err != nil {
		t.Fatalf("unexpected error for non-existent file: %v", err)
	}

	// Should return default config
	if cfg.Proxy.IsEnabled() {
		t.Error("expected default config with proxy disabled")
	}
}

func TestLoadFromValidFile(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `
[proxy]
enabled = true
port = 9090

[sandbox]
base_path = "~/sandbox"

[overlay]
enabled = false

[tools.git]
mode = "readwrite"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if !cfg.Proxy.IsEnabled() {
		t.Error("expected proxy to be enabled")
	}
	if cfg.Proxy.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Proxy.Port)
	}
	if cfg.Overlay.IsEnabled() {
		t.Error("expected overlay to be disabled (explicit false in config)")
	}

	// Check tool config
	gitCfg := cfg.GetToolConfig("git")
	if gitCfg == nil {
		t.Fatal("expected git config")
	}
	if mode, ok := gitCfg["mode"].(string); !ok || mode != "readwrite" {
		t.Errorf("expected git mode 'readwrite', got %v", gitCfg["mode"])
	}
}

func TestLoadFromEmptyPath(t *testing.T) {
	cfg, err := LoadFrom("")
	if err != nil {
		t.Fatalf("unexpected error for empty path: %v", err)
	}

	// Should return default config
	if cfg.Proxy.IsEnabled() {
		t.Error("expected default config")
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot get home dir")
	}

	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"~", home},
		{"~/foo", filepath.Join(home, "foo")},
		{"~foo", "~foo"}, // Not expanded (no slash)
		{"/absolute", "/absolute"},
		{"relative", "relative"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := expandHome(tt.input)
			if got != tt.expected {
				t.Errorf("expandHome(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestGetToolConfig(t *testing.T) {
	cfg := &Config{
		Tools: map[string]any{
			"git": map[string]any{
				"mode": "readonly",
			},
			"invalid": "not a map",
		},
	}

	// Valid tool config
	gitCfg := cfg.GetToolConfig("git")
	if gitCfg == nil {
		t.Error("expected git config")
	}
	if mode := gitCfg["mode"]; mode != "readonly" {
		t.Errorf("expected mode 'readonly', got %v", mode)
	}

	// Non-existent tool
	if cfg.GetToolConfig("nonexistent") != nil {
		t.Error("expected nil for non-existent tool")
	}

	// Invalid type (not a map)
	if cfg.GetToolConfig("invalid") != nil {
		t.Error("expected nil for invalid tool config type")
	}

	// Nil tools map
	nilCfg := &Config{}
	if nilCfg.GetToolConfig("git") != nil {
		t.Error("expected nil for nil tools map")
	}
}

func TestLoadFromInvalidTOML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	// Invalid TOML content
	if err := os.WriteFile(configPath, []byte("invalid = [unclosed"), 0o644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	_, err := LoadFrom(configPath)
	if err == nil {
		t.Error("expected error for invalid TOML")
	}
}

func TestGenerateDefault(t *testing.T) {
	output := GenerateDefault()
	if len(output) == 0 {
		t.Error("expected non-empty default config")
	}
	if !contains(output, "[proxy]") {
		t.Error("expected [proxy] section in default config")
	}
	if !contains(output, "[sandbox]") {
		t.Error("expected [sandbox] section in default config")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid default config",
			cfg:     DefaultConfig(),
			wantErr: false,
		},
		{
			name: "invalid port too low",
			cfg: &Config{
				Proxy: ProxyConfig{Port: 0},
			},
			wantErr: false, // 0 is allowed (means use default)
		},
		{
			name: "invalid port negative",
			cfg: &Config{
				Proxy: ProxyConfig{Port: -1},
			},
			wantErr: true,
			errMsg:  "proxy.port must be between",
		},
		{
			name: "invalid port too high",
			cfg: &Config{
				Proxy: ProxyConfig{Port: 70000},
			},
			wantErr: true,
			errMsg:  "proxy.port must be between",
		},
		{
			name: "valid port",
			cfg: &Config{
				Proxy: ProxyConfig{Port: 8080},
			},
			wantErr: false,
		},
		{
			name: "negative ask timeout",
			cfg: &Config{
				Proxy: ProxyConfig{
					Filter: ProxyFilterConfig{AskTimeout: -1},
				},
			},
			wantErr: true,
			errMsg:  "ask_timeout cannot be negative",
		},
		{
			name: "ask timeout too high",
			cfg: &Config{
				Proxy: ProxyConfig{
					Filter: ProxyFilterConfig{AskTimeout: 1000},
				},
			},
			wantErr: true,
			errMsg:  "ask_timeout cannot exceed",
		},
		{
			name: "path traversal in base path",
			cfg: &Config{
				Sandbox: SandboxConfig{BasePath: "/home/user/../../../etc/passwd"},
			},
			wantErr: true,
			errMsg:  "path contains traversal",
		},
		{
			name: "relative base path",
			cfg: &Config{
				Sandbox: SandboxConfig{BasePath: "relative/path"},
			},
			wantErr: true,
			errMsg:  "path must be absolute",
		},
		{
			name: "valid absolute base path",
			cfg: &Config{
				Sandbox: SandboxConfig{BasePath: "/home/user/.local/share/devsandbox"},
			},
			wantErr: false,
		},
		{
			name: "invalid filter action",
			cfg: &Config{
				Proxy: ProxyConfig{
					Filter: ProxyFilterConfig{DefaultAction: "invalid"},
				},
			},
			wantErr: true,
			errMsg:  "default_action must be",
		},
		{
			name: "valid filter actions",
			cfg: &Config{
				Proxy: ProxyConfig{
					Filter: ProxyFilterConfig{
						DefaultAction: "block",
						Rules: []ProxyFilterRule{
							{Pattern: "*.example.com", Action: "allow"},
							{Pattern: "bad.com", Action: "block"},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "empty rule pattern",
			cfg: &Config{
				Proxy: ProxyConfig{
					Filter: ProxyFilterConfig{
						Rules: []ProxyFilterRule{
							{Pattern: "", Action: "allow"},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "pattern cannot be empty",
		},
		{
			name: "invalid rule action",
			cfg: &Config{
				Proxy: ProxyConfig{
					Filter: ProxyFilterConfig{
						Rules: []ProxyFilterRule{
							{Pattern: "*.com", Action: "invalid"},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "action must be",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestValidatePath(t *testing.T) {
	tests := []struct {
		path    string
		wantErr bool
	}{
		{"/absolute/path", false},
		{"/home/user/.config", false},
		{"relative/path", true},
		{"/path/with/../traversal", true},
		{"/normal/path", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			err := validatePath(tt.path)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr, 0))
}

func containsAt(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestLoadWithProjectDir_IncludeMatch(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")
	projectDir := filepath.Join(tmpDir, "work", "myproject")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Main config with include
	mainConfig := fmt.Sprintf(`
[proxy]
port = 8080

[[include]]
if = "dir:%s/**"
path = "%s"
`, filepath.Join(tmpDir, "work"), filepath.Join(configDir, "work.toml"))

	mainPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(mainPath, []byte(mainConfig), 0644); err != nil {
		t.Fatalf("failed to write main config: %v", err)
	}

	// Include file
	includeConfig := `
[proxy]
port = 9090
enabled = true
`
	if err := os.WriteFile(filepath.Join(configDir, "work.toml"), []byte(includeConfig), 0644); err != nil {
		t.Fatalf("failed to write include config: %v", err)
	}

	cfg, err := LoadWithProjectDir(mainPath, projectDir, &LoadOptions{
		TrustStore: emptyTrustStore(),
	})
	if err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	if cfg.Proxy.Port != 9090 {
		t.Errorf("expected port 9090 from include, got %d", cfg.Proxy.Port)
	}
	if !cfg.Proxy.IsEnabled() {
		t.Error("expected proxy enabled from include")
	}
}

func TestLoadWithProjectDir_NoIncludeMatch(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")
	projectDir := filepath.Join(tmpDir, "personal", "myproject")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Main config with include that won't match
	mainConfig := fmt.Sprintf(`
[proxy]
port = 8080

[[include]]
if = "dir:%s/**"
path = "%s"
`, filepath.Join(tmpDir, "work"), filepath.Join(configDir, "work.toml"))

	mainPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(mainPath, []byte(mainConfig), 0644); err != nil {
		t.Fatalf("failed to write main config: %v", err)
	}

	cfg, err := LoadWithProjectDir(mainPath, projectDir, &LoadOptions{
		TrustStore: emptyTrustStore(),
	})
	if err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	// Should use base config values since include doesn't match
	if cfg.Proxy.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.Proxy.Port)
	}
}

func TestLoadWithProjectDir_MissingIncludeFile(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "work", "project")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	mainConfig := fmt.Sprintf(`
[proxy]
port = 8080

[[include]]
if = "dir:%s/**"
path = "/nonexistent/config.toml"
`, filepath.Join(tmpDir, "work"))

	mainPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(mainPath, []byte(mainConfig), 0644); err != nil {
		t.Fatalf("failed to write main config: %v", err)
	}

	// Missing include file should be an error
	_, err := LoadWithProjectDir(mainPath, projectDir, &LoadOptions{
		TrustStore: emptyTrustStore(),
	})
	if err == nil {
		t.Fatal("expected error for missing include file")
	}
	if !contains(err.Error(), "failed to load include") {
		t.Errorf("expected 'failed to load include' error, got: %v", err)
	}
}

func TestConfig_PortForwarding(t *testing.T) {
	content := `
[port_forwarding]
enabled = true

[[port_forwarding.rules]]
name = "devserver"
direction = "inbound"
protocol = "tcp"
host_port = 3000
sandbox_port = 3000

[[port_forwarding.rules]]
direction = "outbound"
host_port = 5432
sandbox_port = 5432
`
	tmpFile := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(tmpFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(tmpFile)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}

	if !cfg.PortForwarding.IsEnabled() {
		t.Error("PortForwarding should be enabled")
	}
	if len(cfg.PortForwarding.Rules) != 2 {
		t.Errorf("expected 2 rules, got %d", len(cfg.PortForwarding.Rules))
	}

	// First rule: explicit name
	r1 := cfg.PortForwarding.Rules[0]
	if r1.Name != "devserver" {
		t.Errorf("expected name 'devserver', got %q", r1.Name)
	}
	if r1.Direction != "inbound" {
		t.Errorf("expected direction 'inbound', got %q", r1.Direction)
	}
	if r1.Protocol != "tcp" {
		t.Errorf("expected protocol 'tcp', got %q", r1.Protocol)
	}

	// Second rule: no name (will be auto-generated during validation)
	r2 := cfg.PortForwarding.Rules[1]
	if r2.Direction != "outbound" {
		t.Errorf("expected direction 'outbound', got %q", r2.Direction)
	}
}

func TestDockerConfig_Defaults(t *testing.T) {
	cfg := DockerConfig{}

	if !cfg.IsHideEnvFilesEnabled() {
		t.Error("HideEnvFiles should default to true")
	}

	// Test explicit false
	falseVal := false
	cfg.HideEnvFiles = &falseVal
	if cfg.IsHideEnvFilesEnabled() {
		t.Error("HideEnvFiles should be false when explicitly set")
	}

	// Test explicit true
	trueVal := true
	cfg.HideEnvFiles = &trueVal
	if !cfg.IsHideEnvFilesEnabled() {
		t.Error("HideEnvFiles should be true when explicitly set")
	}
}

func TestDockerConfig_Dockerfile(t *testing.T) {
	content := `
[sandbox.docker]
dockerfile = "/custom/Dockerfile"
`
	tmpFile := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(tmpFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(tmpFile)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Sandbox.Docker.Dockerfile != "/custom/Dockerfile" {
		t.Errorf("expected dockerfile '/custom/Dockerfile', got %q", cfg.Sandbox.Docker.Dockerfile)
	}
}

func TestSandboxConfig_GetIsolation(t *testing.T) {
	tests := []struct {
		isolation IsolationBackend
		expected  IsolationBackend
	}{
		{"", IsolationAuto},
		{IsolationAuto, IsolationAuto},
		{IsolationBwrap, IsolationBwrap},
		{IsolationDocker, IsolationDocker},
	}

	for _, tt := range tests {
		cfg := SandboxConfig{Isolation: tt.isolation}
		if got := cfg.GetIsolation(); got != tt.expected {
			t.Errorf("GetIsolation() = %s, want %s", got, tt.expected)
		}
	}
}

func TestConfig_PortForwarding_Validation(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name: "invalid_direction",
			content: `
[port_forwarding]
enabled = true
[[port_forwarding.rules]]
direction = "invalid"
host_port = 3000
sandbox_port = 3000
`,
			wantErr: "direction must be",
		},
		{
			name: "invalid_protocol",
			content: `
[port_forwarding]
enabled = true
[[port_forwarding.rules]]
direction = "inbound"
protocol = "http"
host_port = 3000
sandbox_port = 3000
`,
			wantErr: "protocol must be",
		},
		{
			name: "invalid_host_port_zero",
			content: `
[port_forwarding]
enabled = true
[[port_forwarding.rules]]
direction = "inbound"
host_port = 0
sandbox_port = 3000
`,
			wantErr: "host_port must be",
		},
		{
			name: "invalid_host_port_too_high",
			content: `
[port_forwarding]
enabled = true
[[port_forwarding.rules]]
direction = "inbound"
host_port = 70000
sandbox_port = 3000
`,
			wantErr: "host_port must be",
		},
		{
			name: "duplicate_inbound_host_port",
			content: `
[port_forwarding]
enabled = true
[[port_forwarding.rules]]
name = "first"
direction = "inbound"
host_port = 3000
sandbox_port = 3000
[[port_forwarding.rules]]
name = "second"
direction = "inbound"
host_port = 3000
sandbox_port = 4000
`,
			wantErr: "conflict",
		},
		{
			name: "duplicate_outbound_sandbox_port",
			content: `
[port_forwarding]
enabled = true
[[port_forwarding.rules]]
name = "first"
direction = "outbound"
host_port = 5432
sandbox_port = 5432
[[port_forwarding.rules]]
name = "second"
direction = "outbound"
host_port = 5433
sandbox_port = 5432
`,
			wantErr: "conflict",
		},
		{
			name: "valid_same_port_different_protocol",
			content: `
[port_forwarding]
enabled = true
[[port_forwarding.rules]]
direction = "inbound"
protocol = "tcp"
host_port = 3000
sandbox_port = 3000
[[port_forwarding.rules]]
direction = "inbound"
protocol = "udp"
host_port = 3000
sandbox_port = 3000
`,
			wantErr: "",
		},
		{
			name: "auto_generated_name",
			content: `
[port_forwarding]
enabled = true
[[port_forwarding.rules]]
direction = "inbound"
host_port = 8080
sandbox_port = 8080
`,
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpFile := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(tmpFile, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}

			_, err := LoadFrom(tmpFile)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("expected no error, got: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.wantErr)
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got: %v", tt.wantErr, err)
				}
			}
		})
	}
}
