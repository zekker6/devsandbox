package tools

import (
	"context"
	"testing"
)

// mockActiveTool is a test implementation of ActiveTool
type mockActiveTool struct {
	name     string
	started  bool
	stopped  bool
	startErr error
	stopErr  error
}

func (m *mockActiveTool) Name() string                                     { return m.name }
func (m *mockActiveTool) Description() string                              { return "mock active tool" }
func (m *mockActiveTool) Available(homeDir string) bool                    { return true }
func (m *mockActiveTool) Bindings(homeDir, sandboxHome string) []Binding   { return nil }
func (m *mockActiveTool) Environment(homeDir, sandboxHome string) []EnvVar { return nil }
func (m *mockActiveTool) ShellInit(shell string) string                    { return "" }

func (m *mockActiveTool) Start(ctx context.Context, homeDir, sandboxHome string) error {
	if m.startErr != nil {
		return m.startErr
	}
	m.started = true
	return nil
}

func (m *mockActiveTool) Stop() error {
	if m.stopErr != nil {
		return m.stopErr
	}
	m.stopped = true
	return nil
}

func TestActiveTool_Interface(t *testing.T) {
	mock := &mockActiveTool{name: "test"}

	// Verify it implements Tool
	var _ Tool = mock

	// Verify it implements ActiveTool
	var _ ActiveTool = mock

	// Test Start
	ctx := context.Background()
	if err := mock.Start(ctx, "/home/user", "/sandbox/home"); err != nil {
		t.Errorf("Start failed: %v", err)
	}
	if !mock.started {
		t.Error("expected started=true after Start()")
	}

	// Test Stop
	if err := mock.Stop(); err != nil {
		t.Errorf("Stop failed: %v", err)
	}
	if !mock.stopped {
		t.Error("expected stopped=true after Stop()")
	}
}

// configRecordingTool captures the GlobalConfig the runner hands to Configure.
type configRecordingTool struct {
	mockActiveTool
	seen GlobalConfig
}

func (c *configRecordingTool) Configure(globalCfg GlobalConfig, _ map[string]any) {
	c.seen = globalCfg
}

// TestActiveToolsRunner_PropagatesLaunchedAgent pins the path the agent name
// travels: command argv -> ActiveToolsConfig -> GlobalConfig -> the tool. It is
// the only trusted source of agent identity, so a break here would leave the
// herdr filter with no anchor to bind reports to.
func TestActiveToolsRunner_PropagatesLaunchedAgent(t *testing.T) {
	// Keep the real active tools out of it: this test is about plumbing, and
	// whether the machine running it happens to be inside a herdr or kitty
	// session must not change the result.
	t.Setenv("HERDR_ENV", "")
	t.Setenv("KITTY_LISTEN_ON", "")

	tool := &configRecordingTool{mockActiveTool: mockActiveTool{name: "config-recording-tool"}}
	Register(tool)
	defer Unregister(tool.Name())

	start, cleanup := NewActiveToolsRunner(ActiveToolsConfig{
		HomeDir:       t.TempDir(),
		SandboxHome:   shortSocketDir(t),
		ProjectDir:    "/work/proj",
		LaunchedAgent: "claude",
	}, nil)
	defer cleanup()

	if _, err := start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}

	if tool.seen.LaunchedAgent != "claude" {
		t.Errorf("LaunchedAgent = %q, want %q", tool.seen.LaunchedAgent, "claude")
	}
	if tool.seen.ProjectDir != "/work/proj" {
		t.Errorf("ProjectDir = %q, want the configured project dir", tool.seen.ProjectDir)
	}
}
