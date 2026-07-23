package isolator

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"devsandbox/internal/egress"
)

// The rule-content assertions (route command shape, backend detection, and the
// deny-by-default firewall rules) live in internal/egress, where the rules are
// now built. What stays here is krun's application mechanism: the nsenter
// wrapping, the fail-closed sequencing, and the sentinel handshake.

// TestNsenterArgv asserts the surgery is wrapped to enter the target PID's user
// + net namespaces as userns-root (no --preserve-credentials, which would leave
// the caller unprivileged and the route ops would fail with EPERM).
func TestNsenterArgv(t *testing.T) {
	got := nsenterArgv(4242, "ip", "route", "del", "default")
	want := []string{"--target", "4242", "--user", "--net", "--", "ip", "route", "del", "default"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nsenterArgv = %v, want %v", got, want)
	}
	for _, a := range got {
		if a == "--preserve-credentials" {
			t.Fatal("nsenterArgv must NOT pass --preserve-credentials (route surgery needs userns-root)")
		}
	}
}

func TestParseDefaultRouteDevice(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    string
		wantErr bool
	}{
		{
			name:   "typical pasta-mirrored output",
			output: "default via 192.168.12.1 dev enp5s0 proto dhcp metric 100 \n",
			want:   "enp5s0",
		},
		{
			name:   "device immediately after default",
			output: "default dev wg0 scope link \n",
			want:   "wg0",
		},
		{
			name:    "no device field",
			output:  "default via 10.0.2.2 proto dhcp \n",
			wantErr: true,
		},
		{
			name:    "empty output",
			output:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDefaultRouteDevice(tt.output)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for output %q, got device %q", tt.output, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseDefaultRouteDevice(%q) = %q, want %q", tt.output, got, tt.want)
			}
		})
	}
}

// testEgressTools is the resolved-binary set the krun lockdown tests inject. The
// paths are absolute because nsenter runs with a PATH this process does not
// control.
var testEgressTools = egress.Tools{IP: "/usr/sbin/ip", Firewall: "/usr/sbin/nft", Backend: egress.BackendNft}

// testLookPath resolves the binaries the lockdown needs, mimicking a host where
// they live in /usr/sbin.
func testLookPath(name string) (string, error) {
	switch name {
	case "ip", "nft":
		return "/usr/sbin/" + name, nil
	}
	return "", errors.New("absent")
}

