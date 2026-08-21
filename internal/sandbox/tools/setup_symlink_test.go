package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSetup_DestinationIsSymlink pins that every tool Setup writes through the
// destination *name*, never through a link found there.
//
// sandboxHome is bind-mounted read-write into the sandbox at homeDir, and none
// of these destinations is shadowed by a binding at its own path, so a session
// can leave a symlink at one of them pointing at any host file. Following it
// truncated and overwrote that file on the next launch - the same primitive
// TestGit_Setup_ReadOnlyMode_RepoConfigDestinationIsSymlink covers for git.
func TestSetup_DestinationIsSymlink(t *testing.T) {
	tests := []struct {
		name string
		tool func() ToolWithSetup
		// seed creates whatever host state makes Setup reach its write.
		seed func(t *testing.T, homeDir string)
		dest func(sandboxHome string) string
	}{
		{
			name: "starship",
			tool: func() ToolWithSetup { return &Starship{} },
			seed: func(t *testing.T, homeDir string) {
				if err := os.MkdirAll(filepath.Join(homeDir, ".config"), 0o755); err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(homeDir, ".config", "starship.toml"), "[character]\n")
			},
			dest: func(sandboxHome string) string {
				return filepath.Join(sandboxHome, ".config", "starship.toml")
			},
		},
		{
			name: "tmux",
			tool: func() ToolWithSetup { return &Tmux{} },
			seed: func(t *testing.T, homeDir string) {
				writeFile(t, filepath.Join(homeDir, ".tmux.conf"), "set -g mouse on\n")
			},
			dest: func(sandboxHome string) string {
				return filepath.Join(sandboxHome, ".tmux.conf")
			},
		},
		{
			name: "powerlevel10k",
			tool: func() ToolWithSetup { return &Powerlevel10k{} },
			seed: func(t *testing.T, homeDir string) {
				writeFile(t, filepath.Join(homeDir, ".p10k.zsh"), "# p10k\n")
			},
			dest: func(sandboxHome string) string {
				return filepath.Join(sandboxHome, ".p10k.zsh")
			},
		},
		{
			name: "ohmyzsh",
			tool: func() ToolWithSetup { return &OhMyZsh{} },
			seed: func(t *testing.T, homeDir string) {
				if err := os.MkdirAll(filepath.Join(homeDir, ".oh-my-zsh"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			dest: func(sandboxHome string) string {
				return filepath.Join(sandboxHome, ".oh-my-zsh-custom", "plugins", "devsandbox", "devsandbox.plugin.zsh")
			},
		},
		{
			name: "portal",
			tool: func() ToolWithSetup { return &Portal{notifications: true} },
			seed: func(t *testing.T, homeDir string) {},
			dest: func(sandboxHome string) string {
				return filepath.Join(sandboxHome, ".flatpak-info")
			},
		},
	}

	const victimContent = "host file the sandbox must not reach\n"

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			homeDir := filepath.Join(tmpDir, "home")
			sandboxHome := filepath.Join(tmpDir, "sandbox")
			for _, d := range []string{homeDir, sandboxHome} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			tt.seed(t, homeDir)

			victim := filepath.Join(tmpDir, "victim")
			writeFile(t, victim, victimContent)

			dest := tt.dest(sandboxHome)
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, dest); err != nil {
				t.Fatalf("symlink: %v", err)
			}

			if err := tt.tool().Setup(homeDir, sandboxHome); err != nil {
				t.Fatalf("Setup failed: %v", err)
			}

			if got, err := os.ReadFile(victim); err != nil || string(got) != victimContent {
				t.Errorf("the host file was written through the symlink: content = %q, err = %v", got, err)
			}

			info, err := os.Lstat(dest)
			if err != nil {
				t.Fatalf("Lstat(%s): %v", dest, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				t.Error("the symlink is still in place, so the binding would mount whatever it names")
			}
		})
	}
}
