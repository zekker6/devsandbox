package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"devsandbox/internal/cmdpattern"
	"devsandbox/internal/herdrproxy"
	"devsandbox/internal/notice"
)

func init() {
	Register(&Herdr{})
}

const (
	herdrProxySocketName = "herdr.sock"
	herdrModeAuto        = "auto"
	herdrModeDisabled    = "disabled"
	herdrModeEnforce     = "enforce"

	// herdrScriptsRelPath is where validated launch scripts are copied. It sits
	// under the host's cache directory and is deliberately NOT bind-mounted into
	// the sandbox: the whole point of relocation is that the sandbox cannot
	// touch a script after it has been validated.
	herdrScriptsRelPath = ".cache/devsandbox/herdr-scripts"
)

// Herdr exposes a filtering proxy for the host's herdr control socket.
//
// The host socket is NOT bind-mounted into the sandbox; only the proxy socket
// is, and the sandboxed herdr CLI is pointed at it via HERDR_SOCKET_PATH.
// Capabilities are aggregated from any enabled tool implementing
// ToolWithHerdrRequirements.
type Herdr struct {
	mode string

	projectDir string
	homeDir    string

	// launchedAgent is the canonical agent devsandbox was asked to run, derived
	// host-side from the command argv. It is one of the two trust anchors for
	// CapAgentReporting; the other is HERDR_PANE_ID.
	launchedAgent string

	logger    ErrorLogger
	proxy     *herdrproxy.Proxy
	relocator *herdrproxy.Relocator
}

func (h *Herdr) Name() string { return "herdr" }
func (h *Herdr) Description() string {
	return "herdr terminal workspace control socket proxy (capability-filtered)"
}
func (h *Herdr) ShellInit(_ string) string { return "" }

// Available reports whether this process is running inside a herdr session and
// the herdr binary is reachable. The proxy additionally requires that some tool
// declare a capability (or that mode is "enforce").
func (h *Herdr) Available(_ string) bool {
	if os.Getenv("HERDR_ENV") != "1" {
		return false
	}
	_, err := exec.LookPath("herdr")
	return err == nil
}

// Configure implements ToolWithConfig.
//
// Only `mode` is configurable. There is deliberately no `extra_capabilities`
// escape hatch as kitty has: with so few capabilities it would configure
// nothing, and every one of them grants host-visible effects.
func (h *Herdr) Configure(globalCfg GlobalConfig, toolCfg map[string]any) {
	h.mode = herdrModeAuto
	h.projectDir = globalCfg.ProjectDir
	h.homeDir = globalCfg.HomeDir
	h.launchedAgent = globalCfg.LaunchedAgent

	if toolCfg == nil {
		return
	}
	if v, ok := toolCfg["mode"].(string); ok {
		switch v {
		case herdrModeAuto, herdrModeDisabled, herdrModeEnforce:
			h.mode = v
		default:
			notice.Warn("herdr: ignoring unknown mode %q; using %q", v, herdrModeAuto)
		}
	}
}

// SetLogger implements ToolWithLogger.
func (h *Herdr) SetLogger(l ErrorLogger) { h.logger = l }

// herdrHostSocket returns the host control socket path. A host-set
// HERDR_SOCKET_PATH wins, matching the herdr client's own resolution order.
func herdrHostSocket(homeDir string) string {
	if p := os.Getenv("HERDR_SOCKET_PATH"); p != "" {
		return p
	}
	if homeDir == "" {
		homeDir, _ = os.UserHomeDir()
	}
	return filepath.Join(homeDir, ".config", "herdr", "herdr.sock")
}

// herdrScriptsPath returns the host-only directory for relocated scripts,
// namespaced per sandbox session so concurrent sessions cannot see each other's.
func herdrScriptsPath(homeDir, sandboxHome string) string {
	sum := sha256.Sum256([]byte(sandboxHome))
	return filepath.Join(homeDir, herdrScriptsRelPath, hex.EncodeToString(sum[:6]))
}

