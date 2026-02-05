// internal/config/merge_test.go
package config

import (
	"testing"
)

func Test_mergeConfigs_ScalarOverride(t *testing.T) {
	base := &Config{
		Proxy: ProxyConfig{
			Enabled: boolPtr(false),
			Port:    8080,
		},
	}
	overlay := &Config{
		Proxy: ProxyConfig{
			Enabled: boolPtr(true),
			Port:    9090,
		},
	}

	result := mergeConfigs(base, overlay)

	if !result.Proxy.IsEnabled() {
		t.Error("expected proxy.enabled to be overridden to true")
	}
	if result.Proxy.Port != 9090 {
		t.Errorf("expected port 9090, got %d", result.Proxy.Port)
	}
}

func Test_mergeConfigs_ToolsDeepMerge(t *testing.T) {
	base := &Config{
		Tools: map[string]any{
			"git":  map[string]any{"mode": "readonly"},
			"mise": map[string]any{"writable": false},
		},
	}
	overlay := &Config{
		Tools: map[string]any{
			"git": map[string]any{"mode": "readwrite"},
		},
	}

	result := mergeConfigs(base, overlay)

	gitCfg := result.GetToolConfig("git")
	if gitCfg["mode"] != "readwrite" {
		t.Errorf("expected git mode 'readwrite', got %v", gitCfg["mode"])
	}

	miseCfg := result.GetToolConfig("mise")
	if miseCfg == nil {
		t.Error("expected mise config to be preserved from base")
	}
}

func Test_mergeConfigs_ArrayConcat(t *testing.T) {
	base := &Config{
		Proxy: ProxyConfig{
			Filter: ProxyFilterConfig{
				Rules: []ProxyFilterRule{
					{Pattern: "*.github.com", Action: "allow"},
				},
			},
		},
	}
	overlay := &Config{
		Proxy: ProxyConfig{
			Filter: ProxyFilterConfig{
				Rules: []ProxyFilterRule{
					{Pattern: "*.npm.com", Action: "allow"},
				},
			},
		},
	}

	result := mergeConfigs(base, overlay)

	if len(result.Proxy.Filter.Rules) != 2 {
		t.Errorf("expected 2 rules, got %d", len(result.Proxy.Filter.Rules))
	}
	// Overlay rules come first (higher priority)
	if result.Proxy.Filter.Rules[0].Pattern != "*.npm.com" {
		t.Error("expected overlay rule first")
	}
	if result.Proxy.Filter.Rules[1].Pattern != "*.github.com" {
		t.Error("expected base rule second")
	}
}

func Test_mergeConfigs_NilOverlay(t *testing.T) {
	base := &Config{
		Proxy: ProxyConfig{Port: 8080},
	}

	result := mergeConfigs(base, nil)

	if result.Proxy.Port != 8080 {
		t.Error("expected base config unchanged with nil overlay")
	}
}

func Test_mergeConfigs_ExplicitFalseOverridesTrue(t *testing.T) {
	base := &Config{
		Proxy: ProxyConfig{
			Enabled: boolPtr(true),
			Port:    8080,
		},
	}
	// Explicit false should override true
	overlay := &Config{
		Proxy: ProxyConfig{
			Enabled: boolPtr(false),
		},
	}

	result := mergeConfigs(base, overlay)

	if result.Proxy.IsEnabled() {
		t.Error("explicit false should override true")
	}
	// Port should remain from base since overlay is 0
	if result.Proxy.Port != 8080 {
		t.Errorf("expected port 8080, got %d", result.Proxy.Port)
	}
}

func Test_mergeConfigs_NilValuesNotOverride(t *testing.T) {
	base := &Config{
		Proxy: ProxyConfig{
			Enabled: boolPtr(true),
			Port:    9090,
		},
	}
	// Overlay with nil/zero values should not override
	overlay := &Config{
		Proxy: ProxyConfig{
			// Enabled: nil (not set)
			// Port: 0 (zero value)
		},
	}

	result := mergeConfigs(base, overlay)

	if !result.Proxy.IsEnabled() {
		t.Error("nil value should not override true")
	}
	if result.Proxy.Port != 9090 {
		t.Error("zero value should not override non-zero port")
	}
}

func Test_mergeConfigs_DockerDockerfile(t *testing.T) {
	base := &Config{
		Sandbox: SandboxConfig{
			Docker: DockerConfig{
				Dockerfile: "/global/Dockerfile",
			},
		},
	}
	overlay := &Config{
		Sandbox: SandboxConfig{
			Docker: DockerConfig{
				Dockerfile: "/project/Dockerfile",
			},
		},
	}

	result := mergeConfigs(base, overlay)

	if result.Sandbox.Docker.Dockerfile != "/project/Dockerfile" {
		t.Errorf("expected dockerfile '/project/Dockerfile', got %q", result.Sandbox.Docker.Dockerfile)
	}
}

func Test_mergeConfigs_DockerDockerfileNotOverriddenByEmpty(t *testing.T) {
	base := &Config{
		Sandbox: SandboxConfig{
			Docker: DockerConfig{
				Dockerfile: "/global/Dockerfile",
			},
		},
	}
	overlay := &Config{}

	result := mergeConfigs(base, overlay)

	if result.Sandbox.Docker.Dockerfile != "/global/Dockerfile" {
		t.Errorf("expected dockerfile preserved from base, got %q", result.Sandbox.Docker.Dockerfile)
	}
}
