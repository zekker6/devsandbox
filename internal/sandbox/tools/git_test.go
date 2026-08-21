package tools

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"devsandbox/internal/cmdpattern"

	"devsandbox/internal/notice"
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

	// Without projectDir: the safe gitconfig plus the global ignore and
	// attributes copies, which are emitted unconditionally.
	if len(bindings) != 3 {
		t.Fatalf("expected 3 bindings for readonly mode without project, got %d", len(bindings))
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

	sandboxHome := "/sandbox/home"
	g := &Git{}
	g.Configure(GlobalConfig{ProjectDir: tmpDir}, map[string]any{"mode": "readonly"})

	bindings := g.Bindings("/home/user", sandboxHome)

	// With projectDir containing .git + config, should have 5 bindings:
	// gitconfig.safe, the global ignore and attributes copies, .git (ro), and
	// the sanitized .git-config.safe overlaid on .git/config
	if len(bindings) != 5 {
		t.Fatalf("expected 5 bindings for readonly mode with .git, got %d", len(bindings))
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

	// Find the sanitized .git/config binding — must be the safe file from sandbox home,
	// NOT /dev/null (which would break `git log`, pre-commit hooks, and any other git command).
	expectedSafeRepoConfig := filepath.Join(sandboxHome, ".git-config.safe")
	var configBinding *Binding
	for i := range bindings {
		if bindings[i].Dest == gitConfig {
			configBinding = &bindings[i]
			break
		}
	}

	if configBinding == nil {
		t.Fatal("expected sanitized .git/config binding")
	}

	if configBinding.Source != expectedSafeRepoConfig {
		t.Errorf(".git/config binding source: expected %q, got %q", expectedSafeRepoConfig, configBinding.Source)
	}

	if configBinding.Type != MountBind {
		t.Errorf(".git/config binding should have Type=MountBind, got %q", configBinding.Type)
	}

	if !configBinding.ReadOnly {
		t.Error(".git/config binding should be read-only")
	}

	if !configBinding.Optional {
		t.Error(".git/config binding should be optional (Setup may have skipped if source unreadable)")
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

	// Don't create gitconfig. XDG has to be pinned too, or the developer's own
	// ~/.config/git/config satisfies the existence check.
	isolateGitEnv(t, homeDir)

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

	// The generator resolves the global config with HOME set to homeDir, so the
	// only thing left that could pull in the developer's real configuration is
	// XDG_CONFIG_HOME.
	isolateGitEnv(t, homeDir)

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

	// Hermetic now, so the identity is the fixture's - whether it arrived
	// through the resolver or through the parseGitconfig fallback.
	if !strings.Contains(safeContent, `name = "Test User"`) {
		t.Errorf("safe gitconfig should carry the fixture name, got:\n%s", safeContent)
	}
	if !strings.Contains(safeContent, `email = "test@example.com"`) {
		t.Errorf("safe gitconfig should carry the fixture email, got:\n%s", safeContent)
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

			got := parseGitconfig(tmpFile)

			if got["user.name"] != tt.expectedName {
				t.Errorf("expected name %q, got %q", tt.expectedName, got["user.name"])
			}
			if got["user.email"] != tt.expectedEmail {
				t.Errorf("expected email %q, got %q", tt.expectedEmail, got["user.email"])
			}
		})
	}
}

func TestParseGitconfig_NonExistent(t *testing.T) {
	if got := parseGitconfig("/nonexistent/path/.gitconfig"); len(got) != 0 {
		t.Errorf("expected no keys for non-existent file, got %v", got)
	}
}

// TestParseGitconfig_ValueForm pins the form values come back in. They feed
// quoteGitConfigValue, so anything left in that git itself would have stripped
// - the surrounding quotes, an inline comment - is written back into the safe
// config escaped, as part of the value.
func TestParseGitconfig_ValueForm(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    map[string]string
	}{
		{
			name:    "quoted value is unquoted",
			content: "[user]\n\tname = \"Jane Doe\"\n",
			want:    map[string]string{"user.name": "Jane Doe"},
		},
		{
			name:    "inline comment ends the value",
			content: "[user]\n\tname = Jane ; the one from accounting\n\temail = j@e.com # work\n",
			want:    map[string]string{"user.name": "Jane", "user.email": "j@e.com"},
		},
		{
			name:    "a quoted comment introducer is literal",
			content: "[user]\n\tname = \"Jane #1 Dev\"\n",
			want:    map[string]string{"user.name": "Jane #1 Dev"},
		},
		{
			name:    "escapes are applied",
			content: "[user]\n\tname = \"Jane \\\"JD\\\" Doe\"\n",
			want:    map[string]string{"user.name": `Jane "JD" Doe`},
		},
		{
			name:    "whitespace inside quotes survives, outside does not",
			content: "[user]\n\tname = \"Jane \"   \n",
			want:    map[string]string{"user.name": "Jane "},
		},
		{
			name:    "file-valued keys are read too",
			content: "[core]\n\texcludesFile = ~/.gitignore_global\n\tattributesFile = ~/.gitattributes\n",
			want: map[string]string{
				"core.excludesfile":   "~/.gitignore_global",
				"core.attributesfile": "~/.gitattributes",
			},
		},
		{
			name:    "a key in a subsection is not a top-level key",
			content: "[includeIf \"gitdir:/work/\"]\n\tpath = /nowhere\n[remote \"user\"]\n\tname = nope\n",
			want:    map[string]string{},
		},
		{
			name:    "a key that merely starts with an allowlisted name is not one",
			content: "[user]\n\tnameOfThing = nope\n\temailAlias = nope\n",
			want:    map[string]string{},
		},
		{
			name:    "a commented-out key is not read",
			content: "[user]\n\t# name = Commented\n\t; email = commented@example.com\n",
			want:    map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".gitconfig")
			writeFile(t, path, tt.content)

			got := parseGitconfig(path)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseGitconfig() = %v, want %v", got, tt.want)
			}
		})
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

func TestStripURLCredentials(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "https with embedded token",
			in:   "https://ghp_abc123@github.com/user/repo.git",
			want: "https://github.com/user/repo.git",
		},
		{
			name: "https with username and password",
			in:   "https://alice:s3cret@gitlab.example.com/group/proj.git",
			want: "https://gitlab.example.com/group/proj.git",
		},
		{
			name: "http with embedded token",
			in:   "http://token@example.com/repo.git",
			want: "http://example.com/repo.git",
		},
		{
			name: "https without credentials passes through",
			in:   "https://github.com/user/repo.git",
			want: "https://github.com/user/repo.git",
		},
		{
			name: "ssh URL with git user is preserved (user is required for auth)",
			in:   "ssh://git@github.com/user/repo.git",
			want: "ssh://git@github.com/user/repo.git",
		},
		{
			name: "scp-style git URL passes through unchanged",
			in:   "git@github.com:user/repo.git",
			want: "git@github.com:user/repo.git",
		},
		{
			name: "local path passes through",
			in:   "/srv/git/repo.git",
			want: "/srv/git/repo.git",
		},
		{
			name: "file URL passes through",
			in:   "file:///srv/git/repo.git",
			want: "file:///srv/git/repo.git",
		},
		{
			name: "url-encoded password is stripped",
			in:   "https://user:p%40ss@example.com/repo.git",
			want: "https://example.com/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripURLCredentials(tt.in)
			if got != tt.want {
				t.Errorf("stripURLCredentials(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestGenerateSafeRepoConfig(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		mustContain    []string
		mustNotContain []string
	}{
		{
			name: "remote url with embedded token is sanitized",
			input: `[remote "origin"]
	url = https://ghp_secret123@github.com/user/repo.git
	fetch = +refs/heads/*:refs/remotes/origin/*
`,
			mustContain: []string{
				`[remote "origin"]`,
				"https://github.com/user/repo.git",
				"fetch = +refs/heads/*:refs/remotes/origin/*",
			},
			mustNotContain: []string{
				"ghp_secret123",
			},
		},
		{
			name: "pushurl is also sanitized",
			input: `[remote "origin"]
	url = https://github.com/user/repo.git
	pushurl = https://token@github.com/user/repo.git
`,
			mustContain: []string{
				"pushurl = https://github.com/user/repo.git",
			},
			mustNotContain: []string{
				"token@",
			},
		},
		{
			name: "credential section is dropped entirely",
			input: `[core]
	repositoryformatversion = 0
[credential]
	helper = store
[remote "origin"]
	url = https://github.com/user/repo.git
`,
			mustContain: []string{
				"[core]",
				"repositoryformatversion = 0",
				`[remote "origin"]`,
				"https://github.com/user/repo.git",
			},
			mustNotContain: []string{
				"[credential]",
				"helper = store",
			},
		},
		{
			name: "credential subsection is dropped",
			input: `[credential "https://github.com"]
	username = alice
	helper = !gh auth git-credential
[branch "main"]
	remote = origin
`,
			mustContain: []string{
				`[branch "main"]`,
				"remote = origin",
			},
			mustNotContain: []string{
				"credential",
				"alice",
				"gh auth",
			},
		},
		{
			name: "core, branch, and other sections are preserved verbatim",
			input: `[core]
	repositoryformatversion = 0
	filemode = true
	bare = false
	logallrefupdates = true
[branch "main"]
	remote = origin
	merge = refs/heads/main
[remote "origin"]
	url = git@github.com:user/repo.git
	fetch = +refs/heads/*:refs/remotes/origin/*
`,
			mustContain: []string{
				"repositoryformatversion = 0",
				"filemode = true",
				"logallrefupdates = true",
				`[branch "main"]`,
				"merge = refs/heads/main",
				"git@github.com:user/repo.git",
			},
		},
		{
			name: "ssh url with embedded user is preserved",
			input: `[remote "origin"]
	url = ssh://git@github.com/user/repo.git
`,
			mustContain: []string{
				"ssh://git@github.com/user/repo.git",
			},
		},
		{
			name:        "empty config produces empty output",
			input:       "",
			mustContain: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			src := filepath.Join(tmp, "config")
			dst := filepath.Join(tmp, "config.safe")
			if err := os.WriteFile(src, []byte(tt.input), 0o644); err != nil {
				t.Fatal(err)
			}

			if err := generateSafeRepoConfig(src, dst); err != nil {
				t.Fatalf("generateSafeRepoConfig: %v", err)
			}

			data, err := os.ReadFile(dst)
			if err != nil {
				t.Fatalf("read result: %v", err)
			}
			got := string(data)

			for _, want := range tt.mustContain {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q\nfull output:\n%s", want, got)
				}
			}
			for _, forbidden := range tt.mustNotContain {
				if strings.Contains(got, forbidden) {
					t.Errorf("output contains forbidden %q\nfull output:\n%s", forbidden, got)
				}
			}
		})
	}
}

