package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"devsandbox/internal/kittyproxy"
	"devsandbox/internal/notice"
)

func init() {
	Register(&Kitty{})
}

const (
	kittyProxySocketName = ".kitty.sock"
	kittyModeAuto        = "auto"
	kittyModeDisabled    = "disabled"
	kittyModeEnforce     = "enforce"
)

// Kitty exposes a filtering proxy for the host's kitty remote-control socket.
// The host socket is NOT bind-mounted into the sandbox; only the proxy socket is.
// Capabilities are aggregated from any enabled tool implementing
// ToolWithKittyRequirements.
type Kitty struct {
	mode              string
	extraCapabilities []kittyproxy.Capability

	logger ErrorLogger
	proxy  *kittyproxy.Proxy
}

func (k *Kitty) Name() string { return "kitty" }
func (k *Kitty) Description() string {
	return "Kitty terminal remote-control socket proxy (capability-filtered)"
}

// Available reports whether the host has KITTY_LISTEN_ON set and the kitty binary
// on PATH. The proxy still requires that some other tool declare a need for it.
func (k *Kitty) Available(_ string) bool {
	if os.Getenv("KITTY_LISTEN_ON") == "" {
		return false
	}
	_, err := exec.LookPath("kitty")
	return err == nil
}

// kittyHostSocket returns the host UDS path extracted from KITTY_LISTEN_ON.
func kittyHostSocket() string {
	return strings.TrimPrefix(os.Getenv("KITTY_LISTEN_ON"), "unix:")
}

// Configure implements ToolWithConfig.
func (k *Kitty) Configure(_ GlobalConfig, toolCfg map[string]any) {
	k.mode = kittyModeAuto
	k.extraCapabilities = nil
	if toolCfg == nil {
		return
	}
	if v, ok := toolCfg["mode"].(string); ok {
		switch v {
		case kittyModeAuto, kittyModeDisabled, kittyModeEnforce:
			k.mode = v
		}
	}
	if list, ok := toolCfg["extra_capabilities"].([]any); ok {
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				continue
			}
			cap := kittyproxy.Capability(s)
			if kittyproxy.IsLaunch(cap) {
				notice.Warn("kitty: ignoring extra_capabilities entry %q (launch_* may only come from a tool with declared launch patterns)", s)
				continue
			}
			k.extraCapabilities = append(k.extraCapabilities, cap)
		}
	}
}

// SetLogger implements ToolWithLogger.
func (k *Kitty) SetLogger(l ErrorLogger) { k.logger = l }

// Bindings: only the kitty binary (read-only). No host socket — the proxy
// replaces that.
func (k *Kitty) Bindings(_ string, _ string) []Binding {
	if os.Getenv("KITTY_LISTEN_ON") == "" {
		return nil
	}
	var bs []Binding
	if bin, err := exec.LookPath("kitty"); err == nil {
		bs = append(bs, Binding{
			Source: bin, Dest: bin,
			Category: CategoryRuntime,
			ReadOnly: true, Optional: true,
		})
	}
	return bs
}

// Environment exports KITTY_LISTEN_ON (pointing at the proxy socket) only when
// the proxy is actually running. KITTY_WINDOW_ID/KITTY_PID are passed through
// for tools that read them (they're read-only signals about the host pane).
func (k *Kitty) Environment(homeDir, _ string) []EnvVar {
	if k.proxy == nil {
		return nil
	}
	return []EnvVar{
		{Name: "KITTY_LISTEN_ON", Value: "unix:" + filepath.Join(homeDir, kittyProxySocketName)},
		{Name: "KITTY_WINDOW_ID", FromHost: true},
		{Name: "KITTY_PID", FromHost: true},
	}
}

func (k *Kitty) ShellInit(_ string) string { return "" }

