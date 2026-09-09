package isolator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildDocker_KeepContainer_RefreshManifestBeforeRestart(t *testing.T) {
	for _, tc := range []struct {
		name      string
		running   bool
		writeFail bool
	}{
		{name: "stopped"},
		{name: "write failure", writeFail: true},
		{name: "running", running: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, original := claudeSeedConfig(t)
			manifestPath := filepath.Join(cfg.SandboxRoot, overlayManifestFileName)
			callsPath := filepath.Join(t.TempDir(), "calls")
			snapshotPath := filepath.Join(t.TempDir(), "manifest-at-start.json")
			t.Setenv("TEST_ENGINE_CALLS", callsPath)
			t.Setenv("TEST_MANIFEST", manifestPath)
			t.Setenv("TEST_SNAPSHOT", snapshotPath)
			t.Setenv("TEST_RUNNING", "false")
			if tc.running {
				t.Setenv("TEST_RUNNING", "true")
			}
			bin := writeFakeEngine(t, `printf '%s\n' "$1" >> "$TEST_ENGINE_CALLS"
case "$1:$3" in
  inspect:'{{.State.Running}}') printf '%s\n' "$TEST_RUNNING" ;;
  inspect:*) printf '%s\n' "$TEST_HASH" ;;
  start:*) /bin/cp "$TEST_MANIFEST" "$TEST_SNAPSHOT" ;;
  *) exit 1 ;;
esac`)
			d := newFakeEngineIsolator(bin)
			d.config = DockerConfig{KeepContainer: true, ConfigDir: setupTestDockerfile(t)}
			d.imageTag = "devsandbox:local"
			if _, err := d.buildCommonArgs(cfg); err != nil {
				t.Fatal(err)
			}
			hash := d.configHash(cfg)
			t.Setenv("TEST_HASH", hash)

			updated := []byte(`{"hasCompletedOnboarding":true,"host":"updated"}`)
			if err := os.WriteFile(filepath.Join(cfg.HomeDir, ".claude.json"), updated, 0o600); err != nil {
				t.Fatal(err)
			}
			// A missing private copy is reseeded by configHash's tool setup.
			if err := os.Remove(filepath.Join(cfg.SandboxHome, ".claude.json")); err != nil {
				t.Fatal(err)
			}
			if got := d.configHash(cfg); got != hash {
				t.Fatalf("seed content changed the container hash: %s -> %s", hash, got)
			}
			if tc.writeFail {
				if err := os.Remove(manifestPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(manifestPath, 0o700); err != nil {
					t.Fatal(err)
				}
			}

			result, err := d.BuildDocker(context.Background(), cfg)
			calls, readErr := os.ReadFile(callsPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			wantCalls := "inspect\ninspect\n"
			if !tc.running && !tc.writeFail {
				wantCalls += "start\n"
			}
			if string(calls) != wantCalls {
				t.Errorf("engine calls = %q, want %q", calls, wantCalls)
			}
			if tc.writeFail {
				var pathErr *os.PathError
				if err == nil || !strings.Contains(err.Error(), "write overlay manifest") || !errors.As(err, &pathErr) {
					t.Fatalf("manifest write failure = %v, want wrapped filesystem error", err)
				}
				if result != nil {
					t.Fatalf("manifest write failure returned a launch result: %+v", result)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Action != DockerActionExec || result.ContainerJustStarted == tc.running {
				t.Fatalf("unexpected reuse result: %+v", result)
			}
			readPath, wantSeed := snapshotPath, updated
			if tc.running {
				readPath, wantSeed = manifestPath, original
			}
			manifest, err := ReadOverlayManifest(readPath)
			if err != nil {
				t.Fatal(err)
			}
			if manifest == nil || len(manifest.Seeds) != 1 || manifest.Seeds[0].Name != ".claude.json" || string(manifest.Seeds[0].Data) != string(wantSeed) {
				t.Fatalf("manifest = %+v, want Claude seed %s", manifest, wantSeed)
			}
		})
	}
}
