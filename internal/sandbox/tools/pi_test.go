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
	t.Setenv("PI_CODING_AGENT_DIR", "")

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
	t.Setenv("PI_CODING_AGENT_DIR", "")

	p := &Pi{}
	envVars := p.Environment("/home/test", "/tmp/sandbox")
	if len(envVars) != 0 {
		t.Errorf("Environment() should return nil/empty, got %v", envVars)
	}
}

func TestPi_Bindings_CustomAgentDir(t *testing.T) {
	customDir := "/etc/pi-agent"
	t.Setenv("PI_CODING_AGENT_DIR", customDir)

	p := &Pi{}
	bindings := p.Bindings("/home/test", "/tmp/sandbox")

	defaultAgentDir := filepath.Join("/home/test", ".pi", "agent")
	expectedSessionsDir := filepath.Join(customDir, "sessions")

	var foundCustom, foundCustomSessions, foundDefault bool
	for _, b := range bindings {
		switch b.Source {
		case customDir:
			foundCustom = true
			if b.Category != CategoryConfig {
				t.Errorf("custom agent dir binding: want category %q, got %q", CategoryConfig, b.Category)
			}
			if !b.Optional {
				t.Error("custom agent dir binding should be optional")
			}
		case expectedSessionsDir:
			foundCustomSessions = true
			if b.Category != CategoryData {
				t.Errorf("custom sessions binding: want category %q, got %q", CategoryData, b.Category)
			}
		case defaultAgentDir:
			foundDefault = true
		}
	}

	if !foundCustom {
		t.Error("expected PI_CODING_AGENT_DIR binding when env var is set")
	}
	if !foundCustomSessions {
		t.Error("expected sessions/ subdirectory of PI_CODING_AGENT_DIR to be bound")
	}
	if foundDefault {
		t.Error("default ~/.pi/agent should NOT be mounted when PI_CODING_AGENT_DIR is set")
	}
}

func TestPi_Environment_WithCustomAgentDir(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "/etc/pi-agent")

	p := &Pi{}
	envVars := p.Environment("/home/test", "/tmp/sandbox")

	var found bool
	for _, env := range envVars {
		if env.Name == "PI_CODING_AGENT_DIR" {
			found = true
			if !env.FromHost {
				t.Error("PI_CODING_AGENT_DIR should use FromHost so the host value reaches the sandbox")
			}
		}
	}

	if !found {
		t.Error("expected PI_CODING_AGENT_DIR env var when it is set on the host")
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
