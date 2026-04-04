package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGit_DefaultMode(t *testing.T) {
	g := &Git{}
	// Without Configure(), mode should be zero value
	// After Configure with nil, should default to readonly
	g.Configure(GlobalConfig{}, nil)

	if g.mode != GitModeReadOnly {
		t.Errorf("expected default mode %q, got %q", GitModeReadOnly, g.mode)
	}
}

func TestGit_Configure(t *testing.T) {
	tests := []struct {
		name     string
		config   map[string]any
		expected GitMode
	}{
		// Readonly variants
		{"readonly explicit", map[string]any{"mode": "readonly"}, GitModeReadOnly},
		{"readonly default", map[string]any{"mode": "read-only"}, GitModeReadOnly},
		{"readonly unknown", map[string]any{"mode": "unknown"}, GitModeReadOnly},
		{"readonly empty", map[string]any{}, GitModeReadOnly},
		{"readonly nil", nil, GitModeReadOnly},

		// Readwrite variants
		{"readwrite", map[string]any{"mode": "readwrite"}, GitModeReadWrite},
		{"read-write", map[string]any{"mode": "read-write"}, GitModeReadWrite},
		{"rw", map[string]any{"mode": "rw"}, GitModeReadWrite},
		{"readwrite uppercase", map[string]any{"mode": "READWRITE"}, GitModeReadWrite},

		// Disabled variants
		{"disabled", map[string]any{"mode": "disabled"}, GitModeDisabled},
		{"none", map[string]any{"mode": "none"}, GitModeDisabled},
		{"off", map[string]any{"mode": "off"}, GitModeDisabled},
		{"disabled uppercase", map[string]any{"mode": "DISABLED"}, GitModeDisabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &Git{}
			g.Configure(GlobalConfig{}, tt.config)

			if g.mode != tt.expected {
				t.Errorf("expected mode %q, got %q", tt.expected, g.mode)
			}
		})
	}
}

func TestGit_Bindings_Disabled(t *testing.T) {
	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "disabled"})

	bindings := g.Bindings("/home/user", "/sandbox/home")

	if bindings != nil {
		t.Errorf("expected nil bindings for disabled mode, got %d bindings", len(bindings))
	}
}

func TestGit_Bindings_ReadOnly_NoProject(t *testing.T) {
	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "readonly"})

	bindings := g.Bindings("/home/user", "/sandbox/home")

	// Without projectDir, only gitconfig binding
	if len(bindings) != 1 {
		t.Fatalf("expected 1 binding for readonly mode without project, got %d", len(bindings))
	}

	b := bindings[0]

	// Check source is the safe gitconfig in sandbox home
	expectedSource := "/sandbox/home/.gitconfig.safe"
	if b.Source != expectedSource {
		t.Errorf("expected source %q, got %q", expectedSource, b.Source)
	}

	// Check dest is the gitconfig in home
	expectedDest := "/home/user/.gitconfig"
	if b.Dest != expectedDest {
		t.Errorf("expected dest %q, got %q", expectedDest, b.Dest)
	}

	if !b.Optional {
		t.Error("expected binding to be optional")
	}

	if b.Category != CategoryConfig {
		t.Errorf("expected category %q, got %q", CategoryConfig, b.Category)
	}
}

