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
