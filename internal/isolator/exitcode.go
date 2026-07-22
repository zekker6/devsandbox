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

// There is deliberately no asScopeOrCommandExit counterpart for the systemd
// transient scope the bwrap backend launches under when resource limits are
// configured.
//
// The engine case above works because 125 is a code podman/docker reserve and
// the shim entrypoint never produces. systemd-run reserves nothing: it exits 1
// on its own failure, which is also the single most common status a sandboxed
// workload exits with. Mapping 1 to a launcher failure would report a failing
// test run, a non-matching grep or a plain `false` as a broken sandbox - a
// regression far worse than the case it guards.
//
// The scope-refused-at-D-Bus-time case is instead caught before launch, by the
// live probe in cgroups.Preflight: it creates a throwaway scope carrying the
// same properties, so a refusal surfaces as a specific error naming what
// systemd rejected rather than as an ambiguous exit status afterwards. That
// also covers the exec-replace launch path, where devsandbox is gone by the
// time any exit code exists and no post-hoc mapping is possible at all.
