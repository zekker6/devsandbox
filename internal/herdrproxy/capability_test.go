package herdrproxy

import (
	"slices"
	"testing"
)

func TestIsLaunch(t *testing.T) {
	if !IsLaunch(CapLaunchOverlay) {
		t.Error("IsLaunch(CapLaunchOverlay) = false, want true")
	}
	if IsLaunch(CapNotify) {
		t.Error("IsLaunch(CapNotify) = true, want false — notify runs no host code")
	}
	if IsLaunch(CapAgentReporting) {
		t.Error("IsLaunch(CapAgentReporting) = true, want false — reporting runs no host code")
	}
	if IsLaunch(Capability("nonsense")) {
		t.Error("IsLaunch(unknown) = true, want false")
	}
}

func TestMethodsForLaunchOverlay(t *testing.T) {
	got := methodsFor(CapLaunchOverlay)
	for _, want := range []string{methodTabCreate, methodPaneSendInput, methodTabClose} {
		if !slices.Contains(got, want) {
			t.Errorf("methodsFor(CapLaunchOverlay) missing %q, got %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("methodsFor(CapLaunchOverlay) = %v, want exactly the three overlay methods", got)
	}
}

// TestMethodsForAgentReporting pins the capability to the three lifecycle
// methods Pi's integration sends. pane.clear_agent_authority stays out: it
// revokes another party's claim on a pane, which reporting one's own identity
// never needs.
func TestMethodsForAgentReporting(t *testing.T) {
	got := methodsFor(CapAgentReporting)
	want := []string{methodPaneReportAgentSession, methodPaneReportAgent, methodPaneReleaseAgent}
	if len(got) != len(want) {
		t.Fatalf("methodsFor(CapAgentReporting) = %v, want exactly %v", got, want)
	}
	for _, m := range want {
		if !slices.Contains(got, m) {
			t.Errorf("methodsFor(CapAgentReporting) missing %q, got %v", m, got)
		}
	}
	if slices.Contains(got, "pane.clear_agent_authority") {
		t.Error("methodsFor(CapAgentReporting) grants pane.clear_agent_authority")
	}
}

func TestMethodsForUnknownCapabilityGrantsNothing(t *testing.T) {
	if got := methodsFor(Capability("pane.read")); got != nil {
		t.Errorf("methodsFor(unknown) = %v, want nil so a typo cannot widen the allowlist", got)
	}
}

// TestNoCapabilityReachesDangerousMethods is the guard that matters most: the
// union of everything every capability can grant must stay inside the small
// in-scope set. If someone adds a capability that reaches pane.read or
// server.stop, this fails.
func TestNoCapabilityReachesDangerousMethods(t *testing.T) {
	inScope := []string{
		methodTabCreate, methodPaneSendInput, methodTabClose,
		methodNotificationShow, methodPaneReportAgentSession,
		methodPaneReportAgent, methodPaneReleaseAgent,
	}

	var all []string
	for _, c := range knownCapabilities() {
		all = append(all, methodsFor(c)...)
	}

	for _, m := range all {
		if !slices.Contains(inScope, m) {
			t.Errorf("capability grants out-of-scope method %q", m)
		}
	}

	forbidden := []string{
		"pane.read", "pane.send_text", "pane.send_keys", "pane.list",
		"agent.send", "agent.start", "server.stop", "server.reload_config",
		"plugin.pane.open", "plugin.link", "worktree.create", "worktree.remove",
		"workspace.close", "events.subscribe", "layout.apply", "session.snapshot",
		"pane.clear_agent_authority",
	}
	for _, m := range forbidden {
		if slices.Contains(all, m) {
			t.Errorf("a capability grants forbidden method %q", m)
		}
	}
}

func TestAllowedMethods(t *testing.T) {
	tests := []struct {
		name string
		caps []Capability
		want []string
		deny []string
	}{
		{
			name: "no capabilities permits nothing",
			caps: nil,
			deny: []string{methodTabCreate, methodNotificationShow, "pane.read"},
		},
		{
			name: "launch overlay only",
			caps: []Capability{CapLaunchOverlay},
			want: []string{methodTabCreate, methodPaneSendInput, methodTabClose},
			deny: []string{methodNotificationShow, "pane.read"},
		},
		{
			name: "notify only",
			caps: []Capability{CapNotify},
			want: []string{methodNotificationShow},
			deny: []string{methodTabCreate, methodPaneSendInput},
		},
		{
			name: "both, deduplicated",
			caps: []Capability{CapLaunchOverlay, CapNotify, CapLaunchOverlay},
			want: []string{methodTabCreate, methodPaneSendInput, methodTabClose, methodNotificationShow},
			deny: []string{
				"pane.read", "server.stop",
				methodPaneReportAgentSession, methodPaneReportAgent, methodPaneReleaseAgent,
			},
		},
		{
			name: "agent reporting only",
			caps: []Capability{CapAgentReporting},
			want: []string{methodPaneReportAgentSession, methodPaneReportAgent, methodPaneReleaseAgent},
			deny: []string{
				"pane.clear_agent_authority", "pane.read", "agent.send",
				methodTabCreate, methodPaneSendInput, methodTabClose, methodNotificationShow,
			},
		},
		{
			name: "agent reporting alongside the others",
			caps: []Capability{CapLaunchOverlay, CapNotify, CapAgentReporting},
			want: []string{
				methodTabCreate, methodPaneSendInput, methodTabClose,
				methodNotificationShow, methodPaneReportAgentSession,
				methodPaneReportAgent, methodPaneReleaseAgent,
			},
			deny: []string{"pane.clear_agent_authority", "pane.read", "server.stop"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := allowedMethods(tt.caps)
			for _, m := range tt.want {
				if _, ok := got[m]; !ok {
					t.Errorf("allowedMethods missing %q", m)
				}
			}
			for _, m := range tt.deny {
				if _, ok := got[m]; ok {
					t.Errorf("allowedMethods unexpectedly permits %q", m)
				}
			}
		})
	}
}

func TestIsKnown(t *testing.T) {
	for _, c := range knownCapabilities() {
		if !IsKnown(c) {
			t.Errorf("IsKnown(%q) = false for a listed capability", c)
		}
	}
	if !IsKnown(CapAgentReporting) {
		t.Error("IsKnown(CapAgentReporting) = false; a tool declaring it would silently do nothing")
	}
	if IsKnown(Capability("launch_window")) {
		t.Error(`IsKnown("launch_window") = true, but that is a kitty capability herdr does not implement`)
	}
	if IsKnown(Capability("agent_report")) {
		t.Error(`IsKnown("agent_report") = true, but the capability is named "agent_reporting"`)
	}
}

// TestPingIsAlwaysAllowed pins the one deliberate exception to deny-by-default.
//
// ping observes and mutates nothing — it returns version, protocol, and feature
// flags, strictly less than a successful connect(2) already reveals. It is
// permitted regardless of declared capabilities so `herdr status` works.
func TestPingIsAlwaysAllowed(t *testing.T) {
	cases := []struct {
		name string
		caps []Capability
	}{
		{name: "no capabilities (enforce mode)", caps: nil},
		{name: "launch overlay only", caps: []Capability{CapLaunchOverlay}},
		{name: "notify only", caps: []Capability{CapNotify}},
		{name: "agent reporting only", caps: []Capability{CapAgentReporting}},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := allowedMethods(tt.caps)[methodPing]; !ok {
				t.Error("ping is not permitted; `herdr status` would fail")
			}
		})
	}
}

