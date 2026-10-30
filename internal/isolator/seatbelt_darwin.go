package isolator

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"devsandbox/internal/procsignal"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func runNativeCommand(cmd *exec.Cmd, onStart func(int) error) (retErr error) {
	fd := int(os.Stdin.Fd())
	interactive := term.IsTerminal(fd)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if interactive {
		previous, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
		if err != nil {
			return fmt.Errorf("seatbelt read terminal foreground group: %w", err)
		}
		// Restoring the terminal from a background process group would stop
		// the supervisor unless SIGTTOU is ignored during the handoff.
		signal.Ignore(syscall.SIGTTOU)
		defer signal.Reset(syscall.SIGTTOU)
		defer func() {
			if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, previous); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("restore terminal foreground group: %w", err))
			}
		}()
		cmd.SysProcAttr.Foreground = true
		cmd.SysProcAttr.Ctty = fd
	}
	// CommandContext must terminate the group too, not just sandbox-exec's
	// immediate child. Processes that deliberately create a new session remain
	// confined but are not covered by process-group cleanup.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start seatbelt workload: %w", err)
	}
	if onStart != nil {
		if err := onStart(cmd.Process.Pid); err != nil {
			callbackErr := fmt.Errorf("register seatbelt workload: %w", err)
			if killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
				callbackErr = errors.Join(callbackErr, fmt.Errorf("stop seatbelt process group: %w", killErr))
			}
			if waitErr := cmd.Wait(); waitErr != nil {
				callbackErr = errors.Join(callbackErr, fmt.Errorf("reap seatbelt workload: %w", waitErr))
			}
			return callbackErr
		}
	}
	stop := procsignal.Forward(func(sig os.Signal) error {
		return syscall.Kill(-cmd.Process.Pid, sig.(syscall.Signal))
	})
	defer stop()
	waitErr := cmd.Wait()
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return errors.Join(asCommandExit(waitErr), fmt.Errorf("stop seatbelt process group: %w", err))
	}
	return asCommandExit(waitErr)
}
