package isolator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNativeDeveloperDirCommand(t *testing.T) {
	for _, name := range []string{"DYLD_INSERT_LIBRARIES", "BASH_ENV", "GIT_CONFIG_GLOBAL", "UNRELATED_SECRET"} {
		t.Setenv(name, "/untrusted")
	}
	for _, developerDir := range []string{"", "/Applications/Alternate Xcode.app/Contents/Developer"} {
		t.Run(developerDir, func(t *testing.T) {
			t.Setenv("DEVELOPER_DIR", developerDir)
			cmd := nativeDeveloperDirCommand(t.Context())
			if cmd.Path != "/usr/bin/xcode-select" || cmd.Dir != "/" ||
				!reflect.DeepEqual(cmd.Args, []string{"/usr/bin/xcode-select", "-p"}) {
				t.Fatalf("unsafe developer-directory query: path=%q dir=%q args=%q", cmd.Path, cmd.Dir, cmd.Args)
			}
			want := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
			if developerDir != "" {
				want = append(want, "DEVELOPER_DIR="+developerDir)
			}
			if !reflect.DeepEqual(cmd.Env, want) {
				t.Fatalf("query environment = %q, want %q", cmd.Env, want)
			}
		})
	}
}

func TestPrepareNativeDeveloperDir(t *testing.T) {
	root := t.TempDir()
	developerDir := filepath.Join(root, "Developer Tools ")
	if err := os.Mkdir(developerDir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, output, wantErr string
	}{
		{name: "selected directory", output: developerDir + "\n"},
		{name: "empty", output: "\n", wantErr: "absolute"},
		{name: "relative", output: "relative/path\n", wantErr: "absolute"},
		{name: "multiple lines", output: developerDir + "\nextra\n", wantErr: "single path"},
		{name: "missing", output: filepath.Join(root, "missing") + "\n", wantErr: "stat"},
		{name: "file", output: file + "\n", wantErr: "not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"HOME": "/private-home"}
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", `printf '%s' "$1"`, "developer-dir-test", tc.output)
			err := prepareNativeDeveloperDir(env, cmd)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				if _, exists := env["DEVELOPER_DIR"]; exists {
					t.Fatal("failed discovery changed developer-directory selection")
				}
			} else if err != nil || env["DEVELOPER_DIR"] != developerDir {
				t.Fatalf("selection = %q, error = %v", env["DEVELOPER_DIR"], err)
			}
			if env["HOME"] != "/private-home" {
				t.Fatal("discovery changed unrelated environment")
			}
		})
	}
}

func TestPrepareNativeDeveloperDirPreservesExplicitSelection(t *testing.T) {
	for _, selection := range []string{"", "/configured/developer"} {
		env := map[string]string{"DEVELOPER_DIR": selection}
		cmd := exec.CommandContext(t.Context(), "/nonexistent/xcode-select")
		if err := prepareNativeDeveloperDir(env, cmd); err != nil {
			t.Fatal(err)
		}
		if cmd.Process != nil || env["DEVELOPER_DIR"] != selection {
			t.Fatal("explicit selection was queried or replaced")
		}
	}
}

func TestPrepareNativeDeveloperDirReportsFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  *exec.Cmd
		want string
	}{
		{"failed query", exec.CommandContext(t.Context(), "/bin/sh", "-c", "printf 'no selection\\n' >&2; exit 2"), "no selection"},
		{"missing executable", exec.CommandContext(t.Context(), "/nonexistent/xcode-select"), "xcode-select"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := make(map[string]string)
			err := prepareNativeDeveloperDir(env, tc.cmd)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if len(env) != 0 {
				t.Fatalf("failed discovery changed environment: %v", env)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := prepareNativeDeveloperDir(make(map[string]string), exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")); err == nil {
		t.Fatal("canceled query succeeded")
	}
}
