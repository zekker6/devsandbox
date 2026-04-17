// internal/config/merge_test.go
package config

import (
	"testing"

	"devsandbox/internal/source"
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

func Test_mergeConfigs_CredentialsDeepMerge(t *testing.T) {
	base := &Config{
		Proxy: ProxyConfig{
			Credentials: map[string]any{
				"github": map[string]any{"enabled": true},
				"gitlab": map[string]any{"enabled": false},
			},
		},
	}
	overlay := &Config{
		Proxy: ProxyConfig{
			Credentials: map[string]any{
				"github": map[string]any{"enabled": false},
			},
		},
	}

	result := mergeConfigs(base, overlay)

	ghCfg, ok := result.Proxy.Credentials["github"].(map[string]any)
	if !ok {
		t.Fatal("expected github credentials config")
	}
	if ghCfg["enabled"] != false {
		t.Errorf("expected github enabled=false from overlay, got %v", ghCfg["enabled"])
	}

	glCfg, ok := result.Proxy.Credentials["gitlab"].(map[string]any)
	if !ok {
		t.Fatal("expected gitlab credentials config preserved from base")
	}
	if glCfg["enabled"] != false {
		t.Errorf("expected gitlab enabled=false preserved, got %v", glCfg["enabled"])
	}
}

func Test_mergeConfigs_CredentialsNilBase(t *testing.T) {
	base := &Config{}
	overlay := &Config{
		Proxy: ProxyConfig{
			Credentials: map[string]any{
				"github": map[string]any{"enabled": true},
			},
		},
	}

	result := mergeConfigs(base, overlay)

	if result.Proxy.Credentials == nil {
		t.Fatal("expected credentials from overlay")
	}
	ghCfg, ok := result.Proxy.Credentials["github"].(map[string]any)
	if !ok {
		t.Fatal("expected github credentials config")
	}
	if ghCfg["enabled"] != true {
		t.Errorf("expected github enabled=true, got %v", ghCfg["enabled"])
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

func TestMergeConfigs_ProxyExtraEnv(t *testing.T) {
	base := DefaultConfig()
	base.Proxy.ExtraEnv = []string{"BASE_PROXY"}

	overlay := &Config{
		Proxy: ProxyConfig{
			ExtraEnv: []string{"OVERLAY_PROXY"},
		},
	}

	result := mergeConfigs(base, overlay)

	// Overlay prepends (higher priority, consistent with filter rules)
	if len(result.Proxy.ExtraEnv) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(result.Proxy.ExtraEnv), result.Proxy.ExtraEnv)
	}
	if result.Proxy.ExtraEnv[0] != "OVERLAY_PROXY" {
		t.Errorf("expected overlay entry first, got %q", result.Proxy.ExtraEnv[0])
	}
	if result.Proxy.ExtraEnv[1] != "BASE_PROXY" {
		t.Errorf("expected base entry second, got %q", result.Proxy.ExtraEnv[1])
	}
}

func TestMergeConfigs_ProxyExtraCAEnv(t *testing.T) {
	base := DefaultConfig()
	base.Proxy.ExtraCAEnv = []string{"BASE_CA"}

	overlay := &Config{
		Proxy: ProxyConfig{
			ExtraCAEnv: []string{"OVERLAY_CA"},
		},
	}

	result := mergeConfigs(base, overlay)

	if len(result.Proxy.ExtraCAEnv) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(result.Proxy.ExtraCAEnv), result.Proxy.ExtraCAEnv)
	}
	if result.Proxy.ExtraCAEnv[0] != "OVERLAY_CA" {
		t.Errorf("expected overlay entry first, got %q", result.Proxy.ExtraCAEnv[0])
	}
	if result.Proxy.ExtraCAEnv[1] != "BASE_CA" {
		t.Errorf("expected base entry second, got %q", result.Proxy.ExtraCAEnv[1])
	}
}

func TestMergeConfigs_SandboxEnvPassthrough(t *testing.T) {
	base := DefaultConfig()
	base.Sandbox.EnvPassthrough = []string{"BASE_VAR"}

	overlay := &Config{
		Sandbox: SandboxConfig{
			EnvPassthrough: []string{"OVERLAY_VAR"},
		},
	}

	result := mergeConfigs(base, overlay)

	if len(result.Sandbox.EnvPassthrough) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(result.Sandbox.EnvPassthrough), result.Sandbox.EnvPassthrough)
	}
	if result.Sandbox.EnvPassthrough[0] != "OVERLAY_VAR" {
		t.Errorf("expected overlay entry first, got %q", result.Sandbox.EnvPassthrough[0])
	}
	if result.Sandbox.EnvPassthrough[1] != "BASE_VAR" {
		t.Errorf("expected base entry second, got %q", result.Sandbox.EnvPassthrough[1])
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

func Test_mergeConfigs_MITMOverlay(t *testing.T) {
	base := &Config{
		Proxy: ProxyConfig{
			MITM: boolPtr(true),
		},
	}
	overlay := &Config{
		Proxy: ProxyConfig{
			MITM: boolPtr(false),
		},
	}

	result := mergeConfigs(base, overlay)

	if result.Proxy.IsMITMEnabled() {
		t.Error("overlay MITM=false should override base MITM=true")
	}
}

func Test_mergeConfigs_MITMNilNotOverride(t *testing.T) {
	base := &Config{
		Proxy: ProxyConfig{
			MITM: boolPtr(false),
		},
	}
	overlay := &Config{
		Proxy: ProxyConfig{
			// MITM: nil (not set)
		},
	}

	result := mergeConfigs(base, overlay)

	if result.Proxy.IsMITMEnabled() {
		t.Error("nil overlay should not override base MITM=false")
	}
}

func TestMergeConfigs_SandboxEnvironment(t *testing.T) {
	base := &Config{}
	base.Sandbox.Environment = map[string]source.Source{
		"ONLY_BASE": {Value: "base"},
		"SHARED":    {Value: "base-value"},
	}
	overlay := &Config{
		Sandbox: SandboxConfig{
			Environment: map[string]source.Source{
				"ONLY_OVERLAY": {Value: "overlay"},
				"SHARED":       {Value: "overlay-value"},
			},
		},
	}

	result := mergeConfigs(base, overlay)

	got := result.Sandbox.Environment
	if len(got) != 3 {
		t.Fatalf("expected 3 keys, got %d: %+v", len(got), got)
	}
	if got["ONLY_BASE"].Value != "base" {
		t.Errorf("ONLY_BASE: %+v", got["ONLY_BASE"])
	}
	if got["ONLY_OVERLAY"].Value != "overlay" {
		t.Errorf("ONLY_OVERLAY: %+v", got["ONLY_OVERLAY"])
	}
	if got["SHARED"].Value != "overlay-value" {
		t.Errorf("SHARED: %+v (expected overlay to win)", got["SHARED"])
	}
}