func TestGit_Setup_ReadOnlyMode_GeneratesSafeRepoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")
	projectDir := filepath.Join(tmpDir, "project")
	gitDir := filepath.Join(projectDir, ".git")

	for _, d := range []string{homeDir, sandboxHome, gitDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Source .git/config with an embedded token in a remote URL.
	repoConfig := filepath.Join(gitDir, "config")
	repoConfigContent := `[core]
	repositoryformatversion = 0
[remote "origin"]
	url = https://ghp_supersecret@github.com/user/repo.git
[credential]
	helper = store
`
	if err := os.WriteFile(repoConfig, []byte(repoConfigContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// A user gitconfig so the existing safe-gitconfig path also runs.
	gitconfig := filepath.Join(homeDir, ".gitconfig")
	if err := os.WriteFile(gitconfig, []byte("[user]\n\tname = Test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// XDG_CONFIG_HOME is not covered by the resolver's HOME override, so without
	// this the run reads the developer's real ~/.config/git/config and copies
	// their real global ignore file into the test's sandbox home.
	isolateGitEnv(t, homeDir)

	g := &Git{}
	g.Configure(GlobalConfig{ProjectDir: projectDir}, map[string]any{"mode": "readonly"})

	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	safePath := filepath.Join(sandboxHome, ".git-config.safe")
	data, err := os.ReadFile(safePath)
	if err != nil {
		t.Fatalf("safe repo config not generated: %v", err)
	}
	got := string(data)

	if !strings.Contains(got, "repositoryformatversion = 0") {
		t.Error("safe repo config should preserve [core] settings so git can open the repo")
	}
	if !strings.Contains(got, "https://github.com/user/repo.git") {
		t.Error("safe repo config should keep the sanitized remote URL")
	}
	if strings.Contains(got, "ghp_supersecret") {
		t.Errorf("safe repo config leaked credentials:\n%s", got)
	}
	if strings.Contains(got, "[credential]") || strings.Contains(got, "helper = store") {
		t.Errorf("safe repo config should drop credential section:\n%s", got)
	}
}

// TestGit_Setup_ReadOnlyMode_RepoConfigDestinationIsSymlink pins that the safe
// repo config is written through the destination *name*, never through a link
// found there.
//
// sandboxHome is bind-mounted read-write into the sandbox at homeDir and
// nothing shadows this file, so a session can leave a symlink at
// .git-config.safe pointing at any host file. Following it gave the sandbox two
// primitives at once: the write truncated and overwrote the host file, and the
// binding - which resolves its source on the host - mounted whatever the link
// named into the next session at .git/config.
func TestGit_Setup_ReadOnlyMode_RepoConfigDestinationIsSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")
	projectDir := filepath.Join(tmpDir, "project")
	gitDir := filepath.Join(projectDir, ".git")

	for _, d := range []string{homeDir, sandboxHome, gitDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	isolateGitEnv(t, homeDir)

	writeFile(t, filepath.Join(gitDir, "config"), "[core]\n\trepositoryformatversion = 0\n")

	victim := filepath.Join(tmpDir, "victim")
	const victimContent = "host file the sandbox must not reach\n"
	writeFile(t, victim, victimContent)

	safePath := filepath.Join(sandboxHome, ".git-config.safe")
	if err := os.Symlink(victim, safePath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// The victim is newer than the source, which is what used to make the mtime
	// comparison short-circuit and leave the link standing for the binding.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(victim, future, future); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	g := &Git{}
	g.Configure(GlobalConfig{ProjectDir: projectDir}, map[string]any{"mode": "readonly"})

	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	if got, err := os.ReadFile(victim); err != nil || string(got) != victimContent {
		t.Errorf("the host file was written through the symlink: content = %q, err = %v", got, err)
	}

	info, err := os.Lstat(safePath)
	if err != nil {
		t.Fatalf("Lstat(%s): %v", safePath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the symlink is still in place, so the binding would mount whatever it names")
	}
	got, err := os.ReadFile(safePath)
	if err != nil {
		t.Fatalf("safe repo config not generated: %v", err)
	}
	if !strings.Contains(string(got), "repositoryformatversion = 0") {
		t.Errorf("safe repo config = %q, want the sanitized source", got)
	}
}

func TestGit_Setup_ReadOnlyMode_NoRepoConfig(t *testing.T) {
	// When projectDir has no .git/config, Setup should still succeed (no-op for repo config).
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")
	projectDir := filepath.Join(tmpDir, "project")
	for _, d := range []string{homeDir, sandboxHome, projectDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// XDG_CONFIG_HOME is not covered by the resolver's HOME override, so without
	// this the run reads the developer's real ~/.config/git/config and copies
	// their real global ignore file into the test's sandbox home.
	isolateGitEnv(t, homeDir)

	g := &Git{}
	g.Configure(GlobalConfig{ProjectDir: projectDir}, map[string]any{"mode": "readonly"})

	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Errorf("Setup should succeed when .git/config is missing, got: %v", err)
	}

	safePath := filepath.Join(sandboxHome, ".git-config.safe")
	if _, err := os.Stat(safePath); !os.IsNotExist(err) {
		t.Error("safe repo config should not be created when source is missing")
	}
}

func TestGit_Setup_ReadOnlyMode_NonRegularRepoConfig(t *testing.T) {
	// Recursive-sandbox case: .git/config is a device file (e.g., /dev/null).
	// Setup must not crash; it should silently skip safe-config generation.
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")
	projectDir := filepath.Join(tmpDir, "project")
	gitDir := filepath.Join(projectDir, ".git")
	for _, d := range []string{homeDir, sandboxHome, gitDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Symlink .git/config to /dev/null to simulate the device-file case.
	repoConfig := filepath.Join(gitDir, "config")
	if err := os.Symlink("/dev/null", repoConfig); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// XDG_CONFIG_HOME is not covered by the resolver's HOME override, so without
	// this the run reads the developer's real ~/.config/git/config and copies
	// their real global ignore file into the test's sandbox home.
	isolateGitEnv(t, homeDir)

	g := &Git{}
	g.Configure(GlobalConfig{ProjectDir: projectDir}, map[string]any{"mode": "readonly"})

	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Errorf("Setup should tolerate non-regular .git/config, got: %v", err)
	}

	safePath := filepath.Join(sandboxHome, ".git-config.safe")
	if _, err := os.Stat(safePath); !os.IsNotExist(err) {
		t.Error("safe repo config should not be created when source is non-regular")
	}
}

func TestGitReadOnlyBindingsUsesGitRepoRootForWorktree(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	sandboxHome := t.TempDir()

	g := &Git{}
	g.Configure(GlobalConfig{ProjectDir: wt, GitRepoRoot: repo}, map[string]any{"mode": "readonly"})
	bindings := g.Bindings(home, sandboxHome)

	// Exactly one binding targets <repo>/.git, readonly, with Dest pinned to host path.
	want := filepath.Join(repo, ".git")
	found := false
	for _, b := range bindings {
		if b.Source == want && b.Dest == want && b.ReadOnly {
			found = true
		}
		// Must NOT bind <wt>/.git as a directory — it's a file in worktree mode.
		if b.Source == filepath.Join(wt, ".git") {
			t.Errorf("unexpectedly bound worktree .git as directory: %+v", b)
		}
	}
	if !found {
		t.Errorf("expected readonly binding of %s with Dest pinned to host path; got %+v", want, bindings)
	}
}

func TestGitReadWriteBindingsWorktreeMountsMainGitDir(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir()

	g := &Git{}
	g.Configure(GlobalConfig{ProjectDir: wt, GitRepoRoot: repo}, map[string]any{"mode": "readwrite"})
	bindings := g.Bindings(t.TempDir(), t.TempDir())

	// In readwrite worktree mode, main repo's .git must be bound writable
	// with Dest pinned to host path so the gitdir: pointer resolves.
	want := filepath.Join(repo, ".git")
	found := false
	for _, b := range bindings {
		if b.Source == want && b.Dest == want {
			if b.ReadOnly {
				t.Errorf("readwrite worktree .git binding should be writable, got ReadOnly=true")
			}
			found = true
		}
	}
	if !found {
		t.Errorf("expected writable binding of %s with Dest pinned; got %+v", want, bindings)
	}
}

func TestGitReadWriteBindingsNoWorktreeNoExtraMount(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	g := &Git{}
	g.Configure(GlobalConfig{ProjectDir: repo, GitRepoRoot: ""}, map[string]any{"mode": "readwrite"})
	bindings := g.Bindings(t.TempDir(), t.TempDir())

	// Non-worktree readwrite: should have exactly 4 bindings (gitconfig, credentials, ssh, gnupg).
	// No extra .git binding — it's included in the project dir mount.
	if len(bindings) != 4 {
		t.Errorf("expected 4 bindings for non-worktree readwrite, got %d: %+v", len(bindings), bindings)
	}
	for _, b := range bindings {
		if b.Source == filepath.Join(repo, ".git") {
			t.Errorf("non-worktree readwrite should not have explicit .git binding: %+v", b)
		}
	}
}

func TestGitReadOnlyBindingsMainRepoUnchanged(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &Git{}
	g.Configure(GlobalConfig{ProjectDir: repo, GitRepoRoot: ""}, map[string]any{"mode": "readonly"})
	bindings := g.Bindings(t.TempDir(), t.TempDir())
	for _, b := range bindings {
		if b.Source == filepath.Join(repo, ".git") && b.ReadOnly {
			return
		}
	}
	t.Errorf("expected readonly binding of %s/.git in non-worktree mode; got %+v", repo, bindings)
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

func TestParseGitConfigList(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []gitConfigEntry
	}{
		{
			name: "single record",
			data: "global\x00file:/home/u/.gitconfig\x00user.name\nAda\x00",
			want: []gitConfigEntry{
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "user.name", value: "Ada"},
			},
		},
		{
			name: "multiple records across origins",
			data: "global\x00file:/home/u/.gitconfig\x00user.name\nAda\x00" +
				"global\x00file:/home/u/.gitconfig-work\x00user.email\nada@corp\x00",
			want: []gitConfigEntry{
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "user.name", value: "Ada"},
				{scope: "global", origin: "file:/home/u/.gitconfig-work", key: "user.email", value: "ada@corp"},
			},
		},
		{
			name: "valueless boolean key",
			data: "local\x00file:.git/config\x00core.bare\x00",
			want: []gitConfigEntry{
				{scope: "local", origin: "file:.git/config", key: "core.bare", value: ""},
			},
		},
		{
			name: "valueless boolean key between records",
			data: "local\x00file:.git/config\x00core.bare\x00" +
				"global\x00file:/home/u/.gitconfig\x00user.name\nAda\x00",
			want: []gitConfigEntry{
				{scope: "local", origin: "file:.git/config", key: "core.bare", value: ""},
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "user.name", value: "Ada"},
			},
		},
		{
			name: "value containing a literal newline",
			data: "global\x00file:/home/u/.gitconfig\x00alias.lg\nlog\n--oneline\x00",
			want: []gitConfigEntry{
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "alias.lg", value: "log\n--oneline"},
			},
		},
		{
			name: "empty value after separator",
			data: "global\x00file:/home/u/.gitconfig\x00user.email\n\x00",
			want: []gitConfigEntry{
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "user.email", value: ""},
			},
		},
		{
			name: "empty input",
			data: "",
			want: nil,
		},
		{
			name: "only a terminator",
			data: "\x00",
			want: nil,
		},
		{
			name: "truncated trailing group is discarded",
			data: "global\x00file:/home/u/.gitconfig\x00user.name\nAda\x00global\x00file:/home/u/.gitconfig\x00",
			want: []gitConfigEntry{
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "user.name", value: "Ada"},
			},
		},
		{
			name: "unterminated final record is still parsed",
			data: "global\x00file:/home/u/.gitconfig\x00user.name\nAda",
			want: []gitConfigEntry{
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "user.name", value: "Ada"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseGitConfigList([]byte(tt.data))
			if len(got) != len(tt.want) {
				t.Fatalf("got %d entries %+v, want %d %+v", len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("entry %d: got %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestGlobalConfigMap(t *testing.T) {
	tests := []struct {
		name        string
		entries     []gitConfigEntry
		want        map[string]string
		wantOrigins map[string]string
	}{
		{
			name: "keeps only global scope",
			entries: []gitConfigEntry{
				{scope: "system", origin: "file:/etc/gitconfig", key: "core.editor", value: "vi"},
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "user.name", value: "Ada"},
				{scope: "local", origin: "file:.git/config", key: "user.email", value: "ada@local"},
				{scope: "worktree", origin: "file:.git/config.worktree", key: "core.bare", value: ""},
				{scope: "command", origin: "command line:", key: "user.name", value: "Override"},
			},
			want:        map[string]string{"user.name": "Ada"},
			wantOrigins: map[string]string{"user.name": "file:/home/u/.gitconfig"},
		},
		{
			name: "last wins so an include overrides the outer file",
			entries: []gitConfigEntry{
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "user.email", value: "ada@personal"},
				{scope: "global", origin: "file:/home/u/.gitconfig-work", key: "user.email", value: "ada@corp"},
			},
			want:        map[string]string{"user.email": "ada@corp"},
			wantOrigins: map[string]string{"user.email": "file:/home/u/.gitconfig-work"},
		},
		{
			name: "non-global entry does not override a global one",
			entries: []gitConfigEntry{
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "user.email", value: "ada@corp"},
				{scope: "local", origin: "file:.git/config", key: "user.email", value: "ada@local"},
			},
			want:        map[string]string{"user.email": "ada@corp"},
			wantOrigins: map[string]string{"user.email": "file:/home/u/.gitconfig"},
		},
		{
			name: "valueless boolean key is retained with an empty value",
			entries: []gitConfigEntry{
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "core.bare", value: ""},
			},
			want:        map[string]string{"core.bare": ""},
			wantOrigins: map[string]string{"core.bare": "file:/home/u/.gitconfig"},
		},
		{
			name:        "no entries",
			entries:     nil,
			want:        map[string]string{},
			wantOrigins: map[string]string{},
		},
		{
			name: "no global entries",
			entries: []gitConfigEntry{
				{scope: "local", origin: "file:.git/config", key: "user.name", value: "Ada"},
			},
			want:        map[string]string{},
			wantOrigins: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotOrigins := globalConfigMap(tt.entries)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("globalConfigMap() values = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(gotOrigins, tt.wantOrigins) {
				t.Errorf("globalConfigMap() origins = %v, want %v", gotOrigins, tt.wantOrigins)
			}
		})
	}
}

func TestGitCommandEnv(t *testing.T) {
	got := gitCommandEnv([]string{"PATH=/bin", "HOME=/old", "TERM=xterm"}, "/new")
	want := []string{"PATH=/bin", "TERM=xterm", "HOME=/new", "LC_ALL=C"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("gitCommandEnv() = %v, want %v", got, want)
	}

	got = gitCommandEnv([]string{"PATH=/bin"}, "/new")
	want = []string{"PATH=/bin", "HOME=/new", "LC_ALL=C"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("gitCommandEnv() with no existing HOME = %v, want %v", got, want)
	}

	// isUnsupportedShowScope matches git's untranslated stderr, so a host
	// locale must not reach the resolver. LANGUAGE outranks LC_ALL in gettext,
	// so overriding LC_ALL alone would not be enough.
	got = gitCommandEnv([]string{"LC_ALL=de_DE.UTF-8", "LANGUAGE=de", "LANG=de_DE.UTF-8"}, "/new")
	want = []string{"LANG=de_DE.UTF-8", "HOME=/new", "LC_ALL=C"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("gitCommandEnv() with a host locale = %v, want %v", got, want)
	}
}

// TestGit_ResolveGlobalConfig_IncludeIfGitdir is the one test that shells out to
// real git. It proves the part only git can prove: that an includeIf "gitdir:"
// block resolves against cmd.Dir, which is why resolveGlobalConfig drops
// --global and sets the working directory instead.
func TestGit_ResolveGlobalConfig_IncludeIfGitdir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// t.TempDir() hands back an unresolved path on macOS (/var -> /private/var)
	// while git matches gitdir: against the resolved one, so the pattern must be
	// built from the resolved form or this passes on Linux and fails on macOS CI.
	resolve := func(dir string) string {
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatalf("EvalSymlinks(%s): %v", dir, err)
		}
		return resolved
	}

	homeDir := resolve(t.TempDir())
	workRepo := filepath.Join(homeDir, "work", "project")
	otherRepo := filepath.Join(homeDir, "personal", "project")

	// Set before the fixture is built, not after: `git init` reads the global
	// config too (init.defaultBranch, init.templateDir, core.hooksPath), so a
	// developer's own ~/.config/git/config would shape the repos the
	// assertions then run against. HOME alone does not cover XDG.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(homeDir, ".config"))

	for _, dir := range []string{workRepo, otherRepo} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
		cmd := exec.Command("git", "init", "-q")
		cmd.Dir = dir
		cmd.Env = gitCommandEnv(os.Environ(), homeDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init in %s: %v: %s", dir, err, out)
		}
	}

	workConfig := filepath.Join(homeDir, ".gitconfig-work")
	writeFile(t, workConfig, "[user]\n\temail = ada@corp\n")
	writeFile(t, filepath.Join(homeDir, ".gitconfig"), ""+
		"[user]\n"+
		"\tname = Ada\n"+
		"\temail = ada@personal\n"+
		"[includeIf \"gitdir:"+filepath.Join(homeDir, "work")+"/\"]\n"+
		"\tpath = "+workConfig+"\n")

	t.Run("include resolves against the project dir", func(t *testing.T) {
		g := &Git{mode: GitModeReadOnly, projectDir: workRepo}
		values, origins, _, err := g.resolveGlobalConfig(homeDir)
		if err != nil {
			t.Fatalf("resolveGlobalConfig: %v", err)
		}
		// The origin is what tells an included file apart from the outer one,
		// and it is what copyAuxFile anchors its trust decision on.
		if got, want := origins["user.email"], "file:"+workConfig; got != want {
			t.Errorf("origins[user.email] = %q, want %q", got, want)
		}
		if values["user.email"] != "ada@corp" {
			t.Errorf("user.email = %q, want %q", values["user.email"], "ada@corp")
		}
		if values["user.name"] != "Ada" {
			t.Errorf("user.name = %q, want %q", values["user.name"], "Ada")
		}
		// git reports the include directive itself as an ordinary global key
		// alongside the values it pulled in. The resolver keeps every global
		// key; dropping this one is the allowlist's job, not the resolver's.
		includeKey := "includeif.gitdir:" + filepath.Join(homeDir, "work") + "/.path"
		if values[includeKey] != workConfig {
			t.Errorf("%s = %q, want %q", includeKey, values[includeKey], workConfig)
		}
	})

	t.Run("non-matching project dir keeps the outer identity", func(t *testing.T) {
		g := &Git{mode: GitModeReadOnly, projectDir: otherRepo}
		values, _, _, err := g.resolveGlobalConfig(homeDir)
		if err != nil {
			t.Fatalf("resolveGlobalConfig: %v", err)
		}
		if values["user.email"] != "ada@personal" {
			t.Errorf("user.email = %q, want %q", values["user.email"], "ada@personal")
		}
	})

	t.Run("empty project dir does not match the include", func(t *testing.T) {
		g := &Git{mode: GitModeReadOnly}
		values, _, _, err := g.resolveGlobalConfig(homeDir)
		if err != nil {
			t.Fatalf("resolveGlobalConfig: %v", err)
		}
		if values["user.email"] != "ada@personal" {
			t.Errorf("user.email = %q, want %q", values["user.email"], "ada@personal")
		}
	})
}

