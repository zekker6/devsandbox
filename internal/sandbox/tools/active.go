package tools

import (
	"context"
	"fmt"
	"io"
)

// ActiveToolsConfig contains configuration for running active tools.
type ActiveToolsConfig struct {
	HomeDir        string
	SandboxHome    string
	OverlayEnabled bool
	ProjectDir     string
	ToolsConfig    map[string]any
}

// ActiveToolsRunner manages the lifecycle of active tools.
type ActiveToolsRunner struct {
	cfg     ActiveToolsConfig
	logger  ErrorLogger
	started []ActiveTool
}

// NewActiveToolsRunner creates a runner for active tools.
// Returns a start function and a cleanup function.
// The start function must be called to actually start the tools.
// It returns true if any tools were started.
// The cleanup function should be called when done (even if start fails).
func NewActiveToolsRunner(cfg ActiveToolsConfig, logger ErrorLogger) (start func(ctx context.Context) (bool, error), cleanup func()) {
	runner := &ActiveToolsRunner{
		cfg:    cfg,
		logger: logger,
	}

	return runner.start, runner.cleanup
}

// start configures and starts all active tools.
// Returns true if any tools were started, false otherwise.
func (r *ActiveToolsRunner) start(ctx context.Context) (bool, error) {
	home := r.cfg.HomeDir
	sandboxHome := r.cfg.SandboxHome

	// Find and start active tools
	for _, tool := range Available(home) {
		at, ok := tool.(ActiveTool)
		if !ok {
			continue
		}

		// Configure tool if it supports configuration
		if configurable, ok := tool.(ToolWithConfig); ok {
			globalCfg := GlobalConfig{
				OverlayEnabled: r.cfg.OverlayEnabled,
				ProjectDir:     r.cfg.ProjectDir,
			}
			var toolCfg map[string]any
			if r.cfg.ToolsConfig != nil {
				if section, ok := r.cfg.ToolsConfig[tool.Name()]; ok {
					toolCfg, _ = section.(map[string]any)
				}
			}
			configurable.Configure(globalCfg, toolCfg)
		}

		// Set logger if tool supports it
		if loggable, ok := tool.(ToolWithLogger); ok && r.logger != nil {
			loggable.SetLogger(r.logger)
		}

		// Start the tool
		if err := at.Start(ctx, home, sandboxHome); err != nil {
			// Stop already started tools on error
			r.stopStarted()
			return false, fmt.Errorf("failed to start %s: %w", tool.Name(), err)
		}
		r.started = append(r.started, at)
	}

	return len(r.started) > 0, nil
}

// cleanup stops all started tools in reverse order.
func (r *ActiveToolsRunner) cleanup() {
	r.stopStarted()

	// Close logger if it implements io.Closer
	if closer, ok := r.logger.(io.Closer); ok {
		_ = closer.Close()
	}
}

// stopStarted stops all started tools in reverse order.
func (r *ActiveToolsRunner) stopStarted() {
	for i := len(r.started) - 1; i >= 0; i-- {
		_ = r.started[i].Stop()
	}
	r.started = nil
}
