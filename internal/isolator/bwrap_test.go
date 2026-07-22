package isolator

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"testing"

	"devsandbox/internal/bwrap"
	"devsandbox/internal/cgroups"
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
			var gotLaunch string
			var got cgroups.Limits
			sentinel := errors.New("stub launcher")

			prev := launchers
			launchers = bwrapLaunchers{
				startWithPasta: func(l cgroups.Limits, _, _, _ []string) (*bwrap.SandboxProcess, error) {
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

// Everything the proxy branch does after the launcher returns. The limits tests
// above stop at the launcher's error return, so without this the two remaining
// statements are executed but unasserted: dropping the OnSandboxStart call
// silently disables port forwarding and netns wiring, and dropping the
// asCommandExit wrapper turns a workload's own non-zero exit into a devsandbox
// error instead of a propagated exit code.
func TestBwrapLaunchProxyModeReportsStartAndExitStatus(t *testing.T) {
	const nsPID = 4242

	proc := startedSandboxProcess(t, 7, nsPID)
	prev := launchers
	launchers = bwrapLaunchers{
		startWithPasta: func(cgroups.Limits, []string, []string, []string) (*bwrap.SandboxProcess, error) {
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
	prev := launchers
	launchers = bwrapLaunchers{
		startWithPasta: func(cgroups.Limits, []string, []string, []string) (*bwrap.SandboxProcess, error) {
			return startedSandboxProcess(t, 0, 4242), nil
		},
	}
	t.Cleanup(func() { launchers = prev })

	cfg := &RunConfig{SandboxCfg: &sandbox.Config{ProxyEnabled: true}}
	if err := NewBwrapIsolator(BwrapConfig{}).launch(cfg, nil, nil, nil); err != nil {
		t.Fatalf("launch() error = %v, want nil for a workload that exited 0", err)
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