// TestGit_ResolveGlobalConfig_BrokenLocalConfig covers the cost of dropping
// --global: the resolver now performs repository discovery and reads the local
// config, so a broken .git/config aborts the whole command with exit 128 and
// would take the global scope down with it. The user's identity does not belong
// to that repository, so it must survive.
func TestGit_ResolveGlobalConfig_BrokenLocalConfig(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	homeDir := t.TempDir()
	isolateGitEnv(t, homeDir)

	projectDir := filepath.Join(homeDir, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	initCmd := exec.Command("git", "init", "-q")
	initCmd.Dir = projectDir
	initCmd.Env = gitCommandEnv(os.Environ(), homeDir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	// An unterminated section header: git reports "bad config line" and exits
	// 128. Chosen over chmod 000 because it fails the same way for root.
	writeFile(t, filepath.Join(projectDir, ".git", "config"), "[core\n\trepositoryformatversion = 0\n")

	writeFile(t, filepath.Join(homeDir, ".gitconfig"), "[user]\n\tname = Ada\n\temail = ada@corp\n")

	g := &Git{mode: GitModeReadOnly, projectDir: projectDir}
	values, origins, retried, err := g.resolveGlobalConfig(homeDir)
	if err != nil {
		t.Fatalf("resolveGlobalConfig: %v", err)
	}
	if retried == nil {
		t.Fatal("retried = nil, want the first attempt's error")
	}
	if values["user.name"] != "Ada" || values["user.email"] != "ada@corp" {
		t.Errorf("values = %v, want the host identity", values)
	}
	if origins["user.name"] != "file:"+filepath.Join(homeDir, ".gitconfig") {
		t.Errorf("origins[user.name] = %q, want the host global config", origins["user.name"])
	}

	t.Run("a plain include still expands on the retry", func(t *testing.T) {
		included := filepath.Join(homeDir, ".gitconfig-included")
		writeFile(t, included, "[user]\n\temail = included@corp\n")
		writeFile(t, filepath.Join(homeDir, ".gitconfig"), ""+
			"[user]\n\tname = Ada\n"+
			"[include]\n\tpath = "+included+"\n")

		values, _, retried, err := g.resolveGlobalConfig(homeDir)
		if err != nil {
			t.Fatalf("resolveGlobalConfig: %v", err)
		}
		if retried == nil {
			t.Fatal("retried = nil, want the first attempt's error")
		}
		// Only includeIf conditions need a repository. Losing plain includes
		// too would put the retry no further ahead than the parser fallback.
		if values["user.email"] != "included@corp" {
			t.Errorf("user.email = %q, want the included value", values["user.email"])
		}
		// Nothing was lost, so the retry stays silent - that is what keeps the
		// alert meaning something on the hosts where it does fire.
		if hasConditionalIncludes(values) {
			t.Error("hasConditionalIncludes() = true for a config with no includeIf")
		}
	})

	t.Run("both attempts failing reports the original error", func(t *testing.T) {
		stubGit(t, "exit 128")

		if _, _, _, err := g.resolveGlobalConfig(homeDir); err == nil {
			t.Error("resolveGlobalConfig() = nil error, want the failure from both attempts")
		}
	})
}

func TestHasConditionalIncludes(t *testing.T) {
	if hasConditionalIncludes(map[string]string{"user.name": "Ada", "include.path": "/x"}) {
		t.Error("hasConditionalIncludes() = true for a config with only a plain include")
	}
	if !hasConditionalIncludes(map[string]string{"includeif.gitdir:/work/.path": "/x"}) {
		t.Error("hasConditionalIncludes() = false for a config with an includeIf")
	}
}

// TestQuoteGitConfigValue_InvalidUTF8 pins that the writer copies bytes. A git
// config value is a byte string, so ranging over it and calling WriteRune would
// turn a Latin-1 name into U+FFFD - a silent rewrite of the user's identity
// rather than a copy of it.
func TestQuoteGitConfigValue_InvalidUTF8(t *testing.T) {
	raw := "Jan\xe1k"
	got := quoteGitConfigValue(raw)
	if want := `"` + raw + `"`; got != want {
		t.Errorf("quoteGitConfigValue(%q) = %q, want %q", raw, got, want)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

// isolateGitEnv keeps a test off the developer's own git configuration. The
// code under test overrides HOME with the homeDir it is handed, which does not
// cover $XDG_CONFIG_HOME/git/config - git reads that one regardless.
func isolateGitEnv(t *testing.T, homeDir string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(homeDir, ".config"))
}

func TestGenerateSafeGitconfig(t *testing.T) {
	tests := []struct {
		name        string
		values      map[string]string
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:        "name and email from the resolved map",
			values:      map[string]string{"user.name": "Ada", "user.email": "ada@corp"},
			wantContain: []string{"[user]", `name = "Ada"`, `email = "ada@corp"`},
		},
		{
			name: "every non-allowlisted key is dropped",
			values: map[string]string{
				"user.name":                     "Ada",
				"user.email":                    "ada@corp",
				"user.signingkey":               "ABC123",
				"credential.helper":             "store",
				"alias.co":                      "checkout",
				"url.git@github.com:.insteadof": "https://github.com/",
				"http.extraheader":              "Authorization: Basic c2VjcmV0",
				"sendemail.smtppass":            "hunter2",
				"includeif.gitdir:/work/.path":  "/home/u/.gitconfig-work",
				"core.editor":                   "vim",
			},
			wantContain: []string{`name = "Ada"`, `email = "ada@corp"`},
			wantAbsent: []string{
				"signingkey", "ABC123", "credential", "store", "alias", "checkout",
				"insteadof", "extraheader", "c2VjcmV0", "hunter2", "includeif",
				"gitconfig-work", "editor", "vim",
			},
		},
		{
			name:        "name only",
			values:      map[string]string{"user.name": "Ada"},
			wantContain: []string{"[user]", `name = "Ada"`},
			wantAbsent:  []string{"email"},
		},
		{
			name:        "email only",
			values:      map[string]string{"user.email": "ada@corp"},
			wantContain: []string{"[user]", `email = "ada@corp"`},
			wantAbsent:  []string{"name ="},
		},
		{
			name:        "empty map still writes a section header",
			values:      map[string]string{},
			wantContain: []string{"[user]"},
			wantAbsent:  []string{"name", "email"},
		},
		{
			name:        "surrounding whitespace is trimmed",
			values:      map[string]string{"user.name": "  Ada  ", "user.email": "\tada@corp\t"},
			wantContain: []string{"name = \"Ada\"\n", "email = \"ada@corp\"\n"},
		},
		{
			name:        "whitespace-only values are omitted",
			values:      map[string]string{"user.name": "   ", "user.email": ""},
			wantContain: []string{"[user]"},
			wantAbsent:  []string{"name =", "email ="},
		},
		{
			name: "file-valued keys are emitted under [core]",
			values: map[string]string{
				"user.name":           "Ada",
				"core.excludesfile":   "/home/u/.gitignore.safe",
				"core.attributesfile": "/home/u/.gitattributes.safe",
			},
			wantContain: []string{
				"[core]",
				"excludesFile = \"/home/u/.gitignore.safe\"\n",
				"attributesFile = \"/home/u/.gitattributes.safe\"\n",
			},
		},
		{
			name:        "no [core] section when no file-valued key survived",
			values:      map[string]string{"user.name": "Ada", "core.excludesfile": "  "},
			wantContain: []string{`name = "Ada"`},
			wantAbsent:  []string{"[core]", "excludesFile"},
		},
		{
			// git keeps the newlines in a `name = "Ada\n[core]\n..."` value, so
			// pasting one after `name = ` opens a section the allowlist never
			// agreed to emit. The key is dropped rather than written.
			name: "a value carrying newlines cannot inject a section",
			values: map[string]string{
				"user.name":  "Ada\n[core]\n\tsshCommand = /tmp/evil.sh",
				"user.email": "ada@corp",
			},
			wantContain: []string{`email = "ada@corp"`},
			wantAbsent:  []string{"sshCommand", "evil.sh", "[core]"},
		},
		{
			name:        "a value carrying a C1 control character is dropped",
			values:      map[string]string{"user.name": "Ada\u009bm", "user.email": "ada@corp"},
			wantContain: []string{`email = "ada@corp"`},
			wantAbsent:  []string{"name ="},
		},
		{
			name:        "a comment introducer is quoted rather than truncating the value",
			values:      map[string]string{"user.name": "Jane #1 Dev", "user.email": "a;b@corp"},
			wantContain: []string{`name = "Jane #1 Dev"`, `email = "a;b@corp"`},
		},
		{
			name:        "quotes and backslashes in a value are escaped",
			values:      map[string]string{"user.name": `Ada "The Countess" \Lovelace`},
			wantContain: []string{`name = "Ada \"The Countess\" \\Lovelace"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A dropped value alerts; keep it off the test runner's stderr.
			captureNotices(t)

			dst := filepath.Join(t.TempDir(), ".gitconfig.safe")
			if err := generateSafeGitconfig(tt.values, dst); err != nil {
				t.Fatalf("generateSafeGitconfig: %v", err)
			}

			data, err := os.ReadFile(dst)
			if err != nil {
				t.Fatalf("read %s: %v", dst, err)
			}
			got := string(data)

			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("safe gitconfig missing %q, got:\n%s", want, got)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("safe gitconfig should not contain %q, got:\n%s", absent, got)
				}
			}
		})
	}
}

// TestGenerateSafeGitconfig_RoundTripsThroughGit is the assertion the string
// checks cannot make: that the file devsandbox writes means to git exactly what
// the allowlist says. A `#` truncating a name and a newline opening a `[core]`
// section are both invisible to a Contains() check on the raw bytes.
func TestGenerateSafeGitconfig_RoundTripsThroughGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	captureNotices(t)

	dst := filepath.Join(t.TempDir(), ".gitconfig.safe")
	values := map[string]string{
		"user.name":           `Jane #1 "Dev" \Smith`,
		"user.email":          "jane;test@corp",
		"core.excludesfile":   "/home/u/.gitignore.safe",
		"core.attributesfile": "/home/u/.gitattributes.safe",
		"user.signingkey":     "Ada\n[core]\n\tsshCommand = /tmp/evil.sh",
	}
	if err := generateSafeGitconfig(values, dst); err != nil {
		t.Fatalf("generateSafeGitconfig: %v", err)
	}

	get := func(key string) string {
		out, err := exec.Command("git", "config", "-f", dst, "--get", key).Output()
		if err != nil {
			return ""
		}
		return strings.TrimRight(string(out), "\n")
	}

	for key, want := range map[string]string{
		"user.name":           `Jane #1 "Dev" \Smith`,
		"user.email":          "jane;test@corp",
		"core.excludesfile":   "/home/u/.gitignore.safe",
		"core.attributesfile": "/home/u/.gitattributes.safe",
	} {
		if got := get(key); got != want {
			t.Errorf("git reads %s = %q, want %q", key, got, want)
		}
	}

	// Nothing outside the allowlist may be readable, however the host spelled
	// the values it was built from.
	out, err := exec.Command("git", "config", "-f", dst, "--list").Output()
	if err != nil {
		t.Fatalf("git config --list: %v", err)
	}
	var keys []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		key, _, _ := strings.Cut(line, "=")
		keys = append(keys, key)
	}
	want := []string{"user.name", "user.email", "core.excludesfile", "core.attributesfile"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("git reads keys %v, want exactly %v", keys, want)
	}
}

func TestGlobalConfigSources(t *testing.T) {
	homeDir := "/home/u"

	// git reads $XDG_CONFIG_HOME/git/config *instead of* ~/.config/git/config,
	// never both, so listing both would rank a file git ignores above the one it
	// reads - and fallbackIdentity walks this list last-wins.
	t.Run("XDG_CONFIG_HOME set replaces the default XDG path", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/xdg")
		got := globalConfigSources(homeDir)
		want := []string{"/xdg/git/config", "/home/u/.gitconfig"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("globalConfigSources() = %v, want %v", got, want)
		}
	})

	t.Run("XDG_CONFIG_HOME unset", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		got := globalConfigSources(homeDir)
		want := []string{"/home/u/.config/git/config", "/home/u/.gitconfig"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("globalConfigSources() = %v, want %v", got, want)
		}
	})

	t.Run("XDG_CONFIG_HOME pointing at the default is not listed twice", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/home/u/.config")
		got := globalConfigSources(homeDir)
		want := []string{"/home/u/.config/git/config", "/home/u/.gitconfig"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("globalConfigSources() = %v, want %v", got, want)
		}
	})
}

func TestExistingGlobalConfigs(t *testing.T) {
	homeDir := t.TempDir()
	isolateGitEnv(t, homeDir)

	if got := existingGlobalConfigs(homeDir); len(got) != 0 {
		t.Errorf("existingGlobalConfigs() = %v, want none", got)
	}

	xdgConfig := filepath.Join(homeDir, ".config", "git", "config")
	if err := os.MkdirAll(filepath.Dir(xdgConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, xdgConfig, "[user]\n\tname = XDG\n")

	if got := existingGlobalConfigs(homeDir); !reflect.DeepEqual(got, []string{xdgConfig}) {
		t.Errorf("existingGlobalConfigs() = %v, want %v", got, []string{xdgConfig})
	}

	gitconfig := filepath.Join(homeDir, ".gitconfig")
	writeFile(t, gitconfig, "[user]\n\tname = Home\n")

	// git reads the XDG file first and ~/.gitconfig second, so the order is
	// what makes last-wins in fallbackIdentity match git's own precedence.
	want := []string{xdgConfig, gitconfig}
	if got := existingGlobalConfigs(homeDir); !reflect.DeepEqual(got, want) {
		t.Errorf("existingGlobalConfigs() = %v, want %v", got, want)
	}

	t.Run("non-regular source is not a config file", func(t *testing.T) {
		otherHome := t.TempDir()
		isolateGitEnv(t, otherHome)
		if err := os.MkdirAll(filepath.Join(otherHome, ".gitconfig"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := existingGlobalConfigs(otherHome); len(got) != 0 {
			t.Errorf("existingGlobalConfigs() = %v, want none for a non-regular source", got)
		}
	})
}

func TestFallbackValues(t *testing.T) {
	dir := t.TempDir()

	xdgConfig := filepath.Join(dir, "xdg-config")
	writeFile(t, xdgConfig, "[user]\n\tname = XDG Name\n\temail = xdg@example.com\n")
	gitconfig := filepath.Join(dir, "gitconfig")
	writeFile(t, gitconfig, "[user]\n\temail = home@example.com\n")

	// Later file wins per key, and a key it does not set is left standing.
	got, gotOrigins := fallbackValues([]string{xdgConfig, gitconfig})
	want := map[string]string{"user.name": "XDG Name", "user.email": "home@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fallbackValues() = %v, want %v", got, want)
	}
	// Each value is attributed to the file it was actually read from, not to
	// the last file in the list: copyAuxFile refuses a value whose origin it
	// cannot resolve, so a missing or wrong origin silently drops the key.
	wantOrigins := map[string]string{
		"user.name":  "file:" + xdgConfig,
		"user.email": "file:" + gitconfig,
	}
	if !reflect.DeepEqual(gotOrigins, wantOrigins) {
		t.Errorf("fallbackValues() origins = %v, want %v", gotOrigins, wantOrigins)
	}

	if got, origins := fallbackValues(nil); len(got) != 0 || len(origins) != 0 {
		t.Errorf("fallbackValues(nil) = %v/%v, want empty", got, origins)
	}

	// An identity that only an include supplies is invisible here - that is the
	// downgrade the notice warns about, not a bug in the fallback.
	includeOnly := filepath.Join(dir, "include-only")
	writeFile(t, includeOnly, "[includeIf \"gitdir:/work/\"]\n\tpath = /nowhere\n")
	if got, _ := fallbackValues([]string{includeOnly}); len(got) != 0 {
		t.Errorf("fallbackValues() = %v, want empty for an include-only config", got)
	}

	// The file-valued keys come back too. Without them copyAuxFiles reads a
	// configured core.excludesFile as unset and carries git's XDG default
	// instead - a file the host does not use, in place of the one it does.
	auxConfig := filepath.Join(dir, "aux-config")
	writeFile(t, auxConfig, "[core]\n\texcludesFile = ~/.gitignore_global\n")
	gotAux, gotAuxOrigins := fallbackValues([]string{auxConfig})
	if gotAux["core.excludesfile"] != "~/.gitignore_global" {
		t.Errorf("core.excludesfile = %q, want %q", gotAux["core.excludesfile"], "~/.gitignore_global")
	}
	if gotAuxOrigins["core.excludesfile"] != "file:"+auxConfig {
		t.Errorf("core.excludesfile origin = %q, want %q", gotAuxOrigins["core.excludesfile"], "file:"+auxConfig)
	}
}

func TestIsUnsupportedShowScope(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{
			name:   "git 2.25 usage error",
			stderr: "error: unknown option `show-scope'\nusage: git config [<options>]\n",
			want:   true,
		},
		{
			name:   "usage banner naming the option",
			stderr: "usage: git config --show-scope is not a thing here\n",
			want:   true,
		},
		{
			name:   "unrelated failure",
			stderr: "fatal: unable to read config file '/home/u/.gitconfig': Permission denied\n",
			want:   false,
		},
		{
			name:   "dubious ownership",
			stderr: "fatal: detected dubious ownership in repository at '/project'\n",
			want:   false,
		},
		{
			name:   "empty",
			stderr: "",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUnsupportedShowScope([]byte(tt.stderr)); got != tt.want {
				t.Errorf("isUnsupportedShowScope(%q) = %v, want %v", tt.stderr, got, tt.want)
			}
		})
	}
}

// TestGit_SetupUserGitconfig_ResolverFailureFallsBack neuters PATH so the
// resolver cannot run at all, which is the fallback path the error policy
// describes: the safe config is still written, from the top-level [user]
// section alone.
func TestGit_SetupUserGitconfig_ResolverFailureFallsBack(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")
	emptyBin := filepath.Join(tmpDir, "empty-bin")

	for _, d := range []string{homeDir, sandboxHome, emptyBin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	isolateGitEnv(t, homeDir)
	writeFile(t, filepath.Join(homeDir, ".gitconfig"),
		"[user]\n\tname = Fallback User\n\temail = fallback@example.com\n[credential]\n\thelper = store\n")

	t.Setenv("PATH", emptyBin)
	stderr := captureNotices(t)

	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "readonly"})

	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Fatalf("Setup must not fail when the resolver does: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(sandboxHome, ".gitconfig.safe"))
	if err != nil {
		t.Fatalf("safe gitconfig not generated: %v", err)
	}
	got := string(data)

	if !strings.Contains(got, `name = "Fallback User"`) || !strings.Contains(got, `email = "fallback@example.com"`) {
		t.Errorf("fallback identity missing, got:\n%s", got)
	}
	if strings.Contains(got, "helper") {
		t.Errorf("fallback path must still apply the allowlist, got:\n%s", got)
	}
	// The user is reaching less than the config says, so this half of the error
	// policy is a notice.Alert - not the silent branch old-git takes.
	if !strings.Contains(stderr.String(), "could not read the resolved global config") {
		t.Errorf("a resolver failure must alert, got: %s", stderr)
	}
}

// stubGit puts a fake `git` first on PATH so the resolver's failure modes can be
// driven directly. Everything else in the process still resolves against the
// real PATH, which the stub directory is prepended to.
func stubGit(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "git")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("write stub git: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestGit_SetupUserGitconfig_OldGitFallsBackSilently drives the branch the error
// policy marks silent. --show-scope arrived in git 2.26; an older host is not a
// setting the user got wrong, and a notice here would be raised on every launch
// and, per the warning-confirmation gate, turn each one into a prompt.
func TestGit_SetupUserGitconfig_OldGitFallsBackSilently(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")
	for _, d := range []string{homeDir, sandboxHome} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	isolateGitEnv(t, homeDir)
	// The include is what makes this test non-vacuous: only the resolver
	// expands it, so an assertion that the *top-level* identity was written
	// fails if the stub is not the git that ran.
	included := filepath.Join(homeDir, ".gitconfig-work")
	writeFile(t, included, "[user]\n\tname = Resolver User\n\temail = resolver@example.com\n")
	writeFile(t, filepath.Join(homeDir, ".gitconfig"),
		"[user]\n\tname = Old Git User\n\temail = old@example.com\n"+
			"[include]\n\tpath = "+included+"\n")

	// The exact stderr an old git emits, backtick-quoted option name included.
	stubGit(t, "echo \"error: unknown option \\`show-scope'\" >&2\n"+
		"echo \"usage: git config [<options>]\" >&2\n"+
		"exit 129")
	stderr := captureNotices(t)

	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "readonly"})
	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Fatalf("Setup must not fail on an old git: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(sandboxHome, ".gitconfig.safe"))
	if err != nil {
		t.Fatalf("safe gitconfig not generated: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, `name = "Old Git User"`) {
		t.Errorf("fallback identity missing, got:\n%s", got)
	}
	if strings.Contains(got, "Resolver User") {
		t.Fatal("the stub git did not run: the include was expanded, so this test proves nothing")
	}
	if stderr.Len() != 0 {
		t.Errorf("an unsupported --show-scope must be silent, got: %s", stderr)
	}
}

// TestGit_SetupUserGitconfig_OtherGitFailureAlerts is the mirror: a non-zero
// exit that is not the old-git usage error still has to be reported, or a real
// breakage looks like a host with no identity.
func TestGit_SetupUserGitconfig_OtherGitFailureAlerts(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")
	for _, d := range []string{homeDir, sandboxHome} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	isolateGitEnv(t, homeDir)
	writeFile(t, filepath.Join(homeDir, ".gitconfig"),
		"[user]\n\tname = Ada\n\temail = ada@corp\n")

	stubGit(t, "echo \"fatal: detected dubious ownership in repository\" >&2\nexit 128")
	stderr := captureNotices(t)

	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "readonly"})
	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Fatalf("Setup must not fail when the resolver does: %v", err)
	}

	if !strings.Contains(stderr.String(), "could not read the resolved global config") {
		t.Errorf("an unexpected resolver failure must alert, got: %s", stderr)
	}
}