// Bindings mounts the herdr binary read-only when it lives outside the user's
// home, plus the proxy socket at the path herdr resolves by default. The host
// control socket is deliberately absent: reaching it is what the proxy mediates.
func (h *Herdr) Bindings(homeDir string, sandboxHome string) []Binding {
	if os.Getenv("HERDR_ENV") != "1" {
		return nil
	}
	if homeDir == "" {
		homeDir = h.homeDir
	}

	var bs []Binding

	if bin, err := exec.LookPath("herdr"); err == nil && binaryNeedsBind(bin, homeDir) {
		bs = append(bs, Binding{
			Source: bin, Dest: bin,
			Category: CategoryRuntime,
			ReadOnly: true, Optional: true,
		})
	}

	// Expose the proxy socket a second time, at the default path herdr's client
	// derives on its own.
	//
	// Most subcommands honor HERDR_SOCKET_PATH, but some do not: `herdr session
	// list` connects straight to <config dir>/herdr.sock and reports the session
	// "stopped" when that connect fails — which it always did in the sandbox,
	// since the host socket is never mounted. The probe is connect(2) only, with
	// no protocol traffic, so pointing it at the proxy makes the status correct
	// without any request crossing the filter.
	//
	// This grants no additional reach: it is the same filtered socket the env
	// var already names, reachable under a second path.
	if h.proxy != nil && sandboxHome != "" {
		bs = append(bs, Binding{
			Source: filepath.Join(runDir(sandboxHome), herdrProxySocketName),
			Dest:   filepath.Join(homeDir, ".config", "herdr", herdrProxySocketName),
			// A real bind: a unix socket cannot be reached through an overlay
			// lower layer, and it must stay writable because connect(2) requires
			// write permission on the socket file.
			Type:     MountBind,
			Category: CategoryRuntime,
			ReadOnly: false,
			Optional: true,
		})
	}

	return bs
}

// binaryNeedsBind reports whether a tool binary must be bind-mounted explicitly.
//
// Binaries under the user's home are already reachable through the home and
// per-tool mounts the sandbox sets up — mise installs land in
// ~/.local/share/mise, which the mise tool mounts as an overlay. Adding a second
// mount for the binary itself is not merely redundant: it registers a child
// mount whose parent is mounted later, and the builder panics rather than let
// the parent shadow it. Binaries outside home (/usr/bin, /usr/local/bin) are not
// covered by any of those mounts, so they still need one.
func binaryNeedsBind(binPath, homeDir string) bool {
	if homeDir == "" {
		return true
	}
	bin := filepath.Clean(binPath)
	home := filepath.Clean(homeDir)
	return bin != home && !strings.HasPrefix(bin, home+string(filepath.Separator))
}

// Environment points the sandboxed herdr CLI at the proxy socket, and only when
// the proxy is actually running. The remaining variables are read-only signals
// about the caller's pane that launchers consult (notably HERDR_WORKSPACE_ID,
// which revdiff reads from the environment rather than over the API).
// The exported path must be the one the *sandbox* sees. Start creates the
// socket under the host-side sandbox home, but that directory is mounted at the
// user's home path inside the sandbox, so exporting the host path would point
// the CLI at something that does not exist there.
func (h *Herdr) Environment(homeDir, _ string) []EnvVar {
	if h.proxy == nil {
		return nil
	}
	return []EnvVar{
		{Name: "HERDR_SOCKET_PATH", Value: filepath.Join(runDir(homeDir), herdrProxySocketName)},
		{Name: "HERDR_ENV", FromHost: true},
		{Name: "HERDR_SESSION", FromHost: true},
		{Name: "HERDR_WORKSPACE_ID", FromHost: true},
		{Name: "HERDR_TAB_ID", FromHost: true},
		{Name: "HERDR_PANE_ID", FromHost: true},
	}
}

// aggregate collects capabilities and the launch script pattern from every
// available tool that declares them.
//
// A tool that declares a launch capability without a script pattern gets that
// capability dropped. kitty warns and continues in that situation; herdr does
// not, because an unconstrained herdr launch is arbitrary host execution.
func (h *Herdr) aggregate(homeDir string) ([]herdrproxy.Capability, cmdpattern.ScriptPattern) {
	capSet := make(map[herdrproxy.Capability]struct{})
	var script cmdpattern.ScriptPattern

	for _, t := range All() {
		if t == Tool(h) {
			continue
		}
		if !t.Available(homeDir) {
			continue
		}
		req, ok := t.(ToolWithHerdrRequirements)
		if !ok {
			continue
		}

		declared := req.HerdrCapabilities()
		sp, hasScript := t.(ToolWithHerdrLaunchScript)
		if hasScript {
			script = sp.HerdrLaunchScript()
		}

		for _, c := range declared {
			if !herdrproxy.IsKnown(c) {
				notice.Warn("herdr: tool %q declared unknown capability %q; ignoring", t.Name(), c)
				continue
			}
			if herdrproxy.IsLaunch(c) && !hasScript {
				notice.Warn(
					"herdr: tool %q declared %q without a launch script pattern; "+
						"denying it rather than allowing unrestricted host execution", t.Name(), c)
				continue
			}
			capSet[c] = struct{}{}
		}
	}

	caps := make([]herdrproxy.Capability, 0, len(capSet))
	for c := range capSet {
		caps = append(caps, c)
	}
	return caps, script
}

