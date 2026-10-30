package main

import (
	"os"
	"path/filepath"
	"testing"

	"devsandbox/internal/sandbox"
)

func TestProjectStateLookupsPreserveLegacyNames(t *testing.T) {
	for _, tc := range []struct {
		name       string
		legacy     bool
		customBase bool
	}{
		{name: "new default"},
		{name: "legacy default", legacy: true},
		{name: "new custom", customBase: true},
		{name: "legacy custom", legacy: true, customBase: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := prepareHome(t)
			base := sandbox.SandboxBasePath(home)
			if tc.customBase {
				base = filepath.Join(home, "custom-sandboxes")
				writeGlobalConfig(t, home, "[sandbox]\nbase_path = \""+base+"\"\n")
			}
			projectDir := filepath.Join(home, "victorialogs-datasource")
			if err := os.Mkdir(projectDir, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(projectDir)
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			name := sandbox.GenerateSandboxName(cwd)
			if tc.legacy {
				name = sandbox.GenerateProjectName(cwd)
			}
			want := filepath.Join(base, name)
			if tc.legacy {
				if err := os.MkdirAll(want, 0o700); err != nil {
					t.Fatal(err)
				}
			}

			cfg, err := sandbox.NewConfig(&sandbox.Options{BasePath: base})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.SandboxRoot != want {
				t.Errorf("launch root = %q, want %q", cfg.SandboxRoot, want)
			}
			if root, err := resolveSandboxBase(); err != nil || root != want {
				t.Errorf("monitor root = %q, err=%v, want %q", root, err, want)
			}
			if logDir, err := resolveLogDir("", ""); err != nil || logDir != filepath.Join(want, "logs", "proxy") {
				t.Errorf("filter log directory = %q, err=%v, want logs under %q", logDir, err, want)
			}
			if !tc.customBase {
				if root, err := resolveLogsSandboxRoot(home, ""); err != nil || root != want {
					t.Errorf("logs root = %q, err=%v, want %q", root, err, want)
				}
				if root, err := resolveLogsSandboxRoot(home, "explicit-name"); err != nil || root != filepath.Join(base, "explicit-name") {
					t.Errorf("explicit logs root = %q, err=%v", root, err)
				}
			}
		})
	}
}