// TestGit_Setup_ReadOnlyMode_XDGOnlyConfig covers a host whose git identity
// lives solely at ~/.config/git/config. The old existence check looked at
// ~/.gitconfig only, so such a host got no safe config at all.
func TestGit_Setup_ReadOnlyMode_XDGOnlyConfig(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")
	xdgConfig := filepath.Join(homeDir, ".config", "git", "config")

	for _, d := range []string{sandboxHome, filepath.Dir(xdgConfig)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	isolateGitEnv(t, homeDir)
	writeFile(t, xdgConfig, "[user]\n\tname = XDG User\n\temail = xdg@example.com\n")

	if _, err := os.Stat(filepath.Join(homeDir, ".gitconfig")); !os.IsNotExist(err) {
		t.Fatalf("fixture must have no ~/.gitconfig, got err=%v", err)
	}

	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "readonly"})

	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(sandboxHome, ".gitconfig.safe"))
	if err != nil {
		t.Fatalf("safe gitconfig not generated for an XDG-only host: %v", err)
	}
	got := string(data)

	if !strings.Contains(got, `name = "XDG User"`) || !strings.Contains(got, `email = "xdg@example.com"`) {
		t.Errorf("XDG-only identity missing, got:\n%s", got)
	}
}

// TestGit_SetupUserGitconfig_RegeneratesWhenSafeIsNewer pins the removal of the
// mtime cache. It compared the safe config against ~/.gitconfig only, so an
// edit to an included file - or, as here, any newer safe file - froze the
// output indefinitely.
func TestGit_SetupUserGitconfig_RegeneratesWhenSafeIsNewer(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")

	for _, d := range []string{homeDir, sandboxHome} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	isolateGitEnv(t, homeDir)
	writeFile(t, filepath.Join(homeDir, ".gitconfig"),
		"[user]\n\tname = Current User\n\temail = current@example.com\n")

	safeConfig := filepath.Join(sandboxHome, ".gitconfig.safe")
	writeFile(t, safeConfig, "[user]\n\tname = Stale User\n")

	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(safeConfig, future, future); err != nil {
		t.Fatal(err)
	}

	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "readonly"})

	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	data, err := os.ReadFile(safeConfig)
	if err != nil {
		t.Fatalf("read safe gitconfig: %v", err)
	}
	got := string(data)

	if strings.Contains(got, "Stale User") {
		t.Errorf("safe gitconfig was not regenerated, got:\n%s", got)
	}
	if !strings.Contains(got, `name = "Current User"`) {
		t.Errorf("safe gitconfig missing the current identity, got:\n%s", got)
	}
}

