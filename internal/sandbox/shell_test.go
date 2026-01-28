package sandbox

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildShellCommand_Interactive(t *testing.T) {
	cfg := &Config{
		ProjectName: "testproject",
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

func TestBuildShellCommand_SingleCommand(t *testing.T) {
	cfg := &Config{
		ProjectName: "testproject",
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

func TestBuildShellCommand_ComplexCommand(t *testing.T) {
	cfg := &Config{
		ProjectName: "testproject",
	}

	cmd := BuildShellCommand(cfg, []string{"echo", "hello world", "&&", "ls"})

	expected := []string{"/usr/bin/fish", "-c", "if command -q mise; mise activate fish | source; end; echo hello world && ls"}

	if !reflect.DeepEqual(cmd, expected) {
		t.Errorf("BuildShellCommand() = %v, want %v", cmd, expected)
	}
}
