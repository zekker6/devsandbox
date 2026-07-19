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
