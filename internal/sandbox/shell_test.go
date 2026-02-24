package sandbox

import (
	"strings"
	"testing"
)

func TestBuildShellCommand_Fish_Interactive(t *testing.T) {
	cfg := &Config{
		ProjectName: "testproject",
		Shell:       ShellFish,
		ShellPath:   "/usr/bin/fish",
	}

	cmd := BuildShellCommand(cfg, []string{})

	if len(cmd) != 3 {
		t.Fatalf("Expected 3 elements, got %d: %v", len(cmd), cmd)
	}

	if cmd[0] != "/usr/bin/fish" {
		t.Errorf("Expected fish shell, got %s", cmd[0])
	}

	if cmd[1] != "-c" {
		t.Errorf("Expected -c flag, got %s", cmd[1])
	}

	if !strings.Contains(cmd[2], "mise activate fish") {
		t.Error("Expected mise activation in command")
	}

	if !strings.Contains(cmd[2], "fish_greeting") {
		t.Error("Expected fish_greeting in interactive mode")
	}

	if !strings.Contains(cmd[2], "exec fish") {
		t.Error("Expected exec fish in interactive mode")
	}
}

func TestBuildShellCommand_Fish_SingleCommand(t *testing.T) {
	cfg := &Config{
		ProjectName: "testproject",
		Shell:       ShellFish,
		ShellPath:   "/usr/bin/fish",
	}

	cmd := BuildShellCommand(cfg, []string{"npm", "install"})

	if len(cmd) != 3 {
		t.Fatalf("Expected 3 elements, got %d: %v", len(cmd), cmd)
	}

	if cmd[0] != "/usr/bin/fish" {
		t.Errorf("Expected fish shell, got %s", cmd[0])
	}

	if !strings.Contains(cmd[2], "mise activate fish") {
		t.Error("Expected mise activation in command")
	}

	if !strings.Contains(cmd[2], "npm install") {
		t.Error("Expected 'npm install' in command")
	}

	if strings.Contains(cmd[2], "fish_greeting") {
		t.Error("Should not have fish_greeting in single command mode")
	}
}

func TestBuildShellCommand_Bash_Interactive(t *testing.T) {
	cfg := &Config{
		ProjectName: "testproject",
		Shell:       ShellBash,
		ShellPath:   "/bin/bash",
	}

	cmd := BuildShellCommand(cfg, []string{})

	if len(cmd) != 3 {
		t.Fatalf("Expected 3 elements, got %d: %v", len(cmd), cmd)
	}

	if cmd[0] != "/bin/bash" {
		t.Errorf("Expected bash shell, got %s", cmd[0])
	}

	if !strings.Contains(cmd[2], "mise activate bash") {
		t.Error("Expected mise activation in command")
	}

	if !strings.Contains(cmd[2], "PS1=") {
		t.Error("Expected PS1 prompt in interactive mode")
	}

	if !strings.Contains(cmd[2], "exec bash") {
		t.Error("Expected exec bash in interactive mode")
	}
}

func TestBuildShellCommand_Bash_SingleCommand(t *testing.T) {
	cfg := &Config{
		ProjectName: "testproject",
		Shell:       ShellBash,
		ShellPath:   "/bin/bash",
	}

	cmd := BuildShellCommand(cfg, []string{"npm", "install"})

	if cmd[0] != "/bin/bash" {
		t.Errorf("Expected bash shell, got %s", cmd[0])
	}

	if !strings.Contains(cmd[2], "mise activate bash") {
		t.Error("Expected mise activation in command")
	}

	if !strings.Contains(cmd[2], "npm install") {
		t.Error("Expected 'npm install' in command")
	}
}

