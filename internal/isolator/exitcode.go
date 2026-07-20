package isolator

import (
	"errors"
	"fmt"
	"os/exec"
)

// CommandExitError reports the exit status of the sandboxed workload command, as
// distinct from a devsandbox setup failure (image build, container create,
// egress lockdown). The CLI unwraps it to propagate the code as its own exit
// status without printing an error, the way a shell surfaces a child's status.
type CommandExitError struct{ Code int }

func (e *CommandExitError) Error() string {
	return fmt.Sprintf("sandboxed command exited with status %d", e.Code)
}

// asCommandExit converts the error from running the sandboxed command into a
// CommandExitError carrying its exit code. A process terminated by a signal
// (ExitCode() == -1) and any non-ExitError (a setup or plumbing failure) pass
// through unchanged so the caller still treats them as devsandbox errors.
func asCommandExit(err error) error {
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if code := ee.ExitCode(); code >= 0 {
			return &CommandExitError{Code: code}
		}
	}
	return err
}

// engineFailureExitCode is the exit code the container engine (podman/docker)
// reserves for a failure in the engine itself - a bad flag, a missing image, a
// create/start/exec that never launched the workload - as opposed to the
// workload's own status. The container entrypoint is always the devsandbox shim,
// which never exits with this code.
const engineFailureExitCode = 125

// asEngineOrCommandExit is asCommandExit for errors from a container-engine
// invocation (`podman run`/`exec`). It converts the workload's exit status to a
// silent CommandExitError as asCommandExit does, EXCEPT engineFailureExitCode,
// which it wraps as an ordinary error so the CLI prints it - an engine that
// failed to launch the workload is a devsandbox failure and must not be swallowed
// (the engine also wrote its own reason to stderr). A shell workload's own
// 126/127 stay silent CommandExitErrors; only 125, which the shim entrypoint
// never produces, is treated as an engine failure (the rare workload that exits
// exactly 125 is surfaced as an error rather than propagated silently).
func asEngineOrCommandExit(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == engineFailureExitCode {
		return fmt.Errorf("container engine failed to launch the workload (exit %d); see the engine output above: %w", engineFailureExitCode, err)
	}
	return asCommandExit(err)
}