// TestGit_Setup_ReadOnlyMode_IdentityFromInclude is the end-to-end proof of the
// reported bug: an identity that only an [include] supplies used to reach the
// sandbox as nothing at all, because both sources the generator had - `git
// config --global <key>` and parseGitconfig - are blind to includes. The
// fixture is deliberately one the fallback path cannot satisfy, so this fails
// if Setup stops consulting the resolver.
func TestGit_Setup_ReadOnlyMode_IdentityFromInclude(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")

	for _, d := range []string{homeDir, sandboxHome} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	isolateGitEnv(t, homeDir)

	identity := filepath.Join(homeDir, ".gitconfig-identity")
	writeFile(t, identity, "[user]\n\tname = Included User\n\temail = included@example.com\n[credential]\n\thelper = store\n")
	writeFile(t, filepath.Join(homeDir, ".gitconfig"), "[include]\n\tpath = "+identity+"\n")

	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "readonly"})

	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(sandboxHome, ".gitconfig.safe"))
	if err != nil {
		t.Fatalf("safe gitconfig not generated: %v", err)
	}
	got := string(data)

	if !strings.Contains(got, `name = "Included User"`) || !strings.Contains(got, `email = "included@example.com"`) {
		t.Errorf("include-supplied identity missing from the safe gitconfig, got:\n%s", got)
	}
	// The include's other keys, and the include directive itself, are still
	// dropped - the allowlist did not widen.
	if strings.Contains(got, "helper") || strings.Contains(got, "path =") {
		t.Errorf("safe gitconfig should carry only the allowlisted keys, got:\n%s", got)
	}
}

// captureNotices routes notice output into a buffer for the duration of a test.
//
// The capture deliberately reproduces the conditions tool setup actually runs
// under: PhaseRunning, and not verbose. writeMessagePhase reaches stderr when
// `always || verbose || PhaseStartup || PhaseTeardown`, so capturing with
// verbose set - or leaving the phase at its PhaseStartup zero value - lets a
// plain notice.Warn through as readily as a notice.Alert, and every assertion
// over this buffer would keep passing after a downgrade that in production
// diverts the message to the log file and skips the warning-confirmation gate.
// Only notice.Alert survives here, which is what a ToolWithSetup must use.
func captureNotices(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	if err := notice.Setup("", false, &buf); err != nil {
		t.Fatalf("notice.Setup: %v", err)
	}
	notice.SetRunning()
	t.Cleanup(func() {
		notice.SetStartup()
		_ = notice.Setup("", false, nil)
	})
	return &buf
}

// TestCaptureNotices_OnlyAlertSurvives guards the helper itself: every
// "must be silent" assertion in this file is vacuous if a Warn also lands in
// the buffer.
func TestCaptureNotices_OnlyAlertSurvives(t *testing.T) {
	stderr := captureNotices(t)

	notice.Warn("warn-level message")
	if stderr.Len() != 0 {
		t.Fatalf("notice.Warn must not reach stderr in PhaseRunning, got: %s", stderr)
	}

	notice.Alert("alert-level message")
	if !strings.Contains(stderr.String(), "alert-level message") {
		t.Errorf("notice.Alert must reach stderr in PhaseRunning, got: %s", stderr)
	}
}

func TestExpandGitPath(t *testing.T) {
	const homeDir = "/home/u"

	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "tilde slash expands to the home dir", value: "~/.config/git/ignore", want: "/home/u/.config/git/ignore"},
		{name: "tilde slash with a bare name", value: "~/ignore", want: "/home/u/ignore"},
		{name: "absolute path passes through", value: "/etc/gitignore", want: "/etc/gitignore"},
		{name: "absolute path is cleaned", value: "/etc/../etc/gitignore", want: "/etc/gitignore"},
		{name: "tilde user form is rejected", value: "~other/ignore", wantErr: true},
		{name: "bare tilde is rejected", value: "~", wantErr: true},
		{name: "relative path is rejected", value: "ignore", wantErr: true},
		{name: "dot relative path is rejected", value: "./ignore", wantErr: true},
		{name: "empty value is rejected", value: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandGitPath(tt.value, homeDir)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expandGitPath(%q) = %q, want an error", tt.value, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("expandGitPath(%q): %v", tt.value, err)
			}
			if got != tt.want {
				t.Errorf("expandGitPath(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestPathDenied(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		roots []string
		want  bool
	}{
		{name: "directly inside", path: "/proj/sub/ignore", roots: []string{"/proj"}, want: true},
		{name: "equal to a root", path: "/proj", roots: []string{"/proj"}, want: true},
		{name: "outside every root", path: "/home/u/.gitignore", roots: []string{"/proj"}, want: false},
		{name: "sibling sharing a name prefix", path: "/proj-other/ignore", roots: []string{"/proj"}, want: false},
		{name: "no roots matches nothing", path: "/proj/ignore", roots: nil, want: false},
		{name: "empty path matches nothing", path: "", roots: []string{"/proj"}, want: false},
		{name: "parent of a root", path: "/", roots: []string{"/proj"}, want: false},
		{name: "second root matches", path: "/tmp/shared/x", roots: []string{"/proj", "/tmp/shared"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pathDenied(tt.path, tt.roots); got != tt.want {
				t.Errorf("pathDenied(%q, %v) = %v, want %v", tt.path, tt.roots, got, tt.want)
			}
		})
	}
}

// TestPathDenied_SymlinkSpelling covers the reason the check walks both
// spellings: projectDir comes from os.Getwd(), which returns $PWD verbatim, so
// a shell that cd'd through a symlink leaves the deny root spelled as the link
// while a config value may name the target - or the other way round.
func TestPathDenied_SymlinkSpelling(t *testing.T) {
	tmpDir := t.TempDir()
	real := filepath.Join(tmpDir, "real-project")
	link := filepath.Join(tmpDir, "project-link")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	viaLink := filepath.Join(link, "ignore")
	viaReal := filepath.Join(real, "ignore")

	// cmdpattern.ResolveRoots supplies both spellings of the root; pathDenied
	// supplies both spellings of the path. Either half alone leaves a gap.
	for _, root := range []string{real, link} {
		roots := cmdpattern.ResolveRoots([]string{root})
		for _, path := range []string{viaLink, viaReal} {
			if !pathDenied(path, roots) {
				t.Errorf("pathDenied(%q, root %q) = false, want true", path, root)
			}
		}
	}
}

