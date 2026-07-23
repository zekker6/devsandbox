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

func TestCodex_Bindings_DefaultPath(t *testing.T) {
	t.Setenv("CODEX_HOME", "")

	c := &Codex{}
	bindings := c.Bindings("/home/test", "/tmp/sandbox")

	var foundDefault, foundSessions bool
	for _, b := range bindings {
		switch b.Source {
		case "/home/test/.codex":
			foundDefault = true
			if b.Category != CategoryConfig {
				t.Errorf("~/.codex binding: expected category %q, got %q", CategoryConfig, b.Category)
			}
		case "/home/test/.codex/sessions":
			foundSessions = true
			if b.Category != CategoryData {
				t.Errorf("~/.codex/sessions binding: expected category %q, got %q", CategoryData, b.Category)
			}
			if !b.Optional {
				t.Error("~/.codex/sessions binding should be optional")
			}
		}
	}

	if !foundDefault {
		t.Error("expected ~/.codex binding when CODEX_HOME is not set")
	}
	if !foundSessions {
		t.Error("expected ~/.codex/sessions binding when CODEX_HOME is not set")
	}
}

func TestCodex_Bindings_CodexHome(t *testing.T) {
	customDir := "/etc/codex-home"
	t.Setenv("CODEX_HOME", customDir)

	c := &Codex{}
	bindings := c.Bindings("/home/test", "/tmp/sandbox")

	var foundCustom, foundCustomSessions, foundDefault bool
	for _, b := range bindings {
		switch b.Source {
		case customDir:
			foundCustom = true
			if b.Category != CategoryConfig {
				t.Errorf("CODEX_HOME binding: expected category %q, got %q", CategoryConfig, b.Category)
			}
			if !b.Optional {
				t.Error("CODEX_HOME binding should be optional")
			}
		case filepath.Join(customDir, "sessions"):
			foundCustomSessions = true
			if b.Category != CategoryData {
				t.Errorf("CODEX_HOME sessions binding: expected category %q, got %q", CategoryData, b.Category)
			}
		case "/home/test/.codex", "/home/test/.codex/sessions":
			foundDefault = true
		}
	}

	if !foundCustom {
		t.Error("expected CODEX_HOME binding when env var is set")
	}
	if !foundCustomSessions {
		t.Error("expected the sessions subdirectory of CODEX_HOME to be bound")
	}
	if foundDefault {
		t.Error("default ~/.codex should NOT be mounted when CODEX_HOME is set")
	}
}

// TestCodex_AgentSessionDirMatchesSessionsBinding pins the same invariant as its
// Pi counterpart: the bound the herdr filter confines path reports to and the
// directory Codex's sessions are actually persisted in are one path. If they
// ever disagree, either every path report is denied or the persisted directory
// is not the one being protected.
func TestCodex_AgentSessionDirMatchesSessionsBinding(t *testing.T) {
	const homeDir = "/home/test"

	tests := []struct {
		name      string
		codexHome string
		want      string
	}{
		{name: "default", codexHome: "", want: filepath.Join(homeDir, ".codex", "sessions")},
		{name: "CODEX_HOME", codexHome: "/etc/codex-home", want: "/etc/codex-home/sessions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CODEX_HOME", tt.codexHome)

			c := &Codex{}
			got := c.AgentSessionDir(homeDir)
			if got != tt.want {
				t.Errorf("AgentSessionDir = %q, want %q", got, tt.want)
			}

			found := false
			for _, b := range c.Bindings(homeDir, "/tmp/sandbox") {
				if b.Source == got {
					found = true
					if b.Category != CategoryData {
						t.Errorf("binding for %q has category %q, want %q", got, b.Category, CategoryData)
					}
				}
			}
			if !found {
				t.Errorf("AgentSessionDir %q is not among Codex's bindings", got)
			}
		})
	}
}

