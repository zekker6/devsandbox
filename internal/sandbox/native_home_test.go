package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeHomeInitializationConfinesSymlinks(t *testing.T) {
	for _, linkHome := range []bool{false, true} {
		t.Run(map[bool]string{false: "nested link", true: "home link"}[linkHome], func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "sandbox")
			home := filepath.Join(root, "home")
			outside := filepath.Join(base, "outside")
			for _, dir := range []string{root, outside} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			link := home
			if !linkHome {
				if err := os.Mkdir(home, 0o700); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(home, ".cache")
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			cfg := &Config{SandboxRoot: root, SandboxHome: home, Isolation: IsolationSeatbelt}
			if err := cfg.EnsureSandboxDirs(); err == nil {
				t.Fatal("followed sandbox symlink during host setup")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("wrote outside private home: %v, %v", entries, err)
			}
		})
	}
}

func TestNativeHomeInitializationCreatesPrivateDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox")
	cfg := &Config{SandboxRoot: root, SandboxHome: filepath.Join(root, "home"), Isolation: IsolationSeatbelt}
	if err := cfg.EnsureSandboxDirs(); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".", ".config", ".cache/go-build", ".cache/go-mod", ".local/state", "go"} {
		info, err := os.Stat(filepath.Join(cfg.SandboxHome, rel))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("private directory %s: %v, %v", rel, info, err)
		}
	}
	if err := cfg.EnsureSandboxDirs(); err != nil {
		t.Fatalf("repeat initialization: %v", err)
	}
}
