package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNewConfig_XDGRuntime(t *testing.T) {
	// When XDG_RUNTIME_DIR is unset, it should use /run/user/<uid> with numeric UID
	t.Setenv("XDG_RUNTIME_DIR", "")
	cfg, err := NewConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	uid := os.Getuid()
	expected := filepath.Join("/run/user", strconv.Itoa(uid))
	if cfg.XDGRuntime != expected {
		t.Errorf("XDGRuntime = %q, want %q", cfg.XDGRuntime, expected)
	}
}

func TestSanitizeProjectName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple name",
			input:    "myproject",
			expected: "myproject",
		},
		{
			name:     "with spaces",
			input:    "my project",
			expected: "my_project",
		},
		{
			name:     "with special chars",
			input:    "my@project#123",
			expected: "my_project_123",
		},
		{
			name:     "preserves dots",
			input:    "my.project",
			expected: "my.project",
		},
		{
			name:     "preserves hyphens",
			input:    "my-project",
			expected: "my-project",
		},
		{
			name:     "preserves underscores",
			input:    "my_project",
			expected: "my_project",
		},
		{
			name:     "mixed special chars",
			input:    "my project@v1.0-beta_test",
			expected: "my_project_v1.0-beta_test",
		},
		{
			name:     "unicode chars",
			input:    "проект",
			expected: "______",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeProjectName(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeProjectName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGenerateSessionID(t *testing.T) {
	id1, err := GenerateSessionID()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(id1) != 8 {
		t.Errorf("expected 8 hex chars, got %d: %q", len(id1), id1)
	}

	id2, err := GenerateSessionID()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id1 == id2 {
		t.Error("expected unique IDs, got duplicates")
	}
}

func TestGenerateSandboxName(t *testing.T) {
	// Same directory name in different paths should produce different sandbox names
	name1 := GenerateSandboxName("/home/user/work/myproject")
	name2 := GenerateSandboxName("/home/user/personal/myproject")

	if name1 == name2 {
		t.Errorf("Different paths with same basename should have different names: %s == %s", name1, name2)
	}

	// Same path should always produce the same name
	name3 := GenerateSandboxName("/home/user/work/myproject")
	if name1 != name3 {
		t.Errorf("Same path should produce same name: %s != %s", name1, name3)
	}

	// Name should start with sanitized basename
	name := GenerateSandboxName("/home/user/my project")
	if name[:10] != "my_project" {
		t.Errorf("Name should start with sanitized basename: %s", name)
	}

	// Name should contain hash suffix
	if len(name) < 12 { // basename + "-" + 8 char hash
		t.Errorf("Name should include hash suffix: %s", name)
	}
}

func TestGenerateSandboxName_SoftLimit(t *testing.T) {
	for _, tt := range []struct {
		name     string
		basename string
		hashOnly bool
	}{
		{name: "below limit", basename: strings.Repeat("a", 14)},
		{name: "at limit", basename: strings.Repeat("a", 15)},
		{name: "over limit", basename: strings.Repeat("a", 16), hashOnly: true},
		{name: "reported project", basename: "victorialogs-datasource", hashOnly: true},
		{name: "very long", basename: strings.Repeat("a", 256), hashOnly: true},
		{name: "sanitized unicode", basename: strings.Repeat("界", 15)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			projectDir := filepath.Join("/projects", tt.basename)
			hash := sha256.Sum256([]byte(projectDir))
			shortHash := hex.EncodeToString(hash[:4])
			want := shortHash
			if !tt.hashOnly {
				want = SanitizeProjectName(tt.basename) + "-" + shortHash
			}
			if got := GenerateSandboxName(projectDir); got != want {
				t.Errorf("GenerateSandboxName(%q) = %q, want %q", projectDir, got, want)
			}
			if got := GenerateSandboxName(filepath.Join("/other", tt.basename)); got == want {
				t.Errorf("different project paths share name %q", got)
			}
		})
	}
}

func TestGenerateSandboxName_MacOSSocketPath(t *testing.T) {
	const home = "/Users/arturminchukov"
	name := GenerateSandboxName(filepath.Join(home, "projects", "victorialogs-datasource"))
	root := filepath.Join(SandboxBasePath(home), name)
	socket := filepath.Join(SandboxHomePath(root), ".run", "2147483647", "herdr.sock")
	if len(socket) > 103 {
		t.Fatalf("socket path is %d bytes, exceeds macOS limit: %s", len(socket), socket)
	}
}

func TestNewConfig_HashOnlyRootPreservesMetadata(t *testing.T) {
	base := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), "victorialogs-datasource")
	if err := os.Mkdir(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(projectDir)
	cfg, err := NewConfig(&Options{BasePath: base})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(cfg.ProjectDir))
	shortHash := hex.EncodeToString(hash[:4])
	if want := filepath.Join(base, shortHash); cfg.SandboxRoot != want {
		t.Fatalf("SandboxRoot = %q, want %q", cfg.SandboxRoot, want)
	}
	fullName := "victorialogs-datasource-" + shortHash
	if cfg.ProjectName != fullName {
		t.Errorf("ProjectName = %q, want %q", cfg.ProjectName, fullName)
	}
	if err := cfg.EnsureSandboxDirs(); err != nil {
		t.Fatal(err)
	}
	listed, err := ListSandboxes(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d sandboxes, want 1", len(listed))
	}
	if m := listed[0]; m.Name != fullName || m.ProjectDir != cfg.ProjectDir || m.SandboxRoot != cfg.SandboxRoot || m.Orphaned {
		t.Errorf("hash-only sandbox lost project metadata: %+v", m)
	}
}