// The sessions binding is Optional, and the builder skips an Optional overlay
// whose host source does not exist - so declaring the persistent overlay is not
// enough on its own. A user who authenticated codex on the host but only ever
// runs it sandboxed has the Codex home (tmpoverlay, writes discarded) without
// sessions/, and every rollout would keep vanishing. Setup is what makes the
// declared persistence actually take effect.
func TestCodex_SetupCreatesSessionsDirUnderAnExistingCodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "")

	homeDir := t.TempDir()
	codexHome := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatalf("create codex home: %v", err)
	}
	// Only auth.json: the state a host-authenticated, sandbox-only user has.
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}

	c := &Codex{}
	sessions := c.AgentSessionDir(homeDir)
	if _, err := os.Stat(sessions); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("precondition: sessions dir must not exist yet, stat err = %v", err)
	}

	if err := c.Setup(homeDir, "/tmp/sandbox"); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	info, err := os.Stat(sessions)
	if err != nil {
		t.Fatalf("Setup did not create %s: %v", sessions, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", sessions)
	}

	// With the source present, the persistent overlay the binding declares is
	// no longer skipped as a missing Optional source.
	found := false
	for _, b := range c.Bindings(homeDir, "/tmp/sandbox") {
		if b.Source == sessions {
			found = true
			if _, err := os.Stat(b.Source); err != nil {
				t.Errorf("binding source %q still missing: %v", b.Source, err)
			}
		}
	}
	if !found {
		t.Errorf("no binding for %q", sessions)
	}

	// Idempotent: a second launch must not fail on an existing directory.
	if err := c.Setup(homeDir, "/tmp/sandbox"); err != nil {
		t.Fatalf("second Setup: %v", err)
	}
}

// Without a Codex home there is no tmpoverlay to escape: writes land in the
// sandbox home and persist there already, so devsandbox must not conjure a
// host directory for a tool the user has never run.
func TestCodex_SetupCreatesNothingWithoutACodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "")

	homeDir := t.TempDir()
	c := &Codex{}
	if err := c.Setup(homeDir, "/tmp/sandbox"); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".codex")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Setup created a Codex home that did not exist, stat err = %v", err)
	}
}

