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
