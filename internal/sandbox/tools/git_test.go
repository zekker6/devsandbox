package tools

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
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

		// The builder resolves ReadOnly via mount mode for the credentials this
		// mode shares. ~/.gitconfig is the exception and must stay one: it is
		// the root of the config devsandbox parses to pick which host files to
		// mount, so a mount mode that made it writable would let the sandbox
		// rewrite it between launches and choose them.
		if b.Source == "/home/user/.gitconfig" {
			if b.Type != MountBind || !b.ReadOnly {
				t.Errorf("binding %s: Type = %q ReadOnly = %v, want a pinned read-only bind - "+
					"the resolved config decides what gets mounted", b.Source, b.Type, b.ReadOnly)
			}
			continue
		}
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

	// Setup in readwrite mode resolves the host config for real, so this needs
	// the same isolation every other readwrite test has: without it the
	// resolver reads the developer's own $XDG_CONFIG_HOME and runs git in the
	// package directory, and any notice it raises reaches the real stderr.
	isolateGitEnv(t, homeDir)
	captureNotices(t)

	g := &Git{}
	g.Configure(GlobalConfig{ProjectDir: filepath.Join(tmpDir, "project")}, map[string]any{"mode": "readwrite"})

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

// parsedValues collapses a parseGitconfig entry stream the way fallbackValues
// does, so a test that only cares about the winning value per key can say so.
func parsedValues(entries []gitConfigEntry) map[string]string {
	values, _ := fallbackValues(entries)
	return values
}

