package isolator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"devsandbox/internal/bwrap"
	"devsandbox/internal/cgroups"
	"devsandbox/internal/config"
	"devsandbox/internal/egress"
	"devsandbox/internal/network"
	"devsandbox/internal/sandbox"
)

func TestBwrapIsolator_Name(t *testing.T) {
	iso := NewBwrapIsolator(BwrapConfig{})
	if iso.Name() != BackendBwrap {
		t.Errorf("Name() = %s, want %s", iso.Name(), BackendBwrap)
	}
}

func TestBwrapIsolator_Available(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only test")
	}
	iso := NewBwrapIsolator(BwrapConfig{})
	err := iso.Available()
	// With embedded binaries, Available() should succeed if extraction works.
	// Just verify it doesn't panic and returns a sensible result.
	t.Logf("Available() error: %v", err)
}

func TestBwrapIsolator_IsolationType(t *testing.T) {
	iso := NewBwrapIsolator(BwrapConfig{})
	if iso.IsolationType() != "bwrap" {
		t.Errorf("IsolationType() = %s, want bwrap", iso.IsolationType())
	}
}

func TestBwrapIsolator_PrepareNetwork(t *testing.T) {
	iso := NewBwrapIsolator(BwrapConfig{})
	info, err := iso.PrepareNetwork(context.Background(), "/tmp/test")
	if err != nil {
		t.Fatalf("PrepareNetwork() error: %v", err)
	}
	if info != nil {
		t.Error("PrepareNetwork() should return nil for bwrap")
	}
}

func TestBwrapIsolator_Cleanup(t *testing.T) {
	iso := NewBwrapIsolator(BwrapConfig{})
	err := iso.Cleanup()
	if err != nil {
		t.Errorf("Cleanup() error: %v", err)
	}
}

func TestBwrapIsolator_ImplementsInterface(t *testing.T) {
	var _ Isolator = (*BwrapIsolator)(nil)
}

// The configured limits reach the sandbox only as an argument to one of the
// three bwrap entry points. Nothing else in the chain observes them: Preflight
// reads b.config.Limits separately, internal/bwrap's tests are handed limits
// directly, and the option-layer tests stop at BwrapConfig. That leaves the hop
// from the isolator's own field to the launch as the one link where the whole
// feature can be neutered - preflight passing, the user told nothing, the
// sandbox running unlimited - so it is asserted for every dispatch branch.
func TestBwrapLaunchCarriesTheConfiguredLimits(t *testing.T) {
	want := cgroups.Limits{Memory: "4g", CPUs: "2", PIDs: 64}

	tests := []struct {
		name       string
		cfg        *RunConfig
		wantLaunch string
	}{
		{
			name:       "proxy mode launches under pasta",
			cfg:        &RunConfig{SandboxCfg: &sandbox.Config{ProxyEnabled: true}},
			wantLaunch: "startWithPasta",
		},
		{
			name:       "active tools keep the parent alive",
			cfg:        &RunConfig{SandboxCfg: &sandbox.Config{}, HasActiveTools: true},
			wantLaunch: "execRun",
		},
		{
			name:       "remove on exit keeps the parent alive",
			cfg:        &RunConfig{SandboxCfg: &sandbox.Config{}, RemoveOnExit: true},
			wantLaunch: "execRun",
		},
		{
			name:       "concurrent sessions keep the parent alive",
			cfg:        &RunConfig{SandboxCfg: &sandbox.Config{IsConcurrent: true}},
			wantLaunch: "execRun",
		},
		{
			name:       "plain run replaces the process",
			cfg:        &RunConfig{SandboxCfg: &sandbox.Config{}},
			wantLaunch: "exec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubEgressPreflight(t)
			var gotLaunch string
			var got cgroups.Limits
			sentinel := errors.New("stub launcher")

			prev := launchers
			launchers = bwrapLaunchers{
				startWithPasta: func(l cgroups.Limits, _, _, _ []string, _ egress.Lockdown, _ egress.Tools) (*bwrap.SandboxProcess, error) {
					gotLaunch, got = "startWithPasta", l
					return nil, sentinel
				},
				execRun: func(l cgroups.Limits, _, _ []string) error {
					gotLaunch, got = "execRun", l
					return sentinel
				},
				exec: func(l cgroups.Limits, _, _ []string) error {
					gotLaunch, got = "exec", l
					return sentinel
				},
			}
			t.Cleanup(func() { launchers = prev })

			iso := NewBwrapIsolator(BwrapConfig{Limits: want})
			if err := iso.launch(tt.cfg, nil, nil, nil); !errors.Is(err, sentinel) {
				t.Fatalf("launch() error = %v, want the stub launcher's error", err)
			}
			if gotLaunch != tt.wantLaunch {
				t.Errorf("launch() used %q, want %q", gotLaunch, tt.wantLaunch)
			}
			if got != want {
				t.Errorf("%s received limits %+v, want the configured %+v", gotLaunch, got, want)
			}
		})
	}
}

