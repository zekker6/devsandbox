package isolator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNativeStartCallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	calls := 0
	err := runNativeCommand(cmd, func(pid int) error {
		calls++
		if pid != cmd.Process.Pid || pid <= 0 {
			t.Errorf("callback PID = %d, workload PID = %d", pid, cmd.Process.Pid)
		}
		return syscall.Kill(pid, 0)
	})
	if err != nil || calls != 1 || cmd.ProcessState == nil {
		t.Fatalf("run = %v, callbacks = %d, state = %v", err, calls, cmd.ProcessState)
	}
}

func TestNativeStartFailureSkipsCallback(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "/nonexistent/devsandbox-workload")
	called := false
	err := runNativeCommand(cmd, func(int) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("start failure = %v, callback called = %v", err, called)
	}
}

func TestNativeCallbackFailureKillsAndReapsGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 30 & echo $!; wait")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	registrationErr := errors.New("session registration failed")
	childPID := 0
	err = runNativeCommand(cmd, func(int) error {
		line, readErr := bufio.NewReader(stdout).ReadString('\n')
		if readErr != nil {
			return fmt.Errorf("read child PID: %w", readErr)
		}
		var parseErr error
		childPID, parseErr = strconv.Atoi(strings.TrimSpace(line))
		if parseErr != nil {
			return parseErr
		}
		return registrationErr
	})
	if !errors.Is(err, registrationErr) || cmd.ProcessState == nil || childPID <= 0 {
		t.Fatalf("run = %v, state = %v, child = %d", err, cmd.ProcessState, childPID)
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("workload was not reaped: %v", err)
	}
	// The child is reparented to init after the group kill, so allow time for
	// init to reap it rather than interpreting a transient zombie as alive.
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("child survived process-group cleanup: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