// parsedIncludes lists the unexpanded include spellings in the order the file
// declared them.
func parsedIncludes(entries []gitConfigEntry) []string {
	var out []string
	for _, e := range entries {
		if e.key == gitIncludeKey {
			out = append(out, e.value)
		}
	}
	return out
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

			entries, _, err := parseGitconfig(tmpFile)
			if err != nil {
				t.Fatalf("parseGitconfig: %v", err)
			}
			got := parsedValues(entries)

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
	got, _, err := parseGitconfig("/nonexistent/path/.gitconfig")
	if len(got) != 0 {
		t.Errorf("expected no keys for non-existent file, got %v", got)
	}
	// The error is what stops the degraded path from reading "could not open"
	// as "declares no includes" and going silent about the loss.
	if err == nil {
		t.Error("parseGitconfig() on a missing file must report the error")
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
			// Git takes a key written on its section header's own line.
			name:    "a key on the header line belongs to that section",
			content: "[user] name = Ada\n\temail = ada@corp\n",
			want:    map[string]string{"user.name": "Ada", "user.email": "ada@corp"},
		},
		{
			name:    "a key on a subsectioned header line is still not a top-level key",
			content: "[remote \"user\"] name = nope\n",
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

			entries, _, err := parseGitconfig(path)
			if err != nil {
				t.Fatalf("parseGitconfig: %v", err)
			}
			got := parsedValues(entries)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseGitconfig() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestParseGitconfig_Includes pins what the fallback path can see of a config's
// include directives. The spelling is returned unexpanded because the spelling
// is what decides where the target has to be mounted inside the sandbox.
func TestParseGitconfig_Includes(t *testing.T) {
	tests := []struct {
		name            string
		content         string
		wantIncludes    []string
		wantConditional bool
	}{
		{
			name:         "a top-level include is reported with its spelling intact",
			content:      "[include]\n\tpath = ~/inc.gitconfig\n",
			wantIncludes: []string{"~/inc.gitconfig"},
		},
		{
			name:         "every include survives, in file order",
			content:      "[include]\n\tpath = ~/one\n\tpath = /abs/two\n[include]\n\tpath = three\n",
			wantIncludes: []string{"~/one", "/abs/two", "three"},
		},
		{
			// The condition needs a repository to evaluate, which the fallback
			// path does not have. Reporting the path would invite carrying it.
			name:            "a conditional include is reported without its path",
			content:         "[includeIf \"gitdir:~/work/\"]\n\tpath = ~/work.gitconfig\n",
			wantConditional: true,
		},
		{
			name:    "an include with no path names no file",
			content: "[include]\n\tpath =\n",
		},
		{
			name:         "a quoted include is unquoted like any other value",
			content:      "[include]\n\tpath = \"~/my inc.gitconfig\" # work\n",
			wantIncludes: []string{"~/my inc.gitconfig"},
		},
		{
			name:    "a commented-out include is not one",
			content: "[include]\n\t# path = ~/inc.gitconfig\n",
		},
		{
			name:    "no include at all",
			content: "[user]\n\tname = Ada\n",
		},
		{
			// Git acts on a key written on the header's own line. Dropping the
			// remainder made this read as "declares no includes", which on the
			// degraded path loses the target and the report about it together.
			name:         "an include written on the header line is reported",
			content:      "[include] path = ~/inc.gitconfig\n",
			wantIncludes: []string{"~/inc.gitconfig"},
		},
		{
			name:            "a conditional include on the header line reports the condition only",
			content:         "[includeIf \"gitdir:~/work/\"] path = ~/work.gitconfig\n",
			wantConditional: true,
		},
		{
			name:    "a bare header followed by a comment is still a bare header",
			content: "[include] # nothing here\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".gitconfig")
			writeFile(t, path, tt.content)

			entries, conditional, err := parseGitconfig(path)
			if err != nil {
				t.Fatalf("parseGitconfig: %v", err)
			}
			values, includes := parsedValues(entries), parsedIncludes(entries)
			if !slices.Equal(includes, tt.wantIncludes) {
				t.Errorf("includes = %v, want %v", includes, tt.wantIncludes)
			}
			if conditional != tt.wantConditional {
				t.Errorf("conditional = %v, want %v", conditional, tt.wantConditional)
			}
			// The directive is not a value: generateSafeGitconfig emits an
			// allowlist, and an include path is not on it.
			if _, ok := values[gitIncludeKey]; ok {
				t.Errorf("values = %v, want no include entry", values)
			}
		})
	}
}

func TestIsIncludeIfHeader(t *testing.T) {
	tests := map[string]bool{
		`[includeIf "gitdir:~/work/"]`: true,
		`[includeif "gitdir:/w/"]`:     true,
		`[ includeIf "gitdir:/w/"]`:    true,
		`[include]`:                    false,
		`[remote "origin"]`:            false,
		`[user]`:                       false,
	}
	for line, want := range tests {
		if got := isIncludeIfHeader(line); got != want {
			t.Errorf("isIncludeIfHeader(%q) = %v, want %v", line, got, want)
		}
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
			// An unset Type resolves to MountTmpOverlay under the split
			// policy, which sends every commit to a discarded upper layer.
			if b.Type != MountBind {
				t.Errorf("readwrite worktree .git binding must pin Type=%q so commits land, got %q", MountBind, b.Type)
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

// TestGlobalConfigMap covers the collapse to a value map. Scope is not its
// concern - every caller is handed globalScopeEntries' output, and
// TestGlobalScopeEntries pins that filter - so these entries are already
// global, exactly as production's are.
func TestGlobalConfigMap(t *testing.T) {
	tests := []struct {
		name        string
		entries     []gitConfigEntry
		want        map[string]string
		wantOrigins map[string]string
	}{
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
			name: "each key keeps the origin of the value that won",
			entries: []gitConfigEntry{
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "user.name", value: "Ada"},
				{scope: "global", origin: "file:/home/u/inc.gitconfig", key: "user.email", value: "ada@corp"},
			},
			want: map[string]string{"user.name": "Ada", "user.email": "ada@corp"},
			wantOrigins: map[string]string{
				"user.name":  "file:/home/u/.gitconfig",
				"user.email": "file:/home/u/inc.gitconfig",
			},
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

func TestGlobalScopeEntries(t *testing.T) {
	tests := []struct {
		name    string
		entries []gitConfigEntry
		want    []gitConfigEntry
	}{
		{
			name: "drops every scope but global",
			entries: []gitConfigEntry{
				{scope: "system", origin: "file:/etc/gitconfig", key: "core.editor", value: "vi"},
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "user.name", value: "Ada"},
				{scope: "local", origin: "file:.git/config", key: "user.email", value: "ada@local"},
				{scope: "worktree", origin: "file:.git/config.worktree", key: "core.bare", value: ""},
				{scope: "command", origin: "command line:", key: "user.name", value: "Override"},
			},
			want: []gitConfigEntry{
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "user.name", value: "Ada"},
			},
		},
		{
			// The map form keys on include.path and keeps one of these; the
			// entry form is what carries both, which is the whole reason the
			// resolver returns entries.
			name: "keeps every include directive rather than collapsing them",
			entries: []gitConfigEntry{
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "include.path", value: "~/a.gitconfig"},
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "include.path", value: "~/b.gitconfig"},
			},
			want: []gitConfigEntry{
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "include.path", value: "~/a.gitconfig"},
				{scope: "global", origin: "file:/home/u/.gitconfig", key: "include.path", value: "~/b.gitconfig"},
			},
		},
		{name: "no entries", entries: nil, want: nil},
		{
			name: "no global entries",
			entries: []gitConfigEntry{
				{scope: "local", origin: "file:.git/config", key: "user.name", value: "Ada"},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := globalScopeEntries(tt.entries)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("globalScopeEntries() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestGit_ResolveGlobalConfig_KeepsEveryIncludeEntry drives the resolver over a
// stubbed --show-scope stream, because the property under test - that a nested
// include chain survives as distinct entries - is exactly what the map form
// destroys and so cannot be asserted through values/origins.
func TestGit_ResolveGlobalConfig_KeepsEveryIncludeEntry(t *testing.T) {
	homeDir := t.TempDir()
	isolateGitEnv(t, homeDir)

	rec := func(scope, origin, key, value string) string {
		return scope + "\x00" + origin + "\x00" + key + "\n" + value + "\x00"
	}
	stream := rec("global", "file:"+homeDir+"/.gitconfig", "include.path", "~/.gitconfig-work") +
		rec("global", "file:"+homeDir+"/.gitconfig-work", "include.path", "~/.gitconfig-deep") +
		rec("global", "file:"+homeDir+"/.gitconfig-deep", "user.email", "deep@corp") +
		rec("local", "file:.git/config", "user.email", "local@corp") +
		rec("system", "file:/etc/gitconfig", "core.editor", "vi")

	fixture := filepath.Join(t.TempDir(), "config-list")
	if err := os.WriteFile(fixture, []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	stubGit(t, "cat '"+fixture+"'")

	g := &Git{mode: GitModeReadWrite}
	entries, retried, err := g.resolveGlobalConfig(homeDir)
	if err != nil {
		t.Fatalf("resolveGlobalConfig: %v", err)
	}
	if retried != nil {
		t.Errorf("retriedOutsideRepo = %v, want nil", retried)
	}

	want := []gitConfigEntry{
		{scope: "global", origin: "file:" + homeDir + "/.gitconfig", key: "include.path", value: "~/.gitconfig-work"},
		{scope: "global", origin: "file:" + homeDir + "/.gitconfig-work", key: "include.path", value: "~/.gitconfig-deep"},
		{scope: "global", origin: "file:" + homeDir + "/.gitconfig-deep", key: "user.email", value: "deep@corp"},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("resolveGlobalConfig() = %+v, want %+v", entries, want)
	}

	// The collapse the entry form exists to avoid: one include.path key, so the
	// outer file's target is gone and the chain cannot be walked.
	values, _ := globalConfigMap(entries)
	if values["include.path"] != "~/.gitconfig-deep" {
		t.Errorf("globalConfigMap() include.path = %q, want the last-wins value", values["include.path"])
	}
	if values["user.email"] != "deep@corp" {
		t.Errorf("globalConfigMap() user.email = %q, want %q", values["user.email"], "deep@corp")
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

	// GIT_CONFIG_GLOBAL replaces the whole global scope, so a resolver that
	// inherits it reports a file the sandbox never reads - builder.go clears the
	// environment and does not re-add it, leaving in-sandbox git on the bound
	// ~/.gitconfig. The values this tool acts on have to come from the config
	// that will actually apply.
	got = gitCommandEnv([]string{"PATH=/bin", "GIT_CONFIG_GLOBAL=/opt/mycfg"}, "/new")
	want = []string{"PATH=/bin", "HOME=/new", "LC_ALL=C"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("gitCommandEnv() with GIT_CONFIG_GLOBAL = %v, want %v", got, want)
	}

	// The rest of git's config environment is reported outside the global scope
	// and dropped by globalScopeEntries, so it passes through untouched.
	// XDG_CONFIG_HOME is deliberately kept: hostGitXDGDir reads the same
	// variable, and the two must agree on where the XDG config lives.
	got = gitCommandEnv([]string{
		"GIT_CONFIG_SYSTEM=/opt/sys",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=1",
		"XDG_CONFIG_HOME=/opt/xdg",
	}, "/new")
	want = []string{
		"GIT_CONFIG_SYSTEM=/opt/sys",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=1",
		"XDG_CONFIG_HOME=/opt/xdg",
		"HOME=/new",
		"LC_ALL=C",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("gitCommandEnv() with the rest of git's config env = %v, want %v", got, want)
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
		entries, _, err := g.resolveGlobalConfig(homeDir)
		if err != nil {
			t.Fatalf("resolveGlobalConfig: %v", err)
		}
		values, origins := globalConfigMap(entries)
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
		entries, _, err := g.resolveGlobalConfig(homeDir)
		if err != nil {
			t.Fatalf("resolveGlobalConfig: %v", err)
		}
		values, _ := globalConfigMap(entries)
		if values["user.email"] != "ada@personal" {
			t.Errorf("user.email = %q, want %q", values["user.email"], "ada@personal")
		}
	})

	t.Run("empty project dir does not match the include", func(t *testing.T) {
		g := &Git{mode: GitModeReadOnly}
		entries, _, err := g.resolveGlobalConfig(homeDir)
		if err != nil {
			t.Fatalf("resolveGlobalConfig: %v", err)
		}
		values, _ := globalConfigMap(entries)
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
	entries, retried, err := g.resolveGlobalConfig(homeDir)
	if err != nil {
		t.Fatalf("resolveGlobalConfig: %v", err)
	}
	values, origins := globalConfigMap(entries)
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

		entries, retried, err := g.resolveGlobalConfig(homeDir)
		if err != nil {
			t.Fatalf("resolveGlobalConfig: %v", err)
		}
		values, _ := globalConfigMap(entries)
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

		if _, _, err := g.resolveGlobalConfig(homeDir); err == nil {
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
	for line := range strings.SplitSeq(strings.TrimRight(string(out), "\n"), "\n") {
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
	got, gotOrigins := fallbackValues(fallbackFileEntries([]string{xdgConfig, gitconfig}))
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
	if got, _ := fallbackValues(fallbackFileEntries([]string{includeOnly})); len(got) != 0 {
		t.Errorf("fallbackValues() = %v, want empty for an include-only config", got)
	}

	// The file-valued keys come back too. Without them copyAuxFiles reads a
	// configured core.excludesFile as unset and carries git's XDG default
	// instead - a file the host does not use, in place of the one it does.
	auxConfig := filepath.Join(dir, "aux-config")
	writeFile(t, auxConfig, "[core]\n\texcludesFile = ~/.gitignore_global\n")
	gotAux, gotAuxOrigins := fallbackValues(fallbackFileEntries([]string{auxConfig}))
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

func TestExpandGitPathRel(t *testing.T) {
	const homeDir = "/home/u"

	tests := []struct {
		name        string
		value       string
		want        string
		wantHomeRel bool
		wantErr     bool
	}{
		{name: "tilde slash is home relative", value: "~/.config/git/ignore", want: "/home/u/.config/git/ignore", wantHomeRel: true},
		{name: "tilde slash with a bare name", value: "~/ignore", want: "/home/u/ignore", wantHomeRel: true},
		{name: "tilde slash is cleaned", value: "~/sub/../sub/ignore", want: "/home/u/sub/ignore", wantHomeRel: true},
		{name: "absolute path is verbatim", value: "/etc/gitignore", want: "/etc/gitignore"},
		{name: "absolute path is cleaned", value: "/etc/../etc/gitignore", want: "/etc/gitignore"},
		// An absolute path that happens to sit under the host home is still
		// verbatim: the spelling decides, not the location. Marking it home
		// relative would rewrite it to /home/sandboxuser on Docker and krun,
		// where git was never told to look.
		{name: "absolute path under the home dir stays verbatim", value: "/home/u/ignore", want: "/home/u/ignore"},
		{name: "tilde user form is rejected", value: "~other/ignore", wantErr: true},
		{name: "bare tilde is rejected", value: "~", wantErr: true},
		{name: "relative path is rejected", value: "ignore", wantErr: true},
		{name: "empty value is rejected", value: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, homeRel, err := expandGitPathRel(tt.value, homeDir)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expandGitPathRel(%q) = %q, want an error", tt.value, got)
				}
				if homeRel {
					t.Errorf("expandGitPathRel(%q) homeRelative = true on an error", tt.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("expandGitPathRel(%q): %v", tt.value, err)
			}
			if got != tt.want {
				t.Errorf("expandGitPathRel(%q) = %q, want %q", tt.value, got, tt.want)
			}
			if homeRel != tt.wantHomeRel {
				t.Errorf("expandGitPathRel(%q) homeRelative = %v, want %v", tt.value, homeRel, tt.wantHomeRel)
			}

			// The wrapper must stay a pure projection, or the two callers
			// disagree about the same value.
			wrapped, wrappedErr := expandGitPath(tt.value, homeDir)
			if wrappedErr != nil || wrapped != got {
				t.Errorf("expandGitPath(%q) = (%q, %v), want (%q, nil)", tt.value, wrapped, wrappedErr, got)
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

// TestGit_AuxFileBindings covers the readwrite half of the file-valued keys.
// readwrite mounts the host ~/.gitconfig verbatim, so nothing rewrites these
// values and the binding has to land where the *spelling* resolves inside the
// sandbox.
//
// Every case asserts HomeRelativeDest as a struct field rather than a rendered
// path. bwrap binds the sandbox home at the host home path, so the
// home-relative and verbatim spellings of Dest collapse to the same string
// there and an assertion on the string alone passes whether or not the flag is
// right - with the bug then appearing only on Docker and krun.
func TestGit_AuxFileBindings(t *testing.T) {
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

	findBinding := func(t *testing.T, bindings []Binding, source string) Binding {
		t.Helper()
		for _, b := range bindings {
			if b.Source == source {
				return b
			}
		}
		t.Fatalf("no binding with Source %q, got %+v", source, bindings)
		return Binding{}
	}

	t.Run("tilde-spelled excludesFile binds home-relative", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		src := filepath.Join(f.homeDir, "tilde-ignore")
		writeFile(t, src, "build/\n")

		values := map[string]string{"core.excludesfile": "~/tilde-ignore"}
		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		bindings := g.auxFileBindings(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		b := findBinding(t, bindings, src)
		if b.Dest != src {
			t.Errorf("Dest = %q, want %q", b.Dest, src)
		}
		if !b.HomeRelativeDest {
			t.Error("a ~/-spelled value is re-expanded against the sandbox $HOME, so its Dest must be home-relative")
		}
		if b.Category != CategoryConfig {
			t.Errorf("Category = %q, want %q", b.Category, CategoryConfig)
		}
		if b.Type != "" || b.ReadOnly {
			t.Errorf("Type = %q, ReadOnly = %v; both must stay unset so ResolveBindingType applies the ~/.gitconfig policy",
				b.Type, b.ReadOnly)
		}
		if stderr.Len() != 0 {
			t.Errorf("a usable configured value must be silent, got: %s", stderr)
		}
	})

	t.Run("absolute-spelled excludesFile binds verbatim", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		// Outside the home dir, so the value cannot be read as ~/-relative by
		// accident.
		src := filepath.Join(t.TempDir(), "abs-ignore")
		writeFile(t, src, "*.log\n")

		values := map[string]string{"core.excludesfile": src}
		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		bindings := g.auxFileBindings(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		b := findBinding(t, bindings, src)
		if b.Dest != src {
			t.Errorf("Dest = %q, want %q", b.Dest, src)
		}
		if b.HomeRelativeDest {
			t.Error("an absolute value names the same path on every backend and must be bound verbatim")
		}
		if stderr.Len() != 0 {
			t.Errorf("a usable configured value must be silent, got: %s", stderr)
		}
	})

	t.Run("attributesFile is carried alongside excludesFile", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		ignore := filepath.Join(f.homeDir, "my-ignore")
		attributes := filepath.Join(f.homeDir, "my-attributes")
		writeFile(t, ignore, "*.log\n")
		writeFile(t, attributes, "*.bin binary\n")

		values := map[string]string{
			"core.excludesfile":   "~/my-ignore",
			"core.attributesfile": "~/my-attributes",
		}
		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		bindings := g.auxFileBindings(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		if len(bindings) != 2 {
			t.Fatalf("got %d bindings, want one per configured key: %+v", len(bindings), bindings)
		}
		for _, src := range []string{ignore, attributes} {
			b := findBinding(t, bindings, src)
			if b.Dest != src || !b.HomeRelativeDest {
				t.Errorf("binding for %q = {Dest: %q, HomeRelativeDest: %v}", src, b.Dest, b.HomeRelativeDest)
			}
		}
		if stderr.Len() != 0 {
			t.Errorf("two usable configured values must be silent, got: %s", stderr)
		}
	})

	t.Run("unset key falls back to the XDG default", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		src := filepath.Join(f.homeDir, ".config", "git", "ignore")
		writeFile(t, src, "xdg-ignored\n")

		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		bindings := g.auxFileBindings(map[string]string{}, nil, f.homeDir, f.sandboxHome)

		b := findBinding(t, bindings, src)
		want := filepath.Join(f.homeDir, ".config", "git", "ignore")
		if b.Dest != want {
			t.Errorf("Dest = %q, want %q", b.Dest, want)
		}
		if !b.HomeRelativeDest {
			t.Error("git reads the XDG default from the in-sandbox $HOME/.config, so the Dest must be home-relative")
		}
		if stderr.Len() != 0 {
			t.Errorf("carrying the XDG default must be silent, got: %s", stderr)
		}
	})

	t.Run("XDG_CONFIG_HOME outside the home dir keeps the Dest under the home dir", func(t *testing.T) {
		f := setup(t)
		// builder.go pins XDG_CONFIG_HOME to $HOME/.config inside the sandbox
		// unconditionally, so the host's own setting picks the Source and says
		// nothing about the Dest. Deriving the Dest from hostGitXDGDir would
		// mount the file where in-sandbox git never looks.
		xdgDir := filepath.Join(t.TempDir(), "xdg")
		if err := os.MkdirAll(filepath.Join(xdgDir, "git"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_CONFIG_HOME", xdgDir)
		src := filepath.Join(xdgDir, "git", "ignore")
		writeFile(t, src, "xdg-ignored\n")

		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		bindings := g.auxFileBindings(map[string]string{}, nil, f.homeDir, f.sandboxHome)

		b := findBinding(t, bindings, src)
		want := filepath.Join(f.homeDir, ".config", "git", "ignore")
		if b.Dest != want {
			t.Errorf("Dest = %q, want %q - the in-sandbox XDG dir, not the host's", b.Dest, want)
		}
		if !b.HomeRelativeDest {
			t.Error("the in-sandbox XDG dir is inside the sandbox home, so the Dest must be home-relative")
		}
	})

	t.Run("nonexistent target is skipped silently", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)

		// Git ignores a missing excludesFile on the host too, so nothing is
		// lost by not binding it and there is nothing to report.
		values := map[string]string{"core.excludesfile": "~/gone"}
		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		bindings := g.auxFileBindings(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		if len(bindings) != 0 {
			t.Errorf("got %+v, want no binding for a file the host does not have", bindings)
		}
		if stderr.Len() != 0 {
			t.Errorf("a file the host itself ignores is not a warning, got: %s", stderr)
		}
	})

	t.Run("a directory target is skipped", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		dir := filepath.Join(f.homeDir, "ignore-dir")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}

		values := map[string]string{"core.excludesfile": dir}
		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		bindings := g.auxFileBindings(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		if len(bindings) != 0 {
			t.Errorf("got %+v, want no binding for a non-regular target", bindings)
		}
	})

	t.Run("an empty value binds nothing and does not fall back", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		writeFile(t, filepath.Join(f.homeDir, ".config", "git", "ignore"), "xdg-ignored\n")

		values := map[string]string{"core.excludesfile": "   "}
		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		bindings := g.auxFileBindings(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		if len(bindings) != 0 {
			t.Errorf("got %+v, want the emptied key to bind nothing rather than the default it was written over", bindings)
		}
		if stderr.Len() != 0 {
			t.Errorf("an emptied key is not a warning, got: %s", stderr)
		}
	})

	t.Run("an unexpandable value alerts and binds nothing", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)

		values := map[string]string{"core.excludesfile": "~someone/ignore"}
		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		bindings := g.auxFileBindings(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		if len(bindings) != 0 {
			t.Errorf("got %+v, want nothing bound for a value devsandbox cannot resolve", bindings)
		}
		if !strings.Contains(stderr.String(), "~someone/ignore") {
			t.Errorf("alert must name the value, got: %s", stderr)
		}
		if !strings.Contains(stderr.String(), "excludesFile") {
			t.Errorf("alert must name the key, got: %s", stderr)
		}
	})

	t.Run("a key set from a file in the shared temp dir is refused", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		src := filepath.Join(f.homeDir, "id_ed25519")
		writeFile(t, src, "PRIVATE KEY\n")

		// The shared temp directory is bind-mounted read-write at an identical
		// path on host and sandbox, so a config file there is the sandbox's
		// word about which host file devsandbox should mount.
		sharedTmp := SharedTmpPath(f.homeDir, f.sandboxHome)
		if err := os.MkdirAll(sharedTmp, 0o755); err != nil {
			t.Fatal(err)
		}
		values := map[string]string{"core.excludesfile": src}
		origins := map[string]string{"core.excludesfile": "file:" + filepath.Join(sharedTmp, "planted.gitconfig")}

		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		bindings := g.auxFileBindings(values, origins, f.homeDir, f.sandboxHome)

		if len(bindings) != 0 {
			t.Errorf("got %+v, want no host file bound on the strength of a sandbox-supplied path", bindings)
		}
		if stderr.Len() != 0 {
			t.Errorf("a refused origin is a silent skip, got: %s", stderr)
		}
	})

	t.Run("a target named through a symlinked project dir is refused", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		// os.Getwd returns $PWD verbatim, so a shell that cd'd through a
		// symlink hands devsandbox the link's name while the bind mount uses
		// the target's inode. Either spelling reaches the same read-write tree.
		link := filepath.Join(filepath.Dir(f.projectDir), "project-link")
		if err := os.Symlink(f.projectDir, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		writeFile(t, filepath.Join(f.projectDir, "shared-ignore"), "*.tmp\n")

		viaLink := filepath.Join(link, "shared-ignore")
		values := map[string]string{"core.excludesfile": viaLink}
		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		bindings := g.auxFileBindings(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		if len(bindings) != 0 {
			t.Errorf("got %+v, want the project tree refused in both spellings", bindings)
		}
	})

	t.Run("a target inside the sandbox home is refused", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		secret := filepath.Join(f.homeDir, "id_ed25519")
		writeFile(t, secret, "PRIVATE KEY\n")

		// The sandbox home is mounted read-write as the sandbox's $HOME, so a
		// link planted there would otherwise have this host file bound where
		// the sandbox reads it.
		planted := filepath.Join(f.sandboxHome, "planted-ignore")
		if err := os.Symlink(secret, planted); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		values := map[string]string{"core.excludesfile": planted}
		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		bindings := g.auxFileBindings(values, hostOrigins(f.homeDir, values), f.homeDir, f.sandboxHome)

		if len(bindings) != 0 {
			t.Errorf("got %+v, want no binding for a path inside the sandbox home", bindings)
		}
	})

	t.Run("an origin git did not report as a file is refused", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		src := filepath.Join(f.homeDir, "my-ignore")
		writeFile(t, src, "*.log\n")

		values := map[string]string{"core.excludesfile": src}
		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		bindings := g.auxFileBindings(values, map[string]string{"core.excludesfile": ""}, f.homeDir, f.sandboxHome)

		if len(bindings) != 0 {
			t.Errorf("got %+v, want an unresolvable origin to deny rather than default to trusted", bindings)
		}
	})
}

// TestGit_IncludeOriginBindings covers the correlation walk: every config file
// the host's global scope is assembled from has to be bound where the
// *spelling* that named it resolves inside the sandbox.
//
// Entries are hand-built rather than shelled out to git, so the assertions do
// not depend on the host having any particular include chain. Every case
// asserts HomeRelativeDest as a struct field: bwrap binds the sandbox home at
// the host home path, so the home-relative and verbatim spellings of Dest
// collapse to the same string there and a string-only assertion passes whether
// or not the flag is right - with the bug then appearing only on Docker and
// krun.
func TestGit_IncludeOriginBindings(t *testing.T) {
	type fixture struct {
		tmpDir      string
		homeDir     string
		sandboxHome string
		projectDir  string
	}

	setup := func(t *testing.T) fixture {
		t.Helper()
		tmpDir := t.TempDir()
		f := fixture{
			tmpDir:      tmpDir,
			homeDir:     filepath.Join(tmpDir, "home"),
			sandboxHome: filepath.Join(tmpDir, "sandbox"),
			projectDir:  filepath.Join(tmpDir, "project"),
		}
		for _, d := range []string{f.homeDir, f.sandboxHome, f.projectDir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		isolateGitEnv(t, f.homeDir)
		return f
	}

	entry := func(origin, key, value string) gitConfigEntry {
		return gitConfigEntry{scope: "global", origin: gitOriginFilePrefix + origin, key: key, value: value}
	}

	type wantBinding struct {
		source       string
		dest         string
		homeRelative bool
	}

	check := func(t *testing.T, bindings []Binding, want []wantBinding) {
		t.Helper()
		if len(bindings) != len(want) {
			t.Fatalf("got %d bindings %+v, want %d", len(bindings), bindings, len(want))
		}
		for i, w := range want {
			b := bindings[i]
			if b.Source != w.source {
				t.Errorf("binding %d: Source = %q, want %q", i, b.Source, w.source)
			}
			if b.Dest != w.dest {
				t.Errorf("binding %d: Dest = %q, want %q", i, b.Dest, w.dest)
			}
			if b.HomeRelativeDest != w.homeRelative {
				t.Errorf("binding %d (%s): HomeRelativeDest = %v, want %v",
					i, b.Source, b.HomeRelativeDest, w.homeRelative)
			}
			if b.Category != CategoryConfig {
				t.Errorf("binding %d: Category = %q, want %q", i, b.Category, CategoryConfig)
			}
			// Pinned read-only, exactly like the ~/.gitconfig binding these
			// sit beside. Every file here is one devsandbox resolves keys
			// out of in order to decide what to mount, so leaving it to
			// ResolveBindingType let mount_mode = "readwrite" hand the
			// sandbox a writable host config it could name any host file
			// from on the next launch.
			if b.Type != MountBind || !b.ReadOnly {
				t.Errorf("binding %d: Type = %q ReadOnly = %v, want a pinned read-only bind", i, b.Type, b.ReadOnly)
			}
			if !b.Optional {
				t.Errorf("binding %d: Optional = false, want true", i)
			}
		}
	}

	spellings := []struct {
		name  string
		build func(f fixture) (entries []gitConfigEntry, want []wantBinding)
	}{
		{
			name: "a tilde-spelled include binds home-relative",
			build: func(f fixture) ([]gitConfigEntry, []wantBinding) {
				root := filepath.Join(f.homeDir, ".gitconfig")
				inc := filepath.Join(f.homeDir, "inc.gitconfig")
				return []gitConfigEntry{
					entry(root, "include.path", "~/inc.gitconfig"),
					entry(inc, "user.email", "ada@corp"),
				}, []wantBinding{
					{source: root, dest: root, homeRelative: true},
					{source: inc, dest: inc, homeRelative: true},
				}
			},
		},
		{
			name: "an absolute-spelled include binds verbatim",
			build: func(f fixture) ([]gitConfigEntry, []wantBinding) {
				root := filepath.Join(f.homeDir, ".gitconfig")
				inc := filepath.Join(f.tmpDir, "shared", "team.gitconfig")
				return []gitConfigEntry{
					entry(root, "include.path", inc),
					entry(inc, "user.email", "ada@corp"),
				}, []wantBinding{
					{source: root, dest: root, homeRelative: true},
					{source: inc, dest: inc, homeRelative: false},
				}
			},
		},
		{
			name: "a relative include inherits its parent's flag",
			build: func(f fixture) ([]gitConfigEntry, []wantBinding) {
				root := filepath.Join(f.homeDir, ".gitconfig")
				inc := filepath.Join(f.homeDir, "inc-rel.gitconfig")
				return []gitConfigEntry{
					entry(root, "include.path", "inc-rel.gitconfig"),
					entry(inc, "user.email", "ada@corp"),
				}, []wantBinding{
					{source: root, dest: root, homeRelative: true},
					{source: inc, dest: inc, homeRelative: true},
				}
			},
		},
		{
			name: "a two-level nested chain binds every file in it",
			build: func(f fixture) ([]gitConfigEntry, []wantBinding) {
				root := filepath.Join(f.homeDir, ".gitconfig")
				mid := filepath.Join(f.homeDir, "mid.gitconfig")
				leaf := filepath.Join(f.homeDir, "leaf.gitconfig")
				// The intermediate file appears as an origin in its own right,
				// because declaring include.path is a contributed key.
				return []gitConfigEntry{
					entry(root, "include.path", "~/mid.gitconfig"),
					entry(mid, "include.path", "leaf.gitconfig"),
					entry(leaf, "user.email", "ada@corp"),
				}, []wantBinding{
					{source: root, dest: root, homeRelative: true},
					{source: mid, dest: mid, homeRelative: true},
					{source: leaf, dest: leaf, homeRelative: true},
				}
			},
		},
		{
			name: "a spelling with a dot-dot segment matches its uncleaned origin",
			build: func(f fixture) ([]gitConfigEntry, []wantBinding) {
				root := filepath.Join(f.homeDir, ".gitconfig")
				// git reports the origin exactly as the spelling produced it,
				// without cleaning, while filepath.Join collapses the segment.
				uncleaned := f.homeDir + "/sub/../sub/inc.gitconfig"
				cleaned := filepath.Join(f.homeDir, "sub", "inc.gitconfig")
				return []gitConfigEntry{
					entry(root, "include.path", "~/sub/../sub/inc.gitconfig"),
					entry(uncleaned, "user.email", "ada@corp"),
				}, []wantBinding{
					{source: root, dest: root, homeRelative: true},
					{source: cleaned, dest: cleaned, homeRelative: true},
				}
			},
		},
	}

	for _, tc := range spellings {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			stderr := captureNotices(t)
			entries, want := tc.build(f)

			g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
			check(t, g.includeOriginBindings(entries, f.homeDir, f.sandboxHome), want)

			if stderr.Len() != 0 {
				t.Errorf("a resolvable include chain must be silent, got: %s", stderr)
			}
		})
	}

	t.Run("a relative include from an out-of-home XDG config keeps the home-relative destination", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		// hostGitXDGDir is the right Source and the wrong Dest: builder.go
		// pins XDG_CONFIG_HOME to $HOME/.config inside the sandbox whatever
		// the host's own setting is, so in-sandbox git reads the chain from
		// under $HOME regardless.
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.tmpDir, "xdg"))
		root := filepath.Join(f.tmpDir, "xdg", "git", "config")
		inc := filepath.Join(f.tmpDir, "xdg", "git", "inc.gitconfig")

		entries := []gitConfigEntry{
			entry(root, "include.path", "inc.gitconfig"),
			entry(inc, "user.email", "ada@corp"),
		}

		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		check(t, g.includeOriginBindings(entries, f.homeDir, f.sandboxHome), []wantBinding{
			{source: root, dest: filepath.Join(f.homeDir, ".config", "git", "config"), homeRelative: true},
			{source: inc, dest: filepath.Join(f.homeDir, ".config", "git", "inc.gitconfig"), homeRelative: true},
		})
	})

	t.Run("a matching includeIf target is bound", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		root := filepath.Join(f.homeDir, ".gitconfig")
		work := filepath.Join(f.homeDir, "work.gitconfig")

		// The condition is a subsection carrying dots and slashes of its own,
		// so the key is matched as a prefix plus a .path suffix.
		entries := []gitConfigEntry{
			entry(root, "includeif.gitdir:~/work/.path", "~/work.gitconfig"),
			entry(work, "user.email", "ada@work"),
		}

		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		check(t, g.includeOriginBindings(entries, f.homeDir, f.sandboxHome), []wantBinding{
			{source: root, dest: root, homeRelative: true},
			{source: work, dest: work, homeRelative: true},
		})
	})

	t.Run("a non-matching includeIf target is absent", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		root := filepath.Join(f.homeDir, ".gitconfig")

		// git reports the directive whether or not the condition matched, but
		// a target it did not read contributes no origin - which keeps a
		// work-identity file out of a personal project's sandbox for free.
		entries := []gitConfigEntry{
			entry(root, "includeif.gitdir:~/work/.path", "~/work.gitconfig"),
			entry(root, "user.email", "ada@home"),
		}

		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		check(t, g.includeOriginBindings(entries, f.homeDir, f.sandboxHome), []wantBinding{
			{source: root, dest: root, homeRelative: true},
		})
	})

	t.Run("an empty include target is absent", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		root := filepath.Join(f.homeDir, ".gitconfig")

		entries := []gitConfigEntry{
			entry(root, "include.path", ""),
			entry(root, "user.email", "ada@home"),
		}

		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		check(t, g.includeOriginBindings(entries, f.homeDir, f.sandboxHome), []wantBinding{
			{source: root, dest: root, homeRelative: true},
		})
	})

	t.Run("a bare includeif.path is not an include directive", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		root := filepath.Join(f.homeDir, ".gitconfig")
		inc := filepath.Join(f.homeDir, "inc.gitconfig")

		// No condition, so git never acts on it either.
		entries := []gitConfigEntry{
			entry(root, "includeif.path", "~/inc.gitconfig"),
			entry(inc, "user.email", "ada@corp"),
		}

		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		check(t, g.includeOriginBindings(entries, f.homeDir, f.sandboxHome), []wantBinding{
			{source: root, dest: root, homeRelative: true},
		})
	})

	t.Run("an origin git did not report as a file is skipped silently", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		root := filepath.Join(f.homeDir, ".gitconfig")

		entries := []gitConfigEntry{
			entry(root, "user.email", "ada@home"),
			{scope: "global", origin: "command line:", key: "user.name", value: "Ada"},
			{scope: "global", origin: "", key: "user.name", value: "Ada"},
		}

		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		check(t, g.includeOriginBindings(entries, f.homeDir, f.sandboxHome), []wantBinding{
			{source: root, dest: root, homeRelative: true},
		})
		if stderr.Len() != 0 {
			t.Errorf("a non-file origin cannot occur for the global scope; alerting would be noise, got: %s", stderr)
		}
	})

	t.Run("an origin under the project tree is refused, and so is what it declares", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		root := filepath.Join(f.homeDir, ".gitconfig")
		planted := filepath.Join(f.projectDir, "planted.gitconfig")

		// The project tree is bind-mounted read-write, so a config file there
		// is the sandbox's word about which host file devsandbox should mount
		// on the next launch - including the ones that file includes.
		entries := []gitConfigEntry{
			entry(root, "include.path", planted),
			entry(planted, "include.path", filepath.Join(f.homeDir, ".ssh", "id_ed25519")),
			entry(filepath.Join(f.homeDir, ".ssh", "id_ed25519"), "user.email", "ada@corp"),
		}

		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		check(t, g.includeOriginBindings(entries, f.homeDir, f.sandboxHome), []wantBinding{
			{source: root, dest: root, homeRelative: true},
		})
		if stderr.Len() != 0 {
			t.Errorf("a refused origin is a silent skip, got: %s", stderr)
		}
	})

	t.Run("an origin in the shared temp dir is refused", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		root := filepath.Join(f.homeDir, ".gitconfig")
		sharedTmp := SharedTmpPath(f.homeDir, f.sandboxHome)
		if err := os.MkdirAll(sharedTmp, 0o755); err != nil {
			t.Fatal(err)
		}
		planted := filepath.Join(sharedTmp, "planted.gitconfig")

		entries := []gitConfigEntry{
			entry(root, "include.path", planted),
			entry(planted, "user.email", "ada@corp"),
		}

		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		check(t, g.includeOriginBindings(entries, f.homeDir, f.sandboxHome), []wantBinding{
			{source: root, dest: root, homeRelative: true},
		})
	})

	t.Run("an origin named through a symlinked project dir is refused", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		link := filepath.Join(f.tmpDir, "project-link")
		if err := os.Symlink(f.projectDir, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		root := filepath.Join(f.homeDir, ".gitconfig")
		viaLink := filepath.Join(link, "planted.gitconfig")

		entries := []gitConfigEntry{
			entry(root, "include.path", viaLink),
			entry(viaLink, "user.email", "ada@corp"),
		}

		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		check(t, g.includeOriginBindings(entries, f.homeDir, f.sandboxHome), []wantBinding{
			{source: root, dest: root, homeRelative: true},
		})
	})

	t.Run("a tilde-user include alerts and binds nothing", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		root := filepath.Join(f.homeDir, ".gitconfig")

		entries := []gitConfigEntry{
			entry(root, "include.path", "~build/shared.gitconfig"),
			entry(root, "user.email", "ada@home"),
		}

		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		check(t, g.includeOriginBindings(entries, f.homeDir, f.sandboxHome), []wantBinding{
			{source: root, dest: root, homeRelative: true},
		})
		if !strings.Contains(stderr.String(), "include.path") {
			t.Errorf("the alert must name the key that will not apply, got: %s", stderr)
		}
	})

	t.Run("a file contributing more than one key is bound once", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		root := filepath.Join(f.homeDir, ".gitconfig")

		entries := []gitConfigEntry{
			entry(root, "user.name", "Ada"),
			entry(root, "user.email", "ada@home"),
			entry(root, "core.excludesfile", "~/.gitignore"),
		}

		g := &Git{mode: GitModeReadWrite, projectDir: f.projectDir}
		check(t, g.includeOriginBindings(entries, f.homeDir, f.sandboxHome), []wantBinding{
			{source: root, dest: root, homeRelative: true},
		})
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
		return slices.Contains(result.ConfigPaths, path)
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

	// disabled is the one mode that carries nothing, and resolving where the
	// aux files live costs a git subprocess. Reporting them there would name
	// files that are deliberately not carried.
	t.Run("aux files are not reported in disabled mode", func(t *testing.T) {
		homeDir := newHome(t)
		ignore := filepath.Join(homeDir, ".config", "git", "ignore")
		writeFile(t, ignore, "*.log\n")

		result := (&Git{mode: GitModeDisabled}).Check(homeDir)

		if hasPath(result, ignore) {
			t.Errorf("disabled mode reported %q, which it does not carry", ignore)
		}
	})

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

	// readwrite mounts the aux files at the paths the host config already
	// names, so the check has to report them there too - a mode that carries
	// them and says nothing is the gap this closes.
	t.Run("readwrite reports the aux files alongside ssh and gnupg", func(t *testing.T) {
		homeDir := newHome(t)
		for _, name := range []string{".ssh", ".gnupg"} {
			if err := os.MkdirAll(filepath.Join(homeDir, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		ignore := filepath.Join(homeDir, ".config", "git", "ignore")
		attributes := filepath.Join(homeDir, ".config", "git", "attributes")
		writeFile(t, ignore, "*.log\n")
		writeFile(t, attributes, "*.bin binary\n")

		result := (&Git{mode: GitModeReadWrite}).Check(homeDir)

		for _, want := range []string{
			ignore,
			attributes,
			filepath.Join(homeDir, ".ssh"),
			filepath.Join(homeDir, ".gnupg"),
		} {
			if !hasPath(result, want) {
				t.Errorf("ConfigPaths = %v, want it to include %q", result.ConfigPaths, want)
			}
		}
	})

	// Same sharpness as the readonly case above: the value only the resolver
	// sees is the one readwrite binds, so it is the one to report.
	t.Run("readwrite reports core.excludesFile from an included file", func(t *testing.T) {
		homeDir := newHome(t)
		custom := filepath.Join(homeDir, "work-ignore")
		writeFile(t, custom, "*.tmp\n")
		writeFile(t, filepath.Join(homeDir, ".config", "git", "ignore"), "*.log\n")

		included := filepath.Join(homeDir, ".gitconfig-work")
		writeFile(t, included, "[core]\n\texcludesFile = "+custom+"\n")
		writeFile(t, filepath.Join(homeDir, ".gitconfig"), "[include]\n\tpath = "+included+"\n")

		result := (&Git{mode: GitModeReadWrite}).Check(homeDir)

		if !hasPath(result, custom) {
			t.Errorf("ConfigPaths = %v, want the included value %q", result.ConfigPaths, custom)
		}
		if hasPath(result, filepath.Join(homeDir, ".config", "git", "ignore")) {
			t.Errorf("ConfigPaths = %v, want the configured file to replace the XDG default", result.ConfigPaths)
		}
	})

	// The refusals auxFileBindings applies have to hold here too, or readwrite
	// `tools check` names a file the launch declines to bind.
	t.Run("readwrite does not report a file inside the working directory", func(t *testing.T) {
		homeDir := newHome(t)
		workDir := t.TempDir()
		t.Chdir(workDir)

		projectIgnore := filepath.Join(workDir, "ignore")
		writeFile(t, projectIgnore, "*.tmp\n")
		writeFile(t, filepath.Join(homeDir, ".gitconfig"), "[core]\n\texcludesFile = "+projectIgnore+"\n")

		result := (&Git{mode: GitModeReadWrite}).Check(homeDir)

		if hasPath(result, projectIgnore) {
			t.Errorf("ConfigPaths = %v, want no entry for %q, which the launch refuses to bind",
				result.ConfigPaths, projectIgnore)
		}
	})

	t.Run("readwrite does not report a value set from inside the working directory", func(t *testing.T) {
		homeDir := newHome(t)
		workDir := t.TempDir()
		t.Chdir(workDir)

		custom := filepath.Join(homeDir, "work-ignore")
		writeFile(t, custom, "*.tmp\n")
		included := filepath.Join(workDir, ".gitconfig-project")
		writeFile(t, included, "[core]\n\texcludesFile = "+custom+"\n")
		writeFile(t, filepath.Join(homeDir, ".gitconfig"), "[include]\n\tpath = "+included+"\n")

		result := (&Git{mode: GitModeReadWrite}).Check(homeDir)

		if hasPath(result, custom) {
			t.Errorf("ConfigPaths = %v, want no entry for a value set from %q, which the sandbox can write",
				result.ConfigPaths, included)
		}
	})
}

// TestGit_Setup_ReadWriteRefBindings covers the wiring between Setup and
// Bindings in readwrite mode: Setup resolves the host config, and Bindings
// emits the fixed set plus whatever that config references.
//
// The whole point is that a value arrives inside the sandbox spelled exactly as
// the host wrote it, so the file it names has to be mounted where that spelling
// resolves. Every case asserts HomeRelativeDest as a struct field: bwrap binds
// the sandbox home at the host home path, so the two Dest spellings collapse to
// one string there and a string assertion passes either way.
func TestGit_Setup_ReadWriteRefBindings(t *testing.T) {
	// Every subtest here asserts the resolver path, several by requiring
	// silence. Without a git that has --show-scope, Setup takes the degraded
	// fallback instead and raises its own alert, so the failure would name the
	// wrong thing.
	requireResolvableGit(t)

	setup := newReadWriteRefFixture
	newGit := newReadWriteRefGit
	dests := bindingDests
	findBinding := findBindingBySource

	t.Run("a referenced excludesFile reaches Bindings", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		src := filepath.Join(f.homeDir, "my-ignore")
		writeFile(t, src, "build/\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"),
			"[core]\n\texcludesfile = ~/my-ignore\n")

		g := newGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}

		bindings := g.Bindings(f.homeDir, f.sandboxHome)
		b := findBinding(t, bindings, src)
		if b.Dest != src {
			t.Errorf("Dest = %q, want %q", b.Dest, src)
		}
		if !b.HomeRelativeDest {
			t.Error("a ~/-spelled value is re-expanded against the sandbox $HOME, so its Dest must be home-relative")
		}

		// The fixed set is an addition, never a replacement.
		for _, name := range []string{".gitconfig", ".git-credentials", ".ssh", ".gnupg"} {
			findBinding(t, bindings, filepath.Join(f.homeDir, name))
		}
		if stderr.Len() != 0 {
			t.Errorf("carrying a resolvable file must be silent, got: %s", stderr)
		}
	})

	// Overview loss 3: an identity-per-directory setup loses its identity in
	// the one mode where commits land, because the include target is a host
	// file nothing mounts and git ignores a missing one silently.
	t.Run("an included config reaches Bindings", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		inc := filepath.Join(f.homeDir, "inc.gitconfig")
		writeFile(t, inc, "[user]\n\temail = ada@corp\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"),
			"[include]\n\tpath = ~/inc.gitconfig\n")

		g := newGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}

		b := findBinding(t, g.Bindings(f.homeDir, f.sandboxHome), inc)
		if b.Dest != inc {
			t.Errorf("Dest = %q, want %q", b.Dest, inc)
		}
		if !b.HomeRelativeDest {
			t.Error("a ~/-spelled include is re-expanded against the sandbox $HOME, so its Dest must be home-relative")
		}
		if stderr.Len() != 0 {
			t.Errorf("carrying a resolvable include must be silent, got: %s", stderr)
		}
	})

	// Overview loss 2: builder.go repoints XDG_CONFIG_HOME into the sandbox
	// unconditionally, so a host whose global config lives only at
	// $XDG/git/config loses the whole file - identity included - unless it is
	// bound. It is carried as an origin rather than by binding the XDG
	// directory: binding the directory would put every file in it into the
	// sandbox, including ones no config names.
	t.Run("an XDG-only global config keeps its identity", func(t *testing.T) {
		f := setup(t)
		stderr := captureNotices(t)
		xdgConfig := filepath.Join(f.homeDir, ".config", "git", "config")
		if err := os.MkdirAll(filepath.Dir(xdgConfig), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, xdgConfig, "[user]\n\temail = ada@corp\n")
		// Deliberately no ~/.gitconfig: this host has one global config file
		// and it is not the one the static bindings already mount.

		g := newGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}

		b := findBinding(t, g.Bindings(f.homeDir, f.sandboxHome), xdgConfig)
		if b.Dest != xdgConfig {
			t.Errorf("Dest = %q, want %q", b.Dest, xdgConfig)
		}
		// In-sandbox git reads $HOME/.config/git/config however the host's own
		// XDG_CONFIG_HOME is set, so the destination follows the sandbox home.
		if !b.HomeRelativeDest {
			t.Error("the XDG global config is read at $HOME/.config/git/config in the sandbox, so its Dest must be home-relative")
		}
		if stderr.Len() != 0 {
			t.Errorf("carrying the XDG global config must be silent, got: %s", stderr)
		}
	})

	// git expands ~/ by concatenation and leaves the .. for the kernel, so a
	// spelling that climbs back out of $HOME names a path outside it.
	// HomeRelativeDest means "inside the sandbox home", and docker.go rewrites
	// only a Dest still carrying the host home prefix - which filepath.Join has
	// cleaned away here. Flagging it anyway is a no-op that reads as applied,
	// the exact silent nothing the flag exists to remove.
	t.Run("a ~/ spelling that escapes $HOME is not home-relative", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		shared := filepath.Join(filepath.Dir(f.homeDir), "shared")
		if err := os.MkdirAll(shared, 0o755); err != nil {
			t.Fatal(err)
		}
		ignore := filepath.Join(shared, "ignore")
		writeFile(t, ignore, "build/\n")
		inc := filepath.Join(shared, "team.gitconfig")
		writeFile(t, inc, "[user]\n\tname = Ada\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"),
			"[core]\n\texcludesfile = ~/../shared/ignore\n[include]\n\tpath = ~/../shared/team.gitconfig\n")

		g := newGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		bindings := g.Bindings(f.homeDir, f.sandboxHome)

		for _, src := range []string{ignore, inc} {
			b := findBinding(t, bindings, src)
			if b.Dest != src {
				t.Errorf("Dest = %q, want %q", b.Dest, src)
			}
			if b.HomeRelativeDest {
				t.Errorf("%q is outside the sandbox home, so HomeRelativeDest would be a no-op that reads as applied", src)
			}
		}
	})

	// An include target inside the project tree is chosen by a directory the
	// sandbox writes, so the next launch would bind whatever last session's
	// code pointed it at. Refused silently, because the alternative is mounting
	// nothing - which is what git already does with a missing include.
	t.Run("an include under a trust-denied root is not carried", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		inc := filepath.Join(f.projectDir, "inc.gitconfig")
		writeFile(t, inc, "[user]\n\temail = attacker@corp\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"),
			"[include]\n\tpath = "+inc+"\n")

		g := newGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}

		for _, b := range g.Bindings(f.homeDir, f.sandboxHome) {
			if b.Source == inc {
				t.Errorf("an include target under the project dir was carried: %+v", b)
			}
		}
	})

	// The worktree main repo's .git is the fourth sandbox-writable root, and the
	// one this mode creates: readwrite binds it read-write so commits can land,
	// and in worktree mode it sits outside projectDir, the shared temp dir and
	// the sandbox home alike. Left off the deny list, a global include pointing
	// into it let the sandbox rewrite that file between launches and name any
	// host file it liked for core.excludesFile - which devsandbox would then
	// bind in, both trust checks having passed.
	t.Run("an include under the worktree main repo .git is not carried", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)

		repo := filepath.Join(t.TempDir(), "main-repo")
		gitDir := filepath.Join(repo, ".git")
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		secret := filepath.Join(f.homeDir, "secret")
		writeFile(t, secret, "hunter2\n")
		inc := filepath.Join(gitDir, "mygitconfig")
		writeFile(t, inc, "[core]\n\texcludesfile = "+secret+"\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"),
			"[include]\n\tpath = "+inc+"\n")

		g := &Git{}
		g.Configure(GlobalConfig{ProjectDir: f.projectDir, GitRepoRoot: repo},
			map[string]any{"mode": "readwrite"})
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}

		for _, b := range g.Bindings(f.homeDir, f.sandboxHome) {
			if b.Source == inc {
				t.Errorf("an include under the worktree main repo .git was carried: %+v", b)
			}
			if b.Source == secret {
				t.Errorf("a sandbox-writable include chose which host file to mount: %+v", b)
			}
		}
	})

	// The mirror case, and the reason the deny root is derived from the binding
	// rather than from gitRepoRoot alone: mount_mode = "readonly" makes that
	// same tree a read-only bind, so nothing in it is the sandbox's word and
	// refusing a host-owned config there would drop a setting for no reason.
	t.Run("a readonly mount_mode leaves the main repo .git trusted", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)

		repo := filepath.Join(t.TempDir(), "main-repo")
		gitDir := filepath.Join(repo, ".git")
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		ignore := filepath.Join(f.homeDir, "my-ignore")
		writeFile(t, ignore, "build/\n")
		inc := filepath.Join(gitDir, "mygitconfig")
		writeFile(t, inc, "[core]\n\texcludesfile = "+ignore+"\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"),
			"[include]\n\tpath = "+inc+"\n")

		g := &Git{}
		g.Configure(GlobalConfig{ProjectDir: f.projectDir, GitRepoRoot: repo},
			map[string]any{"mode": "readwrite", "mount_mode": "readonly"})
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}

		findBinding(t, g.Bindings(f.homeDir, f.sandboxHome), ignore)
	})

	// docker.go calls getToolBindings twice per launch and tools.Register hands
	// out singletons, so Setup runs more than once against this same struct.
	// Appending instead of assigning emits every resolved binding twice, which
	// is a trackMount panic on bwrap and a "Duplicate mount point" on Docker.
	t.Run("Setup twice yields each destination once", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		writeFile(t, filepath.Join(f.homeDir, "my-ignore"), "build/\n")
		writeFile(t, filepath.Join(f.homeDir, "my-attrs"), "*.bin binary\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"),
			"[core]\n\texcludesfile = ~/my-ignore\n\tattributesfile = ~/my-attrs\n")

		g := newGit(f)
		for i := range 2 {
			if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
				t.Fatalf("Setup #%d: %v", i+1, err)
			}
		}

		seen := map[string]int{}
		for _, d := range dests(g.Bindings(f.homeDir, f.sandboxHome)) {
			seen[d]++
		}
		for dest, n := range seen {
			if n != 1 {
				t.Errorf("destination %s emitted %d times, want 1", dest, n)
			}
		}
		if len(seen) != 6 {
			t.Errorf("got %d distinct destinations, want 6 (4 static + 2 referenced): %v", len(seen), seen)
		}
	})

	// The static bindings leave Dest empty and are mounted at their Source, so
	// a collision with them is only visible once the effective destination is
	// compared. Comparing the Dest fields finds nothing and the duplicate
	// reaches the backend.
	t.Run("a reference colliding with a static binding is dropped", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		// Contrived, but it is the shape that matters: a resolved reference
		// whose destination is one the fixed set already mounts.
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"),
			"[core]\n\texcludesfile = ~/.gitconfig\n")

		g := newGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}

		bindings := g.Bindings(f.homeDir, f.sandboxHome)
		if len(bindings) != 4 {
			t.Fatalf("got %d bindings, want the 4 static ones: %v", len(bindings), dests(bindings))
		}
		var n int
		for _, d := range dests(bindings) {
			if d == filepath.Join(f.homeDir, ".gitconfig") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("~/.gitconfig mounted %d times, want 1", n)
		}
	})

	// `tools info` builds a registry and calls Bindings straight out.
	t.Run("Bindings without Setup is the static set", func(t *testing.T) {
		f := setup(t)
		writeFile(t, filepath.Join(f.homeDir, "my-ignore"), "build/\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"),
			"[core]\n\texcludesfile = ~/my-ignore\n")

		bindings := newGit(f).Bindings(f.homeDir, f.sandboxHome)
		if len(bindings) != 4 {
			t.Fatalf("got %d bindings without Setup, want 4: %v", len(bindings), dests(bindings))
		}
	})

	// builder.go aborts the launch on a Setup error while docker.go only warns,
	// so a host condition the user should merely be told about must never be
	// returned as one - the same condition would be fatal on one backend and
	// cosmetic on the other.
	t.Run("an unresolvable config is not a Setup error", func(t *testing.T) {
		f := setup(t)
		captureNotices(t)
		stubGit(t, "exit 128")

		g := newGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup must not fail the launch over an unreadable host config, got: %v", err)
		}
		if bindings := g.Bindings(f.homeDir, f.sandboxHome); len(bindings) != 4 {
			t.Errorf("got %d bindings, want the 4 static ones: %v", len(bindings), dests(bindings))
		}
	})

	// readonly and disabled must be untouched by this change: neither consults
	// refBindings, so Setup must leave their binding sets exactly as a
	// Setup-less instance produces them.
	for _, mode := range []string{"readonly", "disabled"} {
		t.Run(mode+" bindings are unchanged by Setup", func(t *testing.T) {
			f := setup(t)
			captureNotices(t)
			writeFile(t, filepath.Join(f.homeDir, "my-ignore"), "build/\n")
			writeFile(t, filepath.Join(f.homeDir, ".gitconfig"),
				"[core]\n\texcludesfile = ~/my-ignore\n")

			fresh := &Git{}
			fresh.Configure(GlobalConfig{ProjectDir: f.projectDir}, map[string]any{"mode": mode})
			want := fresh.Bindings(f.homeDir, f.sandboxHome)

			g := &Git{}
			g.Configure(GlobalConfig{ProjectDir: f.projectDir}, map[string]any{"mode": mode})
			if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			if g.refBindings != nil {
				t.Errorf("%s mode must resolve no references, got %v", mode, dests(g.refBindings))
			}
			if got := g.Bindings(f.homeDir, f.sandboxHome); !reflect.DeepEqual(got, want) {
				t.Errorf("%s bindings changed after Setup:\ngot  %+v\nwant %+v", mode, got, want)
			}
		})
	}
}