// aggregate returns capabilities + launch patterns collected from every
// available tool implementing ToolWithKittyRequirements (and optionally
// ToolWithKittyLaunchPatterns), de-duplicated.
func (k *Kitty) aggregate(homeDir string) ([]kittyproxy.Capability, []kittyproxy.CommandPattern) {
	capSet := make(map[kittyproxy.Capability]struct{})
	for _, c := range k.extraCapabilities {
		capSet[c] = struct{}{}
	}
	var patterns []kittyproxy.CommandPattern
	declaredLaunch := make(map[string]bool)
	declaredPatterns := make(map[string]bool)

	for _, t := range All() {
		if t == k {
			continue
		}
		if !t.Available(homeDir) {
			continue
		}
		req, ok := t.(ToolWithKittyRequirements)
		if !ok {
			continue
		}
		for _, c := range req.KittyCapabilities() {
			capSet[c] = struct{}{}
			if kittyproxy.IsLaunch(c) {
				declaredLaunch[t.Name()] = true
			}
		}
		if pp, ok := t.(ToolWithKittyLaunchPatterns); ok {
			declaredPatterns[t.Name()] = true
			patterns = append(patterns, pp.KittyLaunchPatterns()...)
		}
	}

	for name := range declaredLaunch {
		if !declaredPatterns[name] {
			notice.Warn("kitty: tool %q declared a launch_* capability without launch patterns; any command will be allowed for this tool", name)
		}
	}

	caps := make([]kittyproxy.Capability, 0, len(capSet))
	for c := range capSet {
		caps = append(caps, c)
	}
	return caps, patterns
}

// Start implements ActiveTool.
func (k *Kitty) Start(ctx context.Context, _, sandboxHome string) error {
	if k.mode == kittyModeDisabled {
		return nil
	}
	hostSock := kittyHostSocket()
	if hostSock == "" {
		return nil
	}

	caps, patterns := k.aggregate("")
	if len(caps) == 0 {
		switch k.mode {
		case kittyModeAuto:
			return nil
		case kittyModeEnforce:
			// Continue with empty allowlist — every request will be denied.
		}
	}

	if _, err := os.Stat(hostSock); err != nil {
		return fmt.Errorf("kitty: host socket %s not reachable: %w", hostSock, err)
	}

	owned := kittyproxy.NewOwnedSet()
	filter := kittyproxy.NewFilter(kittyproxy.FilterConfig{
		Capabilities:   caps,
		LaunchPatterns: patterns,
		Owned:          owned,
	})

	listenPath := filepath.Join(sandboxHome, kittyProxySocketName)
	k.proxy = kittyproxy.New(hostSock, listenPath, filter, owned)
	if k.logger != nil {
		k.proxy.SetLogger(k.logger)
	}
	if err := k.proxy.Start(ctx); err != nil {
		k.proxy = nil
		return fmt.Errorf("kitty: start proxy: %w", err)
	}
	notice.Warn("kitty proxy active. Capabilities: %v", caps)
	return nil
}

// Stop implements ActiveTool.
func (k *Kitty) Stop() error {
	if k.proxy == nil {
		return nil
	}
	err := k.proxy.Stop()
	k.proxy = nil
	return err
}

// Check retains rough parity with the previous behavior so `tools check` works.
func (k *Kitty) Check(_ string) CheckResult {
	result := CheckBinary("kitty", "Install kitty terminal: https://sw.kovidgoyal.net/kitty/")
	if !result.Available {
		return result
	}
	listenOn := os.Getenv("KITTY_LISTEN_ON")
	if listenOn == "" {
		result.Available = false
		result.AddIssue("KITTY_LISTEN_ON not set — enable remote control in kitty.conf: allow_remote_control socket-only, listen_on unix:/tmp/kitty-{kitty_pid}")
		return result
	}
	sock := kittyHostSocket()
	if _, err := os.Stat(sock); err != nil {
		result.Available = false
		result.AddIssue("kitty socket not found: " + sock)
		return result
	}
	result.AddInfo("listen socket: " + sock)
	result.AddInfo("mode: " + k.mode)
	return result
}

// Ensure interfaces are implemented.
var (
	_ Tool           = (*Kitty)(nil)
	_ ToolWithConfig = (*Kitty)(nil)
	_ ToolWithCheck  = (*Kitty)(nil)
	_ ActiveTool     = (*Kitty)(nil)
	_ ToolWithLogger = (*Kitty)(nil)
)