// TestApplyEgressCommands_Success asserts both lockdown commands run, in order,
// each wrapped in nsenter for the target netns.
func TestApplyEgressCommands_Success(t *testing.T) {
	var got [][]string
	runFn := func(name string, args ...string) error {
		got = append(got, append([]string{name}, args...))
		return nil
	}

	if err := applyEgressCommands(7, testEgressTools, "10.0.2.2", "enp5s0", runFn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := [][]string{
		{"nsenter", "--target", "7", "--user", "--net", "--", "/usr/sbin/ip", "route", "add", "10.0.2.2/32", "dev", "enp5s0"},
		{"nsenter", "--target", "7", "--user", "--net", "--", "/usr/sbin/ip", "route", "del", "default"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commands run = %v, want %v", got, want)
	}
}

// TestApplyEgressCommands_FailsClosed asserts a failure of either command is
// returned and no command runs after a failure - the netns must never be left
// with the default route half-deleted and egress open.
func TestApplyEgressCommands_FailsClosed(t *testing.T) {
	tests := []struct {
		name     string
		failAt   int
		wantRuns int
	}{
		{name: "add-route fails", failAt: 0, wantRuns: 1},
		{name: "del-default fails", failAt: 1, wantRuns: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runs := 0
			runFn := func(name string, args ...string) error {
				defer func() { runs++ }()
				if runs == tt.failAt {
					return errors.New("boom")
				}
				return nil
			}

			err := applyEgressCommands(7, testEgressTools, "10.0.2.2", "enp5s0", runFn)
			if err == nil {
				t.Fatal("expected error, got nil (egress would be left open)")
			}
			if runs != tt.wantRuns {
				t.Errorf("ran %d commands, want %d (no command may run after a failure)", runs, tt.wantRuns)
			}
		})
	}
}

// TestLockdownGuestEgress_EmptyGateway asserts lockdown fails closed when no
// gateway is provided rather than deleting the default route with no replacement.
func TestLockdownGuestEgress_EmptyGateway(t *testing.T) {
	if err := lockdownGuestEgress(1, "", 8080); err == nil {
		t.Fatal("expected error for empty gateway, got nil")
	}
}

// TestLockdownGuestEgress_InvalidPort asserts lockdown fails closed when the
// proxy port is missing/invalid rather than building a firewall that would leave
// the gateway reachable on all ports (the host-loopback exposure this guards).
func TestLockdownGuestEgress_InvalidPort(t *testing.T) {
	for _, port := range []int{0, -1} {
		if err := lockdownGuestEgress(1, "10.0.2.2", port); err == nil {
			t.Fatalf("expected error for proxy port %d, got nil", port)
		}
	}
}

// TestApplyNetnsCommands_FailsClosed asserts a failing firewall command stops the
// sequence and returns an error, so the netns is never left with the gateway
// reachable on all ports.
func TestApplyNetnsCommands_FailsClosed(t *testing.T) {
	cmds, err := egress.FirewallCommands(testEgressTools, egress.Lockdown{Enabled: true, Gateway: "10.0.2.2", ProxyPort: 8080})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	runs := 0
	runFn := func(name string, args ...string) error {
		defer func() { runs++ }()
		if runs == 2 {
			return errors.New("boom")
		}
		return nil
	}
	if err := applyNetnsCommands(9, cmds, runFn); err == nil {
		t.Fatal("expected error, got nil (gateway would stay reachable on all ports)")
	}
	if runs != 3 {
		t.Errorf("ran %d commands, want 3 (no command may run after a failure)", runs)
	}
}

// TestEgressSentinelRoundTrip asserts the sentinel write/remove helpers target
// the shared filename the guest shim watches.
func TestEgressSentinelRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, egressSentinelName)

	if err := writeEgressSentinel(dir); err != nil {
		t.Fatalf("writeEgressSentinel: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("sentinel not created at %s: %v", path, err)
	}

	if err := removeEgressSentinel(dir); err != nil {
		t.Fatalf("removeEgressSentinel: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("sentinel still present after remove: %v", err)
	}

	// removeEgressSentinel on an already-absent file must be a no-op (no error).
	if err := removeEgressSentinel(dir); err != nil {
		t.Fatalf("removeEgressSentinel on absent file returned error: %v", err)
	}
}

// TestRemoveEgressSentinel_ClearsStaleDirectory is the regression guard for the
// stale-directory sentinel spoof: an untrusted run leaves a non-empty directory
// at the sentinel path in the persistent, guest-writable home. os.Remove could
// not delete it (returning an error the old code discarded), so the guest could
// boot against a path a later `mkdir`+populate could turn into a spoofed
// sentinel. removeEgressSentinel must clear the whole subtree and report success
// only when the path is verifiably gone.
func TestRemoveEgressSentinel_ClearsStaleDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, egressSentinelName)

	if err := os.MkdirAll(filepath.Join(path, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "nested", "leftover"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := removeEgressSentinel(dir); err != nil {
		t.Fatalf("removeEgressSentinel must clear a stale non-empty directory, got: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale directory still present after remove: %v", err)
	}
}

// TestRemoveEgressSentinel_ErrorWhenPathSurvives asserts removeEgressSentinel
// fails closed when the sentinel path cannot be cleared. The path is nested
// under a directory whose write permission is stripped, so RemoveAll of the
// child cannot succeed - the helper must surface an error so the caller aborts
// the launch rather than boot against an unclean sentinel path.
func TestRemoveEgressSentinel_ErrorWhenPathSurvives(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory write permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, egressSentinelName)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// Strip write on the parent (sandboxHome) so RemoveAll cannot unlink the
	// sentinel directory entry itself.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if err := removeEgressSentinel(dir); err == nil {
		t.Fatal("expected error when sentinel path cannot be cleared, got nil (launch would proceed against an unclean sentinel)")
	}
}