// agentReporting reports whether CapAgentReporting may be enabled, and when it
// may not, why.
//
// Three host-derived facts are all mandatory: the pane herdr created for this
// process, the agent devsandbox was asked to launch, and that agent recording a
// native session devsandbox can capture. None can be influenced from inside the
// sandbox, which is what makes the capability's validator meaningful.
//
// The session-recording check is what keeps a wrapper-only agent (opencode,
// copilot - in agentid for the wrappers, but with no ToolWithAgentSessionDir
// because herdr cannot replay them) from turning a plain `devsandbox <agent>`
// launch in a herdr pane into a filtered-proxy start for a capability that could
// capture nothing.
func (h *Herdr) agentReporting() (bool, string) {
	paneID := os.Getenv("HERDR_PANE_ID")
	switch {
	case paneID == "" && h.launchedAgent == "":
		return false, "HERDR_PANE_ID is unset and no known agent was launched"
	case paneID == "":
		return false, "HERDR_PANE_ID is unset"
	case h.launchedAgent == "":
		return false, "no known agent was launched; only a direct `devsandbox <agent>` launch enables it"
	}
	if _, ok := Get(h.launchedAgent).(ToolWithAgentSessionDir); !ok {
		return false, h.launchedAgent + " does not record a native session devsandbox can capture"
	}
	return true, ""
}

// capabilities is aggregate plus the capabilities herdr itself contributes.
// CapAgentReporting is one of those: it is granted on host-derived facts about
// this launch rather than declared by a tool.
func (h *Herdr) capabilities(homeDir string) ([]herdrproxy.Capability, cmdpattern.ScriptPattern) {
	caps, script := h.aggregate(homeDir)
	if ok, _ := h.agentReporting(); ok {
		caps = append(caps, herdrproxy.CapAgentReporting)
	}
	return caps, script
}

// agentSessionDir returns the directory bounding agent_session_path for the
// launched agent, as the sandbox sees it.
//
// It is empty when no known agent was launched or when that agent's tool does
// not record native sessions; the filter denies every path in that case rather
// than falling back to an unbounded check.
func (h *Herdr) agentSessionDir(homeDir string) string {
	if h.launchedAgent == "" || homeDir == "" {
		return ""
	}
	if sd, ok := Get(h.launchedAgent).(ToolWithAgentSessionDir); ok {
		return sd.AgentSessionDir(homeDir)
	}
	return ""
}

// filterConfig assembles the proxy filter's configuration.
//
// It is the single place the host-derived trust anchors are read, so what
// "this pane" and "this agent" mean cannot drift between the capability
// decision and the validator that enforces it.
func (h *Herdr) filterConfig(
	caps []herdrproxy.Capability,
	script cmdpattern.ScriptPattern,
	relocator *herdrproxy.Relocator,
	tabs, panes *cmdpattern.OwnedSet[string],
	homeDir string,
) herdrproxy.FilterConfig {
	return herdrproxy.FilterConfig{
		Capabilities: caps,
		LaunchScript: script,
		OwnedTabs:    tabs,
		OwnedPanes:   panes,
		Relocator:    relocator,
		ProjectDir:   h.projectDir,
		WorkspaceID:  os.Getenv("HERDR_WORKSPACE_ID"),
		// Empty values deny every report, which is the correct outcome when
		// the agent-reporting capability is off.
		CurrentPaneID: os.Getenv("HERDR_PANE_ID"),
		ExpectedAgent: h.launchedAgent,
		// homeDir must be the resolved one: an empty value here would yield
		// "/.claude/projects" and deny every real report, so agentSessionDir
		// returns "" instead — which denies every path, not every report.
		AgentSessionDir: h.agentSessionDir(homeDir),
	}
}

