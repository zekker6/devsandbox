package main

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
)

// dieOfEnv makes the test binary re-enter this test as the child process that
// actually terminates. dieOf ends the process, so it cannot be exercised in-place.
const dieOfEnv = "DEVSANDBOX_TEST_DIE_OF"

// A sandbox that was terminated must terminate devsandbox the same way, which is
// what the calling shell saw while the bwrap launch replaced this process
// outright. Exiting 1 with an error instead reports an interrupted sandbox as a
// devsandbox failure and hides the interrupt from the shell, so a loop over
// devsandbox would run straight past a Ctrl-C.
//
// The child installs a handler for the signal first, because that is the live
// configuration: proxy mode registers one for SIGINT and SIGTERM. Without
// resetting the disposition the raised signal would be delivered to that handler
// and swallowed, and devsandbox would exit normally instead of dying.
//
// The signal the child dies of is also what proves the first step of dieOf landed:
// the SIGKILL escalation and the os.Exit fall-through would both be visible here
// as a different status, and neither is reachable without a full dieOfGrace of
// delay. Wall-clock is not asserted - it would be measuring test-binary startup.
func TestDieOfTerminatesWithTheSandboxsSignal(t *testing.T) {
	if os.Getenv(dieOfEnv) != "" {
		signal.Notify(make(chan os.Signal, 1), syscall.SIGTERM)
		dieOf(syscall.SIGTERM)
		// dieOf must never return.
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestDieOfTerminatesWithTheSandboxsSignal")
	cmd.Env = append(os.Environ(), dieOfEnv+"=1")

	err := cmd.Run()
	if err == nil {
		t.Fatal("the child exited 0; dieOf must terminate the process")
	}

	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("child error = %v (%T), want an *exec.ExitError", err, err)
	}
	status, ok := ee.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("child status is a %T, want a syscall.WaitStatus", ee.Sys())
	}
	if !status.Signaled() {
		t.Fatalf("child exited with status %d, want it killed by a signal", status.ExitStatus())
	}
	if status.Signal() != syscall.SIGTERM {
		t.Errorf("child died of %v, want SIGTERM (SIGKILL would mean the first step was swallowed)", status.Signal())
	}
}