// The opt-in guarantee at this layer: an isolator with no limits configured
// must hand the launch a zero value, not a partially filled one.
func TestBwrapLaunchUnlimitedPassesZeroLimits(t *testing.T) {
	var got cgroups.Limits
	sentinel := errors.New("stub launcher")

	prev := launchers
	launchers = bwrapLaunchers{exec: func(l cgroups.Limits, _, _ []string) error {
		got = l
		return sentinel
	}}
	t.Cleanup(func() { launchers = prev })

	iso := NewBwrapIsolator(BwrapConfig{})
	if err := iso.launch(&RunConfig{SandboxCfg: &sandbox.Config{}}, nil, nil, nil); !errors.Is(err, sentinel) {
		t.Fatalf("launch() error = %v, want the stub launcher's error", err)
	}
	if !got.IsZero() {
		t.Errorf("launch() passed %+v, want zero limits", got)
	}
}

// startedSandboxProcess wraps a real, already-started process that exits with
// code. A genuine *exec.Cmd is what makes Wait return a real *exec.ExitError, so
// the exit-status mapping below is exercised rather than simulated.
func startedSandboxProcess(t *testing.T, code, namespacePID int) *bwrap.SandboxProcess {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("exit %d", code))
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	return &bwrap.SandboxProcess{Cmd: cmd, NamespacePID: namespacePID}
}

// stubEgressPreflight makes the launch preflight succeed without touching the
// host. Every proxy-mode launch runs it, so a test that did not stub it would
// pass or fail on whether the machine running it has nftables and a loadable
// nf_conntrack - which is exactly the host property the preflight exists to
// refuse.
func stubEgressPreflight(t *testing.T) {
	t.Helper()
	prevResolve, prevProbe := resolveEgressTools, probeEgressLockdown
	resolveEgressTools = func() (egress.Tools, error) {
		return egress.Tools{IP: "/usr/bin/ip", Firewall: "/usr/sbin/nft", Backend: egress.BackendNft}, nil
	}
	probeEgressLockdown = func(egress.Tools, egress.Lockdown) error { return nil }
	t.Cleanup(func() { resolveEgressTools, probeEgressLockdown = prevResolve, prevProbe })
}

