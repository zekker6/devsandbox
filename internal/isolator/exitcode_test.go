package isolator

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"
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
	var ce2 *CommandExitError
	if errors.As(asCommandExit(setupErr), &ce2) {
		t.Error("a setup error must not become a CommandExitError")
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
	var ce *CommandExitError
	if errors.As(got, &ce) {
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