// readWriteRefFixture is the host/sandbox/project triple the readwrite
// reference tests resolve a host config against.
type readWriteRefFixture struct {
	homeDir     string
	sandboxHome string
	projectDir  string
}

// newReadWriteRefFixture builds the three directories and points
// XDG_CONFIG_HOME inside the fixture home, so nothing here can reach the
// developer's own git configuration.
func newReadWriteRefFixture(t *testing.T) readWriteRefFixture {
	t.Helper()
	tmpDir := t.TempDir()
	f := readWriteRefFixture{
		homeDir:     filepath.Join(tmpDir, "home"),
		sandboxHome: filepath.Join(tmpDir, "sandbox"),
		projectDir:  filepath.Join(tmpDir, "project"),
	}
	for _, d := range []string{f.homeDir, f.sandboxHome, f.projectDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	isolateGitEnv(t, f.homeDir)
	return f
}

// requireResolvableGit skips when the host has no git, or a git too old for
// `config --list --show-scope`, which is what the readwrite resolver needs. The
// version gate is applied by running the real command rather than parsing
// `git --version`, so it tracks what resolveGlobalConfig actually requires.
//
// The probe runs through Output(), not Run(), because that is the only way the
// gate can fire: os/exec fills ExitError.Stderr from Cmd.Output alone, so under
// Run it is nil, isUnsupportedShowScope sees "" and the skip is unreachable.
// runGitConfigList reads the field the same way and already calls Output.
func requireResolvableGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.Command("git", "config", "--list", "--show-scope").Output(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && isUnsupportedShowScope(exitErr.Stderr) {
			t.Skip("git predates --show-scope (2.26)")
		}
	}
}

