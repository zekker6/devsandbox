package isolator

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"testing"

	"devsandbox/internal/cgroups"
)

func TestAsCommandExit(t *testing.T) {
	// A real *exec.ExitError from a command that exits non-zero.
	runErr := exec.Command("sh", "-c", "exit 42").Run()
	if runErr == nil {
		t.Fatal("expected non-nil error from `exit 42`")
	}

	got := asCommandExit(runErr)
	var ce *CommandExitError
	if !errors.As(got, &ce) {
		t.Fatalf("asCommandExit(ExitError) = %v; want *CommandExitError", got)
	}
	if ce.Code != 42 {
		t.Errorf("CommandExitError.Code = %d; want 42", ce.Code)
	}

	// nil passes through as nil.
	if asCommandExit(nil) != nil {
		t.Error("asCommandExit(nil) should be nil")
	}

	// A non-ExitError (a setup/plumbing failure) passes through unchanged so the
	// caller still surfaces it as a devsandbox error, not a command exit code.
	setupErr := fmt.Errorf("failed to build image: %w", errors.New("boom"))
	if got := asCommandExit(setupErr); !errors.Is(got, setupErr) {
		t.Errorf("asCommandExit(non-ExitError) = %v; want the original error", got)
	}
	if _, ok := errors.AsType[*CommandExitError](asCommandExit(setupErr)); ok {
		t.Error("a setup error must not become a CommandExitError")
	}
}

// A terminated sandbox is the sandbox's result too, not a devsandbox failure, and
// the CLI re-raises this signal on itself so the calling shell sees what it saw
// before devsandbox stopped replacing itself with bwrap: 130 for a Ctrl-C, 137 for
// an OOM kill, and a `while` loop that stops on an interrupt.
func TestAsCommandExitReportsATerminatingSignal(t *testing.T) {
	runErr := exec.Command("sh", "-c", "kill -9 $$").Run()
	if runErr == nil {
		t.Fatal("expected non-nil error from a self-killed helper")
	}

	got := asCommandExit(runErr)
	var cs *CommandSignalError
	if !errors.As(got, &cs) {
		t.Fatalf("asCommandExit(signaled) = %v (%T); want *CommandSignalError", got, got)
	}
	if cs.Signal != syscall.SIGKILL {
		t.Errorf("CommandSignalError.Signal = %v; want SIGKILL", cs.Signal)
	}

	// A terminated command is not an exit status, and must not be mistaken for one:
	// exiting with a code would tell the shell the sandbox ran to completion.
	if ce, ok := errors.AsType[*CommandExitError](got); ok {
		t.Errorf("a signaled command became a CommandExitError with code %d", ce.Code)
	}

	// The underlying *exec.ExitError stays reachable, which is what keeps the
	// session.end audit event reporting the same -1 it reported before.
	if ee, ok := errors.AsType[*exec.ExitError](got); !ok {
		t.Error("CommandSignalError must keep wrapping the *exec.ExitError")
	} else if ee.ExitCode() != -1 {
		t.Errorf("wrapped ExitCode() = %d, want -1", ee.ExitCode())
	}
}

func TestAsEngineOrCommandExit(t *testing.T) {
	// A workload exit status propagates silently as a CommandExitError, exactly as
	// asCommandExit does, so `devsandbox`'s exit code matches the command's.
	for _, code := range []int{1, 42, 126, 127} {
		runErr := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
		got := asEngineOrCommandExit(runErr)
		var ce *CommandExitError
		if !errors.As(got, &ce) {
			t.Fatalf("asEngineOrCommandExit(exit %d) = %v; want *CommandExitError", code, got)
		}
		if ce.Code != code {
			t.Errorf("CommandExitError.Code = %d; want %d", ce.Code, code)
		}
	}

	// The engine-reserved code 125 (podman/docker: the engine itself failed to
	// launch the workload) surfaces as an ordinary error the CLI prints, NOT a
	// silent CommandExitError - a launch failure must not be swallowed.
	engineErr := exec.Command("sh", "-c", "exit 125").Run()
	got := asEngineOrCommandExit(engineErr)
	if _, ok := errors.AsType[*CommandExitError](got); ok {
		t.Errorf("exit 125 must not become a silent CommandExitError, got %v", got)
	}
	if got == nil {
		t.Fatal("exit 125 must surface as a non-nil error")
	}

	// nil passes through.
	if asEngineOrCommandExit(nil) != nil {
		t.Error("asEngineOrCommandExit(nil) should be nil")
	}
}

// A scope systemd refuses must be distinguishable from a workload that exits
// non-zero. It cannot be told apart by exit status - systemd-run reports its own
// failure with exit 1, the workload's most common status - so the split is made
// before launch: cgroups.Preflight fails with a specific error, while the exit
// path keeps treating 1 as the workload's own status.
func TestScopeFailureIsDistinguishableFromWorkloadExit(t *testing.T) {
	// A refused scope aborts at preflight, as a devsandbox error the CLI prints.
	iso := NewBwrapIsolator(BwrapConfig{Limits: cgroups.Limits{CPUs: "0.004"}})
	err := iso.Preflight(context.Background(), "/tmp/test-project")
	if err == nil {
		t.Fatal("Preflight() = nil, want an error for a limit that cannot be enforced")
	}
	var ce *CommandExitError
	if errors.As(err, &ce) {
		t.Errorf("an unenforceable limit must not surface as a command exit: %v", err)
	}

	// The same exit status from the workload itself stays a silent
	// CommandExitError, so `devsandbox -- false` still exits 1 without an error.
	workloadErr := asCommandExit(exec.Command("sh", "-c", "exit 1").Run())
	if !errors.As(workloadErr, &ce) {
		t.Fatalf("asCommandExit(exit 1) = %v; want *CommandExitError", workloadErr)
	}
	if ce.Code != 1 {
		t.Errorf("CommandExitError.Code = %d; want 1", ce.Code)
	}

	// An unlimited sandbox never reaches the scope machinery at all.
	unlimited := NewBwrapIsolator(BwrapConfig{})
	if err := unlimited.Preflight(context.Background(), "/tmp/test-project"); err != nil {
		t.Errorf("Preflight() with no limits = %v, want nil", err)
	}
}
