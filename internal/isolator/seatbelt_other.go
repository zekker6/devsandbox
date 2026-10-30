//go:build !darwin

package isolator

import (
	"errors"
	"os/exec"
)

func runNativeCommand(_ *exec.Cmd, _ func(int) error) error {
	return errors.New("seatbelt is only supported on macOS")
}
