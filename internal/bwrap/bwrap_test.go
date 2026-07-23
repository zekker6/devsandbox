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
	"devsandbox/internal/egress"
	"devsandbox/internal/network"
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
		prog, args, err := pastaInvocation(cgroups.Limits{}, "/opt/pasta", "/opt/bwrap", testBwrapArgs, testShellCmd, nil, false, egress.Lockdown{}, egress.Tools{})
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
		prog, args, err := pastaInvocation(testLimits, "/opt/pasta", "/opt/bwrap", testBwrapArgs, testShellCmd, nil, false, egress.Lockdown{}, egress.Tools{})
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
		if _, _, err := pastaInvocation(testLimits, "/opt/pasta", "/opt/bwrap", testBwrapArgs, testShellCmd, nil, false, egress.Lockdown{}, egress.Tools{}); !errors.Is(err, wantErr) {
			t.Fatalf("pastaInvocation() error = %v, want %v", err, wantErr)
		}
	})
}

// mustPastaCmdline fails the test rather than returning an error, so the
// assembly assertions below stay readable.
func mustPastaCmdline(t *testing.T, portForwardArgs []string, mapHostLoopback bool, lockdown egress.Lockdown) []string {
	t.Helper()
	args, err := pastaCmdline("/opt/bwrap", testBwrapArgs, testShellCmd, portForwardArgs, mapHostLoopback, lockdown, testEgressTools)
	if err != nil {
		t.Fatalf("pastaCmdline() error: %v", err)
	}
	return args
}

// testLockdown is a rendered-lockdown request with everything egress.Script
// requires. The gateway is the real pasta one so the emitted rules match what a
// live proxy launch produces.
var testLockdown = egress.Lockdown{Enabled: true, Gateway: network.PastaGatewayIP, ProxyPort: 8080, ReadyFile: "/run/devsandbox-test/applied"}

// testEgressTools pins the binaries the lockdown renders, so the argv tests
// produce the same script on a host that has iproute2 and nftables and on one
// that has neither.
var testEgressTools = egress.Tools{IP: "/usr/sbin/ip", Firewall: "/usr/sbin/nft", Backend: egress.BackendNft}