func TestOriginTrusted(t *testing.T) {
	roots := []string{"/proj"}
	tests := []struct {
		name   string
		origin string
		want   bool
	}{
		{name: "host config file", origin: "file:/home/u/.gitconfig", want: true},
		{name: "include target inside the project tree", origin: "file:/proj/.gitconfig-shared", want: false},
		{name: "include target equal to the project dir", origin: "file:/proj", want: false},
		{name: "empty origin is refused", origin: "", want: false},
		{name: "file prefix with no path is refused", origin: "file:", want: false},
		{name: "command line origin is refused", origin: "command line:", want: false},
		{name: "blob origin is refused", origin: "blob:HEAD:.gitconfig", want: false},
		{name: "standard input origin is refused", origin: "standard input:", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := originTrusted(tt.origin, roots); got != tt.want {
				t.Errorf("originTrusted(%q) = %v, want %v", tt.origin, got, tt.want)
			}
		})
	}
}

// hostOrigins labels every key in values as read from the host's own
// ~/.gitconfig, which is what the resolver reports for a plain global setting.
// copyAuxFile refuses a key whose origin the sandbox could write, so a
// hand-built resolved map has to carry provenance alongside the values.
func hostOrigins(homeDir string, values map[string]string) map[string]string {
	origins := make(map[string]string, len(values))
	for k := range values {
		origins[k] = "file:" + filepath.Join(homeDir, ".gitconfig")
	}
	return origins
}

// TestGit_CopyAuxFiles covers the file-valued half of the allowlist directly,
// with a hand-built resolved map. Going through Setup would make the assertions
// depend on whether the host git supports --show-scope, because the fallback
// path never reports core.excludesFile at all.
func TestGit_CopyAuxFiles(t *testing.T) {
	type fixture struct {
		homeDir     string
		sandboxHome string
		projectDir  string
	}

	setup := func(t *testing.T) fixture {
		t.Helper()
		tmpDir := t.TempDir()
		f := fixture{
			homeDir:     filepath.Join(tmpDir, "home"),
			sandboxHome: filepath.Join(tmpDir, "sandbox"),
			projectDir:  filepath.Join(tmpDir, "project"),
		}
		for _, d := range []string{f.homeDir, f.sandboxHome, f.projectDir, filepath.Join(f.homeDir, ".config", "git")} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		isolateGitEnv(t, f.homeDir)
		return f
	}

	t.Run("configured excludesFile is copied and the value rewritten", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		src := filepath.Join(f.homeDir, "my-ignore")
		writeFile(t, src, "*.log\n")

		values := map[string]string{"core.excludesfile": src}
		g := &Git{mode: GitModeReadOnly, projectDir: f.projectDir}
		g.copyAuxFiles(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		want := "~/.gitignore.safe"
		if values["core.excludesfile"] != want {
			t.Errorf("core.excludesfile = %q, want %q", values["core.excludesfile"], want)
		}
		data, err := os.ReadFile(filepath.Join(f.sandboxHome, ".gitignore.safe"))
		if err != nil {
			t.Fatalf("copy not written: %v", err)
		}
		if string(data) != "*.log\n" {
			t.Errorf("copy content = %q, want %q", data, "*.log\n")
		}
		if stderr.Len() != 0 {
			t.Errorf("a usable configured value must be silent, got: %s", stderr)
		}
	})

	t.Run("configured attributesFile is copied and the value rewritten", func(t *testing.T) {
		f := setup(t)
		src := filepath.Join(f.homeDir, "my-attributes")
		writeFile(t, src, "*.bin binary\n")

		values := map[string]string{"core.attributesfile": src}
		g := &Git{mode: GitModeReadOnly, projectDir: f.projectDir}
		g.copyAuxFiles(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		want := "~/.gitattributes.safe"
		if values["core.attributesfile"] != want {
			t.Errorf("core.attributesfile = %q, want %q", values["core.attributesfile"], want)
		}
		data, err := os.ReadFile(filepath.Join(f.sandboxHome, ".gitattributes.safe"))
		if err != nil {
			t.Fatalf("copy not written: %v", err)
		}
		if string(data) != "*.bin binary\n" {
			t.Errorf("copy content = %q, want %q", data, "*.bin binary\n")
		}
	})

	t.Run("tilde value is expanded against the home dir", func(t *testing.T) {
		f := setup(t)
		writeFile(t, filepath.Join(f.homeDir, "tilde-ignore"), "build/\n")

		values := map[string]string{"core.excludesfile": "~/tilde-ignore"}
		g := &Git{mode: GitModeReadOnly, projectDir: f.projectDir}
		g.copyAuxFiles(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		if values["core.excludesfile"] != "~/.gitignore.safe" {
			t.Fatalf("tilde value not expanded, key = %q", values["core.excludesfile"])
		}
		data, err := os.ReadFile(filepath.Join(f.sandboxHome, ".gitignore.safe"))
		if err != nil || string(data) != "build/\n" {
			t.Errorf("copy = %q, err = %v", data, err)
		}
	})

	t.Run("unset key falls back to the host XDG default", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		writeFile(t, filepath.Join(f.homeDir, ".config", "git", "ignore"), "xdg-ignored\n")
		writeFile(t, filepath.Join(f.homeDir, ".config", "git", "attributes"), "*.md text\n")

		values := map[string]string{}
		g := &Git{mode: GitModeReadOnly, projectDir: f.projectDir}
		g.copyAuxFiles(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		if values["core.excludesfile"] != "~/.gitignore.safe" {
			t.Errorf("core.excludesfile = %q, want the XDG default to be carried", values["core.excludesfile"])
		}
		if values["core.attributesfile"] != "~/.gitattributes.safe" {
			t.Errorf("core.attributesfile = %q, want the XDG default to be carried", values["core.attributesfile"])
		}
		data, err := os.ReadFile(filepath.Join(f.sandboxHome, ".gitignore.safe"))
		if err != nil || string(data) != "xdg-ignored\n" {
			t.Errorf("copy = %q, err = %v", data, err)
		}
		if stderr.Len() != 0 {
			t.Errorf("carrying the XDG default must be silent, got: %s", stderr)
		}
	})

	t.Run("absent XDG default drops the key silently", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)

		values := map[string]string{}
		g := &Git{mode: GitModeReadOnly, projectDir: f.projectDir}
		g.copyAuxFiles(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		if _, ok := values["core.excludesfile"]; ok {
			t.Errorf("core.excludesfile = %q, want it dropped", values["core.excludesfile"])
		}
		if _, err := os.Stat(filepath.Join(f.sandboxHome, ".gitignore.safe")); !os.IsNotExist(err) {
			t.Errorf("no copy should be written, stat err = %v", err)
		}
		if stderr.Len() != 0 {
			t.Errorf("an unset key with no default is not a warning, got: %s", stderr)
		}
	})

	t.Run("configured source that does not exist alerts and drops the key", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		missing := filepath.Join(f.homeDir, "gone")

		values := map[string]string{"core.excludesfile": missing}
		g := &Git{mode: GitModeReadOnly, projectDir: f.projectDir}
		g.copyAuxFiles(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		if _, ok := values["core.excludesfile"]; ok {
			t.Error("a value that cannot be carried must be dropped, not emitted")
		}
		if !strings.Contains(stderr.String(), missing) {
			t.Errorf("alert must name the path %q, got: %s", missing, stderr)
		}
	})

	t.Run("configured source that is not a regular file alerts and drops the key", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		dir := filepath.Join(f.homeDir, "ignore-dir")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}

		values := map[string]string{"core.excludesfile": dir}
		g := &Git{mode: GitModeReadOnly, projectDir: f.projectDir}
		g.copyAuxFiles(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		if _, ok := values["core.excludesfile"]; ok {
			t.Error("a directory source must be dropped, not emitted")
		}
		if !strings.Contains(stderr.String(), dir) {
			t.Errorf("alert must name the path %q, got: %s", dir, stderr)
		}
	})

	t.Run("source inside the project dir is skipped", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		src := filepath.Join(f.projectDir, "shared-ignore")
		writeFile(t, src, "*.tmp\n")

		values := map[string]string{"core.excludesfile": src}
		g := &Git{mode: GitModeReadOnly, projectDir: f.projectDir}
		g.copyAuxFiles(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		if _, ok := values["core.excludesfile"]; ok {
			t.Error("a source inside the project dir must not be emitted")
		}
		if _, err := os.Stat(filepath.Join(f.sandboxHome, ".gitignore.safe")); !os.IsNotExist(err) {
			t.Error("the project dir is already mounted; copying would shadow the live file")
		}
		if !strings.Contains(stderr.String(), src) {
			t.Errorf("alert must name the path %q, got: %s", src, stderr)
		}
	})

	t.Run("unexpandable value alerts and drops the key", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)

		values := map[string]string{"core.excludesfile": "~someone/ignore"}
		g := &Git{mode: GitModeReadOnly, projectDir: f.projectDir}
		g.copyAuxFiles(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		if _, ok := values["core.excludesfile"]; ok {
			t.Error("an unexpandable value must be dropped, not emitted")
		}
		if !strings.Contains(stderr.String(), "~someone/ignore") {
			t.Errorf("alert must name the value, got: %s", stderr)
		}
	})

	t.Run("empty value is dropped silently and does not fall back", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		writeFile(t, filepath.Join(f.homeDir, ".config", "git", "ignore"), "xdg-ignored\n")

		values := map[string]string{"core.excludesfile": "   "}
		g := &Git{mode: GitModeReadOnly, projectDir: f.projectDir}
		g.copyAuxFiles(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		if _, ok := values["core.excludesfile"]; ok {
			t.Error("an empty value names no file, so nothing may be emitted for it")
		}
		if _, err := os.Stat(filepath.Join(f.sandboxHome, ".gitignore.safe")); !os.IsNotExist(err) {
			t.Error("an emptied key must not be overridden by the XDG default")
		}
		if stderr.Len() != 0 {
			t.Errorf("an emptied key is not a warning, got: %s", stderr)
		}
	})

	t.Run("a key set from a sandbox-writable include is refused", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		src := filepath.Join(f.homeDir, "id_ed25519")
		writeFile(t, src, "PRIVATE KEY\n")

		// Git labels a value pulled in through an [include] as global scope and
		// reports the included file as its origin. An include whose target sits
		// in the project tree is one the sandbox writes, so the value naming
		// this host file did not come from the host.
		sandboxWritable := filepath.Join(f.projectDir, ".gitconfig-shared")
		values := map[string]string{"core.excludesfile": src}
		origins := map[string]string{"core.excludesfile": "file:" + sandboxWritable}

		g := &Git{mode: GitModeReadOnly, projectDir: f.projectDir}
		g.copyAuxFiles(values, origins, f.homeDir, f.sandboxHome)

		if _, ok := values["core.excludesfile"]; ok {
			t.Error("a value the sandbox could have written must not be acted on")
		}
		if _, err := os.Stat(filepath.Join(f.sandboxHome, ".gitignore.safe")); !os.IsNotExist(err) {
			t.Error("no host file may be copied on the strength of a sandbox-supplied path")
		}
		if !strings.Contains(stderr.String(), sandboxWritable) {
			t.Errorf("alert must name the origin %q, got: %s", sandboxWritable, stderr)
		}
	})

	t.Run("a key set from an include inside the sandbox home is refused", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		src := filepath.Join(f.homeDir, "id_ed25519")
		writeFile(t, src, "PRIVATE KEY\n")

		// Every backend mounts the sandbox home read-write as the sandbox's
		// $HOME, so a config file there was written by a previous session of
		// the very sandbox being built.
		sandboxWritable := filepath.Join(f.sandboxHome, ".gitconfig-planted")
		values := map[string]string{"core.excludesfile": src}
		origins := map[string]string{"core.excludesfile": "file:" + sandboxWritable}

		g := &Git{mode: GitModeReadOnly, projectDir: f.projectDir}
		g.copyAuxFiles(values, origins, f.homeDir, f.sandboxHome)

		if _, ok := values["core.excludesfile"]; ok {
			t.Error("a value the sandbox could have written must not be acted on")
		}
		if _, err := os.Stat(filepath.Join(f.sandboxHome, ".gitignore.safe")); !os.IsNotExist(err) {
			t.Error("no host file may be copied on the strength of a sandbox-supplied path")
		}
		if !strings.Contains(stderr.String(), sandboxWritable) {
			t.Errorf("alert must name the origin %q, got: %s", sandboxWritable, stderr)
		}
	})

	t.Run("source inside the sandbox home is skipped", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		secret := filepath.Join(f.homeDir, "id_ed25519")
		writeFile(t, secret, "PRIVATE KEY\n")

		// os.Stat and os.ReadFile follow symlinks, so a link the sandbox
		// planted under its own $HOME would otherwise have this host file
		// copied to ~/.gitignore.safe, which the sandbox reads.
		src := filepath.Join(f.sandboxHome, "planted-ignore")
		if err := os.Symlink(secret, src); err != nil {
			t.Fatal(err)
		}

		values := map[string]string{"core.excludesfile": src}
		g := &Git{mode: GitModeReadOnly, projectDir: f.projectDir}
		g.copyAuxFiles(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		if _, ok := values["core.excludesfile"]; ok {
			t.Error("a source inside the sandbox home must not be emitted")
		}
		if data, err := os.ReadFile(filepath.Join(f.sandboxHome, ".gitignore.safe")); err == nil {
			t.Errorf("no copy may be written from the sandbox home, got %q", data)
		}
		if !strings.Contains(stderr.String(), src) {
			t.Errorf("alert must name the path %q, got: %s", src, stderr)
		}
	})

	t.Run("an origin git did not report as a file is refused", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		src := filepath.Join(f.homeDir, "my-ignore")
		writeFile(t, src, "*.log\n")

		values := map[string]string{"core.excludesfile": src}
		g := &Git{mode: GitModeReadOnly, projectDir: f.projectDir}
		g.copyAuxFiles(values, map[string]string{"core.excludesfile": ""}, f.homeDir, f.sandboxHome)

		if _, ok := values["core.excludesfile"]; ok {
			t.Error("an unresolvable origin must deny, not default to trusted")
		}
	})

	t.Run("a failed write alerts and drops the key", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		src := filepath.Join(f.homeDir, "my-ignore")
		writeFile(t, src, "*.log\n")

		// devsandbox's own failure, not a mistake in the user's config, so it
		// is reported whether the key was configured or defaulted.
		missingSandboxHome := filepath.Join(f.sandboxHome, "does", "not", "exist")
		values := map[string]string{"core.excludesfile": src}
		g := &Git{mode: GitModeReadOnly, projectDir: f.projectDir}
		g.copyAuxFiles(values, hostOrigins(f.homeDir, values), f.homeDir, missingSandboxHome)

		if _, ok := values["core.excludesfile"]; ok {
			t.Error("a key whose copy failed must not be emitted")
		}
		if !strings.Contains(stderr.String(), "could not copy") {
			t.Errorf("a failed copy must alert, got: %s", stderr)
		}
	})

	t.Run("a copy from a previous launch is removed when the key goes away", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		stale := filepath.Join(f.sandboxHome, ".gitignore.safe")
		writeFile(t, stale, "rules from an earlier launch\n")

		// Nothing configured and no XDG default present: this launch carries
		// nothing, so the earlier copy must not stay mounted at ~/.gitignore.safe.
		values := map[string]string{}
		g := &Git{mode: GitModeReadOnly, projectDir: f.projectDir}
		g.copyAuxFiles(values, nil, f.homeDir, f.sandboxHome)

		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Errorf("stale copy still present, stat err = %v", err)
		}
	})

	t.Run("empty project dir does not skip a home-dir source", func(t *testing.T) {
		f := setup(t)
		src := filepath.Join(f.homeDir, "my-ignore")
		writeFile(t, src, "*.log\n")

		values := map[string]string{"core.excludesfile": src}
		g := &Git{mode: GitModeReadOnly}
		g.copyAuxFiles(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		if values["core.excludesfile"] != "~/.gitignore.safe" {
			t.Errorf("core.excludesfile = %q, want the copy to be carried", values["core.excludesfile"])
		}
	})
}

