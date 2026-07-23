package herdrproxy

import "slices"

// Capability identifies a group of herdr operations a tool may request.
//
// herdr exposes 86 methods, most of which grant broad control over the user's
// workspace: pane.read returns any pane's contents, pane.send_text and
// agent.send type into any pane, worktree.* mutates git state, plugin.* loads
// code, and server.stop kills the session. None of that is reachable here.
// A capability names the smallest method set that makes one concrete workflow
// possible, and every method outside the enabled capabilities is denied.
type Capability string

const (
	// CapLaunchOverlay permits opening a tab, running one validated launch
	// command in the pane that tab created, and closing that tab again. The
	// pane and tab must be ones this sandbox created: ownership is tracked
	// from the tab.create response, never taken from the client.
	CapLaunchOverlay Capability = "launch_overlay"

	// CapNotify permits raising a herdr notification. It carries no ownership
	// requirement because it neither reads nor mutates workspace state.
	CapNotify Capability = "notify"

	// CapAgentReporting permits an in-sandbox agent integration to report the
	// native session identity and lifecycle state of the agent devsandbox itself
	// launched, so herdr can show its status and resume it later.
	//
	// It reports identity and nothing else: no pane contents are readable, no
	// input can be injected into any pane, and no host command runs. Every method
	// it grants is bound to host-derived anchors — the pane herdr created for this
	// process and the agent devsandbox was asked to launch — so a report for
	// another pane, or claiming another agent, is denied.
	//
	// pane.clear_agent_authority stays out deliberately: it revokes another
	// party's claim on a pane, which is not something reporting one's own
	// identity ever needs.
	CapAgentReporting Capability = "agent_reporting"
)

// herdr method names, as verified against v0.7.4 (protocol 16).
const (
	methodTabCreate              = "tab.create"
	methodTabClose               = "tab.close"
	methodPaneSendInput          = "pane.send_input"
	methodNotificationShow       = "notification.show"
	methodPaneReportAgentSession = "pane.report_agent_session"
	methodPaneReportAgent        = "pane.report_agent"
	methodPaneReleaseAgent       = "pane.release_agent"
	methodPing                   = "ping"
)

// IsLaunch reports whether c permits running a command on the host.
//
// Launch capabilities are special: they imply host code execution, so the tool
// layer warns when one is declared without an accompanying command pattern,
// and the filter refuses to allow the launch method without one.
func IsLaunch(c Capability) bool {
	return c == CapLaunchOverlay
}

// methodsFor returns the methods c permits.
func methodsFor(c Capability) []string {
	switch c {
	case CapLaunchOverlay:
		return []string{methodTabCreate, methodPaneSendInput, methodTabClose}
	case CapNotify:
		return []string{methodNotificationShow}
	case CapAgentReporting:
		return []string{methodPaneReportAgentSession, methodPaneReportAgent, methodPaneReleaseAgent}
	}
	return nil
}

// allowedMethods collapses caps into the set of permitted method names.
//
// ping is always included, independent of declared capabilities. It is a pure
// liveness handshake — it takes no parameters, mutates nothing, and returns
// only the server's version, protocol number, and feature flags:
//
//	{"type":"pong","version":"0.7.4","protocol":16,"capabilities":{...}}
//
// That is strictly less than the sandbox already learns from a successful
// connect(2) to this socket, and the client version is visible from the binary
// regardless. Denying it bought no safety while breaking `herdr status`, the
// most natural way to ask whether the integration is working.
//
// Consequence worth knowing: under mode="enforce" the proxy answers ping while
// denying everything else, so "enforce denies every request" is really "denies
// every request that can observe or change anything".
func allowedMethods(caps []Capability) map[string]struct{} {
	out := map[string]struct{}{methodPing: {}}
	for _, c := range caps {
		for _, m := range methodsFor(c) {
			out[m] = struct{}{}
		}
	}
	return out
}

// knownCapabilities lists every capability the proxy understands, so the tool
// layer can reject a configured name that would otherwise silently do nothing.
func knownCapabilities() []Capability {
	return []Capability{CapLaunchOverlay, CapNotify, CapAgentReporting}
}

// IsKnown reports whether c is a capability this proxy implements.
func IsKnown(c Capability) bool {
	return slices.Contains(knownCapabilities(), c)
}