// Everything the proxy branch does after the launcher returns. The limits tests
// above stop at the launcher's error return, so without this the two remaining
// statements are executed but unasserted: dropping the OnSandboxStart call
// silently disables port forwarding and netns wiring, and dropping the
// asCommandExit wrapper turns a workload's own non-zero exit into a devsandbox
// error instead of a propagated exit code.
func TestBwrapLaunchProxyModeReportsStartAndExitStatus(t *testing.T) {
	const nsPID = 4242

	stubEgressPreflight(t)
	proc := startedSandboxProcess(t, 7, nsPID)
	prev := launchers
	launchers = bwrapLaunchers{
		startWithPasta: func(cgroups.Limits, []string, []string, []string, egress.Lockdown, egress.Tools) (*bwrap.SandboxProcess, error) {
			return proc, nil
		},
	}
	t.Cleanup(func() { launchers = prev })

	var gotPID int
	var gotPath string
	cfg := &RunConfig{
		SandboxCfg:     &sandbox.Config{ProxyEnabled: true},
		OnSandboxStart: func(pid int, nsPath string) { gotPID, gotPath = pid, nsPath },
	}

	err := NewBwrapIsolator(BwrapConfig{}).launch(cfg, nil, nil, nil)

	if gotPID != nsPID {
		t.Errorf("OnSandboxStart received PID %d, want %d (a callback that never fires leaves the sandbox unwired)", gotPID, nsPID)
	}
	if want := proc.NamespacePath(); gotPath != want {
		t.Errorf("OnSandboxStart received namespace path %q, want %q", gotPath, want)
	}

	var exitErr *CommandExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("launch() error = %v (%T), want a *CommandExitError so the exit code propagates silently", err, err)
	}
	if exitErr.Code != 7 {
		t.Errorf("launch() exit code = %d, want 7", exitErr.Code)
	}
}

// OnSandboxStart is documented as optional, so the nil case has to reach the
// same line without panicking.
func TestBwrapLaunchProxyModeWithoutCallback(t *testing.T) {
	stubEgressPreflight(t)
	prev := launchers
	launchers = bwrapLaunchers{
		startWithPasta: func(cgroups.Limits, []string, []string, []string, egress.Lockdown, egress.Tools) (*bwrap.SandboxProcess, error) {
			return startedSandboxProcess(t, 0, 4242), nil
		},
	}
	t.Cleanup(func() { launchers = prev })

	cfg := &RunConfig{SandboxCfg: &sandbox.Config{ProxyEnabled: true}}
	if err := NewBwrapIsolator(BwrapConfig{}).launch(cfg, nil, nil, nil); err != nil {
		t.Fatalf("launch() error = %v, want nil for a workload that exited 0", err)
	}
}

// The lockdown reaches the sandbox only as this one argument. A launch that
// carried an empty gateway or a zero port would render an unusable rule set (or,
// before Task 3's validation, none at all) while the sandbox came up believing
// it was contained, so what the launcher is handed is asserted directly.
func TestBwrapLaunchProxyModePassesTheLockdown(t *testing.T) {
	stubEgressPreflight(t)
	enabled := true
	cfg := &RunConfig{
		SandboxCfg: &sandbox.Config{ProxyEnabled: true, ProxyPort: 8123, GatewayIP: network.PastaGatewayIP},
		AppCfg: &config.Config{PortForwarding: config.PortForwardingConfig{
			Enabled: &enabled,
			Rules: []config.PortForwardingRule{
				{Direction: "outbound", Protocol: "tcp", HostPort: 5432},
				{Direction: "outbound", Protocol: "udp", HostPort: 5353},
				// Inbound arrives at the sandbox; the OUTPUT hook never sees it and
				// its replies match established/related, so it needs no accept.
				{Direction: "inbound", Protocol: "tcp", HostPort: 3000, SandboxPort: 3000},
			},
		}},
	}

	var got egress.Lockdown
	sentinel := errors.New("stub launcher")
	prev := launchers
	launchers = bwrapLaunchers{
		startWithPasta: func(_ cgroups.Limits, _, _, _ []string, l egress.Lockdown, _ egress.Tools) (*bwrap.SandboxProcess, error) {
			got = l
			return nil, sentinel
		},
	}
	t.Cleanup(func() { launchers = prev })

	if err := NewBwrapIsolator(BwrapConfig{}).launch(cfg, nil, nil, nil); !errors.Is(err, sentinel) {
		t.Fatalf("launch() error = %v, want the stub launcher's error", err)
	}

	if !got.Enabled {
		t.Error("proxy launch passed a disabled lockdown, so the sandbox would start with egress open")
	}
	if got.Gateway != network.PastaGatewayIP {
		t.Errorf("lockdown gateway = %q, want %q", got.Gateway, network.PastaGatewayIP)
	}
	if got.ProxyPort != 8123 {
		t.Errorf("lockdown proxy port = %d, want the configured 8123", got.ProxyPort)
	}
	want := []egress.Forward{{Port: 5432}, {Port: 5353, UDP: true}}
	if !slices.Equal(got.Forwards, want) {
		t.Errorf("lockdown forwards = %v, want only the outbound rules %v", got.Forwards, want)
	}
}