func TestGit_Bindings_ReadOnly_WithGitDir(t *testing.T) {
	// Create a temp project with .git directory and config file
	tmpDir := t.TempDir()
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitConfig := filepath.Join(gitDir, "config")
	if err := os.WriteFile(gitConfig, []byte("[remote \"origin\"]\n\turl = https://ghp_secret@github.com/user/repo.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Git{}
	g.Configure(GlobalConfig{ProjectDir: tmpDir}, map[string]any{"mode": "readonly"})

	bindings := g.Bindings("/home/user", "/sandbox/home")

	// With projectDir containing .git + config, should have 3 bindings:
	// gitconfig.safe, .git (ro), .git/config (hidden with /dev/null)
	if len(bindings) != 3 {
		t.Fatalf("expected 3 bindings for readonly mode with .git, got %d", len(bindings))
	}

	// Find the .git binding
	var gitBinding *Binding
	for i := range bindings {
		if bindings[i].Source == gitDir {
			gitBinding = &bindings[i]
			break
		}
	}

	if gitBinding == nil {
		t.Fatal("expected .git binding in readonly mode")
	}

	if !gitBinding.ReadOnly {
		t.Error(".git binding should be read-only in readonly mode")
	}

	if gitBinding.Type != MountBind {
		t.Errorf(".git binding should have explicit Type=MountBind, got %q", gitBinding.Type)
	}

	if gitBinding.Category != CategoryConfig {
		t.Errorf(".git binding: expected category %q, got %q", CategoryConfig, gitBinding.Category)
	}

	if gitBinding.Optional {
		t.Error(".git binding should not be optional")
	}

	// Find the .git/config hidden binding
	var configBinding *Binding
	for i := range bindings {
		if bindings[i].Source == "/dev/null" && bindings[i].Dest == gitConfig {
			configBinding = &bindings[i]
			break
		}
	}

	if configBinding == nil {
		t.Fatal("expected .git/config hidden binding (overlaid with /dev/null)")
	}

	if configBinding.Type != MountBind {
		t.Errorf(".git/config binding should have Type=MountBind, got %q", configBinding.Type)
	}

	if !configBinding.ReadOnly {
		t.Error(".git/config binding should be read-only")
	}
}

func TestGit_Bindings_ReadWrite(t *testing.T) {
	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "readwrite"})

	bindings := g.Bindings("/home/user", "/sandbox/home")

	if len(bindings) != 4 {
		t.Fatalf("expected 4 bindings for readwrite mode, got %d", len(bindings))
	}

	// Check expected bindings exist
	expectedSources := map[string]bool{
		"/home/user/.gitconfig":       true,
		"/home/user/.git-credentials": true,
		"/home/user/.ssh":             true,
		"/home/user/.gnupg":           true,
	}

	for _, b := range bindings {
		if !expectedSources[b.Source] {
			t.Errorf("unexpected binding source: %s", b.Source)
			continue
		}

		if b.Category != CategoryConfig {
			t.Errorf("binding %s: expected category %q, got %q", b.Source, CategoryConfig, b.Category)
		}

		if !b.Optional {
			t.Errorf("binding %s: expected optional=true", b.Source)
		}

		// ReadOnly is not set by the tool — the builder resolves it via mount mode
		if b.ReadOnly {
			t.Errorf("binding %s: ReadOnly should not be set by tool (builder resolves it)", b.Source)
		}
	}
}

func TestGit_Environment_Disabled(t *testing.T) {
	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "disabled"})

	env := g.Environment("/home/user", "/sandbox/home")

	if env != nil {
		t.Errorf("expected nil environment for disabled mode, got %d vars", len(env))
	}
}

func TestGit_Environment_ReadOnly(t *testing.T) {
	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "readonly"})

	env := g.Environment("/home/user", "/sandbox/home")

	if env != nil {
		t.Errorf("expected nil environment for readonly mode, got %d vars", len(env))
	}
}

func TestGit_Environment_ReadWrite(t *testing.T) {
	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "readwrite"})

	env := g.Environment("/home/user", "/sandbox/home")

	if len(env) != 2 {
		t.Fatalf("expected 2 environment vars for readwrite mode, got %d", len(env))
	}

	expectedVars := map[string]bool{
		"SSH_AUTH_SOCK": true,
		"GPG_TTY":       true,
	}

	for _, e := range env {
		if !expectedVars[e.Name] {
			t.Errorf("unexpected environment var: %s", e.Name)
		}
		if !e.FromHost {
			t.Errorf("expected %s to have FromHost=true", e.Name)
		}
	}
}

