package tools

import (
	"fmt"
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
	sessionsDir := p.AgentSessionDir(homeDir)

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

// AgentSessionDir implements ToolWithAgentSessionDir. It reuses agentDir() so
// the bound cannot disagree with the sessions binding Bindings emits — the two
// must name the same directory or the herdr proxy would deny every real report.
//
// Pi's integration reports agent_session_path in preference to an id, and its
// session files live one level deeper than this directory
// (sessions/<project>/<timestamp>_<uuid>.jsonl), which the filter's prefix
// confinement admits. The path is forwarded unmodified: herdr persists the
// session ref without ever dereferencing it, so translating this sandbox path
// to its host backing path would buy nothing.
func (p *Pi) AgentSessionDir(homeDir string) string {
	return filepath.Join(p.agentDir(homeDir), "sessions")
}

// Setup creates the host sessions directory so the persistent overlay declared
// for it actually applies.
//
// The binding is Optional, and the builder skips an Optional overlay whose host
// source is missing. A user who ran pi on the host but only ever runs it
// sandboxed therefore has the agent dir present — tmpoverlay, writes discarded
// — and sessions/ absent, so every session would vanish with the sandbox even
// with the split binding in place. Pi reports agent_session_path to herdr, so
// the discarded session is one herdr later tries to resume.
//
// Nothing is created when the agent dir itself is absent: no tmpoverlay is
// mounted in that case, so writes already land in the sandbox home and persist.
func (p *Pi) Setup(homeDir, _ string) error {
	if _, err := os.Stat(p.agentDir(homeDir)); err != nil {
		return nil
	}
	sessions := p.AgentSessionDir(homeDir)
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		return fmt.Errorf("create pi sessions directory %s: %w", sessions, err)
	}
	return nil
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

// Ensure interfaces are implemented.
var (
	_ Tool                    = (*Pi)(nil)
	_ ToolWithCheck           = (*Pi)(nil)
	_ ToolWithSetup           = (*Pi)(nil)
	_ ToolWithAgentSessionDir = (*Pi)(nil)
)