// TestCodex_AgentSessionPathConfinement covers the confinement bound Codex's
// ToolWithAgentSessionDir implementation supplies. The installed integration
// (v6) reports an id rather than a path, so this proves the bound is correct
// should a future version start sending one — auth.json in particular stays out.
func TestCodex_AgentSessionPathConfinement(t *testing.T) {
	t.Setenv("CODEX_HOME", "")

	const homeDir = "/home/test"
	codexDir := filepath.Join(homeDir, ".codex")
	sessionsDir := (&Codex{}).AgentSessionDir(homeDir)

	f := herdrproxy.NewFilter(herdrproxy.FilterConfig{
		Capabilities:    []herdrproxy.Capability{herdrproxy.CapAgentReporting},
		CurrentPaneID:   "w1F:p1C",
		ExpectedAgent:   "codex",
		AgentSessionDir: sessionsDir,
	})

	tests := []struct {
		name   string
		path   string
		allow  bool
		wantIn string
	}{
		{
			name:  "real codex rollout file",
			path:  sessionsDir + "/2026/07/19/rollout-2026-07-19T13-15-56-019f79a9-2680-7ca1-b022-3c8eeda438d8.jsonl",
			allow: true,
		},
		{
			name:   "credentials in the codex home",
			path:   filepath.Join(codexDir, "auth.json"),
			wantIn: "outside the launched agent's session directory",
		},
		{
			name:   "sibling directory sharing the prefix",
			path:   sessionsDir + "-evil/x.jsonl",
			wantIn: "outside the launched agent's session directory",
		},
		{
			name:   "traversal out of the sessions dir",
			path:   sessionsDir + "/../auth.json",
			wantIn: `contains a ".." component`,
		},
		{
			name:   "relative path",
			path:   "sessions/2026/07/19/rollout.jsonl",
			wantIn: "is not absolute",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{
				"id":     "herdr:codex:1753188469123:412765",
				"method": "pane.report_agent_session",
				"params": map[string]any{
					"pane_id":            "w1F:p1C",
					"source":             "herdr:codex",
					"agent":              "codex",
					"seq":                int64(1753188469123456789),
					"agent_session_path": tt.path,
				},
			})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}

			d := f.Decide(raw)
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

// TestCodex_ReportsSessionIDAsInstalledHookSends mirrors the byte shape the
// installed codex v6 hook builds on SessionStart: id only, no path.
func TestCodex_ReportsSessionIDAsInstalledHookSends(t *testing.T) {
	t.Setenv("CODEX_HOME", "")

	f := herdrproxy.NewFilter(herdrproxy.FilterConfig{
		Capabilities:    []herdrproxy.Capability{herdrproxy.CapAgentReporting},
		CurrentPaneID:   "w1F:p1C",
		ExpectedAgent:   "codex",
		AgentSessionDir: (&Codex{}).AgentSessionDir("/home/test"),
	})

	line := []byte(`{"id":"herdr:codex:1753188469123:412765","method":"pane.report_agent_session",` +
		`"params":{"pane_id":"w1F:p1C","source":"herdr:codex","agent":"codex","seq":1753188469123456789,` +
		`"agent_session_id":"019f79a9-2680-7ca1-b022-3c8eeda438d8","session_start_source":"startup"}}`)

	d := f.Decide(line)
	if !d.Allow {
		t.Fatalf("captured codex v6 report denied: %s", d.Reason)
	}
	if d.Rewritten != nil {
		t.Errorf("report was rewritten to %s, want the original bytes forwarded", d.Rewritten)
	}
}

func TestCodex_Environment_NoEnvVar(t *testing.T) {
	t.Setenv("CODEX_HOME", "")

	c := &Codex{}
	envVars := c.Environment("/home/test", "/tmp/sandbox")

	for _, env := range envVars {
		if env.Name == "CODEX_HOME" {
			t.Error("CODEX_HOME should not be exported when host env var is empty")
		}
	}
}

func TestCodex_Environment_WithCodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "/etc/codex-home")

	c := &Codex{}
	envVars := c.Environment("/home/test", "/tmp/sandbox")

	var found bool
	for _, env := range envVars {
		if env.Name == "CODEX_HOME" {
			found = true
			if !env.FromHost {
				t.Error("CODEX_HOME should use FromHost to pass through the host value")
			}
		}
	}

	if !found {
		t.Error("expected CODEX_HOME env var when set on host")
	}
}

func TestCodex_Available_CodexHome(t *testing.T) {
	tmpDir := t.TempDir()
	customDir := filepath.Join(tmpDir, "codex-home")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CODEX_HOME", customDir)
	t.Setenv("PATH", "")

	c := &Codex{}
	emptyHome := filepath.Join(tmpDir, "empty-home")
	if err := os.MkdirAll(emptyHome, 0o755); err != nil {
		t.Fatal(err)
	}

	if !c.Available(emptyHome) {
		t.Error("Codex should be available when CODEX_HOME points to existing directory")
	}
}

func TestCodex_Check_CodexHome(t *testing.T) {
	tmpDir := t.TempDir()
	customDir := filepath.Join(tmpDir, "codex-home")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CODEX_HOME", customDir)

	c := &Codex{}
	emptyHome := filepath.Join(tmpDir, "empty-home")
	if err := os.MkdirAll(emptyHome, 0o755); err != nil {
		t.Fatal(err)
	}

	result := c.Check(emptyHome)

	var foundCustom bool
	for _, p := range result.ConfigPaths {
		if p == customDir {
			foundCustom = true
		}
	}

	if !foundCustom {
		t.Errorf("Check() should include CODEX_HOME in config paths, got: %v", result.ConfigPaths)
	}
}
