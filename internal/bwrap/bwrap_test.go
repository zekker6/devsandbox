package bwrap

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"devsandbox/internal/cgroups"
)

func TestCheckInstalled(t *testing.T) {
	// With embedded binaries, CheckInstalled should always succeed
	// (extraction to cache dir or system fallback).
	// It can only fail if both embedded extraction AND system LookPath fail.
	err := CheckInstalled()
	// Don't assert success/failure — depends on test environment.
	// Just verify it doesn't panic.
	t.Logf("CheckInstalled() error: %v", err)
}

func TestPastaSupportsMapHostLoopback(t *testing.T) {
	// Test with a known path — just verify it doesn't panic.
	// With embedded binary, this should use the compile-time constant.
	result := pastaSupportsMapHostLoopback("/nonexistent/pasta")
	if result {
		t.Error("pastaSupportsMapHostLoopback should return false for nonexistent path")
	}
}

const fakeSystemdRun = "/fake/bin/systemd-run"

// withFakeWrap replaces the scope wrapper with a deterministic stand-in, so the
// argv assembly tests run on hosts without systemd-run installed. The real
// prefix cgroups.Wrap emits is pinned by that package's own tests; what these
// tests cover is how each launch path composes around it.
func withFakeWrap(t *testing.T) {
	t.Helper()
	prev := wrapLimits
	wrapLimits = func(l cgroups.Limits, program string, args []string) (string, []string, error) {
		if l.IsZero() {
			return program, args, nil
		}
		wrapped := []string{"--user", "--scope", "--quiet", "--collect", "--", program}
		return fakeSystemdRun, append(wrapped, args...), nil
	}
	t.Cleanup(func() { wrapLimits = prev })
}

// withFailingWrap makes the scope wrapper fail, as it does for an untranslatable
// limit or an unresolvable systemd-run.
func withFailingWrap(t *testing.T, err error) {
	t.Helper()
	prev := wrapLimits
	wrapLimits = func(cgroups.Limits, string, []string) (string, []string, error) {
		return "", nil, err
	}
	t.Cleanup(func() { wrapLimits = prev })
}

var (
	testBwrapArgs = []string{"--unshare-pid", "--bind", "/src", "/src"}
	testShellCmd  = []string{"bash", "-lc", "echo hi"}
	testLimits    = cgroups.Limits{Memory: "4g", CPUs: "2", PIDs: 64}
)

// The opt-in guarantee: with no limits configured, every launch path must
// produce byte-for-byte the argv it produced before scopes existed. This is
// spelled out as a literal rather than derived, so a change to the assembly
// cannot quietly redefine what "unchanged" means.
func TestInvocationsUnlimitedAreUnchanged(t *testing.T) {
	withFakeWrap(t)

	wantArgs := []string{"--unshare-pid", "--bind", "/src", "/src", "--", "bash", "-lc", "echo hi"}

	t.Run("exec", func(t *testing.T) {
		prog, argv, err := execInvocation(cgroups.Limits{}, "/opt/bwrap", testBwrapArgs, testShellCmd)
		if err != nil {
			t.Fatalf("execInvocation() error: %v", err)
		}
		if prog != "/opt/bwrap" {
			t.Errorf("program = %q, want the bwrap path unchanged", prog)
		}
		if want := append([]string{"bwrap"}, wantArgs...); !slices.Equal(argv, want) {
			t.Errorf("argv = %v, want %v", argv, want)
		}
	})

	t.Run("run", func(t *testing.T) {
		prog, args, err := runInvocation(cgroups.Limits{}, "/opt/bwrap", testBwrapArgs, testShellCmd)
		if err != nil {
			t.Fatalf("runInvocation() error: %v", err)
		}
		if prog != "/opt/bwrap" {
			t.Errorf("program = %q, want the bwrap path unchanged", prog)
		}
		if !slices.Equal(args, wantArgs) {
			t.Errorf("args = %v, want %v", args, wantArgs)
		}
	})

	// The proxy path carries the most complex argv, so it is the one that most
	// needs a literal. Comparing it against pastaCmdline - the function it
	// delegates to - could never fail, and would leave every pasta flag free to
	// change without notice; the wrapper script is the one element matched
	// loosely, since it is a formatted block rather than a flag.
	t.Run("pasta", func(t *testing.T) {
		prog, args, err := pastaInvocation(cgroups.Limits{}, "/opt/pasta", "/opt/bwrap", testBwrapArgs, testShellCmd, nil, false)
		if err != nil {
			t.Fatalf("pastaInvocation() error: %v", err)
		}
		if prog != "/opt/pasta" {
			t.Errorf("program = %q, want the pasta path unchanged", prog)
		}

		const scriptIdx = 4
		wantPasta := []string{
			"--config-net", "-f", "--",
			"sh", "-c", "<wrapper script>", "_",
			"/opt/bwrap", "--unshare-pid", "--bind", "/src", "/src",
			"--", "bash", "-lc", "echo hi",
		}
		got := slices.Clone(args)
		if len(got) > scriptIdx+1 {
			if !strings.Contains(got[scriptIdx+1], "ip route del default") {
				t.Errorf("wrapper script = %q, want the route surgery", got[scriptIdx+1])
			}
			got[scriptIdx+1] = "<wrapper script>"
		}
		if !slices.Equal(got, wantPasta) {
			t.Errorf("args = %v, want %v", got, wantPasta)
		}
	})
}

