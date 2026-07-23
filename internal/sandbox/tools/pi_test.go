package tools

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devsandbox/internal/herdrproxy"
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

// TestPi_AgentSessionDirMatchesSessionsBinding pins the invariant the herdr
// filter's path confinement rests on: the bound it is given and the directory
// Pi's sessions are actually persisted in are the same path. If these ever
// disagree, every real Pi session report is denied.
func TestPi_AgentSessionDirMatchesSessionsBinding(t *testing.T) {
	const homeDir = "/home/test"

	tests := []struct {
		name     string
		agentDir string
		want     string
	}{
		{name: "default", agentDir: "", want: filepath.Join(homeDir, ".pi", "agent", "sessions")},
		{name: "PI_CODING_AGENT_DIR", agentDir: "/etc/pi-agent", want: "/etc/pi-agent/sessions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PI_CODING_AGENT_DIR", tt.agentDir)

			p := &Pi{}
			got := p.AgentSessionDir(homeDir)
			if got != tt.want {
				t.Errorf("AgentSessionDir = %q, want %q", got, tt.want)
			}

			found := false
			for _, b := range p.Bindings(homeDir, "/tmp/sandbox") {
				if b.Source == got {
					found = true
					if b.Category != CategoryData {
						t.Errorf("binding for %q has category %q, want %q", got, b.Category, CategoryData)
					}
				}
			}
			if !found {
				t.Errorf("AgentSessionDir %q is not among Pi's bindings", got)
			}
		})
	}
}

// piFilter wires a filter the way Herdr.Start does for a direct `devsandbox pi`
// launch, taking the confinement bound from Pi's own tool rather than a literal
// so the test cannot drift from the implementation.
func piFilter(t *testing.T, homeDir string) *herdrproxy.Filter {
	t.Helper()
	return herdrproxy.NewFilter(herdrproxy.FilterConfig{
		Capabilities:    []herdrproxy.Capability{herdrproxy.CapAgentReporting},
		CurrentPaneID:   "w1F:p1C",
		ExpectedAgent:   "pi",
		AgentSessionDir: (&Pi{}).AgentSessionDir(homeDir),
	})
}

func piRequestLine(t *testing.T, method string, params map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id":     "herdr:pi:session:1753188469123:412765",
		"method": method,
		"params": params,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return raw
}

// TestPi_AgentSessionPathConfinement proves Pi capture is functional now that
// Pi implements ToolWithAgentSessionDir: the real nested path shape Pi writes
// (sessions/<project>/<timestamp>_<uuid>.jsonl) is admitted, and anything
// outside the sessions directory is not.
func TestPi_AgentSessionPathConfinement(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")

	const homeDir = "/home/test"
	sessionsDir := (&Pi{}).AgentSessionDir(homeDir)

	tests := []struct {
		name   string
		path   string
		allow  bool
		wantIn string
	}{
		{
			name:  "real nested pi session file",
			path:  sessionsDir + "/--home-test-Code-proj--/2026-07-20T07-39-33-237Z_019f7e77-43f5-7677-9c82-80a81534a8c3.jsonl",
			allow: true,
		},
		{
			name:  "flat session file",
			path:  sessionsDir + "/01J8Z5K3Q7RN4V2XW9B6TDHFAC.jsonl",
			allow: true,
		},
		{
			name:   "credentials beside the sessions dir",
			path:   filepath.Join(homeDir, ".pi", "agent", "auth.json"),
			wantIn: "outside the launched agent's session directory",
		},
		{
			name:   "sibling directory sharing the prefix",
			path:   sessionsDir + "-evil/x.jsonl",
			wantIn: "outside the launched agent's session directory",
		},
		{
			name:   "the sessions directory itself",
			path:   sessionsDir,
			wantIn: "outside the launched agent's session directory",
		},
		{
			name:   "traversal out of the sessions dir",
			path:   sessionsDir + "/../auth.json",
			wantIn: `contains a ".." component`,
		},
		{
			name:   "relative path",
			path:   "sessions/01J8Z5K3Q7RN4V2XW9B6TDHFAC.jsonl",
			wantIn: "is not absolute",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := piRequestLine(t, "pane.report_agent_session", map[string]any{
				"pane_id":            "w1F:p1C",
				"source":             "herdr:pi",
				"agent":              "pi",
				"seq":                int64(1753188469123456789),
				"agent_session_path": tt.path,
			})

			d := piFilter(t, homeDir).Decide(line)
			if d.Allow != tt.allow {
				t.Fatalf("Decide allow = %v, want %v (reason %q)", d.Allow, tt.allow, d.Reason)
			}
			if tt.allow {
				if d.Rewritten != nil {
					t.Errorf("report was rewritten to %s, want the original bytes forwarded", d.Rewritten)
				}
				return
			}
			if !strings.Contains(d.Reason, tt.wantIn) {
				t.Errorf("reason = %q, want it to contain %q", d.Reason, tt.wantIn)
			}
		})
	}
}

