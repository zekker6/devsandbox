package isolator

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"devsandbox/internal/sandbox"
	"devsandbox/internal/sandbox/tools"
)

func nativeMiseTestConfig(t *testing.T) *sandbox.Config {
	t.Helper()
	cfg := nativeTestConfig(t)
	cfg.ToolsConfig = make(map[string]any)
	for _, tool := range tools.All() {
		if tool.Name() != "mise" {
			cfg.ToolsConfig[tool.Name()] = map[string]any{"mount_mode": "disabled"}
		}
	}
	bin := filepath.Join(cfg.HomeDir, ".local", "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "mise"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return cfg
}

func TestPrepareNativeSharesInstallsNotConfiguration(t *testing.T) {
	for _, mode := range []string{"split", "overlay", "tmpoverlay", "readonly"} {
		t.Run(mode, func(t *testing.T) {
			cfg := nativeMiseTestConfig(t)
			cfg.DefaultMountMode = mode
			binary := filepath.Join(".local", "share", "mise", "installs", "node", "22.0.0", "bin", "node")
			config := filepath.Join(".config", "mise", "config.toml")
			nativeWrite(t, filepath.Join(cfg.HomeDir, binary), "host executable")
			nativeWrite(t, filepath.Join(cfg.HomeDir, config), "host configuration")
			plan, err := prepareNative(&RunConfig{SandboxCfg: cfg})
			if err != nil {
				t.Fatal(err)
			}
			hostInfo, err := os.Stat(filepath.Join(cfg.HomeDir, binary))
			if err != nil {
				t.Fatal(err)
			}
			privateInfo, err := os.Stat(filepath.Join(cfg.SandboxHome, binary))
			if err != nil {
				t.Fatal(err)
			}
			shared := mode == "split" || mode == "readonly"
			if os.SameFile(hostInfo, privateInfo) != shared {
				t.Fatalf("mode %s: installation sharing = %v", mode, !shared)
			}
			hostConfig, err := os.Stat(filepath.Join(cfg.HomeDir, config))
			if err != nil {
				t.Fatal(err)
			}
			privateConfig, err := os.Stat(filepath.Join(cfg.SandboxHome, config))
			if err != nil {
				t.Fatal(err)
			}
			if os.SameFile(hostConfig, privateConfig) {
				t.Fatal("configuration was shared instead of copied")
			}
			if shared {
				installs := filepath.Join(cfg.HomeDir, ".local", "share", "mise")
				if !slices.Contains(plan.policy.ReadOnly, installs) {
					t.Fatal("host installation is not protected by the policy")
				}
			}
		})
	}
}

func TestPrepareNativeKeepsPrivateVersionsAndSeedsNewOnes(t *testing.T) {
	cfg := nativeMiseTestConfig(t)
	root := filepath.Join(".local", "share", "mise", "installs", "node")
	old := filepath.Join(root, "22.0.0")
	nativeWrite(t, filepath.Join(cfg.HomeDir, old, "node"), "host")
	nativeWrite(t, filepath.Join(cfg.SandboxHome, old, "node"), "private")
	if _, err := prepareNative(&RunConfig{SandboxCfg: cfg}); err != nil {
		t.Fatal(err)
	}
	if got := nativeRead(t, filepath.Join(cfg.SandboxHome, old, "node")); got != "private" {
		t.Fatalf("replaced private install: %s", got)
	}
	fresh := filepath.Join(root, "24.0.0")
	nativeWrite(t, filepath.Join(cfg.HomeDir, fresh, "node"), "new host version")
	if _, err := prepareNative(&RunConfig{SandboxCfg: cfg}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(filepath.Join(cfg.SandboxHome, fresh)); err != nil {
		t.Fatalf("new version not linked: %v", err)
	}
	if got := nativeRead(t, filepath.Join(cfg.SandboxHome, old, "node")); got != "private" {
		t.Fatalf("refresh replaced private install: %s", got)
	}
}

func TestPrepareNativeReadonlyToWritableData(t *testing.T) {
	for _, mode := range []string{"split", "overlay"} {
		t.Run(mode, func(t *testing.T) {
			cfg := nativeMiseTestConfig(t)
			cfg.DefaultMountMode = "readonly"
			data := filepath.Join(".local", "share", "mise")
			nativeWrite(t, filepath.Join(cfg.HomeDir, data, "tracking"), "host")
			nativeWrite(t, filepath.Join(cfg.HomeDir, data, "installs", "node", "22", "binary"), "installed")
			if _, err := prepareNative(&RunConfig{SandboxCfg: cfg}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Readlink(filepath.Join(cfg.SandboxHome, data)); err != nil {
				t.Fatal(err)
			}
			cfg.DefaultMountMode = mode
			if _, err := prepareNative(&RunConfig{SandboxCfg: cfg}); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(filepath.Join(cfg.SandboxHome, data))
			if err != nil || !info.IsDir() {
				t.Fatalf("writable data remains a host link: %v", err)
			}
			nativeWrite(t, filepath.Join(cfg.SandboxHome, data, "tracking"), "private")
			if nativeRead(t, filepath.Join(cfg.HomeDir, data, "tracking")) != "host" {
				t.Fatal("changed host state")
			}
		})
	}
}

func TestNativeSharedParentWithPrivateChildStaysACopy(t *testing.T) {
	cfg := nativeTestConfig(t)
	source := filepath.Join(cfg.HomeDir, "data")
	nativeWrite(t, filepath.Join(source, "installed"), "host")
	nativeWrite(t, filepath.Join(source, "state", "session"), "initial")
	home, err := os.OpenRoot(cfg.SandboxHome)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := home.Close(); err != nil {
			t.Error(err)
		}
	})
	plan := &nativePlan{setup: filepath.Join(cfg.SandboxRoot, "setup")}
	bindings := []tools.Binding{
		{Source: source, Type: tools.MountBind, ReadOnly: true, Category: tools.CategoryData},
		{Source: filepath.Join(source, "state"), Type: tools.MountOverlay, Category: tools.CategoryData},
	}
	if err := plan.applyBindings(home, cfg, bindings); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(cfg.SandboxHome, "data"))
	if err != nil || !info.IsDir() {
		t.Fatalf("parent is not a private copy: %v", err)
	}
	if got := nativeRead(t, filepath.Join(source, "state", "session")); got != "initial" {
		t.Fatal("host state changed")
	}
}