func newReadWriteRefGit(f readWriteRefFixture) *Git {
	g := &Git{}
	g.Configure(GlobalConfig{ProjectDir: f.projectDir}, map[string]any{"mode": "readwrite"})
	return g
}

// bindingDests reports the path each binding actually lands on, which is Source
// whenever Dest is empty - the four static readwrite bindings all leave it so.
func bindingDests(bindings []Binding) []string {
	out := make([]string, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, bindingDest(b))
	}
	return out
}

// TestGit_ConfigChainIsNeverSandboxWritable pins the line between the two
// classes of readwrite binding, under every mount mode, in both directions.
//
// A file devsandbox *parses* - ~/.gitconfig, an XDG root, an [include] target -
// is a read-only bind whatever the mount mode says. auxFileBindings and
// includeOriginBindings decide which host files to mount by resolving these and
// anchoring on the origin path, so a config the sandbox can write is a config
// the sandbox can choose from: append core.excludesFile = ~/.aws/credentials to
// a writable host ~/.gitconfig on one launch and devsandbox binds that file in
// on the next, both trust checks satisfied. The deny list cannot close it -
// the origin is the user's own ~/.gitconfig.
//
// A file devsandbox only *names* - the ignore and attributes files - must keep
// following the mount mode. Pinning those would break the documented
// mount_mode = "readwrite" behavior for no gain, since nothing in an ignore file
// decides what the next launch mounts.
func TestGit_ConfigChainIsNeverSandboxWritable(t *testing.T) {
	requireResolvableGit(t)

	for _, mountMode := range []string{"", "split", "overlay", "tmpoverlay", "readonly", "readwrite"} {
		t.Run("mount_mode="+mountMode, func(t *testing.T) {
			f := newReadWriteRefFixture(t)
			captureNotices(t)

			ignore := filepath.Join(f.homeDir, "my-ignore")
			writeFile(t, ignore, "build/\n")
			inc := filepath.Join(f.homeDir, "work.gitconfig")
			writeFile(t, inc, "[user]\n\temail = ada@corp\n")
			root := filepath.Join(f.homeDir, ".gitconfig")
			writeFile(t, root,
				"[core]\n\texcludesfile = "+ignore+"\n[include]\n\tpath = "+inc+"\n")

			toolCfg := map[string]any{"mode": "readwrite"}
			if mountMode != "" {
				toolCfg["mount_mode"] = mountMode
			}
			g := &Git{}
			g.Configure(GlobalConfig{ProjectDir: f.projectDir}, toolCfg)
			if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			bindings := g.Bindings(f.homeDir, f.sandboxHome)

			for _, src := range []string{root, inc} {
				b := findBindingBySource(t, bindings, src)
				if b.Type != MountBind || !b.ReadOnly {
					t.Errorf("%s: Type = %q ReadOnly = %v, want a pinned read-only bind - "+
						"devsandbox resolves this file to decide which host files to mount",
						src, b.Type, b.ReadOnly)
				}
			}

			b := findBindingBySource(t, bindings, ignore)
			if b.Type != "" || b.ReadOnly {
				t.Errorf("%s: Type = %q ReadOnly = %v, want both unset so the mount mode applies - "+
					"devsandbox never parses this file", ignore, b.Type, b.ReadOnly)
			}
		})
	}
}