func TestNewConfig_ReusesLegacySandbox(t *testing.T) {
	for _, compactExists := range []bool{false, true} {
		t.Run(strconv.FormatBool(compactExists), func(t *testing.T) {
			base := t.TempDir()
			projectDir := filepath.Join(t.TempDir(), "victorialogs-datasource")
			if err := os.Mkdir(projectDir, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Chdir(projectDir)
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			legacyRoot := filepath.Join(base, GenerateProjectName(cwd))
			legacyHome := SandboxHomePath(legacyRoot)
			if err := os.MkdirAll(legacyHome, 0o755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(legacyHome, "keep")
			if err := os.WriteFile(marker, []byte("persistent state"), 0o600); err != nil {
				t.Fatal(err)
			}
			compactRoot := filepath.Join(base, GenerateSandboxName(cwd))
			if compactExists {
				if err := os.Mkdir(compactRoot, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := NewConfig(&Options{BasePath: base})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.SandboxRoot != legacyRoot || cfg.SandboxHome != legacyHome {
				t.Fatalf("legacy sandbox not reused: root=%q, home=%q", cfg.SandboxRoot, cfg.SandboxHome)
			}
			if err := cfg.EnsureSandboxDirs(); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(marker); err != nil || string(data) != "persistent state" {
				t.Fatalf("legacy state changed: data=%q, err=%v", data, err)
			}
			if !compactExists {
				if _, err := os.Stat(compactRoot); !os.IsNotExist(err) {
					t.Fatalf("created a second sandbox: stat err=%v", err)
				}
			}
		})
	}
}

func TestResolveSandboxRoot_UnrepresentableLegacyName(t *testing.T) {
	base := t.TempDir()
	projectDir := filepath.Join("/projects", strings.Repeat("a", 255))
	root, err := ResolveSandboxRoot(base, projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, GenerateSandboxName(projectDir)); root != want {
		t.Errorf("root = %q, want %q", root, want)
	}
}

func TestResolveSandboxRoot_LegacyLookupErrors(t *testing.T) {
	for _, symlinkLoop := range []bool{false, true} {
		t.Run(strconv.FormatBool(symlinkLoop), func(t *testing.T) {
			base := t.TempDir()
			projectDir := "/projects/victorialogs-datasource"
			legacyRoot := filepath.Join(base, GenerateProjectName(projectDir))
			if symlinkLoop {
				if err := os.Symlink(legacyRoot, legacyRoot); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(legacyRoot, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			root, err := ResolveSandboxRoot(base, projectDir)
			if err == nil || root != "" {
				t.Fatalf("legacy lookup failure must not select fresh state: root=%q, err=%v", root, err)
			}
			if !strings.Contains(err.Error(), legacyRoot) {
				t.Errorf("error does not identify legacy path: %v", err)
			}
		})
	}
}

func TestScratchpadBasePath(t *testing.T) {
	got := ScratchpadBasePath("/home/alice")
	want := "/home/alice/.local/share/devsandbox-scratchpads"
	if got != want {
		t.Errorf("ScratchpadBasePath = %q, want %q", got, want)
	}
}

func TestScratchpadDir(t *testing.T) {
	got := ScratchpadDir("/home/alice", "foo")
	want := "/home/alice/.local/share/devsandbox-scratchpads/scratchpad-foo"
	if got != want {
		t.Errorf("ScratchpadDir = %q, want %q", got, want)
	}
}

func TestValidateScratchpadName(t *testing.T) {
	valid := []string{"default", "foo", "foo-bar", "foo.bar", "a_b", "A1", "x"}
	for _, name := range valid {
		if err := ValidateScratchpadName(name); err != nil {
			t.Errorf("ValidateScratchpadName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"",
		".",
		"..",
		"foo/bar",
		"foo bar",
		"foo\x00bar",
		"-foo", // leading dash risks being parsed as a flag
		"foo\n",
		"scratchpad-foo", // reserved prefix (avoids double-prefix on disk)
	}
	for _, name := range invalid {
		if err := ValidateScratchpadName(name); err == nil {
			t.Errorf("ValidateScratchpadName(%q) = nil, want error", name)
		}
	}
}

func TestConfigHasWorktree(t *testing.T) {
	c := &Config{ProjectDir: "/tmp/wt", GitRepoRoot: ""}
	if c.HasWorktree() {
		t.Error("HasWorktree() = true with empty GitRepoRoot")
	}
	c.GitRepoRoot = "/tmp/repo"
	if !c.HasWorktree() {
		t.Error("HasWorktree() = false with GitRepoRoot set")
	}
	c = &Config{ProjectDir: "/tmp/repo", GitRepoRoot: "/tmp/repo"}
	if c.HasWorktree() {
		t.Error("HasWorktree() = true when ProjectDir == GitRepoRoot")
	}
}