// TestRemoveEgressSentinel_ClearsSymlink asserts the host clear removes a SYMLINK
// planted at the sentinel path (persistent, guest-writable home) as the link
// itself - never following it to its target - and then verifies absence. A prior
// untrusted run could point the sentinel path at a host-sensitive file; the clear
// must unlink it without touching what it references.
func TestRemoveEgressSentinel_ClearsSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, egressSentinelName)

	external := filepath.Join(t.TempDir(), "sensitive")
	if err := os.WriteFile(external, []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, path); err != nil {
		t.Fatal(err)
	}

	if err := removeEgressSentinel(dir); err != nil {
		t.Fatalf("removeEgressSentinel must clear a symlink at the sentinel path, got: %v", err)
	}
	// The link is gone (Lstat, so a residual link would be seen)...
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("symlink still present after remove: %v", err)
	}
	// ...and its target was NOT followed/removed.
	if _, err := os.Stat(external); err != nil {
		t.Errorf("symlink target must not be followed/removed by the clear: %v", err)
	}
}

// TestWriteEgressSentinel_RefusesSymlink asserts the host write never follows a
// symlink at the sentinel path: O_CREATE|O_EXCL fails closed on any pre-existing
// entry (a symlink included), so the write cannot clobber the file a planted link
// points at. This is defense-in-depth behind removeEgressSentinel, which clears
// the path first.
func TestWriteEgressSentinel_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, egressSentinelName)

	external := filepath.Join(t.TempDir(), "sensitive")
	if err := os.WriteFile(external, []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, path); err != nil {
		t.Fatal(err)
	}

	if err := writeEgressSentinel(dir); err == nil {
		t.Fatal("expected writeEgressSentinel to fail on a pre-existing symlink, got nil (it would write through the link)")
	}
	// The link's target must be untouched (not truncated/overwritten).
	if data, err := os.ReadFile(external); err != nil || string(data) != "host" {
		t.Errorf("symlink target must not be written through: data=%q err=%v", data, err)
	}
}

// TestPrepareEgressLockdown asserts the pre-boot sentinel clean/verify step that
// runMicroVMSession runs BEFORE cmd.Start(): it skips cleanly for non-microVM or
// non-Linux backends, clears the sentinel on the happy path, and - the
// security-critical case - returns the abort error when the sentinel path cannot
// be verified clean, so the guest never boots against a spoofable sentinel while
// direct egress is still open.
func TestPrepareEgressLockdown(t *testing.T) {
	t.Run("non-microvm skips", func(t *testing.T) {
		applied, err := prepareEgressLockdown(false, "linux", "/nonexistent")
		if applied || err != nil {
			t.Fatalf("prepareEgressLockdown(docker) = (%v,%v), want (false,nil)", applied, err)
		}
	})

	t.Run("non-linux skips", func(t *testing.T) {
		applied, err := prepareEgressLockdown(true, "darwin", "/nonexistent")
		if applied || err != nil {
			t.Fatalf("prepareEgressLockdown(darwin) = (%v,%v), want (false,nil)", applied, err)
		}
	})

	t.Run("happy path clears sentinel", func(t *testing.T) {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, egressSentinelName), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		applied, err := prepareEgressLockdown(true, "linux", home)
		if !applied || err != nil {
			t.Fatalf("prepareEgressLockdown = (%v,%v), want (true,nil)", applied, err)
		}
		if _, err := os.Stat(filepath.Join(home, egressSentinelName)); !os.IsNotExist(err) {
			t.Fatalf("sentinel not cleared before boot: %v", err)
		}
	})

	t.Run("unclean sentinel aborts before boot", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root bypasses directory write permissions")
		}
		home := t.TempDir()
		path := filepath.Join(home, egressSentinelName)
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "child"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(home, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(home, 0o755) })

		applied, err := prepareEgressLockdown(true, "linux", home)
		if !applied {
			t.Error("lockdown must still be reported as applying so the caller aborts rather than boots open")
		}
		if err == nil {
			t.Fatal("expected abort error for an unclean sentinel path; the guest would boot against a spoofable sentinel")
		}
	})
}