// The binaries the preflight proved usable must be the exact ones the wrapper
// script renders. Letting the launcher resolve its own would mean the probe
// verified one set and the sandbox ran another, which is a security control
// verified in name only.
func TestBwrapLaunchRendersThePreflightedTools(t *testing.T) {
	stubEgressPreflight(t)
	wantTools, err := resolveEgressTools()
	if err != nil {
		t.Fatalf("stubbed resolve failed: %v", err)
	}

	var got egress.Tools
	sentinel := errors.New("stub launcher")
	prev := launchers
	launchers = bwrapLaunchers{
		startWithPasta: func(_ cgroups.Limits, _, _, _ []string, _ egress.Lockdown, tools egress.Tools) (*bwrap.SandboxProcess, error) {
			got = tools
			return nil, sentinel
		},
	}
	t.Cleanup(func() { launchers = prev })

	cfg := &RunConfig{SandboxCfg: &sandbox.Config{ProxyEnabled: true, ProxyPort: 8123, GatewayIP: network.PastaGatewayIP}}
	if err := NewBwrapIsolator(BwrapConfig{}).launch(cfg, nil, nil, nil); !errors.Is(err, sentinel) {
		t.Fatalf("launch() error = %v, want the stub launcher's error", err)
	}
	if got != wantTools {
		t.Errorf("launcher received tools %+v, want the preflight's %+v", got, wantTools)
	}
}

// A non-proxy launch must never be handed lockdown tools: it renders no
// lockdown, and a populated Tools would mean the default path had acquired a
// dependency on the host having netfilter.
func TestBwrapNonProxyLaunchGetsNoTools(t *testing.T) {
	tools, err := preflightEgressLockdown(egress.Lockdown{})
	if err != nil {
		t.Fatalf("preflightEgressLockdown() error = %v, want a non-proxy launch to skip the preflight", err)
	}
	if tools != (egress.Tools{}) {
		t.Errorf("preflightEgressLockdown() tools = %+v for a non-proxy launch, want the zero value", tools)
	}
}

// A non-proxy launch must not describe a lockdown at all: the pasta invocation
// (and with it the IPv4 restriction) has to stay exactly as it was.
func TestBwrapLockdownIsProxyOnly(t *testing.T) {
	cfg := &RunConfig{SandboxCfg: &sandbox.Config{ProxyPort: 8123, GatewayIP: network.PastaGatewayIP}}
	if got := egressLockdown(cfg); got.Enabled || got.Gateway != "" || got.ProxyPort != 0 || got.Forwards != nil {
		t.Errorf("egressLockdown() = %+v for a non-proxy launch, want the zero value", got)
	}
}

// The wrapper script's abort code arrives through Wait, indistinguishable from a
// workload exit status. Reporting it as one would tell the user their command
// exited 78 when in fact the egress lockdown never applied and the command never
// ran - the single most misleading outcome this change could produce.
func TestBwrapLaunchReportsTheLockdownAbort(t *testing.T) {
	stubEgressPreflight(t)
	proc := startedSandboxProcess(t, egress.LockdownExitCode, 4242)
	prev := launchers
	launchers = bwrapLaunchers{
		startWithPasta: func(cgroups.Limits, []string, []string, []string, egress.Lockdown, egress.Tools) (*bwrap.SandboxProcess, error) {
			return proc, nil
		},
	}
	t.Cleanup(func() { launchers = prev })

	cfg := &RunConfig{SandboxCfg: &sandbox.Config{ProxyEnabled: true, ProxyPort: 8123, GatewayIP: network.PastaGatewayIP}}
	err := NewBwrapIsolator(BwrapConfig{}).launch(cfg, nil, nil, nil)

	if !errors.Is(err, ErrEgressLockdown) {
		t.Fatalf("launch() error = %v, want it to wrap ErrEgressLockdown", err)
	}
	var exitErr *CommandExitError
	if errors.As(err, &exitErr) {
		t.Errorf("launch() error = %v, want it NOT to be a silent CommandExitError - the workload never ran", err)
	}
}