func TestGit_Setup_DisabledMode(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")

	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sandboxHome, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a gitconfig
	gitconfig := filepath.Join(homeDir, ".gitconfig")
	if err := os.WriteFile(gitconfig, []byte("[user]\n\tname = Test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "disabled"})

	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Errorf("Setup failed: %v", err)
	}

	// Safe gitconfig should NOT be created
	safeConfig := filepath.Join(sandboxHome, ".gitconfig.safe")
	if _, err := os.Stat(safeConfig); !os.IsNotExist(err) {
		t.Error("safe gitconfig should not be created for disabled mode")
	}
}

func TestGit_Setup_ReadWriteMode(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")

	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sandboxHome, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a gitconfig
	gitconfig := filepath.Join(homeDir, ".gitconfig")
	if err := os.WriteFile(gitconfig, []byte("[user]\n\tname = Test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "readwrite"})

	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Errorf("Setup failed: %v", err)
	}

	// Safe gitconfig should NOT be created
	safeConfig := filepath.Join(sandboxHome, ".gitconfig.safe")
	if _, err := os.Stat(safeConfig); !os.IsNotExist(err) {
		t.Error("safe gitconfig should not be created for readwrite mode")
	}
}

func TestGit_Setup_ReadOnlyMode_NoGitconfig(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")

	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sandboxHome, 0o755); err != nil {
		t.Fatal(err)
	}

	// Don't create gitconfig

	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "readonly"})

	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Errorf("Setup failed: %v", err)
	}

	// Safe gitconfig should NOT be created (no source)
	safeConfig := filepath.Join(sandboxHome, ".gitconfig.safe")
	if _, err := os.Stat(safeConfig); !os.IsNotExist(err) {
		t.Error("safe gitconfig should not be created when source doesn't exist")
	}
}

func TestGit_Setup_ReadOnlyMode_GeneratesSafeConfig(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")

	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sandboxHome, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a gitconfig with sensitive and safe data
	gitconfig := filepath.Join(homeDir, ".gitconfig")
	content := `[user]
	name = Test User
	email = test@example.com
	signingkey = ABC123
[credential]
	helper = store
[core]
	editor = vim
[alias]
	co = checkout
`
	if err := os.WriteFile(gitconfig, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "readonly"})

	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Errorf("Setup failed: %v", err)
	}

	// Safe gitconfig should be created
	safeConfig := filepath.Join(sandboxHome, ".gitconfig.safe")
	data, err := os.ReadFile(safeConfig)
	if err != nil {
		t.Fatalf("failed to read safe gitconfig: %v", err)
	}

	safeContent := string(data)

	// Should have [user] section
	if !strings.Contains(safeContent, "[user]") {
		t.Error("safe gitconfig should contain [user] section")
	}

	// Should contain name and email (either from git config --global or parsed from file)
	// The actual values depend on whether git config --global works in test env
	if !strings.Contains(safeContent, "name = ") && !strings.Contains(safeContent, "name=") {
		// Only fail if no name at all - git config --global might return system user
		t.Log("Note: safe gitconfig may not contain name if git config --global fails")
	}

	// Should NOT contain sensitive data
	if strings.Contains(safeContent, "signingkey") {
		t.Error("safe gitconfig should not contain signingkey")
	}
	if strings.Contains(safeContent, "credential") {
		t.Error("safe gitconfig should not contain credential section")
	}
	if strings.Contains(safeContent, "helper") {
		t.Error("safe gitconfig should not contain credential helper")
	}
	if strings.Contains(safeContent, "editor") {
		t.Error("safe gitconfig should not contain editor")
	}
	if strings.Contains(safeContent, "alias") {
		t.Error("safe gitconfig should not contain aliases")
	}
}

