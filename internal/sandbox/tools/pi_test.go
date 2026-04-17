package tools

import (
	"path/filepath"
	"testing"
)

func TestPi_Name(t *testing.T) {
	p := &Pi{}
	if got := p.Name(); got != "pi" {
		t.Errorf("Name() = %q, want %q", got, "pi")
	}
}

func TestPi_Description(t *testing.T) {
	p := &Pi{}
	if p.Description() == "" {
		t.Error("Description() must not be empty")
	}
}

func TestPi_Available_NoBinary(t *testing.T) {
	// Empty PATH ensures exec.LookPath cannot find a host `pi` binary.
	t.Setenv("PATH", "")

	p := &Pi{}
	if p.Available(t.TempDir()) {
		t.Error("Available() should return false when pi binary is not in PATH")
	}
}

func TestPi_Bindings_Paths(t *testing.T) {
	homeDir := "/home/test"
	sandboxHome := "/tmp/sandbox"

	p := &Pi{}
	bindings := p.Bindings(homeDir, sandboxHome)

	agentDir := filepath.Join(homeDir, ".pi", "agent")
	sessionsDir := filepath.Join(agentDir, "sessions")

	var foundAgent, foundSessions bool
	for _, b := range bindings {
		switch b.Source {
		case agentDir:
			foundAgent = true
			if b.Category != CategoryConfig {
				t.Errorf("~/.pi/agent binding: want category %q, got %q", CategoryConfig, b.Category)
			}
			if !b.Optional {
				t.Error("~/.pi/agent binding should be optional")
			}
		case sessionsDir:
			foundSessions = true
			if b.Category != CategoryData {
				t.Errorf("~/.pi/agent/sessions binding: want category %q, got %q", CategoryData, b.Category)
			}
			if !b.Optional {
				t.Error("~/.pi/agent/sessions binding should be optional")
			}
		}
	}

	if !foundAgent {
		t.Error("Bindings() missing ~/.pi/agent")
	}
	if !foundSessions {
		t.Error("Bindings() missing ~/.pi/agent/sessions")
	}
}

func TestPi_Environment(t *testing.T) {
	p := &Pi{}
	envVars := p.Environment("/home/test", "/tmp/sandbox")
	if len(envVars) != 0 {
		t.Errorf("Environment() should return nil/empty, got %v", envVars)
	}
}

func TestPi_ShellInit(t *testing.T) {
	p := &Pi{}
	for _, shell := range []string{"bash", "zsh", "fish"} {
		if got := p.ShellInit(shell); got != "" {
			t.Errorf("ShellInit(%q) = %q, want empty string", shell, got)
		}
	}
}

func TestPi_Check_NoBinary(t *testing.T) {
	t.Setenv("PATH", "")

	p := &Pi{}
	result := p.Check(t.TempDir())

	if result.Available {
		t.Error("Check() should report not available when pi binary is missing")
	}
	if len(result.Issues) == 0 {
		t.Error("Check() should record an issue when pi binary is missing")
	}
	if result.InstallHint == "" {
		t.Error("Check() must provide a non-empty InstallHint")
	}
}

func TestPi_Registered(t *testing.T) {
	tool := Get("pi")
	if tool == nil {
		t.Fatal("pi tool must be registered in the tools registry")
	}
	if _, ok := tool.(*Pi); !ok {
		t.Errorf("registered pi tool has unexpected type %T", tool)
	}
}