// Exec and ExecRun disagree on argv[0] by construction: syscall.Exec requires
// the caller to supply it, exec.Command derives it from the program path. The
// prefix must be applied under both conventions without dropping a flag, so the
// two argvs are asserted against each other directly.
func TestExecAndRunArgv0Asymmetry(t *testing.T) {
	withFakeWrap(t)

	for _, tc := range []struct {
		name      string
		limits    cgroups.Limits
		wantProg  string
		wantArgv0 string
	}{
		{name: "unlimited", limits: cgroups.Limits{}, wantProg: "/opt/bwrap", wantArgv0: "bwrap"},
		{name: "limited", limits: testLimits, wantProg: fakeSystemdRun, wantArgv0: "systemd-run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execProg, argv, err := execInvocation(tc.limits, "/opt/bwrap", testBwrapArgs, testShellCmd)
			if err != nil {
				t.Fatalf("execInvocation() error: %v", err)
			}
			runProg, args, err := runInvocation(tc.limits, "/opt/bwrap", testBwrapArgs, testShellCmd)
			if err != nil {
				t.Fatalf("runInvocation() error: %v", err)
			}

			if execProg != tc.wantProg || runProg != tc.wantProg {
				t.Errorf("programs = %q / %q, want both %q", execProg, runProg, tc.wantProg)
			}
			if argv[0] != tc.wantArgv0 {
				t.Errorf("exec argv[0] = %q, want %q", argv[0], tc.wantArgv0)
			}
			// The exec argv is exactly the exec.Command args with argv[0] in
			// front. Anything else means one path lost or gained a flag.
			if !slices.Equal(argv[1:], args) {
				t.Errorf("exec argv[1:] = %v, want it to equal the exec.Command args %v", argv[1:], args)
			}
			if slices.Contains(args, tc.wantArgv0) && tc.limits.IsZero() {
				t.Errorf("exec.Command args must not carry argv[0]: %v", args)
			}
		})
	}
}

func TestInvocationsLimitedCarryTheScopePrefix(t *testing.T) {
	withFakeWrap(t)

	t.Run("exec wraps bwrap", func(t *testing.T) {
		prog, argv, err := execInvocation(testLimits, "/opt/bwrap", testBwrapArgs, testShellCmd)
		if err != nil {
			t.Fatalf("execInvocation() error: %v", err)
		}
		if prog != fakeSystemdRun {
			t.Errorf("program = %q, want %q", prog, fakeSystemdRun)
		}
		assertWrapped(t, argv[1:], "/opt/bwrap")
	})

	t.Run("run wraps bwrap", func(t *testing.T) {
		prog, args, err := runInvocation(testLimits, "/opt/bwrap", testBwrapArgs, testShellCmd)
		if err != nil {
			t.Fatalf("runInvocation() error: %v", err)
		}
		if prog != fakeSystemdRun {
			t.Errorf("program = %q, want %q", prog, fakeSystemdRun)
		}
		assertWrapped(t, args, "/opt/bwrap")
	})

	// On the proxy path pasta is the outermost process, so pasta - not bwrap -
	// is what the scope has to contain.
	t.Run("pasta wraps pasta, not bwrap", func(t *testing.T) {
		prog, args, err := pastaInvocation(testLimits, "/opt/pasta", "/opt/bwrap", testBwrapArgs, testShellCmd, nil, false)
		if err != nil {
			t.Fatalf("pastaInvocation() error: %v", err)
		}
		if prog != fakeSystemdRun {
			t.Errorf("program = %q, want %q", prog, fakeSystemdRun)
		}
		assertWrapped(t, args, "/opt/pasta")
		if !slices.Contains(args, "/opt/bwrap") {
			t.Errorf("args must still carry the inner bwrap path: %v", args)
		}
	})
}

// assertWrapped checks that the scope prefix ends with "--" immediately
// followed by the program it wraps, and that the original command line survives
// intact after it.
func assertWrapped(t *testing.T, args []string, wrapped string) {
	t.Helper()
	sep := slices.Index(args, "--")
	if sep < 0 {
		t.Fatalf("args carry no -- separator: %v", args)
	}
	if sep+1 >= len(args) || args[sep+1] != wrapped {
		t.Fatalf("args[%d] after -- = %v, want %q", sep+1, args[sep+1:], wrapped)
	}
	if !slices.Contains(args[sep+2:], "--unshare-pid") {
		t.Errorf("the wrapped command line did not survive: %v", args[sep+2:])
	}
}

