package bwrap

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSecretArgsPrologue_IgnoresPoisonedPATH(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	file, err := writeSecretArgs([]string{"private-value"})
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marker := filepath.Join(bin, "rm-invoked")
	if err := os.WriteFile(filepath.Join(bin, "rm"), []byte("#!/bin/sh\nprintf invoked > \"$RM_MARKER\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("RM_MARKER", marker)

	cmd := exec.Command("/bin/sh", "-c", secretArgsPrologue+`printf '%s\n' "$1"; /bin/cat <&3`, "_", file, "bwrap")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("prologue failed: %v\n%s", err, out)
	}
	if string(out) != "bwrap\nprivate-value\x00" {
		t.Errorf("output = %q, want shifted argv and private data on fd 3", out)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("PATH-supplied rm was invoked: stat marker = %v", err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("private argument file survived: stat = %v", err)
	}
}

func TestSecretArgsPrologue_AbortsOnRemovalFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("opening a directory on fd 3 exercises the Linux proxy prologue")
	}
	// A directory can be opened for reading on Linux but rm -f cannot unlink it,
	// giving a deterministic failure even when tests run as root.
	file := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", secretArgsPrologue+`echo reached-bwrap`, "_", file, "bwrap")
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Errorf("prologue error = %v, want exit status 1\n%s", err, out)
	}
	if !strings.Contains(string(out), "devsandbox: cannot remove the private bwrap argument file "+file) {
		t.Errorf("output = %q, want removal failure naming the private file", out)
	}
	if strings.Contains(string(out), "reached-bwrap") {
		t.Error("prologue continued after removal failed")
	}
}