// TestLockdownGuestEgressWith_Ordering asserts the security-relevant sequencing
// through the injected seams: the firewall backend is resolved BEFORE any netns
// command runs (a host lacking both nft and iptables fails closed without
// half-applying the lockdown), and the route surgery runs BEFORE the port-scoped
// firewall.
func TestLockdownGuestEgressWith_Ordering(t *testing.T) {
	t.Run("no backend fails before touching the netns", func(t *testing.T) {
		lookPath := func(name string) (string, error) {
			if name == "ip" {
				return "/usr/sbin/ip", nil
			}
			return "", errors.New("absent")
		}
		resolveCalled := false
		resolveDev := func(int, string) (string, error) { resolveCalled = true; return "eth0", nil }
		ran := 0
		runFn := func(string, ...string) error { ran++; return nil }

		err := lockdownGuestEgressWith(7, "10.0.2.2", 8080, lookPath, resolveDev, runFn)
		if err == nil {
			t.Fatal("expected fail-closed error when no firewall backend is available")
		}
		if resolveCalled {
			t.Error("device resolution ran before the firewall backend was confirmed; a missing backend must abort first")
		}
		if ran != 0 {
			t.Errorf("ran %d netns commands with no firewall backend; the netns must not be mutated", ran)
		}
	})

	t.Run("missing ip fails before touching the netns", func(t *testing.T) {
		lookPath := func(name string) (string, error) {
			if name == "nft" {
				return "/usr/sbin/nft", nil
			}
			return "", errors.New("absent")
		}
		resolveCalled := false
		resolveDev := func(int, string) (string, error) { resolveCalled = true; return "eth0", nil }
		ran := 0
		runFn := func(string, ...string) error { ran++; return nil }

		err := lockdownGuestEgressWith(7, "10.0.2.2", 8080, lookPath, resolveDev, runFn)
		if !errors.Is(err, egress.ErrNoIPBinary) {
			t.Fatalf("error = %v, want %v", err, egress.ErrNoIPBinary)
		}
		if resolveCalled || ran != 0 {
			t.Errorf("netns touched with no `ip` binary (resolveDev=%v, ran=%d)", resolveCalled, ran)
		}
	})

	t.Run("route surgery precedes the firewall", func(t *testing.T) {
		resolveCalled := false
		var gotIPPath string
		resolveDev := func(_ int, ipPath string) (string, error) {
			resolveCalled = true
			gotIPPath = ipPath
			return "eth0", nil
		}
		var order []string
		runFn := func(name string, args ...string) error {
			order = append(order, strings.Join(append([]string{name}, args...), " "))
			return nil
		}

		if err := lockdownGuestEgressWith(7, "10.0.2.2", 8080, testLookPath, resolveDev, runFn); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resolveCalled {
			t.Error("device resolver was never called on the happy path")
		}
		if gotIPPath != "/usr/sbin/ip" {
			t.Errorf("device resolver got ip path %q, want the resolved absolute path", gotIPPath)
		}
		if len(order) < 3 {
			t.Fatalf("expected route surgery + firewall commands, got %d: %v", len(order), order)
		}
		if !strings.Contains(order[0], "route add") {
			t.Errorf("first netns command must be the route add, got %q", order[0])
		}
		if !strings.Contains(order[1], "route del") {
			t.Errorf("second netns command must be the route del, got %q", order[1])
		}
		// Every command after the route surgery is a firewall rule; none of the
		// route-surgery commands may appear among them.
		for _, c := range order[2:] {
			if strings.Contains(c, "route add") || strings.Contains(c, "route del") {
				t.Errorf("route surgery ran after a firewall command: %q", c)
			}
		}
	})
}