// The mirror image, and the reason the marker file exists: a workload that
// chooses 78 itself has already run, so its status must propagate silently like
// any other. Claiming the lockdown here would print a security error for a
// normal command exit and replace the command's status with 1.
func TestBwrapLaunchKeepsAWorkloadExit78(t *testing.T) {
	stubEgressPreflight(t)
	proc := startedSandboxProcess(t, egress.LockdownExitCode, 4242)
	prev := launchers
	launchers = bwrapLaunchers{
		startWithPasta: func(_ cgroups.Limits, _, _, _ []string, l egress.Lockdown, _ egress.Tools) (*bwrap.SandboxProcess, error) {
			// Stand in for the prologue: the marker is written once every rule
			// has applied, immediately before the workload is exec'd.
			if l.ReadyFile == "" {
				t.Fatal("launch() rendered a lockdown with no marker path; the 78 mapping would be a guess again")
			}
			if err := os.WriteFile(l.ReadyFile, nil, 0o600); err != nil {
				t.Fatalf("write marker: %v", err)
			}
			return proc, nil
		},
	}
	t.Cleanup(func() { launchers = prev })

	cfg := &RunConfig{SandboxCfg: &sandbox.Config{ProxyEnabled: true, ProxyPort: 8123, GatewayIP: network.PastaGatewayIP}}
	err := NewBwrapIsolator(BwrapConfig{}).launch(cfg, nil, nil, nil)

	if errors.Is(err, ErrEgressLockdown) {
		t.Fatalf("launch() error = %v, want no lockdown claim once the lockdown applied", err)
	}
	var exitErr *CommandExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("launch() error = %v (%T), want a *CommandExitError carrying the workload's status", err, err)
	}
	if exitErr.Code != egress.LockdownExitCode {
		t.Errorf("launch() exit code = %d, want the workload's own %d", exitErr.Code, egress.LockdownExitCode)
	}
}

// Every other status stays a workload status. 78 is the only code the mapping
// claims, so a workload that happens to exit 77 or 79 is untouched.
func TestBwrapLaunchNeighbouringExitCodesStayWorkloadStatuses(t *testing.T) {
	for _, code := range []int{egress.LockdownExitCode - 1, egress.LockdownExitCode + 1} {
		t.Run(fmt.Sprintf("exit %d", code), func(t *testing.T) {
			stubEgressPreflight(t)
			proc := startedSandboxProcess(t, code, 4242)
			prev := launchers
			launchers = bwrapLaunchers{
				startWithPasta: func(cgroups.Limits, []string, []string, []string, egress.Lockdown, egress.Tools) (*bwrap.SandboxProcess, error) {
					return proc, nil
				},
			}
			t.Cleanup(func() { launchers = prev })

			cfg := &RunConfig{SandboxCfg: &sandbox.Config{ProxyEnabled: true, ProxyPort: 8123, GatewayIP: network.PastaGatewayIP}}
			err := NewBwrapIsolator(BwrapConfig{}).launch(cfg, nil, nil, nil)

			var exitErr *CommandExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("launch() error = %v (%T), want a *CommandExitError", err, err)
			}
			if exitErr.Code != code {
				t.Errorf("launch() exit code = %d, want %d", exitErr.Code, code)
			}
			if errors.Is(err, ErrEgressLockdown) {
				t.Errorf("launch() error = %v, want no lockdown claim for exit %d", err, code)
			}
		})
	}
}