// TestGit_XDGConfigUnderWritableMountIsRefused is the end-to-end case that
// showed the deny list was still being written from the instance in hand.
//
// A read-only pin on the config file does not make it unwritable when the same
// inode is reachable through a writable directory bind beside it: with
// XDG_CONFIG_HOME under ~/.ssh, mount_mode = "readwrite" mounts ~/.ssh writable
// and the sandbox edits ~/.ssh/git/config through it. The root has to be
// refused as a config source, which is what walking the tool's own writable
// bindings achieves.
func TestGit_XDGConfigUnderWritableMountIsRefused(t *testing.T) {
	requireResolvableGit(t)

	f := newReadWriteRefFixture(t)
	captureNotices(t)

	xdg := filepath.Join(f.homeDir, ".ssh")
	if err := os.MkdirAll(filepath.Join(xdg, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)

	secret := filepath.Join(f.homeDir, "secret")
	writeFile(t, secret, "hunter2\n")
	// Stands in for what the sandbox wrote through the ~/.ssh mount.
	writeFile(t, filepath.Join(xdg, "git", "config"),
		"[core]\n\texcludesfile = "+secret+"\n")

	g := &Git{}
	g.Configure(GlobalConfig{ProjectDir: f.projectDir},
		map[string]any{"mode": "readwrite", "mount_mode": "readwrite"})
	if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	for _, b := range g.Bindings(f.homeDir, f.sandboxHome) {
		if b.Source == secret {
			t.Errorf("a config under a writable mount chose which host file to carry in: %+v", b)
		}
		if b.Source == filepath.Join(xdg, "git", "config") {
			t.Errorf("a config under a writable mount was carried as a trusted root: %+v", b)
		}
	}
}

// TestGit_SandboxWritableRoots enumerates the whole input space of the walk
// that puts this tool's own writable mounts on the deny list, because it is
// wrong in both directions. Naming a root the tool mounts read-only refuses a
// host-owned config and alerts about a write that cannot happen; missing one it
// mounts read-write lets a config the sandbox rewrote pick which host file the
// next launch carries in.
//
// Every writable static binding has to appear, not just the worktree .git:
// ~/.ssh and ~/.gnupg become writable host binds under mount_mode = "readwrite"
// too, and an XDG config nested under either is reachable through them
// whatever the read-only pin on the config file itself says.
func TestGit_SandboxWritableRoots(t *testing.T) {
	// A real tree, because the walk stats the worktree .git before binding it.
	home := t.TempDir()
	repo := filepath.Join(t.TempDir(), "main-repo")
	project := filepath.Join(t.TempDir(), "worktree")
	for _, d := range []string{filepath.Join(repo, ".git"), project, filepath.Join(home, ".ssh"), filepath.Join(home, ".gnupg")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitDir := filepath.Join(repo, ".git")
	ssh := filepath.Join(home, ".ssh")
	gnupg := filepath.Join(home, ".gnupg")
	creds := filepath.Join(home, ".git-credentials")

	tests := []struct {
		name      string
		mode      string
		mountMode string
		repoRoot  string
		want      []string
	}{
		// An overlay policy keeps the sandbox's writes off the host, so only
		// the .git pin - which escapes that overlay deliberately - is writable.
		{name: "unset", mode: "readwrite", repoRoot: repo, want: []string{gitDir}},
		{name: "split", mode: "readwrite", mountMode: "split", repoRoot: repo, want: []string{gitDir}},
		{name: "overlay", mode: "readwrite", mountMode: "overlay", repoRoot: repo, want: []string{gitDir}},
		{name: "tmpoverlay", mode: "readwrite", mountMode: "tmpoverlay", repoRoot: repo, want: []string{gitDir}},

		// Everything the tool mounts becomes a writable host bind, except
		// ~/.gitconfig, which is pinned read-only because devsandbox parses it.
		{name: "mount readwrite", mode: "readwrite", mountMode: "readwrite", repoRoot: repo,
			want: []string{creds, ssh, gnupg, gitDir}},
		{name: "mount readwrite, no worktree", mode: "readwrite", mountMode: "readwrite",
			want: []string{creds, ssh, gnupg}},

		// Nothing reaches the host writable.
		{name: "mount readonly", mode: "readwrite", mountMode: "readonly", repoRoot: repo},
		{name: "readonly git mode", mode: "readonly", mountMode: "readwrite", repoRoot: repo},
		{name: "disabled git mode", mode: "disabled", mountMode: "readwrite", repoRoot: repo},

		// Not a worktree: the main repo is the project dir, which
		// launchBoundsFor already covers.
		{name: "repo root equals project dir", mode: "readwrite", repoRoot: project},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolCfg := map[string]any{"mode": tt.mode}
			if tt.mountMode != "" {
				toolCfg["mount_mode"] = tt.mountMode
			}

			g := &Git{}
			g.Configure(GlobalConfig{ProjectDir: project, GitRepoRoot: tt.repoRoot}, toolCfg)

			got := g.sandboxWritableRoots(home)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("sandboxWritableRoots() = %v, want %v", got, tt.want)
			}
		})
	}
}

