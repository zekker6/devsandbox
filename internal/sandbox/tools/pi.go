package tools

import (
	"os"
	"os/exec"
	"path/filepath"
)

func init() {
	Register(&Pi{})
}

// Pi provides pi coding agent tool integration.
// Mounts the agent directory with tmpoverlay to protect settings and
// credentials (auth.json), and the sessions/ subdirectory with a persistent
// overlay so session history is preserved across sandbox sessions.
//
// The agent directory defaults to ~/.pi/agent and can be overridden via the
// PI_CODING_AGENT_DIR environment variable; when set, the host value is
// passed through to the sandbox so pi resolves the same path inside.
type Pi struct{}

func (p *Pi) Name() string {
	return "pi"
}

func (p *Pi) Description() string {
	return "Pi coding agent AI assistant"
}

// agentDir returns the host path of pi's agent directory, honoring
// PI_CODING_AGENT_DIR when set and otherwise falling back to ~/.pi/agent.
func (p *Pi) agentDir(homeDir string) string {
	if dir := os.Getenv("PI_CODING_AGENT_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(homeDir, ".pi", "agent")
}

func (p *Pi) Available(_ string) bool {
	_, err := exec.LookPath("pi")
	return err == nil
}

func (p *Pi) Bindings(homeDir, _ string) []Binding {
	agentDir := p.agentDir(homeDir)
	sessionsDir := filepath.Join(agentDir, "sessions")

	return []Binding{
		// Agent dir: settings.json + auth.json (API keys) — tmpoverlay protects credentials.
		{
			Source:   agentDir,
			Category: CategoryConfig,
			Optional: true,
		},
		// sessions: persistent overlay so session history survives sandbox sessions.
		{
			Source:   sessionsDir,
			Category: CategoryData,
			Optional: true,
		},
	}
}

func (p *Pi) Environment(_, _ string) []EnvVar {
	if os.Getenv("PI_CODING_AGENT_DIR") != "" {
		return []EnvVar{
			{Name: "PI_CODING_AGENT_DIR", FromHost: true},
		}
	}
	return nil
}

func (p *Pi) ShellInit(_ string) string {
	return ""
}

func (p *Pi) Check(homeDir string) CheckResult {
	result := CheckBinary("pi", "mise install npm:@mariozechner/pi-coding-agent")
	if !result.Available {
		return result
	}

	agentDir := p.agentDir(homeDir)
	result.AddConfigPaths(
		agentDir,
		filepath.Join(agentDir, "sessions"),
	)

	return result
}