// A host with no nft and no iptables cannot enforce the lockdown, so the launch
// must stop before anything is started. Asserting the launcher was never called
// is the point: reaching it would start pasta and bwrap, and the abort would then
// arrive as an exit code from inside the wrapper script instead of as a named
// missing prerequisite.
func TestBwrapProxyLaunchAbortsWhenTheFirewallBinaryIsMissing(t *testing.T) {
	prevResolve, prevProbe := resolveEgressTools, probeEgressLockdown
	resolveEgressTools = func() (egress.Tools, error) { return egress.Tools{}, egress.ErrNoFirewallBackend }
	probeEgressLockdown = func(egress.Tools, egress.Lockdown) error {
		t.Error("probe ran after tool resolution failed; there is nothing to probe with")
		return nil
	}
	t.Cleanup(func() { resolveEgressTools, probeEgressLockdown = prevResolve, prevProbe })

	prev := launchers
	launchers = bwrapLaunchers{
		startWithPasta: func(cgroups.Limits, []string, []string, []string, egress.Lockdown, egress.Tools) (*bwrap.SandboxProcess, error) {
			t.Error("startWithPasta ran despite an unenforceable lockdown, so the sandbox would come up with egress open")
			return nil, nil
		},
	}
	t.Cleanup(func() { launchers = prev })

	cfg := &RunConfig{SandboxCfg: &sandbox.Config{ProxyEnabled: true, ProxyPort: 8123, GatewayIP: network.PastaGatewayIP}}
	err := NewBwrapIsolator(BwrapConfig{}).launch(cfg, nil, nil, nil)

	if !errors.Is(err, ErrEgressPreflight) {
		t.Fatalf("launch() error = %v, want it to wrap ErrEgressPreflight", err)
	}
	if !errors.Is(err, egress.ErrNoFirewallBackend) {
		t.Errorf("launch() error = %v, want it to name the missing firewall backend", err)
	}
	if !strings.Contains(err.Error(), "devsandbox doctor") {
		t.Errorf("launch() error = %v, want it to point at 'devsandbox doctor'", err)
	}
}

// The binaries being present is not the same question as the rules applying: a
// host with nf_tables loaded and nf_conntrack absent resolves nft fine and then
// refuses the `ct state` rules. The probe's own classification has to survive
// into the launch error, or the user is told to install something they have.
func TestBwrapProxyLaunchAbortsWhenTheProbeFails(t *testing.T) {
	probeErr := &egress.ProbeError{
		Stage:  egress.ProbeStageRules,
		Detail: "Error: Could not process rule: No such file or directory",
		Err:    errors.New("exit status 1"),
	}

	var probedWith egress.Lockdown
	prevResolve, prevProbe := resolveEgressTools, probeEgressLockdown
	resolveEgressTools = func() (egress.Tools, error) {
		return egress.Tools{IP: "/usr/bin/ip", Firewall: "/usr/sbin/nft", Backend: egress.BackendNft}, nil
	}
	probeEgressLockdown = func(_ egress.Tools, l egress.Lockdown) error {
		probedWith = l
		return probeErr
	}
	t.Cleanup(func() { resolveEgressTools, probeEgressLockdown = prevResolve, prevProbe })

	prev := launchers
	launchers = bwrapLaunchers{
		startWithPasta: func(cgroups.Limits, []string, []string, []string, egress.Lockdown, egress.Tools) (*bwrap.SandboxProcess, error) {
			t.Error("startWithPasta ran after the probe refused the rule set")
			return nil, nil
		},
	}
	t.Cleanup(func() { launchers = prev })

	cfg := &RunConfig{SandboxCfg: &sandbox.Config{ProxyEnabled: true, ProxyPort: 8123, GatewayIP: network.PastaGatewayIP}}
	err := NewBwrapIsolator(BwrapConfig{}).launch(cfg, nil, nil, nil)

	if !errors.Is(err, ErrEgressPreflight) {
		t.Fatalf("launch() error = %v, want it to wrap ErrEgressPreflight", err)
	}
	if !errors.Is(err, probeErr) {
		t.Errorf("launch() error = %v, want it to carry the probe failure", err)
	}
	if !strings.Contains(err.Error(), egress.ProbeStageRules.String()) {
		t.Errorf("launch() error = %v, want it to name the failing stage %q", err, egress.ProbeStageRules)
	}
	// Probing the lockdown the launch would actually render, not a stand-in, is
	// what makes a rule set that only this configuration produces get checked.
	if probedWith.ProxyPort != 8123 || probedWith.Gateway != network.PastaGatewayIP {
		t.Errorf("probe received %+v, want the lockdown this launch renders", probedWith)
	}
}