// Start implements ActiveTool.
func (h *Herdr) Start(ctx context.Context, homeDir, sandboxHome string) error {
	if h.mode == herdrModeDisabled {
		return nil
	}
	if os.Getenv("HERDR_ENV") != "1" {
		return nil
	}
	if homeDir == "" {
		homeDir = h.homeDir
	}

	caps, script := h.capabilities("")
	if len(caps) == 0 && h.mode == herdrModeAuto {
		// Nothing needs herdr; do not open a socket at all.
		return nil
	}

	hostSock := herdrHostSocket(homeDir)
	if _, err := os.Stat(hostSock); err != nil {
		// In auto mode the proxy is an enhancement the user never asked for by
		// name, and any `devsandbox <agent>` launch inside a herdr pane now
		// reaches this check. A stale socket left behind by a dead server must
		// not turn that into a failed launch.
		if h.mode == herdrModeAuto {
			notice.Warn("herdr: host socket %s not reachable (%v); continuing without the proxy", hostSock, err)
			return nil
		}
		return fmt.Errorf("herdr: host socket %s not reachable: %w", hostSock, err)
	}

	// Relocated scripts must land outside everything the sandbox can write.
	// Passing the sandbox-visible paths makes that a checked invariant rather
	// than an assumption.
	scriptsDir := herdrScriptsPath(homeDir, sandboxHome)
	relocator, err := herdrproxy.NewRelocator(scriptsDir, sandboxVisiblePaths(homeDir, sandboxHome))
	if err != nil {
		return fmt.Errorf("herdr: %w", err)
	}

	ownedTabs := cmdpattern.NewOwnedSet[string]()
	ownedPanes := cmdpattern.NewOwnedSet[string]()
	// homeDir here is the value settled by the fallback above, not the empty
	// string capabilities() is called with — the session-directory bound is
	// derived from it.
	filter := herdrproxy.NewFilter(h.filterConfig(caps, script, relocator, ownedTabs, ownedPanes, homeDir))

	if _, err := ensureRunDir(sandboxHome); err != nil {
		_ = relocator.Cleanup()
		return fmt.Errorf("herdr: %w", err)
	}
	listenPath := filepath.Join(runDir(sandboxHome), herdrProxySocketName)
	if err := checkSocketPath(listenPath); err != nil {
		_ = relocator.Cleanup()
		return fmt.Errorf("herdr: %w", err)
	}

	proxy := herdrproxy.New(hostSock, listenPath, filter, ownedTabs, ownedPanes)
	if h.logger != nil {
		proxy.SetLogger(h.logger)
	}
	if err := proxy.Start(ctx); err != nil {
		_ = relocator.Cleanup()
		return fmt.Errorf("herdr: start proxy: %w", err)
	}

	h.proxy = proxy
	h.relocator = relocator
	notice.Warn("herdr proxy active. Capabilities: %v", caps)
	return nil
}

// Stop implements ActiveTool. The relocator is owned here, not by the proxy,
// so there is exactly one place that cleans up relocated scripts.
func (h *Herdr) Stop() error {
	var firstErr error
	if h.proxy != nil {
		if err := h.proxy.Stop(); err != nil {
			firstErr = err
		}
		h.proxy = nil
	}
	if h.relocator != nil {
		if err := h.relocator.Cleanup(); err != nil && firstErr == nil {
			firstErr = err
		}
		h.relocator = nil
	}
	return firstErr
}

// sandboxVisiblePaths lists directories the sandbox can write that also exist
// on the host at the same path. Relocated scripts must never land in one.
func sandboxVisiblePaths(homeDir, sandboxHome string) []string {
	var paths []string
	if sandboxHome != "" {
		paths = append(paths, sandboxHome)
	}
	if homeDir != "" {
		// The revdiff IPC directory is a write-through bind shared with the
		// host at an identical path.
		paths = append(paths, filepath.Join(homeDir, revdiffIpcRelPath))
	}
	return paths
}

// Check implements ToolWithCheck.
func (h *Herdr) Check(homeDir string) CheckResult {
	result := CheckBinary("herdr", "Install herdr: https://herdr.dev")
	if !result.Available {
		return result
	}

	if os.Getenv("HERDR_ENV") != "1" {
		result.Available = false
		result.AddIssue("HERDR_ENV is not 1 — not running inside a herdr session")
		return result
	}

	mode := h.mode
	if mode == "" {
		mode = herdrModeAuto
	}
	result.AddInfo("mode: " + mode)

	if mode == herdrModeDisabled {
		result.Available = false
		result.AddIssue("herdr forwarding is disabled; set [tools.herdr] mode = \"auto\" to enable it")
		return result
	}

	sock := herdrHostSocket(homeDir)
	if _, err := os.Stat(sock); err != nil {
		result.Available = false
		result.AddIssue("herdr control socket not found: " + sock + " (is the herdr server running?)")
		return result
	}
	result.AddInfo("control socket: " + sock)

	caps, _ := h.capabilities(homeDir)
	if reporting, why := h.agentReporting(); reporting {
		result.AddInfo("agent session reporting: active for " + h.launchedAgent)
	} else {
		result.AddInfo("agent session reporting: inactive — " + why)
		result.AddInfo("agent session reporting needs a herdr pane and a direct `devsandbox <agent>` launch; " +
			"typing the agent name inside a `devsandbox` shell does not enable it")
	}
	if len(caps) == 0 {
		if mode == herdrModeEnforce {
			result.AddInfo("capabilities: none — proxy runs and denies every request")
		} else {
			result.Available = false
			result.AddIssue("no enabled tool declares a herdr capability; the proxy will not start")
			return result
		}
	} else {
		for _, c := range caps {
			result.AddInfo("capability: " + string(c))
		}
	}

	if ws := os.Getenv("HERDR_WORKSPACE_ID"); ws != "" {
		result.AddInfo("workspace: " + ws)
	}
	return result
}

// Ensure interfaces are implemented.
var (
	_ Tool           = (*Herdr)(nil)
	_ ToolWithConfig = (*Herdr)(nil)
	_ ToolWithCheck  = (*Herdr)(nil)
	_ ActiveTool     = (*Herdr)(nil)
	_ ToolWithLogger = (*Herdr)(nil)
)