// TestGit_Setup_EmptyPathsWriteNothing pins the guard on Setup's inputs. Every
// destination is built with filepath.Join(sandboxHome, ...), which yields a
// *relative* path for an empty sandboxHome - so without the guard the copies
// land in the process's working directory, which for `go test` is the package
// source directory. The homeDir half is worse than untidy: HOME is overridden
// for the resolver but $XDG_CONFIG_HOME is not, so git's default ignore path
// resolves to the invoking user's real one and its contents are what get
// copied out.
func TestGit_Setup_EmptyPathsWriteNothing(t *testing.T) {
	// Run from a scratch directory so a regression is caught here rather than
	// by someone noticing stray files in the repository later.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	if err := os.Chdir(scratch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	captureNotices(t)

	for _, tc := range []struct{ name, homeDir, sandboxHome string }{
		{name: "both empty"},
		{name: "empty sandbox home", homeDir: t.TempDir()},
		{name: "empty home dir", sandboxHome: t.TempDir()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &Git{}
			g.Configure(GlobalConfig{}, map[string]any{"mode": "readonly"})
			if err := g.Setup(tc.homeDir, tc.sandboxHome); err != nil {
				t.Fatalf("Setup: %v", err)
			}
		})
	}

	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("Setup wrote %q into the working directory", e.Name())
	}
}

// TestGit_Setup_ReadOnlyMode_CarriesIgnoreWithoutConfigFile covers a host that
// has global ignore rules and no global config file at all. Git honors
// ~/.config/git/ignore regardless of whether any config file exists, so gating
// the carry on a config file being present drops those rules silently - git
// ignores a missing excludesFile with exit 0 and no warning.
func TestGit_Setup_ReadOnlyMode_CarriesIgnoreWithoutConfigFile(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")
	xdgGit := filepath.Join(homeDir, ".config", "git")

	for _, d := range []string{sandboxHome, xdgGit} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	isolateGitEnv(t, homeDir)
	captureNotices(t)
	writeFile(t, filepath.Join(xdgGit, "ignore"), "*.log\n")
	// Deliberately no ~/.gitconfig and no ~/.config/git/config.

	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "readonly"})
	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(sandboxHome, ".gitignore.safe"))
	if err != nil {
		t.Fatalf("global ignore not carried in: %v", err)
	}
	if string(data) != "*.log\n" {
		t.Errorf("copy content = %q, want %q", data, "*.log\n")
	}

	// The safe config has to exist too, or the copy is mounted with nothing
	// pointing at it - git's own default location resolves inside the sandbox.
	safe, err := os.ReadFile(filepath.Join(sandboxHome, ".gitconfig.safe"))
	if err != nil {
		t.Fatalf("safe gitconfig not generated: %v", err)
	}
	want := "excludesFile = \"~/.gitignore.safe\"\n"
	if !strings.Contains(string(safe), want) {
		t.Errorf("safe gitconfig missing %q, got:\n%s", want, safe)
	}
}

// TestGit_Setup_ReadOnlyMode_RemovesStaleSafeConfig covers the other direction:
// the host had an identity, then stopped. The binding is emitted
// unconditionally and only tests for existence, so a copy left behind would go
// on being mounted as the sandbox's ~/.gitconfig indefinitely.
func TestGit_Setup_ReadOnlyMode_RemovesStaleSafeConfig(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")
	for _, d := range []string{homeDir, sandboxHome} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	isolateGitEnv(t, homeDir)
	captureNotices(t)
	safeConfig := filepath.Join(sandboxHome, ".gitconfig.safe")
	writeFile(t, safeConfig, "[user]\n\tname = \"Removed User\"\n")

	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "readonly"})
	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	if _, err := os.Stat(safeConfig); !os.IsNotExist(err) {
		t.Errorf("stale safe gitconfig still present, stat err = %v", err)
	}
}

// TestGit_Setup_ReadOnlyMode_CarriesGlobalIgnore is the end-to-end half: the
// XDG default reaches .gitignore.safe and the emitted config value points at
// the path the binding mounts it on. The key is unset in the fixture, so this
// holds whether the identity came from the resolver or the fallback.
func TestGit_Setup_ReadOnlyMode_CarriesGlobalIgnore(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")
	xdgGit := filepath.Join(homeDir, ".config", "git")

	for _, d := range []string{sandboxHome, xdgGit} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	isolateGitEnv(t, homeDir)
	writeFile(t, filepath.Join(homeDir, ".gitconfig"), "[user]\n\tname = Ada\n\temail = ada@corp\n")
	writeFile(t, filepath.Join(xdgGit, "ignore"), "*.swp\n")
	writeFile(t, filepath.Join(xdgGit, "attributes"), "*.bin binary\n")

	g := &Git{}
	g.Configure(GlobalConfig{}, map[string]any{"mode": "readonly"})

	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	for name, want := range map[string]string{
		".gitignore.safe":     "*.swp\n",
		".gitattributes.safe": "*.bin binary\n",
	} {
		data, err := os.ReadFile(filepath.Join(sandboxHome, name))
		if err != nil {
			t.Fatalf("%s not written: %v", name, err)
		}
		if string(data) != want {
			t.Errorf("%s = %q, want %q", name, data, want)
		}
	}

	data, err := os.ReadFile(filepath.Join(sandboxHome, ".gitconfig.safe"))
	if err != nil {
		t.Fatalf("safe gitconfig not generated: %v", err)
	}
	got := string(data)

	for _, want := range []string{
		"[core]",
		"excludesFile = \"~/.gitignore.safe\"\n",
		"attributesFile = \"~/.gitattributes.safe\"\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("safe gitconfig missing %q, got:\n%s", want, got)
		}
	}

	// Every emitted path must be one the bindings actually mount, or git
	// ignores it in silence. The emitted value is ~/-relative so it resolves on
	// every backend; git expands it against $HOME, which under bwrap is homeDir
	// and is where the binding puts the copy.
	dests := make(map[string]bool)
	for _, b := range g.Bindings(homeDir, sandboxHome) {
		dests[b.Dest] = true
	}
	for _, name := range []string{".gitignore.safe", ".gitattributes.safe"} {
		if !dests[filepath.Join(homeDir, name)] {
			t.Errorf("no binding mounts %s, so the emitted config value would not resolve", name)
		}
	}
}

func TestGit_Bindings_AuxFiles(t *testing.T) {
	homeDir := "/home/u"
	sandboxHome := "/sandbox/home"

	find := func(bindings []Binding, dest string) (Binding, bool) {
		for _, b := range bindings {
			if b.Dest == dest {
				return b, true
			}
		}
		return Binding{}, false
	}

	t.Run("readonly emits both without Setup having run", func(t *testing.T) {
		g := &Git{mode: GitModeReadOnly}
		bindings := g.Bindings(homeDir, sandboxHome)

		for _, name := range []string{".gitignore.safe", ".gitattributes.safe"} {
			b, ok := find(bindings, filepath.Join(homeDir, name))
			if !ok {
				t.Fatalf("no binding for %s", name)
			}
			if b.Source != filepath.Join(sandboxHome, name) {
				t.Errorf("%s: Source = %q, want %q", name, b.Source, filepath.Join(sandboxHome, name))
			}
			if b.Type != MountBind {
				t.Errorf("%s: Type = %q, want %q - an unset type resolves to an overlay", name, b.Type, MountBind)
			}
			if !b.ReadOnly {
				t.Errorf("%s: must be read-only", name)
			}
			if !b.Optional {
				t.Errorf("%s: must be optional - only Setup knows whether the copy happened", name)
			}
			if b.Category != CategoryConfig {
				t.Errorf("%s: Category = %q, want %q", name, b.Category, CategoryConfig)
			}
		}
	})

	t.Run("readwrite emits neither", func(t *testing.T) {
		g := &Git{mode: GitModeReadWrite}
		for _, name := range []string{".gitignore.safe", ".gitattributes.safe"} {
			if _, ok := find(g.Bindings(homeDir, sandboxHome), filepath.Join(homeDir, name)); ok {
				t.Errorf("readwrite mode must not emit %s", name)
			}
		}
	})

	t.Run("disabled emits nothing at all", func(t *testing.T) {
		g := &Git{mode: GitModeDisabled}
		if bindings := g.Bindings(homeDir, sandboxHome); bindings != nil {
			t.Errorf("disabled mode returned %d bindings", len(bindings))
		}
	})

	t.Run("empty sandboxHome does not panic", func(t *testing.T) {
		g := &Git{mode: GitModeReadOnly}
		if len(g.Bindings(homeDir, "")) == 0 {
			t.Error("bindings are emitted unconditionally, even without a sandbox home")
		}
	})
}

