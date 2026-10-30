package isolator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"devsandbox/internal/sandbox/tools"
)

func TestSeatbeltSharedInstallReadOnly(t *testing.T) {
	cfg := nativeMiseTestConfig(t)
	payload := filepath.Join(".local", "share", "mise", "installs", "node", "22.0.0", "data")
	nativeWrite(t, filepath.Join(cfg.HomeDir, payload), "host payload")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := `/bin/cat "$HOME/.local/share/mise/installs/node/22.0.0/data" >/dev/null || exit 11; if (printf changed > "$HOME/.local/share/mise/installs/node/22.0.0/data") 2>/dev/null; then exit 12; fi`
	if err := NewSeatbeltIsolator(SeatbeltConfig{}).Run(ctx, &RunConfig{SandboxCfg: cfg, Command: []string{"/bin/sh", "-c", command}}); err != nil {
		t.Fatal(err)
	}
	if got := nativeRead(t, filepath.Join(cfg.HomeDir, payload)); got != "host payload" {
		t.Fatalf("host payload changed: %q", got)
	}
}

func TestSeatbeltRunExitStatus(t *testing.T) {
	for _, code := range []int{0, 37} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			cfg := nativeTestConfig(t)
			cfg.ToolsConfig = map[string]any{}
			for _, tool := range tools.All() {
				cfg.ToolsConfig[tool.Name()] = map[string]any{"mount_mode": "disabled"}
			}
			for _, name := range []string{"readable", "secret"} {
				if err := os.WriteFile(filepath.Join(cfg.ProjectDir, name), []byte("data"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink("secret", filepath.Join(cfg.ProjectDir, ".ENV")); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			err := NewSeatbeltIsolator(SeatbeltConfig{}).Run(ctx, &RunConfig{
				SandboxCfg: cfg, Command: []string{"/bin/sh", "-c", "/bin/cat readable >/dev/null || exit 98; if /bin/cat secret >/dev/null 2>&1; then exit 99; fi; exit " + strconv.Itoa(code)},
			})
			if code == 0 {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var exit *CommandExitError
				if !errors.As(err, &exit) || exit.Code != code {
					t.Fatalf("exit status = %v, want %d", err, code)
				}
			}
			if _, err := os.Stat(cfg.SandboxRoot + "/native-setup"); !os.IsNotExist(err) {
				t.Fatalf("setup snapshot not removed: %v", err)
			}
		})
	}
}
