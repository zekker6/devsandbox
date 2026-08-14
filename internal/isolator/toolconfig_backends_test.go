package isolator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devsandbox/internal/sandbox"
)

// fishFunctionsHome builds a home directory holding just enough for the
// shell-fish tool to be available, and returns the home and the source path
// both backends resolve a mount mode for.
func fishFunctionsHome(t *testing.T) (homeDir, src string) {
	t.Helper()
	homeDir = filepath.Join(t.TempDir(), "home", "testuser")
	src = filepath.Join(homeDir, ".config", "fish", "functions")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "hello.fish"), []byte("function hello\nend\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return homeDir, src
}

// bwrapMountMode classifies what the bwrap builder did with src, in the shared
// vocabulary the two backends are compared in.
func bwrapMountMode(args []string, src string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i+1] != src {
			continue
		}
		switch args[i] {
		case "--ro-bind":
			return "readonly"
		case "--bind":
			return "readwrite"
		case "--overlay-src":
			return "overlay"
		}
	}
	return "absent"
}

// dockerMountMode classifies the same for the docker isolator. A tmpoverlay
// directory reaches the container as a read-only mount at a shadow path plus a
// copyoverlay manifest entry, so the manifest is what distinguishes it from a
// plain read-only bind.
func dockerMountMode(mounts []string, manifest *OverlayManifest, src string) string {
	for _, m := range mounts {
		if !strings.HasPrefix(m, src+":") {
			continue
		}
		parts := strings.Split(m, ":")
		dest := parts[1]
		for _, o := range manifest.Overlays {
			if o.Source == dest {
				return "overlay"
			}
		}
		if len(parts) > 2 && parts[2] == "ro" {
			return "readonly"
		}
		return "readwrite"
	}
	return "absent"
}

// TestToolConfig_BackendsResolveTheSameMountMode is the drift guard for the
// tool-config lookup: the two backends read tools.<name>.mount_mode through the
// same helper, and a change that reaches one of them alone would give the same
// config file different mount behavior per backend.
func TestToolConfig_BackendsResolveTheSameMountMode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		toolsConfig map[string]any
		defaultMode string
		want        string
	}{
		{
			name:        "readonly",
			toolsConfig: map[string]any{"shell-fish": map[string]any{"mount_mode": "readonly"}},
			want:        "readonly",
		},
		{
			name:        "readwrite",
			toolsConfig: map[string]any{"shell-fish": map[string]any{"mount_mode": "readwrite"}},
			want:        "readwrite",
		},
		{
			name:        "disabled",
			toolsConfig: map[string]any{"shell-fish": map[string]any{"mount_mode": "disabled"}},
			want:        "absent",
		},
		{
			name:        "tmpoverlay",
			toolsConfig: map[string]any{"shell-fish": map[string]any{"mount_mode": "tmpoverlay"}},
			want:        "overlay",
		},
		{
			name:        "unset falls through to the global default",
			toolsConfig: map[string]any{"shell-fish": map[string]any{}},
			defaultMode: "readonly",
			want:        "readonly",
		},
		{
			name:        "section absent falls through to the global default",
			toolsConfig: map[string]any{"git": map[string]any{"mount_mode": "disabled"}},
			defaultMode: "readwrite",
			want:        "readwrite",
		},
		{
			name:        "section is not a table",
			toolsConfig: map[string]any{"shell-fish": "readonly"},
			defaultMode: "readwrite",
			want:        "readwrite",
		},
		{
			name:        "mount_mode is not a string",
			toolsConfig: map[string]any{"shell-fish": map[string]any{"mount_mode": 42}},
			defaultMode: "readwrite",
			want:        "readwrite",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			homeDir, src := fishFunctionsHome(t)
			sandboxHome := filepath.Join(t.TempDir(), "sandbox")
			if err := os.MkdirAll(sandboxHome, 0o755); err != nil {
				t.Fatal(err)
			}
			projectDir := filepath.Join(t.TempDir(), "project")
			if err := os.MkdirAll(projectDir, 0o755); err != nil {
				t.Fatal(err)
			}

			b := sandbox.NewBuilder(&sandbox.Config{
				HomeDir:          homeDir,
				SandboxHome:      sandboxHome,
				ProjectDir:       projectDir,
				DefaultMountMode: tc.defaultMode,
				ToolsConfig:      tc.toolsConfig,
			})
			b.AddTools()
			if err := b.Err(); err != nil {
				t.Fatalf("AddTools: %v", err)
			}
			bwrapGot := bwrapMountMode(b.Build(), src)

			iso := NewDockerIsolator(DockerConfig{})
			mounts, _, manifest := iso.getToolBindings(&Config{
				HomeDir:          homeDir,
				SandboxHome:      sandboxHome,
				ProjectDir:       projectDir,
				Shell:            "fish",
				DefaultMountMode: tc.defaultMode,
				ToolsConfig:      tc.toolsConfig,
			})
			dockerGot := dockerMountMode(mounts, manifest, src)

			if bwrapGot != dockerGot {
				t.Errorf("backends disagree on %s: bwrap %q, docker %q", src, bwrapGot, dockerGot)
			}
			if bwrapGot != tc.want {
				t.Errorf("bwrap resolved %q, want %q", bwrapGot, tc.want)
			}
			if dockerGot != tc.want {
				t.Errorf("docker resolved %q, want %q", dockerGot, tc.want)
			}
		})
	}
}