func TestInvocationsPropagateWrapErrors(t *testing.T) {
	wantErr := errors.New("cpu limit rounds to CPUQuota=0%")
	withFailingWrap(t, wantErr)

	t.Run("exec", func(t *testing.T) {
		prog, argv, err := execInvocation(testLimits, "/opt/bwrap", testBwrapArgs, testShellCmd)
		if !errors.Is(err, wantErr) {
			t.Fatalf("execInvocation() error = %v, want %v", err, wantErr)
		}
		if prog != "" || argv != nil {
			t.Errorf("execInvocation() returned %q / %v alongside an error, want empty results", prog, argv)
		}
	})

	t.Run("run", func(t *testing.T) {
		if _, _, err := runInvocation(testLimits, "/opt/bwrap", testBwrapArgs, testShellCmd); !errors.Is(err, wantErr) {
			t.Fatalf("runInvocation() error = %v, want %v", err, wantErr)
		}
	})

	t.Run("pasta", func(t *testing.T) {
		if _, _, err := pastaInvocation(testLimits, "/opt/pasta", "/opt/bwrap", testBwrapArgs, testShellCmd, nil, false); !errors.Is(err, wantErr) {
			t.Fatalf("pastaInvocation() error = %v, want %v", err, wantErr)
		}
	})
}

func TestPastaCmdline(t *testing.T) {
	t.Run("map-host-loopback is opt-in", func(t *testing.T) {
		with := pastaCmdline("/opt/bwrap", testBwrapArgs, testShellCmd, nil, true)
		if !slices.Contains(with, "--map-host-loopback") {
			t.Errorf("args = %v, want --map-host-loopback when supported", with)
		}
		without := pastaCmdline("/opt/bwrap", testBwrapArgs, testShellCmd, nil, false)
		if slices.Contains(without, "--map-host-loopback") {
			t.Errorf("args = %v, want no --map-host-loopback when unsupported", without)
		}
	})

	t.Run("port forwarding args are carried", func(t *testing.T) {
		args := pastaCmdline("/opt/bwrap", testBwrapArgs, testShellCmd, []string{"-t", "3000"}, false)
		if !slices.Contains(args, "-t") || !slices.Contains(args, "3000") {
			t.Errorf("args = %v, want the port forwarding flags", args)
		}
	})

	t.Run("the wrapper script precedes bwrap", func(t *testing.T) {
		args := pastaCmdline("/opt/bwrap", testBwrapArgs, testShellCmd, nil, false)
		sh := slices.Index(args, "sh")
		bw := slices.Index(args, "/opt/bwrap")
		if sh < 0 || bw < 0 || sh > bw {
			t.Fatalf("args = %v, want the sh wrapper before the bwrap path", args)
		}
		if !strings.Contains(args[sh+2], "ip route del default") {
			t.Errorf("wrapper script = %q, want the route surgery", args[sh+2])
		}
	})
}

// A launcher that fails after preflight passed must be reported immediately.
// Polling alone cannot see it - a zombie's children file reads empty, exactly
// like a process that has not forked yet - so the whole budget would be waited
// out and the failure then blamed on a missing sandbox PID. The scoped budget
// is 10s, which makes waiting it out the difference between a prompt error and
// an apparent hang.
func TestWaitForFirstChildPIDFailsFastOnExit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("procfs child tracking is Linux-only")
	}

	cmd := exec.Command("/bin/sh", "-c", "exit 3")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start doomed process: %v", err)
	}
	pid := cmd.Process.Pid
	// Deliberately left unreaped until the assertion is done: an unreaped child
	// is a zombie, which is the state this has to recognise.
	t.Cleanup(func() { _ = cmd.Wait() })

	start := time.Now()
	got, err := waitForFirstChildPID(pid, 10*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("waitForFirstChildPID() = %d, want an error once the process has exited", got)
	}
	if !strings.Contains(err.Error(), "exited without a visible child") {
		t.Errorf("waitForFirstChildPID() error = %q, want it to name the early exit", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("waitForFirstChildPID() took %v, want it to fail well before the budget", elapsed)
	}
}

func TestProcessExited(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("procfs state is Linux-only")
	}

	if processExited(os.Getpid()) {
		t.Error("processExited(self) = true, want false for a running process")
	}
	if !processExited(-1) {
		t.Error("processExited(-1) = false, want true for a PID with no procfs entry")
	}

	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start doomed process: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	deadline := time.Now().Add(2 * time.Second)
	for !processExited(cmd.Process.Pid) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !processExited(cmd.Process.Pid) {
		t.Error("processExited(zombie) = false, want true for an exited but unreaped process")
	}
}

// systemd-run adds a D-Bus round trip to the user manager before it execs
// pasta, so the budget for pasta's first child cannot stay sized for pasta
// starting immediately.
func TestPastaStartTimeout(t *testing.T) {
	if got := pastaStartTimeout(cgroups.Limits{}); got != 2*time.Second {
		t.Errorf("pastaStartTimeout(unlimited) = %v, want the original 2s budget", got)
	}
	if got := pastaStartTimeout(testLimits); got <= 2*time.Second {
		t.Errorf("pastaStartTimeout(limited) = %v, want more than the unlimited budget", got)
	}
}
