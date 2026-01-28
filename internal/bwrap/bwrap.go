package bwrap

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func CheckInstalled() error {
	_, err := exec.LookPath("bwrap")
	if err != nil {
		return errors.New("bubblewrap (bwrap) is not installed\nInstall with: sudo apt install bubblewrap")
	}
	return nil
}

func Exec(bwrapArgs []string, shellCmd []string) error {
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		return err
	}

	args := make([]string, 0, len(bwrapArgs)+len(shellCmd)+2)
	args = append(args, "bwrap")
	args = append(args, bwrapArgs...)
	args = append(args, "--")
	args = append(args, shellCmd...)

	return syscall.Exec(bwrapPath, args, os.Environ())
}