func findBindingBySource(t *testing.T, bindings []Binding, source string) Binding {
	t.Helper()
	for _, b := range bindings {
		if b.Source == source {
			return b
		}
	}
	t.Fatalf("no binding with Source %q, got %v", source, bindingDests(bindings))
	return Binding{}
}

// assertOneBindingPerDest fails when two bindings land on the same path. Both
// backends refuse that outright - trackMount panics on bwrap, Docker rejects a
// duplicate mount point - so a dedup regression is a failed launch, not a
// cosmetic one.
func assertOneBindingPerDest(t *testing.T, bindings []Binding) {
	t.Helper()
	seen := make(map[string]int, len(bindings))
	for _, b := range bindings {
		seen[bindingDest(b)]++
	}
	for dest, n := range seen {
		if n > 1 {
			t.Errorf("%d bindings land on %q, want one: %v", n, dest, bindingDests(bindings))
		}
	}
}

func hasBindingSource(bindings []Binding, source string) bool {
	for _, b := range bindings {
		if b.Source == source {
			return true
		}
	}
	return false
}

// TestGit_Setup_ReadWriteRefsDegradedResolver covers what readwrite still
// carries when `git config --list` cannot be used at all.
//
// The resolver is what expands includes, so without it the only thing left to
// read is each global config file's own top-level sections. That is strictly
// less than the host has, and the gap is exactly the kind git keeps quiet
// about: a missing include target is ignored with exit 0 and no warning. The
// fallback therefore carries what it can see and says what it cannot.
func TestGit_Setup_ReadWriteRefsDegradedResolver(t *testing.T) {
	t.Run("a top-level include is carried and the loss is reported", func(t *testing.T) {
		f := newReadWriteRefFixture(t)
		inc := filepath.Join(f.homeDir, "inc.gitconfig")
		writeFile(t, inc, "[user]\n\temail = ada@corp\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"), "[include]\n\tpath = ~/inc.gitconfig\n")
		stubGit(t, "exit 128")
		stderr := captureNotices(t)

		g := newReadWriteRefGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup must not fail when the resolver does: %v", err)
		}

		b := findBindingBySource(t, g.Bindings(f.homeDir, f.sandboxHome), inc)
		if b.Dest != inc {
			t.Errorf("Dest = %q, want %q", b.Dest, inc)
		}
		if !b.HomeRelativeDest {
			t.Error("a ~/-spelled include is re-expanded against the sandbox $HOME, so its Dest must be home-relative")
		}
		if !strings.Contains(stderr.String(), "could not read the resolved global config") {
			t.Errorf("a resolver failure must name itself, got: %s", stderr)
		}
		if !strings.Contains(stderr.String(), "top-level [include] targets") {
			t.Errorf("the alert must say what the sandbox is left with, got: %s", stderr)
		}
	})

	t.Run("an absolutely spelled include is bound verbatim", func(t *testing.T) {
		f := newReadWriteRefFixture(t)
		captureNotices(t)
		inc := filepath.Join(t.TempDir(), "inc.gitconfig")
		writeFile(t, inc, "[user]\n\temail = ada@corp\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"), "[include]\n\tpath = "+inc+"\n")
		stubGit(t, "exit 128")

		g := newReadWriteRefGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}

		b := findBindingBySource(t, g.Bindings(f.homeDir, f.sandboxHome), inc)
		if b.Dest != inc {
			t.Errorf("Dest = %q, want %q", b.Dest, inc)
		}
		if b.HomeRelativeDest {
			t.Error("an absolute include names the same path on every backend, so its Dest must stay verbatim")
		}
	})

	// The fallback path re-reads the file-valued keys as well. Leaving them
	// unset does not make them skipped, it makes a configured
	// core.excludesFile read as absent and git's XDG default carried instead -
	// a file the host does not use, in place of the one it does.
	t.Run("a host with no include is silent and keeps its file-valued keys", func(t *testing.T) {
		f := newReadWriteRefFixture(t)
		ignore := filepath.Join(f.homeDir, "my-ignore")
		writeFile(t, ignore, "build/\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"), "[core]\n\texcludesfile = ~/my-ignore\n")
		stubGit(t, "exit 128")
		stderr := captureNotices(t)

		g := newReadWriteRefGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}

		b := findBindingBySource(t, g.Bindings(f.homeDir, f.sandboxHome), ignore)
		if !b.HomeRelativeDest {
			t.Error("a ~/-spelled excludesFile must still be home-relative on the fallback path")
		}
		// Nothing was lost, so nothing is reported: an alert on every launch of
		// a host with no includes is one the user learns to answer unread.
		if stderr.Len() != 0 {
			t.Errorf("a host with no include must be silent, got: %s", stderr)
		}
		// The fallback re-emits ~/.gitconfig, which the static set already
		// mounts. Only dedupBindingDests keeps that from being a duplicate
		// mount - a trackMount panic on bwrap and a "Duplicate mount point"
		// error on Docker, i.e. every readwrite launch failing on such a host.
		assertOneBindingPerDest(t, g.Bindings(f.homeDir, f.sandboxHome))
	})

	// An old git with nothing to lose must stay silent too: the warning gate
	// blocks the launch on a confirmation prompt, so an announcement here
	// trains the user to answer it unread.
	t.Run("an old git with no include is silent", func(t *testing.T) {
		f := newReadWriteRefFixture(t)
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"), "[user]\n\tname = Ada\n")
		stubGit(t, "echo \"error: unknown option \\`show-scope'\" >&2\nexit 129")
		stderr := captureNotices(t)

		g := newReadWriteRefGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}

		if stderr.Len() != 0 {
			t.Errorf("an old git with no include loses nothing and must be silent, got: %s", stderr)
		}
	})

	// A config file that cannot be read tells us nothing about what it
	// declares, and an unknown is not an absence. Reading it as "no includes"
	// would mount the parent config naming targets nothing carried, with
	// nothing on screen - the exact silent loss the alert exists to prevent.
	t.Run("an unreadable config file is reported as a possible loss", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a 0000 file regardless of its mode")
		}
		f := newReadWriteRefFixture(t)
		gitconfig := filepath.Join(f.homeDir, ".gitconfig")
		writeFile(t, gitconfig, "[include]\n\tpath = ~/inc.gitconfig\n")
		if err := os.Chmod(gitconfig, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(gitconfig, 0o644) })
		stubGit(t, "exit 128")
		stderr := captureNotices(t)

		g := newReadWriteRefGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}

		if !strings.Contains(stderr.String(), "top-level [include] targets") {
			t.Errorf("an unreadable config must be reported as a possible include loss, got: %s", stderr)
		}
	})

	// An include target the host does not have is not a loss - git ignores a
	// missing one too - but the include directive itself is still a directive
	// the fallback could not expand, so the alert stands.
	t.Run("an include naming a missing file binds nothing and still reports", func(t *testing.T) {
		f := newReadWriteRefFixture(t)
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"), "[include]\n\tpath = ~/absent.gitconfig\n")
		stubGit(t, "exit 128")
		stderr := captureNotices(t)

		g := newReadWriteRefGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}

		absent := filepath.Join(f.homeDir, "absent.gitconfig")
		if hasBindingSource(g.Bindings(f.homeDir, f.sandboxHome), absent) {
			t.Errorf("a missing include target must not be bound, got a binding for %q", absent)
		}
		if !strings.Contains(stderr.String(), "top-level [include] targets") {
			t.Errorf("the unexpandable include must still be reported, got: %s", stderr)
		}
	})

	// --show-scope arrived in git 2.26. An older host is not a setting the user
	// got wrong, so the version is not named - but the includes it cannot
	// expand are a real gap and are.
	t.Run("an old git loses its includes without being named", func(t *testing.T) {
		f := newReadWriteRefFixture(t)
		inc := filepath.Join(f.homeDir, "inc.gitconfig")
		writeFile(t, inc, "[user]\n\temail = ada@corp\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"), "[include]\n\tpath = ~/inc.gitconfig\n")
		stubGit(t, "echo \"error: unknown option \\`show-scope'\" >&2\n"+
			"echo \"usage: git config [<options>]\" >&2\n"+
			"exit 129")
		stderr := captureNotices(t)

		g := newReadWriteRefGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup must not fail on an old git: %v", err)
		}

		findBindingBySource(t, g.Bindings(f.homeDir, f.sandboxHome), inc)
		if !strings.Contains(stderr.String(), "top-level [include] targets") {
			t.Errorf("an old git still loses the nested includes, which must be reported, got: %s", stderr)
		}
		for _, naming := range []string{"unknown option", "usage", "exit status"} {
			if strings.Contains(stderr.String(), naming) {
				t.Errorf("the alert must not name the git version or its error (%q), got: %s", naming, stderr)
			}
		}
	})

	// The condition cannot be evaluated without the resolver, and binding the
	// target anyway would carry a work identity into a personal project's
	// sandbox. It is reported instead of guessed at.
	t.Run("a conditional include is reported but not carried", func(t *testing.T) {
		f := newReadWriteRefFixture(t)
		work := filepath.Join(f.homeDir, "work.gitconfig")
		writeFile(t, work, "[user]\n\temail = ada@work\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"),
			"[includeIf \"gitdir:~/work/\"]\n\tpath = ~/work.gitconfig\n")
		stubGit(t, "exit 128")
		stderr := captureNotices(t)

		g := newReadWriteRefGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}

		if hasBindingSource(g.Bindings(f.homeDir, f.sandboxHome), work) {
			t.Error("an unevaluated includeIf target must not be carried")
		}
		if !strings.Contains(stderr.String(), "includeIf") {
			t.Errorf("a conditional include the fallback cannot evaluate must be reported, got: %s", stderr)
		}
	})

	// The project tree is written by the sandbox, so an include declared to sit
	// there is the sandbox choosing which host file devsandbox mounts next
	// launch.
	t.Run("an include under a trust-denied root is not carried", func(t *testing.T) {
		f := newReadWriteRefFixture(t)
		captureNotices(t)
		inc := filepath.Join(f.projectDir, "inc.gitconfig")
		writeFile(t, inc, "[user]\n\temail = attacker@corp\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"), "[include]\n\tpath = "+inc+"\n")
		stubGit(t, "exit 128")

		g := newReadWriteRefGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}

		if hasBindingSource(g.Bindings(f.homeDir, f.sandboxHome), inc) {
			t.Error("an include target under the project dir was carried")
		}
	})

	// Unlike readonly - which generates a config and mounts no include at all -
	// readwrite mounts the include target, so a core.excludesFile declared
	// inside it applies in the sandbox. Reading the file-valued keys from the
	// two root configs alone leaves that value invisible here, so nothing binds
	// the file it names and git drops the host's ignore rules with exit 0 and
	// no warning.
	t.Run("a file-valued key declared inside a carried include is honored", func(t *testing.T) {
		f := newReadWriteRefFixture(t)
		captureNotices(t)
		ignore := filepath.Join(f.homeDir, "my-ignore")
		writeFile(t, ignore, "build/\n")
		attrs := filepath.Join(f.homeDir, "my-attrs")
		writeFile(t, attrs, "*.bin binary\n")
		writeFile(t, filepath.Join(f.homeDir, "inc.gitconfig"),
			"[core]\n\texcludesfile = ~/my-ignore\n\tattributesfile = ~/my-attrs\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"), "[include]\n\tpath = ~/inc.gitconfig\n")
		stubGit(t, "exit 128")

		g := newReadWriteRefGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		bindings := g.Bindings(f.homeDir, f.sandboxHome)

		for _, src := range []string{ignore, attrs} {
			b := findBindingBySource(t, bindings, src)
			if b.Dest != src {
				t.Errorf("Dest = %q, want %q", b.Dest, src)
			}
			if !b.HomeRelativeDest {
				t.Errorf("%q is ~/-spelled, so its Dest must be home-relative", src)
			}
		}
		// The XDG defaults must not be carried in their place: the host does
		// not read those files, and substituting them is the silent wrong-file
		// swap fallbackValues exists to avoid.
		for _, name := range []string{"ignore", "attributes"} {
			xdg := filepath.Join(f.homeDir, ".config", "git", name)
			if hasBindingSource(bindings, xdg) {
				t.Errorf("the XDG default %q was carried over the value the include sets", xdg)
			}
		}
		assertOneBindingPerDest(t, bindings)
	})

	// The declaring file is read after the file it includes, exactly as git
	// reads them, so a root that also sets the key wins over its include.
	t.Run("a root config overrides a file-valued key its include sets", func(t *testing.T) {
		f := newReadWriteRefFixture(t)
		captureNotices(t)
		rootIgnore := filepath.Join(f.homeDir, "root-ignore")
		writeFile(t, rootIgnore, "root/\n")
		incIgnore := filepath.Join(f.homeDir, "inc-ignore")
		writeFile(t, incIgnore, "inc/\n")
		writeFile(t, filepath.Join(f.homeDir, "inc.gitconfig"), "[core]\n\texcludesfile = ~/inc-ignore\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"),
			"[include]\n\tpath = ~/inc.gitconfig\n[core]\n\texcludesfile = ~/root-ignore\n")
		stubGit(t, "exit 128")

		g := newReadWriteRefGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		bindings := g.Bindings(f.homeDir, f.sandboxHome)

		findBindingBySource(t, bindings, rootIgnore)
		if hasBindingSource(bindings, incIgnore) {
			t.Error("the include's excludesFile was carried over the declaring file's own value")
		}
	})

	// An include the trust check refuses is not readable in the sandbox either,
	// so its file-valued keys must not decide what devsandbox mounts - that is
	// the sandbox choosing which host file gets carried in next launch.
	t.Run("a trust-denied include cannot name the file that gets carried", func(t *testing.T) {
		f := newReadWriteRefFixture(t)
		captureNotices(t)
		secret := filepath.Join(t.TempDir(), "secret")
		writeFile(t, secret, "x\n")
		inc := filepath.Join(f.projectDir, "inc.gitconfig")
		writeFile(t, inc, "[core]\n\texcludesfile = "+secret+"\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"), "[include]\n\tpath = "+inc+"\n")
		stubGit(t, "exit 128")

		g := newReadWriteRefGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}

		if hasBindingSource(g.Bindings(f.homeDir, f.sandboxHome), secret) {
			t.Errorf("an include under the project dir named %q and it was carried", secret)
		}
	})

	// The XDG config is the file the fixed set does not already mount, and
	// builder.go repoints XDG_CONFIG_HOME into the sandbox - so on a host that
	// points its own outside $HOME, the host path is the right Source and the
	// wrong Dest. A relative include inside it inherits both halves.
	t.Run("an XDG-only config outside $HOME keeps its identity and its relative include", func(t *testing.T) {
		f := newReadWriteRefFixture(t)
		captureNotices(t)
		xdgHome := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", xdgHome)
		xdgGit := filepath.Join(xdgHome, "git")
		if err := os.MkdirAll(xdgGit, 0o755); err != nil {
			t.Fatal(err)
		}
		xdgConfig := filepath.Join(xdgGit, "config")
		writeFile(t, xdgConfig, "[user]\n\temail = ada@corp\n[include]\n\tpath = inc.gitconfig\n")
		inc := filepath.Join(xdgGit, "inc.gitconfig")
		writeFile(t, inc, "[user]\n\tname = Ada\n")
		// Deliberately no ~/.gitconfig: this host's whole global config is the
		// one file the fixed set does not mount.
		stubGit(t, "exit 128")

		g := newReadWriteRefGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		bindings := g.Bindings(f.homeDir, f.sandboxHome)

		root := findBindingBySource(t, bindings, xdgConfig)
		wantRoot := filepath.Join(f.homeDir, ".config", "git", "config")
		if root.Dest != wantRoot {
			t.Errorf("Dest = %q, want %q - in-sandbox git reads $HOME/.config/git/config", root.Dest, wantRoot)
		}
		if !root.HomeRelativeDest {
			t.Error("the XDG global config is read under the sandbox $HOME, so its Dest must be home-relative")
		}

		target := findBindingBySource(t, bindings, inc)
		wantTarget := filepath.Join(f.homeDir, ".config", "git", "inc.gitconfig")
		if target.Dest != wantTarget {
			t.Errorf("Dest = %q, want %q - a relative include resolves against the declaring file's directory",
				target.Dest, wantTarget)
		}
		if !target.HomeRelativeDest {
			t.Error("a relative include inherits the declaring file's home-relative flag")
		}
	})

	// The retry that keeps a broken repository from costing the user their
	// identity evaluates no includeIf condition, so the file a matching one
	// names is never reported as an origin and never carried.
	t.Run("reading outside the repository reports the unevaluated includeIf", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
		f := newReadWriteRefFixture(t)

		initCmd := exec.Command("git", "init", "-q")
		initCmd.Dir = f.projectDir
		initCmd.Env = gitCommandEnv(os.Environ(), f.homeDir)
		if out, err := initCmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v: %s", err, out)
		}
		// An unterminated section header: git reports "bad config line" and
		// exits 128, which takes the global scope down with it.
		writeFile(t, filepath.Join(f.projectDir, ".git", "config"), "[core\n\trepositoryformatversion = 0\n")
		writeFile(t, filepath.Join(f.homeDir, ".gitconfig"),
			"[user]\n\tname = Ada\n[includeIf \"gitdir:~/work/\"]\n\tpath = ~/work.gitconfig\n")
		stderr := captureNotices(t)

		g := newReadWriteRefGit(f)
		if err := g.Setup(f.homeDir, f.sandboxHome); err != nil {
			t.Fatalf("Setup: %v", err)
		}

		if !strings.Contains(stderr.String(), "no includeIf condition was evaluated") {
			t.Errorf("a config read outside the repository must report the unevaluated includeIf, got: %s", stderr)
		}
	})
}

