package config

import "testing"

// The tools map is untyped - it comes out of the TOML decoder as
// map[string]any - so every failure path has to yield the empty value rather
// than a panic. Both backends and the config accessor share this lookup, so a
// case handled here is handled everywhere.
func TestToolSection(t *testing.T) {
	tools := map[string]any{
		"git":    map[string]any{"mount_mode": "readonly", "mode": "safe"},
		"mise":   map[string]any{"ignore_global_config": true},
		"broken": "not-a-table",
		"nested": map[string]any{"mount_mode": 42},
	}

	for _, tc := range []struct {
		name  string
		tools map[string]any
		tool  string
		want  map[string]any
	}{
		{name: "nil tools map", tools: nil, tool: "git", want: nil},
		{name: "missing section", tools: tools, tool: "absent", want: nil},
		{name: "section is not a table", tools: tools, tool: "broken", want: nil},
		{
			name:  "section present",
			tools: tools,
			tool:  "git",
			want:  map[string]any{"mount_mode": "readonly", "mode": "safe"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ToolSection(tc.tools, tc.tool)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("ToolSection(%q) = %v, want nil", tc.tool, got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("ToolSection(%q) = %v, want %v", tc.tool, got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("ToolSection(%q)[%q] = %v, want %v", tc.tool, k, got[k], v)
				}
			}
		})
	}
}

func TestToolMountMode(t *testing.T) {
	tools := map[string]any{
		"git":      map[string]any{"mount_mode": "readonly"},
		"mise":     map[string]any{"ignore_global_config": true},
		"broken":   "not-a-table",
		"numeric":  map[string]any{"mount_mode": 42},
		"disabled": map[string]any{"mount_mode": "disabled"},
	}

	for _, tc := range []struct {
		name  string
		tools map[string]any
		tool  string
		want  string
	}{
		{name: "nil tools map", tools: nil, tool: "git", want: ""},
		{name: "missing section", tools: tools, tool: "absent", want: ""},
		{name: "section is not a table", tools: tools, tool: "broken", want: ""},
		{name: "section without mount_mode", tools: tools, tool: "mise", want: ""},
		{name: "mount_mode is not a string", tools: tools, tool: "numeric", want: ""},
		{name: "mount_mode set", tools: tools, tool: "git", want: "readonly"},
		{name: "mount_mode disabled", tools: tools, tool: "disabled", want: "disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolMountMode(tc.tools, tc.tool); got != tc.want {
				t.Errorf("ToolMountMode(%q) = %q, want %q", tc.tool, got, tc.want)
			}
		})
	}
}

// GetToolConfig is the *Config-receiver spelling of the same lookup, and must
// stay identical to it - the accessor and the two backends resolving a section
// differently for the same file is the drift this shares one definition to stop.
func TestGetToolConfig_MatchesToolSection(t *testing.T) {
	cfg := &Config{Tools: map[string]any{
		"git":    map[string]any{"mount_mode": "readonly"},
		"broken": "not-a-table",
	}}

	for _, tool := range []string{"git", "broken", "absent"} {
		viaConfig := cfg.GetToolConfig(tool)
		viaHelper := ToolSection(cfg.Tools, tool)
		if len(viaConfig) != len(viaHelper) {
			t.Fatalf("%s: GetToolConfig = %v, ToolSection = %v", tool, viaConfig, viaHelper)
		}
		for k, v := range viaHelper {
			if viaConfig[k] != v {
				t.Errorf("%s: GetToolConfig[%q] = %v, ToolSection[%q] = %v", tool, k, viaConfig[k], k, v)
			}
		}
	}
}
