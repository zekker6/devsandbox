package tools

import (
	"os/exec"
	"path/filepath"
)

func init() {
	Register(&Pi{})
}

// Pi provides pi coding agent tool integration.
// Mounts the ~/.pi/agent directory with tmpoverlay to protect settings and
// credentials (auth.json), and ~/.pi/agent/sessions with a persistent overlay
// so session history is preserved across sandbox sessions.
type Pi struct{}

func (p *Pi) Name() string {
	return "pi"
}

func (p *Pi) Description() string {
	return "Pi coding agent AI assistant"
}

func (p *Pi) Available(_ string) bool {
	_, err := exec.LookPath("pi")
	return err == nil
}

func (p *Pi) Bindings(homeDir, _ string) []Binding {
	agentDir := filepath.Join(homeDir, ".pi", "agent")
	sessionsDir := filepath.Join(agentDir, "sessions")

	return []Binding{
		// ~/.pi/agent: settings.json + auth.json (API keys) — tmpoverlay protects credentials.
		{
			Source:   agentDir,
			Category: CategoryConfig,
			Optional: true,
		},
		// ~/.pi/agent/sessions: persistent overlay so session history survives sandbox sessions.
		{
			Source:   sessionsDir,
			Category: CategoryData,
			Optional: true,
		},
	}
}

func (p *Pi) Environment(_, _ string) []EnvVar {
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

	result.AddConfigPaths(
		filepath.Join(homeDir, ".pi", "agent"),
		filepath.Join(homeDir, ".pi", "agent", "sessions"),
	)

	return result
}