// The default path has no proxy and no lockdown, so netfilter must not become a
// prerequisite for it. A host with neither nft nor iptables still launches.
func TestBwrapNonProxyLaunchNeedsNoFirewall(t *testing.T) {
	prevResolve, prevProbe := resolveEgressTools, probeEgressLockdown
	resolveEgressTools = func() (egress.Tools, error) {
		t.Error("a non-proxy launch resolved egress tools; the lockdown must not gate the default path")
		return egress.Tools{}, egress.ErrNoFirewallBackend
	}
	probeEgressLockdown = func(egress.Tools, egress.Lockdown) error {
		t.Error("a non-proxy launch ran the firewall probe")
		return nil
	}
	t.Cleanup(func() { resolveEgressTools, probeEgressLockdown = prevResolve, prevProbe })

	var launched bool
	prev := launchers
	launchers = bwrapLaunchers{exec: func(cgroups.Limits, []string, []string) error {
		launched = true
		return nil
	}}
	t.Cleanup(func() { launchers = prev })

	if err := NewBwrapIsolator(BwrapConfig{}).launch(&RunConfig{SandboxCfg: &sandbox.Config{}}, nil, nil, nil); err != nil {
		t.Fatalf("launch() error = %v, want a non-proxy launch to succeed with no firewall present", err)
	}
	if !launched {
		t.Error("non-proxy launch never reached the launcher")
	}
}

// The execRun branch carries the same exit-status mapping, and is the path taken
// whenever tools are active, --rm is set, or the session is concurrent.
func TestBwrapLaunchExecRunMapsExitStatus(t *testing.T) {
	runErr := exec.Command("/bin/sh", "-c", "exit 3").Run()
	var ee *exec.ExitError
	if !errors.As(runErr, &ee) {
		t.Fatalf("helper process error = %v, want an *exec.ExitError", runErr)
	}

	prev := launchers
	launchers = bwrapLaunchers{
		execRun: func(cgroups.Limits, []string, []string) error { return ee },
	}
	t.Cleanup(func() { launchers = prev })

	cfg := &RunConfig{SandboxCfg: &sandbox.Config{}, HasActiveTools: true}
	err := NewBwrapIsolator(BwrapConfig{}).launch(cfg, nil, nil, nil)

	var exitErr *CommandExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("launch() error = %v (%T), want a *CommandExitError", err, err)
	}
	if exitErr.Code != 3 {
		t.Errorf("launch() exit code = %d, want 3", exitErr.Code)
	}
}

// TestEgressMarkerDirIgnoresTMPDIR asserts the lockdown marker is rooted at the
// host state directory rather than at $TMPDIR. $TMPDIR is whatever the invoking
// user set, so it can name a directory bound read-write into the sandbox - and a
// workload that can delete the marker makes its own exit code 78 read as an
// aborted lockdown, destroying the signal the marker exists to give.
//
// Sets process environment, so it must not call t.Parallel().
func TestEgressMarkerDirIgnoresTMPDIR(t *testing.T) {
	tmp := t.TempDir()
	state := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("XDG_STATE_HOME", state)

	dir, err := egressMarkerDir()
	if err != nil {
		t.Fatalf("egressMarkerDir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	if strings.HasPrefix(dir, tmp) {
		t.Errorf("egressMarkerDir() = %s, must not live under TMPDIR %s", dir, tmp)
	}
	want := filepath.Join(state, "devsandbox", "egress")
	if !strings.HasPrefix(dir, want+string(os.PathSeparator)) {
		t.Errorf("egressMarkerDir() = %s, want a directory under %s", dir, want)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("egressMarkerDir() did not create the directory: %v", err)
	}
}