func TestBuildShellCommand_Zsh_Interactive(t *testing.T) {
	cfg := &Config{
		ProjectName: "testproject",
		Shell:       ShellZsh,
		ShellPath:   "/usr/bin/zsh",
	}

	cmd := BuildShellCommand(cfg, []string{})

	if len(cmd) != 3 {
		t.Fatalf("Expected 3 elements, got %d: %v", len(cmd), cmd)
	}

	if cmd[0] != "/usr/bin/zsh" {
		t.Errorf("Expected zsh shell, got %s", cmd[0])
	}

	if !strings.Contains(cmd[2], "mise activate zsh") {
		t.Error("Expected mise activation in command")
	}

	if !strings.Contains(cmd[2], "PROMPT=") {
		t.Error("Expected PROMPT in interactive mode")
	}

	if !strings.Contains(cmd[2], "exec zsh") {
		t.Error("Expected exec zsh in interactive mode")
	}
}

func TestBuildShellCommand_Zsh_SingleCommand(t *testing.T) {
	cfg := &Config{
		ProjectName: "testproject",
		Shell:       ShellZsh,
		ShellPath:   "/usr/bin/zsh",
	}

	cmd := BuildShellCommand(cfg, []string{"npm", "install"})

	if cmd[0] != "/usr/bin/zsh" {
		t.Errorf("Expected zsh shell, got %s", cmd[0])
	}

	if !strings.Contains(cmd[2], "mise activate zsh") {
		t.Error("Expected mise activation in command")
	}

	if !strings.Contains(cmd[2], "npm install") {
		t.Error("Expected 'npm install' in command")
	}
}

func TestDetectShell(t *testing.T) {
	tests := []struct {
		name          string
		shellEnv      string
		expectedShell Shell
		expectedPath  string
	}{
		{
			name:          "fish shell",
			shellEnv:      "/usr/bin/fish",
			expectedShell: ShellFish,
			expectedPath:  "/usr/bin/fish",
		},
		{
			name:          "bash shell",
			shellEnv:      "/bin/bash",
			expectedShell: ShellBash,
			expectedPath:  "/bin/bash",
		},
		{
			name:          "zsh shell",
			shellEnv:      "/usr/bin/zsh",
			expectedShell: ShellZsh,
			expectedPath:  "/usr/bin/zsh",
		},
		{
			name:          "zsh at bin",
			shellEnv:      "/bin/zsh",
			expectedShell: ShellZsh,
			expectedPath:  "/bin/zsh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SHELL", tt.shellEnv)
			shell, path := DetectShell()
			if shell != tt.expectedShell {
				t.Errorf("DetectShell() shell = %v, want %v", shell, tt.expectedShell)
			}
			if path != tt.expectedPath {
				t.Errorf("DetectShell() path = %v, want %v", path, tt.expectedPath)
			}
		})
	}
}