// TestPi_LifecycleFlowThroughQuit walks the sequence the installed Pi v5
// integration sends across a session: the session report on session_start, the
// state reports while it runs, and the release on a "quit" shutdown.
func TestPi_LifecycleFlowThroughQuit(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")

	const homeDir = "/home/test"
	sessionPath := (&Pi{}).AgentSessionDir(homeDir) +
		"/--home-test-Code-proj--/2026-07-20T07-39-33-237Z_019f7e77-43f5-7677-9c82-80a81534a8c3.jsonl"

	f := piFilter(t, homeDir)

	steps := []struct {
		name   string
		method string
		params map[string]any
	}{
		{
			name:   "session_start report",
			method: "pane.report_agent_session",
			params: map[string]any{
				"pane_id": "w1F:p1C", "source": "herdr:pi", "agent": "pi",
				"seq": int64(1), "session_start_source": "new",
				"agent_session_path": sessionPath,
			},
		},
		{
			name:   "agent_start state",
			method: "pane.report_agent",
			params: map[string]any{
				"pane_id": "w1F:p1C", "source": "herdr:pi", "agent": "pi",
				"state": "working", "seq": int64(2), "agent_session_path": sessionPath,
			},
		},
		{
			name:   "agent_end state",
			method: "pane.report_agent",
			params: map[string]any{
				"pane_id": "w1F:p1C", "source": "herdr:pi", "agent": "pi",
				"state": "idle", "seq": int64(3), "agent_session_path": sessionPath,
			},
		},
		{
			name:   "session_shutdown release",
			method: "pane.release_agent",
			params: map[string]any{
				"pane_id": "w1F:p1C", "source": "herdr:pi", "agent": "pi", "seq": int64(4),
			},
		},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			d := f.Decide(piRequestLine(t, step.method, step.params))
			if !d.Allow {
				t.Fatalf("Decide denied %s: %s", step.method, d.Reason)
			}
			if d.Rewritten != nil {
				t.Errorf("%s was rewritten to %s, want the original bytes forwarded", step.method, d.Rewritten)
			}
		})
	}

	// The release is anchored like every other report: another pane cannot end
	// this pane's agent authority.
	t.Run("release for another pane denied", func(t *testing.T) {
		d := f.Decide(piRequestLine(t, "pane.release_agent", map[string]any{
			"pane_id": "w1F:p2D", "source": "herdr:pi", "agent": "pi", "seq": int64(5),
		}))
		if d.Allow {
			t.Fatal("Decide allowed a release naming another pane")
		}
		if !strings.Contains(d.Reason, "is not this sandbox's pane") {
			t.Errorf("reason = %q, want the pane anchor rule", d.Reason)
		}
	})
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

// The sessions binding is Optional, and the builder skips an Optional overlay
// whose host source is missing while ~/.pi/agent itself stays a tmpoverlay - so
// without this every session of a sandbox-only user is discarded at exit, and
// the agent_session_path pi reports to herdr names a file that will not exist
// at restore.
func TestPi_SetupCreatesSessionsDirUnderAnExistingAgentDir(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")

	homeDir := t.TempDir()
	agentDir := filepath.Join(homeDir, ".pi", "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatalf("create pi agent dir: %v", err)
	}

	p := &Pi{}
	sessions := p.AgentSessionDir(homeDir)
	if _, err := os.Stat(sessions); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("precondition: sessions dir must not exist yet, stat err = %v", err)
	}

	if err := p.Setup(homeDir, "/tmp/sandbox"); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	info, err := os.Stat(sessions)
	if err != nil {
		t.Fatalf("Setup did not create %s: %v", sessions, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", sessions)
	}

	found := false
	for _, b := range p.Bindings(homeDir, "/tmp/sandbox") {
		if b.Source == sessions {
			found = true
			if b.Category != CategoryData {
				t.Errorf("sessions binding category = %q, want %q", b.Category, CategoryData)
			}
		}
	}
	if !found {
		t.Errorf("no binding for %q", sessions)
	}
}

// Nothing is created when the agent dir is absent: no tmpoverlay is mounted
// there in that case, so in-sandbox writes already persist.
func TestPi_SetupCreatesNothingWithoutAnAgentDir(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")

	homeDir := t.TempDir()
	p := &Pi{}
	if err := p.Setup(homeDir, "/tmp/sandbox"); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".pi")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Setup created ~/.pi, stat err = %v", err)
	}
}

// PI_CODING_AGENT_DIR must move Setup with the bindings.
func TestPi_SetupHonorsAgentDirOverride(t *testing.T) {
	homeDir := t.TempDir()
	custom := filepath.Join(t.TempDir(), "pi-agent")
	if err := os.MkdirAll(custom, 0o700); err != nil {
		t.Fatalf("create custom agent dir: %v", err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", custom)

	p := &Pi{}
	if err := p.Setup(homeDir, "/tmp/sandbox"); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(custom, "sessions")); err != nil {
		t.Fatalf("Setup did not create sessions under PI_CODING_AGENT_DIR: %v", err)
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".pi")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Setup touched ~/.pi despite the override, stat err = %v", err)
	}
}