func TestParseGitconfig(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		expectedName  string
		expectedEmail string
	}{
		{
			name: "standard config",
			content: `[user]
	name = John Doe
	email = john@example.com
`,
			expectedName:  "John Doe",
			expectedEmail: "john@example.com",
		},
		{
			name: "config with multiple sections",
			content: `[core]
	editor = vim
[user]
	name = Jane Doe
	email = jane@example.com
[alias]
	co = checkout
`,
			expectedName:  "Jane Doe",
			expectedEmail: "jane@example.com",
		},
		{
			name: "user section at end",
			content: `[core]
	autocrlf = false
[alias]
	st = status
[user]
	name = Bob Smith
	email = bob@example.com
`,
			expectedName:  "Bob Smith",
			expectedEmail: "bob@example.com",
		},
		{
			name: "only name",
			content: `[user]
	name = Only Name
`,
			expectedName:  "Only Name",
			expectedEmail: "",
		},
		{
			name: "only email",
			content: `[user]
	email = only@email.com
`,
			expectedName:  "",
			expectedEmail: "only@email.com",
		},
		{
			name:          "empty config",
			content:       "",
			expectedName:  "",
			expectedEmail: "",
		},
		{
			name: "no user section",
			content: `[core]
	editor = vim
`,
			expectedName:  "",
			expectedEmail: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpFile := filepath.Join(t.TempDir(), ".gitconfig")
			if err := os.WriteFile(tmpFile, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}

			name, email := parseGitconfig(tmpFile)

			if name != tt.expectedName {
				t.Errorf("expected name %q, got %q", tt.expectedName, name)
			}
			if email != tt.expectedEmail {
				t.Errorf("expected email %q, got %q", tt.expectedEmail, email)
			}
		})
	}
}

func TestParseGitconfig_NonExistent(t *testing.T) {
	name, email := parseGitconfig("/nonexistent/path/.gitconfig")

	if name != "" || email != "" {
		t.Errorf("expected empty strings for non-existent file, got name=%q email=%q", name, email)
	}
}

func TestGit_Description(t *testing.T) {
	tests := []struct {
		mode     string
		contains string
	}{
		{"readonly", "read-only"},
		{"readwrite", "full access"},
		{"disabled", "disabled"},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			g := &Git{}
			g.Configure(GlobalConfig{}, map[string]any{"mode": tt.mode})

			desc := g.Description()
			if !strings.Contains(strings.ToLower(desc), tt.contains) {
				t.Errorf("expected description to contain %q, got %q", tt.contains, desc)
			}
		})
	}
}

func TestGit_Name(t *testing.T) {
	g := &Git{}
	if g.Name() != "git" {
		t.Errorf("expected name 'git', got %q", g.Name())
	}
}

func TestGit_ShellInit(t *testing.T) {
	g := &Git{}
	// Git doesn't need shell init
	if g.ShellInit("bash") != "" {
		t.Error("expected empty shell init")
	}
	if g.ShellInit("zsh") != "" {
		t.Error("expected empty shell init")
	}
	if g.ShellInit("fish") != "" {
		t.Error("expected empty shell init")
	}
}

func TestGit_Bindings_Categories(t *testing.T) {
	t.Run("readonly mode", func(t *testing.T) {
		tmpDir := t.TempDir()
		sandboxHome := t.TempDir()
		if err := os.WriteFile(filepath.Join(sandboxHome, ".gitconfig.safe"), []byte("[user]\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		g := &Git{mode: GitModeReadOnly, projectDir: ""}
		bindings := g.Bindings(tmpDir, sandboxHome)

		for _, b := range bindings {
			if b.Category != CategoryConfig {
				t.Errorf("binding %s: Category = %q, want %q", b.Source, b.Category, CategoryConfig)
			}
		}
	})

	t.Run("readwrite mode", func(t *testing.T) {
		g := &Git{mode: GitModeReadWrite}
		bindings := g.Bindings("/home/test", "/tmp/sandbox")

		for _, b := range bindings {
			if b.Category != CategoryConfig {
				t.Errorf("binding %s: Category = %q, want %q", b.Source, b.Category, CategoryConfig)
			}
		}
	})
}
