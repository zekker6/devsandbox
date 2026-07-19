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