// TestLockdownGuestEgressWith_PropagatesFailures asserts every failure inside
// the lockdown reaches the caller, which aborts the launch on it. The pieces
// each fail closed on their own, but only if their error is returned: an
// unchecked device resolution or a swallowed route-surgery error leaves the
// guest running with egress open, which is the failure mode the whole lockdown
// exists to prevent, and no ordering assertion would notice.
func TestLockdownGuestEgressWith_PropagatesFailures(t *testing.T) {
	t.Run("device resolution fails", func(t *testing.T) {
		wantErr := errors.New("no default route in netns")
		ran := 0
		err := lockdownGuestEgressWith(7, "10.0.2.2", 8080, testLookPath,
			func(int, string) (string, error) { return "", wantErr },
			func(string, ...string) error { ran++; return nil })
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
		if ran != 0 {
			t.Errorf("ran %d netns commands without a resolved device; the netns must not be mutated", ran)
		}
	})

	// The route surgery deletes the default route, so a failure part-way through
	// it must abort before the firewall rules are attempted rather than continue
	// against a half-configured netns.
	t.Run("route surgery fails", func(t *testing.T) {
		wantErr := errors.New("route add refused")
		ran := 0
		err := lockdownGuestEgressWith(7, "10.0.2.2", 8080, testLookPath,
			func(int, string) (string, error) { return "eth0", nil },
			func(string, ...string) error { ran++; return wantErr })
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
		if ran != 1 {
			t.Errorf("ran %d netns commands, want 1 (nothing may run after the route surgery failed)", ran)
		}
	})

	// A failing firewall rule leaves the gateway reachable on every port through
	// --map-host-loopback, so it must abort the launch just as loudly.
	t.Run("firewall rules fail", func(t *testing.T) {
		wantErr := errors.New("nft refused the rule")
		ran := 0
		err := lockdownGuestEgressWith(7, "10.0.2.2", 8080, testLookPath,
			func(int, string) (string, error) { return "eth0", nil },
			func(string, ...string) error {
				ran++
				if ran > 2 {
					return wantErr
				}
				return nil
			})
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
		if ran != 3 {
			t.Errorf("ran %d netns commands, want 3 (the first firewall rule aborts the rest)", ran)
		}
	})
}

// TestLockdownGuestEgressWith_ArgvUnchanged pins the EXACT argv krun runs, end to
// end, so moving the rule builders into internal/egress cannot alter what lands
// in the VMM netns. The rules are now shared with the bwrap backend, and a change
// made for bwrap that silently rewrote krun's lockdown would be a security
// regression on a backend nobody was editing.
func TestLockdownGuestEgressWith_ArgvUnchanged(t *testing.T) {
	resolveDev := func(int, string) (string, error) { return "eth0", nil }
	var got [][]string
	runFn := func(name string, args ...string) error {
		got = append(got, append([]string{name}, args...))
		return nil
	}

	if err := lockdownGuestEgressWith(7, "10.0.2.2", 8080, testLookPath, resolveDev, runFn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The only change from the pre-extraction argv is that the binaries are now
	// the absolute paths resolved on the host instead of bare names.
	nsenter := []string{"nsenter", "--target", "7", "--user", "--net", "--"}
	want := [][]string{
		append(append([]string{}, nsenter...), "/usr/sbin/ip", "route", "add", "10.0.2.2/32", "dev", "eth0"),
		append(append([]string{}, nsenter...), "/usr/sbin/ip", "route", "del", "default"),
		append(append([]string{}, nsenter...), "/usr/sbin/nft", "add", "table", "ip", "devsandbox_egress"),
		append(append([]string{}, nsenter...), "/usr/sbin/nft", "add", "chain", "ip", "devsandbox_egress", "output", "{ type filter hook output priority 0 ; policy drop ; }"),
		append(append([]string{}, nsenter...), "/usr/sbin/nft", "add", "rule", "ip", "devsandbox_egress", "output", "ct", "state", "established,related", "accept"),
		append(append([]string{}, nsenter...), "/usr/sbin/nft", "add", "rule", "ip", "devsandbox_egress", "output", "oif", "lo", "accept"),
		append(append([]string{}, nsenter...), "/usr/sbin/nft", "add", "rule", "ip", "devsandbox_egress", "output", "ip", "daddr", "10.0.2.2", "tcp", "dport", "8080", "accept"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("krun lockdown argv changed by the extraction:\n got = %v\nwant = %v", got, want)
	}
}