// TestDedupBindingDests covers the collision rule directly, including the case
// that makes it necessary: the static readwrite bindings leave Dest empty and
// are mounted at their Source by both backends.
func TestDedupBindingDests(t *testing.T) {
	existing := []Binding{
		{Source: "/home/u/.gitconfig"}, // Dest empty - mounted at Source
		{Source: "/gen/safe", Dest: "/home/u/x"},
	}
	candidates := []Binding{
		{Source: "/host/a", Dest: "/home/u/.gitconfig"}, // collides via the empty Dest above
		{Source: "/home/u/x"},                           // collides via an explicit Dest
		{Source: "/host/b", Dest: "/home/u/b"},          // kept
		{Source: "/host/c", Dest: "/home/u/b"},          // collides with the candidate above
		{Source: "/home/u/d"},                           // kept, Dest empty
	}

	got := dedupBindingDests(existing, candidates)
	want := []Binding{
		{Source: "/host/b", Dest: "/home/u/b"},
		{Source: "/home/u/d"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dedupBindingDests:\ngot  %+v\nwant %+v", got, want)
	}

	if got := dedupBindingDests(existing, nil); got != nil {
		t.Errorf("no candidates must yield nil, got %+v", got)
	}
}

// Disabled mode suppresses git configuration, not git itself - and a
// worktree's metadata is the one part of a repository that does not travel
// with the project mount. Without this binding `git status` in a worktree
// fails with "not a git repository" even though disabled mode promises
// working git commands.
func TestGitDisabledBindingsWorktreeMountsMainGitDir(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir()

	g := &Git{}
	g.Configure(GlobalConfig{ProjectDir: wt, GitRepoRoot: repo}, map[string]any{"mode": "disabled"})
	bindings := g.Bindings(t.TempDir(), t.TempDir())

	want := filepath.Join(repo, ".git")
	if len(bindings) != 1 {
		t.Fatalf("expected exactly the shared git dir binding, got %d: %+v", len(bindings), bindings)
	}
	b := bindings[0]
	if b.Source != want || b.Dest != want {
		t.Errorf("binding = %+v, want Source and Dest %s", b, want)
	}
	if b.ReadOnly {
		t.Errorf("disabled mode leaves .git writable, got ReadOnly=true")
	}
}

func TestGitDisabledBindingsNoWorktreeStaysEmpty(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	g := &Git{}
	g.Configure(GlobalConfig{ProjectDir: repo, GitRepoRoot: ""}, map[string]any{"mode": "disabled"})
	if bindings := g.Bindings(t.TempDir(), t.TempDir()); len(bindings) != 0 {
		t.Errorf("disabled mode outside a worktree must emit no bindings, got %+v", bindings)
	}
}

// A GitRepoRoot pointing at a directory with no .git must not produce a
// binding whose source does not exist.
func TestGitDisabledBindingsMissingGitDir(t *testing.T) {
	g := &Git{}
	g.Configure(GlobalConfig{ProjectDir: t.TempDir(), GitRepoRoot: t.TempDir()}, map[string]any{"mode": "disabled"})
	if bindings := g.Bindings(t.TempDir(), t.TempDir()); len(bindings) != 0 {
		t.Errorf("expected no bindings when the shared git dir is absent, got %+v", bindings)
	}
}