// TestPingIsNotOwnedByAnyCapability keeps ping out of the capability tables, so
// it cannot be confused for something a tool declares or that widens on demand.
func TestPingIsNotOwnedByAnyCapability(t *testing.T) {
	for _, c := range knownCapabilities() {
		if slices.Contains(methodsFor(c), methodPing) {
			t.Errorf("capability %q lists ping; it is granted unconditionally instead", c)
		}
	}
}

// TestNoCapabilitiesStillDeniesEverythingObservable is the security half of the
// ping exception: allowing it must not open anything else.
func TestNoCapabilitiesStillDeniesEverythingObservable(t *testing.T) {
	allowed := allowedMethods(nil)

	if len(allowed) != 1 {
		t.Errorf("zero-capability allowlist = %v, want exactly {ping}", allowed)
	}
	for _, m := range []string{
		methodTabCreate, methodPaneSendInput, methodTabClose, methodNotificationShow,
		methodPaneReportAgentSession, methodPaneReportAgent, methodPaneReleaseAgent,
		"pane.clear_agent_authority",
		"pane.read", "agent.send", "server.stop", "pane.list", "worktree.remove",
	} {
		if _, ok := allowed[m]; ok {
			t.Errorf("zero-capability allowlist permits %q", m)
		}
	}
}