func TestPrepareNativeSocketRoots(t *testing.T) {
	cfg := nativeMiseTestConfig(t)
	private := filepath.Join(cfg.SandboxHome, "private-runtime")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("private-runtime", filepath.Join(cfg.SandboxHome, ".runtime")); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareNative(&RunConfig{SandboxCfg: cfg})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(private)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plan.policy.UnixSocketRoots, resolved) || len(plan.policy.UnixSocketRoots) != 2 {
		t.Fatalf("socket roots = %v", plan.policy.UnixSocketRoots)
	}
	if err := os.Remove(filepath.Join(cfg.SandboxHome, ".runtime")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cfg.HomeDir, filepath.Join(cfg.SandboxHome, ".runtime")); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareNative(&RunConfig{SandboxCfg: cfg}); err == nil {
		t.Fatal("accepted private socket root pointing into host home")
	}
}

func BenchmarkNativeInstallationSeed(b *testing.B) {
	source := b.TempDir()
	payload := make([]byte, 1<<20)
	for i := range 10 {
		if err := os.WriteFile(filepath.Join(source, string(rune('a'+i))), payload, 0o600); err != nil {
			b.Fatal(err)
		}
	}
	for _, shared := range []bool{false, true} {
		name := "copy-10MiB"
		if shared {
			name = "link-10MiB"
		}
		b.Run(name, func(b *testing.B) {
			home, err := os.OpenRoot(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := home.Close(); err != nil {
					b.Error(err)
				}
			})
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if shared {
					if err := nativeLinkBinding(home, "version", source); err != nil {
						b.Fatal(err)
					}
				} else {
					if err := home.RemoveAll("version"); err != nil {
						b.Fatal(err)
					}
					if err := nativeCopy(home, source, "version", nil); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
