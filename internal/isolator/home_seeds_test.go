package isolator

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"devsandbox/internal/fsutil"
)

func claudeSeedConfig(t *testing.T) (*Config, []byte) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PATH", "")
	cfg := &Config{HomeDir: t.TempDir(), SandboxHome: t.TempDir(), SandboxRoot: t.TempDir(), ProjectDir: t.TempDir()}
	content := []byte(`{"hasCompletedOnboarding":true,"host":"private"}`)
	if err := os.WriteFile(filepath.Join(cfg.HomeDir, ".claude.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg, content
}

func TestDockerConfigHash_IncludesPrivateManifestPath(t *testing.T) {
	cfg, _ := claudeSeedConfig(t)
	d := NewDockerIsolator(DockerConfig{})
	first := d.configHash(cfg)
	if again := d.configHash(cfg); again != first {
		t.Fatalf("unchanged seed setup changed container fingerprint: %s then %s", first, again)
	}
	cfg.SandboxRoot = t.TempDir()
	if next := d.configHash(cfg); next == first {
		t.Fatal("changed private manifest bind source did not require container recreation")
	}
}

func TestDockerToolBindings_CarriesPrivateClaudeSeed(t *testing.T) {
	cfg, content := claudeSeedConfig(t)
	_, _, manifest := (&DockerIsolator{}).getToolBindings(cfg)
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Seeds []fsutil.FileSeed `json:"seeds"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Seeds) != 1 || got.Seeds[0].Name != ".claude.json" || string(got.Seeds[0].Data) != string(content) {
		t.Fatalf("container setup manifest lost private Claude configuration: %+v", got.Seeds)
	}
}

func TestDockerToolBindings_DisabledClaudeDoesNotSeed(t *testing.T) {
	cfg, _ := claudeSeedConfig(t)
	cfg.ToolsConfig = map[string]any{"claude": map[string]any{"mount_mode": "disabled"}}
	_, _, manifest := (&DockerIsolator{}).getToolBindings(cfg)
	if len(manifest.Seeds) != 0 {
		t.Fatalf("disabled Claude exposed configuration: %+v", manifest.Seeds)
	}
	if _, err := os.Lstat(filepath.Join(cfg.SandboxHome, ".claude.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled setup copied host configuration: %v", err)
	}
}

func TestDockerCommonArgs_MountsSeedOnlyManifest(t *testing.T) {
	cfg, _ := claudeSeedConfig(t)
	d := NewDockerIsolator(DockerConfig{})
	args, err := d.buildCommonArgs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.SandboxRoot, overlayManifestFileName)
	if !slices.Contains(args, path+":"+OverlayManifestPath+":ro") {
		t.Fatalf("seed-only setup manifest was not mounted: %v", args)
	}
	manifest, err := ReadOverlayManifest(path)
	if err != nil || manifest == nil || len(manifest.Seeds) != 1 {
		t.Fatalf("setup manifest missing seed: %+v, %v", manifest, err)
	}
}

func TestCollectHomeSeeds_RejectsUnsafeSources(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "directory", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, ".claude.json")
			switch kind {
			case "symlink":
				victim := filepath.Join(t.TempDir(), "secret")
				if err := os.WriteFile(victim, []byte("must not leak"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(victim, path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				f, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.Truncate(maxHomeSeedBytes + 1); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if seeds, err := collectHomeSeeds(home, []string{".claude.json"}); err == nil || len(seeds) != 0 {
				t.Fatalf("unsafe source %s was accepted: %+v, %v", kind, seeds, err)
			}
		})
	}
}

func TestCollectHomeSeeds_MissingAndInvalid(t *testing.T) {
	if seeds, err := collectHomeSeeds(t.TempDir(), []string{".claude.json"}); err != nil || len(seeds) != 0 {
		t.Fatalf("missing optional seed: %+v, %v", seeds, err)
	}
	for _, name := range []string{"", ".", "..", "../secret", "/tmp/secret"} {
		if _, err := collectHomeSeeds(t.TempDir(), []string{name}); err == nil {
			t.Errorf("accepted invalid seed name %q", name)
		}
	}
}
