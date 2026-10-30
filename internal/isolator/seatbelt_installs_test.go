package isolator

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"devsandbox/internal/sandbox/tools"
)

func TestNativeLinkBindingTransitions(t *testing.T) {
	for _, previous := range []string{"absent", "file", "copy", "symlink", "dangling", "same link"} {
		t.Run(previous, func(t *testing.T) {
			source := t.TempDir()
			payload := filepath.Join(source, "payload")
			if err := os.WriteFile(payload, []byte("host payload"), 0o600); err != nil {
				t.Fatal(err)
			}
			homeDir := t.TempDir()
			home, err := os.OpenRoot(homeDir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := home.Close(); err != nil {
					t.Error(err)
				}
			})
			rel := filepath.Join("data", "install")
			if err := home.MkdirAll("data", 0o700); err != nil {
				t.Fatal(err)
			}
			switch previous {
			case "file":
				if err := home.WriteFile(rel, []byte("old copy"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "copy":
				if err := home.MkdirAll(rel, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := home.WriteFile(filepath.Join(rel, "old"), []byte("old copy"), 0o600); err != nil {
					t.Fatal(err)
				}
				// Removing the old copy must unlink this child, not its host target.
				if err := home.Symlink(source, filepath.Join(rel, "escape")); err != nil {
					t.Fatal(err)
				}
			case "symlink", "dangling", "same link":
				target := source
				switch previous {
				case "symlink":
					target = payload
				case "dangling":
					target = filepath.Join(source, "missing")
				}
				if err := home.Symlink(target, rel); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := nativeLinkBinding(home, rel, source); err != nil {
					t.Fatal(err)
				}
			}
			target, err := home.Readlink(rel)
			if err != nil || target != source {
				t.Fatalf("link = %q, %v; want %q", target, err, source)
			}
			// SameFile proves that no private payload copy was created.
			hostInfo, err := os.Stat(payload)
			if err != nil {
				t.Fatal(err)
			}
			linkedInfo, err := os.Stat(filepath.Join(homeDir, rel, "payload"))
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(hostInfo, linkedInfo) {
				t.Fatal("linked payload is a copy, not the host file")
			}
			data, err := os.ReadFile(payload)
			if err != nil || string(data) != "host payload" {
				t.Fatalf("host payload changed: %q, %v", data, err)
			}
			entries, err := os.ReadDir(source)
			if err != nil || len(entries) != 1 {
				t.Fatalf("source tree changed: %v, %v", entries, err)
			}
		})
	}
}

func TestNativeLinkBindingHostileAncestors(t *testing.T) {
	for _, kind := range []string{"absolute", "relative", "chain", "loop"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			homeDir := filepath.Join(base, "home")
			outside := filepath.Join(base, "outside")
			for _, path := range []string{homeDir, outside} {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			victim := filepath.Join(outside, "entry")
			if err := os.WriteFile(victim, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			home, err := os.OpenRoot(homeDir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := home.Close(); err != nil {
					t.Error(err)
				}
			})
			target := outside
			switch kind {
			case "relative":
				target = "../outside"
			case "chain":
				if err := home.Symlink(outside, "second"); err != nil {
					t.Fatal(err)
				}
				target = "second"
			case "loop":
				target = "ancestor"
			}
			if err := home.Symlink(target, "ancestor"); err != nil {
				t.Fatal(err)
			}
			for _, rel := range []string{"ancestor/entry", "ancestor/new/entry"} {
				if err := nativeLinkBinding(home, rel, outside); err == nil {
					t.Fatalf("accepted hostile path %s", rel)
				}
			}
			data, err := os.ReadFile(victim)
			if err != nil || string(data) != "untouched" {
				t.Fatalf("outside file changed: %q, %v", data, err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 {
				t.Fatalf("outside tree changed: %v, %v", entries, err)
			}
		})
	}
}

func TestNativeLinkBindingRejectsInvalidPaths(t *testing.T) {
	home, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := home.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, rel := range []string{"", ".", "child/..", "../outside", "/absolute"} {
		if err := nativeLinkBinding(home, rel, "/host/source"); err == nil {
			t.Errorf("accepted destination %q", rel)
		}
	}
	if err := nativeLinkBinding(home, "valid", "relative/source"); err == nil {
		t.Fatal("accepted relative source")
	}
	// Parent directories are created by the helper, without reading the source.
	if err := nativeLinkBinding(home, "new/nested/version", "/host/source"); err != nil {
		t.Fatal(err)
	}
}

func TestNativeMiseInstallBindings(t *testing.T) {
	host := t.TempDir()
	installs := filepath.Join(host, ".local", "share", "mise", "installs")
	for _, rel := range []string{"go/1.27.3/bin", "node/22.0.0/bin", "node/24.0.0/bin"} {
		if err := os.MkdirAll(filepath.Join(installs, rel), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(installs, "go", ".mise.backend"), []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, link := range []struct{ name, target string }{
		{"go/latest", "1.27.3"}, {"go/dangling", "/missing"}, {"tool-alias", "go"},
	} {
		if err := os.Symlink(link.target, filepath.Join(installs, link.name)); err != nil {
			t.Fatal(err)
		}
	}
	bindings, err := nativeMiseInstallBindings(host)
	if err != nil {
		t.Fatal(err)
	}
	var want []tools.Binding
	for _, rel := range []string{"go/1.27.3", "node/22.0.0", "node/24.0.0"} {
		source := filepath.Join(installs, rel)
		want = append(want, tools.Binding{
			Source: source, Dest: source, HomeRelativeDest: true,
			Category: tools.CategoryData, Type: tools.MountBind, ReadOnly: true,
		})
	}
	if !reflect.DeepEqual(bindings, want) {
		t.Fatalf("bindings = %#v; want %#v", bindings, want)
	}
}

func TestNativeMiseInstallBindingsMissingAndInvalid(t *testing.T) {
	host := t.TempDir()
	bindings, err := nativeMiseInstallBindings(host)
	if err != nil || len(bindings) != 0 {
		t.Fatalf("missing installs = %v, %v", bindings, err)
	}
	root := filepath.Join(host, ".local", "share", "mise")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "installs"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeMiseInstallBindings(host); err == nil {
		t.Fatal("accepted non-directory installs root")
	}
}