func TestAuxSource(t *testing.T) {
	const homeDir = "/home/u"
	f := gitAuxFile{key: "core.excludesfile", emit: "excludesFile", xdgName: "ignore", safeName: ".gitignore.safe"}

	tests := []struct {
		name           string
		values         map[string]string
		wantSrc        string
		wantConfigured bool
		wantErr        bool
	}{
		{
			name:    "unset key falls back to the host XDG default",
			values:  map[string]string{},
			wantSrc: "/home/u/.config/git/ignore",
		},
		{
			name:           "absolute value passes through",
			values:         map[string]string{"core.excludesfile": "/etc/gitignore"},
			wantSrc:        "/etc/gitignore",
			wantConfigured: true,
		},
		{
			name:           "tilde value expands against the home dir",
			values:         map[string]string{"core.excludesfile": "~/my-ignore"},
			wantSrc:        "/home/u/my-ignore",
			wantConfigured: true,
		},
		{
			name:           "whitespace is trimmed before expansion",
			values:         map[string]string{"core.excludesfile": "  ~/my-ignore  "},
			wantSrc:        "/home/u/my-ignore",
			wantConfigured: true,
		},
		{
			name:           "empty value names no file and does not fall back",
			values:         map[string]string{"core.excludesfile": "   "},
			wantSrc:        "",
			wantConfigured: true,
		},
		{
			name:           "unexpandable value is an error",
			values:         map[string]string{"core.excludesfile": "~someone/ignore"},
			wantConfigured: true,
			wantErr:        true,
		},
		{
			name:    "nil map behaves like an unset key",
			values:  nil,
			wantSrc: "/home/u/.config/git/ignore",
		},
	}

	// hostGitXDGDir reads XDG_CONFIG_HOME, so pin it away from the developer's own.
	t.Setenv("XDG_CONFIG_HOME", "")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, configured, err := auxSource(f, tt.values, homeDir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if configured != tt.wantConfigured {
				t.Errorf("configured = %v, want %v", configured, tt.wantConfigured)
			}
			if err == nil && src != tt.wantSrc {
				t.Errorf("src = %q, want %q", src, tt.wantSrc)
			}
		})
	}
}

// TestGit_Check_ConfigPaths covers the config sources Check reports. It needs a
// real git: CheckBinary gates everything else on the binary being present, and
// the resolved ignore/attributes paths come from a `git config` subprocess.
func TestGit_Check_ConfigPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	newHome := func(t *testing.T) string {
		t.Helper()
		homeDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(homeDir, ".config", "git"), 0o755); err != nil {
			t.Fatal(err)
		}
		isolateGitEnv(t, homeDir)
		return homeDir
	}

	hasPath := func(result CheckResult, path string) bool {
		for _, p := range result.ConfigPaths {
			if p == path {
				return true
			}
		}
		return false
	}

	hasIssue := func(result CheckResult, substr string) bool {
		for _, issue := range result.Issues {
			if strings.Contains(issue, substr) {
				return true
			}
		}
		return false
	}

	t.Run("XDG-only host is reported as configured", func(t *testing.T) {
		homeDir := newHome(t)
		xdgConfig := filepath.Join(homeDir, ".config", "git", "config")
		writeFile(t, xdgConfig, "[user]\n\tname = Ada\n\temail = ada@corp\n")

		result := (&Git{mode: GitModeReadOnly}).Check(homeDir)

		if !hasPath(result, xdgConfig) {
			t.Errorf("ConfigPaths = %v, want it to include %q", result.ConfigPaths, xdgConfig)
		}
		if hasIssue(result, "no global git config") {
			t.Errorf("a host configured only via XDG must not be reported as unconfigured: %v", result.Issues)
		}
	})

	t.Run("~/.gitconfig is still reported", func(t *testing.T) {
		homeDir := newHome(t)
		gitconfig := filepath.Join(homeDir, ".gitconfig")
		writeFile(t, gitconfig, "[user]\n\tname = Ada\n")

		result := (&Git{mode: GitModeReadOnly}).Check(homeDir)

		if !hasPath(result, gitconfig) {
			t.Errorf("ConfigPaths = %v, want it to include %q", result.ConfigPaths, gitconfig)
		}
		if hasIssue(result, "no global git config") {
			t.Errorf("unexpected issue: %v", result.Issues)
		}
	})

	t.Run("no global config at all raises the issue", func(t *testing.T) {
		homeDir := newHome(t)

		result := (&Git{mode: GitModeReadOnly}).Check(homeDir)

		if len(result.ConfigPaths) != 0 {
			t.Errorf("ConfigPaths = %v, want none", result.ConfigPaths)
		}
		if !hasIssue(result, "no global git config") {
			t.Errorf("Issues = %v, want the missing-config issue", result.Issues)
		}
	})

	t.Run("XDG ignore and attributes defaults are reported", func(t *testing.T) {
		homeDir := newHome(t)
		ignore := filepath.Join(homeDir, ".config", "git", "ignore")
		attributes := filepath.Join(homeDir, ".config", "git", "attributes")
		writeFile(t, ignore, "*.log\n")
		writeFile(t, attributes, "*.bin binary\n")

		result := (&Git{mode: GitModeReadOnly}).Check(homeDir)

		if !hasPath(result, ignore) {
			t.Errorf("ConfigPaths = %v, want it to include %q", result.ConfigPaths, ignore)
		}
		if !hasPath(result, attributes) {
			t.Errorf("ConfigPaths = %v, want it to include %q", result.ConfigPaths, attributes)
		}
	})

	t.Run("an absent ignore file is not reported", func(t *testing.T) {
		homeDir := newHome(t)

		result := (&Git{mode: GitModeReadOnly}).Check(homeDir)

		if hasPath(result, filepath.Join(homeDir, ".config", "git", "ignore")) {
			t.Errorf("ConfigPaths = %v, want no entry for a file that does not exist", result.ConfigPaths)
		}
	})

	// Only readonly mode carries the ignore and attributes files in, and
	// resolving where they live costs a git subprocess. Reporting them in the
	// other two modes would name files that are deliberately not carried.
	for _, mode := range []GitMode{GitModeReadWrite, GitModeDisabled} {
		t.Run("aux files are not reported in "+string(mode)+" mode", func(t *testing.T) {
			homeDir := newHome(t)
			ignore := filepath.Join(homeDir, ".config", "git", "ignore")
			writeFile(t, ignore, "*.log\n")

			result := (&Git{mode: mode}).Check(homeDir)

			if hasPath(result, ignore) {
				t.Errorf("%s mode reported %q, which it does not carry", mode, ignore)
			}
		})
	}

	// The shape the CLI actually reaches Check with. `tools check` and `tools
	// info` call Check on the registry singleton and never call Configure, so
	// the mode is still the zero value - testing only the explicitly-readonly
	// Git hid a branch that was dead in production.
	t.Run("an unconfigured Git reports the readonly aux files", func(t *testing.T) {
		homeDir := newHome(t)
		ignore := filepath.Join(homeDir, ".config", "git", "ignore")
		writeFile(t, ignore, "*.log\n")

		result := (&Git{}).Check(homeDir)

		if !hasIssue(result, "mode: readonly") {
			t.Fatalf("Issues = %v, want the readonly mode line", result.Issues)
		}
		if !hasPath(result, ignore) {
			t.Errorf("ConfigPaths = %v, want it to include %q for the default mode", result.ConfigPaths, ignore)
		}
	})

	t.Run("aux paths degrade to the XDG defaults when the resolver fails", func(t *testing.T) {
		homeDir := newHome(t)
		ignore := filepath.Join(homeDir, ".config", "git", "ignore")
		writeFile(t, ignore, "*.log\n")
		stubGit(t, "exit 128")

		result := (&Git{mode: GitModeReadOnly}).Check(homeDir)

		if !hasPath(result, ignore) {
			t.Errorf("ConfigPaths = %v, want the XDG default when the resolver cannot run", result.ConfigPaths)
		}
	})

	t.Run("a directory named by core.excludesFile is not reported", func(t *testing.T) {
		homeDir := newHome(t)
		dir := filepath.Join(homeDir, "ignore-dir")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(homeDir, ".gitconfig"), "[core]\n\texcludesFile = "+dir+"\n")

		result := (&Git{mode: GitModeReadOnly}).Check(homeDir)

		if hasPath(result, dir) {
			t.Errorf("ConfigPaths = %v, want no entry for a directory", result.ConfigPaths)
		}
	})

	// The sharp one: the value lives in an included file, which only the
	// resolver sees. A Check that read the top-level config would report the
	// XDG default instead.
	t.Run("core.excludesFile from an included file is reported", func(t *testing.T) {
		homeDir := newHome(t)
		custom := filepath.Join(homeDir, "work-ignore")
		writeFile(t, custom, "*.tmp\n")
		writeFile(t, filepath.Join(homeDir, ".config", "git", "ignore"), "*.log\n")

		included := filepath.Join(homeDir, ".gitconfig-work")
		writeFile(t, included, "[core]\n\texcludesFile = "+custom+"\n")
		writeFile(t, filepath.Join(homeDir, ".gitconfig"), "[include]\n\tpath = "+included+"\n")

		result := (&Git{mode: GitModeReadOnly}).Check(homeDir)

		if !hasPath(result, custom) {
			t.Errorf("ConfigPaths = %v, want the included value %q", result.ConfigPaths, custom)
		}
		if hasPath(result, filepath.Join(homeDir, ".config", "git", "ignore")) {
			t.Errorf("ConfigPaths = %v, want the configured file to replace the XDG default", result.ConfigPaths)
		}
	})

	// The CLI calls Check on the registry singleton, so projectDir is empty and
	// the deny list has to come from the working directory the resolver already
	// runs in - otherwise the check reports a file the launch refuses to carry.
	t.Run("a file inside the working directory is not reported", func(t *testing.T) {
		homeDir := newHome(t)
		workDir := t.TempDir()
		t.Chdir(workDir)

		projectIgnore := filepath.Join(workDir, "ignore")
		writeFile(t, projectIgnore, "*.tmp\n")
		writeFile(t, filepath.Join(homeDir, ".gitconfig"), "[core]\n\texcludesFile = "+projectIgnore+"\n")

		result := (&Git{}).Check(homeDir)

		if hasPath(result, projectIgnore) {
			t.Errorf("ConfigPaths = %v, want no entry for %q, which the launch refuses to copy",
				result.ConfigPaths, projectIgnore)
		}
	})

	t.Run("a value set from inside the working directory is not reported", func(t *testing.T) {
		homeDir := newHome(t)
		workDir := t.TempDir()
		t.Chdir(workDir)

		custom := filepath.Join(homeDir, "work-ignore")
		writeFile(t, custom, "*.tmp\n")
		included := filepath.Join(workDir, ".gitconfig-project")
		writeFile(t, included, "[core]\n\texcludesFile = "+custom+"\n")
		writeFile(t, filepath.Join(homeDir, ".gitconfig"), "[include]\n\tpath = "+included+"\n")

		result := (&Git{}).Check(homeDir)

		if hasPath(result, custom) {
			t.Errorf("ConfigPaths = %v, want no entry for a value set from %q, which the sandbox can write",
				result.ConfigPaths, included)
		}
	})

	t.Run("readwrite still reports ssh and gnupg", func(t *testing.T) {
		homeDir := newHome(t)
		for _, name := range []string{".ssh", ".gnupg"} {
			if err := os.MkdirAll(filepath.Join(homeDir, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}

		result := (&Git{mode: GitModeReadWrite}).Check(homeDir)

		for _, name := range []string{".ssh", ".gnupg"} {
			if !hasPath(result, filepath.Join(homeDir, name)) {
				t.Errorf("ConfigPaths = %v, want it to include %s", result.ConfigPaths, name)
			}
		}
	})
}