func TestEscapeForShellDoubleQuote(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{`has"quote`, `has\"quote`},
		{"has$dollar", `has\$dollar`},
		{"has`backtick`", "has\\`backtick\\`"},
		{`has\backslash`, `has\\backslash`},
		{`all"$` + "`" + `\special`, `all\"\$` + "\\`" + `\\special`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := escapeForShellDoubleQuote(tt.input)
			if result != tt.expected {
				t.Errorf("escapeForShellDoubleQuote(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestEscapeForFishDoubleQuote(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{`has"quote`, `has\"quote`},
		{"has$dollar", `has\$dollar`},
		{"has`backtick`", "has`backtick`"}, // backtick NOT escaped in fish
		{`has\backslash`, `has\\backslash`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := escapeForFishDoubleQuote(tt.input)
			if result != tt.expected {
				t.Errorf("escapeForFishDoubleQuote(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestBuildShellCommand_ProjectNameEscaping(t *testing.T) {
	t.Run("bash escapes project name", func(t *testing.T) {
		cfg := &Config{
			ProjectName: `test"$(whoami)`,
			Shell:       ShellBash,
			ShellPath:   "/bin/bash",
		}
		cmd := BuildShellCommand(cfg, []string{})
		// $ and " must be escaped in the output
		if !strings.Contains(cmd[2], `\$(whoami)`) {
			t.Errorf("bash prompt missing escaped $: %s", cmd[2])
		}
		if !strings.Contains(cmd[2], `\"`) {
			t.Errorf("bash prompt missing escaped quote: %s", cmd[2])
		}
	})

	t.Run("bash escapes backticks", func(t *testing.T) {
		cfg := &Config{
			ProjectName: "test`id`end",
			Shell:       ShellBash,
			ShellPath:   "/bin/bash",
		}
		cmd := BuildShellCommand(cfg, []string{})
		if !strings.Contains(cmd[2], "\\`id\\`") {
			t.Errorf("bash prompt missing escaped backticks: %s", cmd[2])
		}
	})

	t.Run("zsh escapes project name", func(t *testing.T) {
		cfg := &Config{
			ProjectName: `test"$(whoami)`,
			Shell:       ShellZsh,
			ShellPath:   "/usr/bin/zsh",
		}
		cmd := BuildShellCommand(cfg, []string{})
		if !strings.Contains(cmd[2], `\$(whoami)`) {
			t.Errorf("zsh prompt missing escaped $: %s", cmd[2])
		}
		if !strings.Contains(cmd[2], `\"`) {
			t.Errorf("zsh prompt missing escaped quote: %s", cmd[2])
		}
	})

	t.Run("fish escapes project name", func(t *testing.T) {
		cfg := &Config{
			ProjectName: `test"$(whoami)`,
			Shell:       ShellFish,
			ShellPath:   "/usr/bin/fish",
		}
		cmd := BuildShellCommand(cfg, []string{})
		if !strings.Contains(cmd[2], `\$(whoami)`) {
			t.Errorf("fish greeting missing escaped $: %s", cmd[2])
		}
		if !strings.Contains(cmd[2], `\"`) {
			t.Errorf("fish greeting missing escaped quote: %s", cmd[2])
		}
	})
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Simple strings that don't need quoting
		{"simple", "simple"},
		{"file.txt", "file.txt"},
		{"path/to/file", "path/to/file"},
		{"-flag", "-flag"},
		{"--option=value", "--option=value"},

		// Empty string
		{"", "''"},

		// Strings with spaces need quoting
		{"hello world", "'hello world'"},
		{"test commit message", "'test commit message'"},

		// Strings with special characters
		{"file name.txt", "'file name.txt'"},
		{"$variable", "'$variable'"},
		{"`command`", "'`command`'"},
		{"path with spaces/file", "'path with spaces/file'"},
		{"foo*bar", "'foo*bar'"},
		{"foo?bar", "'foo?bar'"},

		// Strings with quotes need special escaping
		{"it's", "'it'\\''s'"},
		{"say 'hello'", "'say '\\''hello'\\'''"},

		// Double quotes
		{`say "hello"`, `'say "hello"'`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := shellQuote(tt.input)
			if result != tt.expected {
				t.Errorf("shellQuote(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestShellJoinArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name:     "simple command",
			args:     []string{"git", "status"},
			expected: "git status",
		},
		{
			name:     "command with flag",
			args:     []string{"git", "commit", "-m", "test message"},
			expected: "git commit -m 'test message'",
		},
		{
			name:     "command with special chars",
			args:     []string{"echo", "hello world", "$HOME"},
			expected: "echo 'hello world' '$HOME'",
		},
		{
			name:     "npm install",
			args:     []string{"npm", "install"},
			expected: "npm install",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shellJoinArgs(tt.args)
			if result != tt.expected {
				t.Errorf("shellJoinArgs(%v) = %q, want %q", tt.args, result, tt.expected)
			}
		})
	}
}

func TestBuildShellCommand_QuotedArgs(t *testing.T) {
	cfg := &Config{
		ProjectName: "testproject",
		Shell:       ShellBash,
		ShellPath:   "/bin/bash",
	}

	// Test command with spaces in argument
	cmd := BuildShellCommand(cfg, []string{"git", "commit", "-m", "test commit message"})

	if len(cmd) != 3 {
		t.Fatalf("Expected 3 elements, got %d: %v", len(cmd), cmd)
	}

	// The command should contain the properly quoted message
	if !strings.Contains(cmd[2], "'test commit message'") {
		t.Errorf("Expected quoted message in command, got: %s", cmd[2])
	}
}