func TestPastaCmdline(t *testing.T) {
	t.Run("map-host-loopback is opt-in", func(t *testing.T) {
		with := mustPastaCmdline(t, nil, true, egress.Lockdown{})
		if !slices.Contains(with, "--map-host-loopback") {
			t.Errorf("args = %v, want --map-host-loopback when supported", with)
		}
		without := mustPastaCmdline(t, nil, false, egress.Lockdown{})
		if slices.Contains(without, "--map-host-loopback") {
			t.Errorf("args = %v, want no --map-host-loopback when unsupported", without)
		}
	})

	t.Run("port forwarding args are carried", func(t *testing.T) {
		args := mustPastaCmdline(t, []string{"-t", "3000"}, false, egress.Lockdown{})
		if !slices.Contains(args, "-t") || !slices.Contains(args, "3000") {
			t.Errorf("args = %v, want the port forwarding flags", args)
		}
	})

	t.Run("the wrapper script precedes bwrap", func(t *testing.T) {
		args := mustPastaCmdline(t, nil, false, egress.Lockdown{})
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

// The lockdown's ruleset is IPv4-only, so a namespace that still has IPv6
// enforces nothing on that family: -4 and the rendered rules must appear
// together or not at all.
func TestPastaCmdlineIPv4OnlyExactlyWithTheLockdown(t *testing.T) {
	locked := mustPastaCmdline(t, nil, true, testLockdown)
	if !slices.Contains(locked, "-4") {
		t.Errorf("args = %v, want -4 when a lockdown is rendered", locked)
	}

	open := mustPastaCmdline(t, nil, true, egress.Lockdown{})
	if slices.Contains(open, "-4") {
		t.Errorf("args = %v, want no -4 when no lockdown is rendered", open)
	}
}

// pasta defaults -T/-U to `auto`, which binds every host-listening port inside
// the namespace on loopback - reachable through the `oif lo accept` the firewall
// must carry, and so a direct path to the host's own services that survives
// every gateway rule. The lockdown must switch it off, and must not switch off a
// protocol the user configured an outbound forward for.
func TestPastaCmdlineDisablesAutomaticHostForwarding(t *testing.T) {
	tests := []struct {
		name       string
		forwards   []string
		lockdown   egress.Lockdown
		wantSuffix []string
	}{
		{name: "no forwards", lockdown: testLockdown, wantSuffix: []string{"-T", "none", "-U", "none"}},
		{
			name:       "tcp forward keeps its own -T",
			forwards:   []string{"-T", "5000"},
			lockdown:   testLockdown,
			wantSuffix: []string{"-T", "5000", "-U", "none"},
		},
		{
			name:       "udp forward keeps its own -U",
			forwards:   []string{"-U", "5000"},
			lockdown:   testLockdown,
			wantSuffix: []string{"-U", "5000", "-T", "none"},
		},
		{
			name:       "inbound forwards do not count as outbound",
			forwards:   []string{"-t", "3000", "-u", "3001"},
			lockdown:   testLockdown,
			wantSuffix: []string{"-t", "3000", "-u", "3001", "-T", "none", "-U", "none"},
		},
		{
			name:       "no lockdown leaves pasta's defaults alone",
			lockdown:   egress.Lockdown{},
			wantSuffix: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := mustPastaCmdline(t, tt.forwards, true, tt.lockdown)
			fIdx := slices.Index(args, "-f")
			if fIdx < 0 {
				t.Fatalf("args = %v, want a -f", args)
			}
			// Everything between the host-loopback map and -f is port forwarding.
			got := args[slices.Index(args, network.PastaGatewayIP)+1 : fIdx]
			want := tt.wantSuffix
			if want == nil {
				want = tt.forwards
			}
			if !slices.Equal(got, want) {
				t.Errorf("port forwarding args = %v, want %v", got, want)
			}
		})
	}
}

// The lockdown replaces the wrapper script, and only the wrapper script: every
// other element of the pasta argv, and their order, stay as they were.
func TestPastaCmdlineLockdownReplacesOnlyTheWrapperScript(t *testing.T) {
	args := mustPastaCmdline(t, []string{"-t", "3000", "-T", "5000"}, true, testLockdown)

	const scriptIdx = 14
	want := []string{
		"--config-net", "-4", "--map-host-loopback", network.PastaGatewayIP,
		"-t", "3000", "-T", "5000", "-U", "none", "-f", "--",
		"sh", "-c", "<lockdown script>", "_",
		"/opt/bwrap", "--unshare-pid", "--bind", "/src", "/src",
		"--", "bash", "-lc", "echo hi",
	}
	script := args[scriptIdx]
	got := slices.Clone(args)
	got[scriptIdx] = "<lockdown script>"
	if !slices.Equal(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}

	// The best-effort surgery is gone: no discarded errors, and the exec is
	// reached only if every lockdown step succeeded. The retry loop's one-time
	// `sleep` support probe is the sole sanctioned discard - its exit status is
	// the signal - so it is stripped before the assertion rather than exempting
	// the whole script. internal/egress pins the same invariant at the source.
	if strings.Contains(strings.Replace(script, "sleep 0.01 2>/dev/null", "", 1), "2>/dev/null") {
		t.Errorf("lockdown script = %q, want no discarded errors", script)
	}
	for _, want := range []string{"/usr/sbin/ip", "/usr/sbin/nft", `'route' 'del'`, "policy drop", `exec "$@"`} {
		if !strings.Contains(script, want) {
			t.Errorf("lockdown script = %q, want it to contain %q", script, want)
		}
	}
}

// A lockdown that cannot be rendered must abort the assembly. Both failures are
// silent non-application of a security control if they are allowed through: the
// launch would otherwise proceed with the old open-egress wrapper.
func TestPastaCmdlineLockdownFailsClosed(t *testing.T) {
	// Unresolved tools reach here only if the caller's preflight was skipped or
	// its result discarded. Rendering anyway would emit bare binary names, which
	// is the silent non-application the absolute paths exist to prevent.
	t.Run("unresolved tools", func(t *testing.T) {
		args, err := pastaCmdline("/opt/bwrap", testBwrapArgs, testShellCmd, nil, true, testLockdown, egress.Tools{})
		if !errors.Is(err, egress.ErrNoIPBinary) {
			t.Fatalf("pastaCmdline() error = %v, want %v", err, egress.ErrNoIPBinary)
		}
		if args != nil {
			t.Errorf("pastaCmdline() returned %v alongside an error, want no argv", args)
		}
	})

	t.Run("no firewall backend", func(t *testing.T) {
		args, err := pastaCmdline("/opt/bwrap", testBwrapArgs, testShellCmd, nil, true, testLockdown,
			egress.Tools{IP: "/usr/sbin/ip"})
		if !errors.Is(err, egress.ErrNoFirewallBackend) {
			t.Fatalf("pastaCmdline() error = %v, want %v", err, egress.ErrNoFirewallBackend)
		}
		if args != nil {
			t.Errorf("pastaCmdline() returned %v alongside an error, want no argv", args)
		}
	})

	// A zero proxy port renders a rule that permits nothing; refusing is the only
	// safe outcome, since the alternative is a sandbox with no path to the proxy
	// and no explanation.
	t.Run("zero proxy port", func(t *testing.T) {
		_, err := pastaCmdline("/opt/bwrap", testBwrapArgs, testShellCmd, nil, true,
			egress.Lockdown{Enabled: true, Gateway: network.PastaGatewayIP, ProxyPort: 0, ReadyFile: "/run/devsandbox-test/applied"}, testEgressTools)
		if err == nil {
			t.Fatal("pastaCmdline() error = nil, want a refusal for an invalid proxy port")
		}
		if !strings.Contains(err.Error(), "proxy port") {
			t.Errorf("pastaCmdline() error = %v, want it to name the invalid proxy port", err)
		}
	})

	// The lockdown's only accept points at the gateway, which is reachable only
	// because --map-host-loopback maps it. Rendering the rules on a pasta without
	// that option produces a sandbox that can reach nothing and says nothing about
	// why, so the launch must be refused with the prerequisite named.
	t.Run("no map-host-loopback support", func(t *testing.T) {
		args, err := pastaCmdline("/opt/bwrap", testBwrapArgs, testShellCmd, nil, false, testLockdown, testEgressTools)
		if err == nil {
			t.Fatal("pastaCmdline() error = nil, want a refusal without --map-host-loopback support")
		}
		if !strings.Contains(err.Error(), "--map-host-loopback") {
			t.Errorf("pastaCmdline() error = %v, want it to name --map-host-loopback", err)
		}
		if args != nil {
			t.Errorf("pastaCmdline() returned %v alongside an error, want no argv", args)
		}
	})

	// The non-proxy path never touches the tools at all, so a host with no
	// nft/iptables keeps launching exactly as before.
	t.Run("no lockdown needs no tools", func(t *testing.T) {
		if _, err := pastaCmdline("/opt/bwrap", testBwrapArgs, testShellCmd, nil, true, egress.Lockdown{}, egress.Tools{}); err != nil {
			t.Fatalf("pastaCmdline() error = %v, want a non-proxy launch to succeed", err)
		}
	})

	// The refusal above is scoped to proxy mode: an old pasta must keep launching
	// non-proxy sandboxes, which have no gateway to map and no rules to contradict.
	t.Run("no lockdown needs no map-host-loopback", func(t *testing.T) {
		if _, err := pastaCmdline("/opt/bwrap", testBwrapArgs, testShellCmd, nil, false, egress.Lockdown{}, egress.Tools{}); err != nil {
			t.Fatalf("pastaCmdline() error = %v, want a non-proxy launch to succeed without --map-host-loopback", err)
		}
	})
}

// pasta must map the same address the firewall rules permit. They are equal
// today only because pasta is the sole network provider; if the two are ever
// sourced apart, the sandbox gets a mapped gateway at one address and its only
// accept at another, which is total egress loss with no diagnostic.
func TestPastaCmdlineMapsTheGatewayTheRulesWereBuiltFrom(t *testing.T) {
	const gateway = "10.9.9.9"
	args := mustPastaCmdline(t, nil, true, egress.Lockdown{Enabled: true, Gateway: gateway, ProxyPort: 8080, ReadyFile: "/run/devsandbox-test/applied"})

	i := slices.Index(args, "--map-host-loopback")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("args = %v, want --map-host-loopback with a value", args)
	}
	if args[i+1] != gateway {
		t.Errorf("--map-host-loopback = %q, want the lockdown gateway %q", args[i+1], gateway)
	}

	// The non-proxy path has no lockdown to take a gateway from and keeps using
	// the package constant.
	open := mustPastaCmdline(t, nil, true, egress.Lockdown{})
	if j := slices.Index(open, "--map-host-loopback"); j < 0 || open[j+1] != network.PastaGatewayIP {
		t.Errorf("args = %v, want --map-host-loopback %s without a lockdown", open, network.PastaGatewayIP)
	}
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
