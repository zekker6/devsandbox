package isolator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The git tool spells the destinations of the files it generates with the host
// home prefix, because bwrap binds the sandbox home there. This backend mounts
// it at /home/sandboxuser, so a destination left unrewritten lands where the
// guest's git never looks - and git ignores a missing gitconfig, excludesFile
// or attributesFile with exit 0 and no warning, so the failure is silent.
func TestGetToolBindings_HomeRelativeDestsUseContainerHome(t *testing.T) {
	homeDir := t.TempDir()
	ignore := filepath.Join(homeDir, "ignore-global")
	if err := os.WriteFile(ignore, []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitconfig := "[user]\n\tname = Ada\n\temail = ada@example.com\n[core]\n\texcludesFile = " + ignore + "\n"
	if err := os.WriteFile(filepath.Join(homeDir, ".gitconfig"), []byte(gitconfig), 0o644); err != nil {
		t.Fatal(err)
	}

	sandboxHome := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(sandboxHome, 0o755); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(filepath.Join(projectDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	iso := NewDockerIsolator(DockerConfig{})
	mounts, _, _ := iso.getToolBindings(&Config{
		HomeDir:     homeDir,
		SandboxHome: sandboxHome,
		ProjectDir:  projectDir,
		Shell:       "bash",
	})

	// Keyed on the generated file each mount carries, since the destination is
	// exactly what is under test.
	destOf := make(map[string]string, len(mounts))
	for _, m := range mounts {
		src, rest, ok := strings.Cut(m, ":")
		if !ok {
			continue
		}
		dest, _, _ := strings.Cut(rest, ":")
		destOf[src] = dest
	}

	for src, want := range map[string]string{
		filepath.Join(sandboxHome, ".gitconfig.safe"): containerHome + "/.gitconfig",
		filepath.Join(sandboxHome, ".gitignore.safe"): containerHome + "/.gitignore.safe",
	} {
		got, ok := destOf[src]
		if !ok {
			t.Errorf("%s is not mounted at all, got %v", src, mounts)
			continue
		}
		if got != want {
			t.Errorf("%s mounted at %q, want %q - the guest reads its git config under %s", src, got, want, containerHome)
		}
	}
}

func TestRemapHomePrefix(t *testing.T) {
	for _, tc := range []struct {
		name, dest, homeDir, want string
	}{
		{"file under home", "/home/u/.gitconfig", "/home/u", containerHome + "/.gitconfig"},
		{"nested under home", "/home/u/.config/git/ignore", "/home/u", containerHome + "/.config/git/ignore"},
		{"home itself", "/home/u", "/home/u", containerHome},
		{"outside home", "/etc/gitconfig", "/home/u", "/etc/gitconfig"},
		{"prefix is not a path component", "/home/user2/.gitconfig", "/home/u", "/home/user2/.gitconfig"},
		{"empty home", "/home/u/.gitconfig", "", "/home/u/.gitconfig"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := remapHomePrefix(tc.dest, tc.homeDir); got != tc.want {
				t.Errorf("remapHomePrefix(%q, %q) = %q, want %q", tc.dest, tc.homeDir, got, tc.want)
			}
		})
	}
}
