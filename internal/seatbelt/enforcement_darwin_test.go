//go:build darwin

package seatbelt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A distinct exit code proves the helper reached the tested syscall and got a
// permission error. A runtime startup failure must not pass a denial test.
const permissionDeniedExit = 23

func TestSeatbeltHelper(t *testing.T) {
	if os.Getenv("DEVSANDBOX_SEATBELT_TEST_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 3 {
		t.Fatal("missing helper operation or operand")
	}
	var err error
	switch args[1] {
	case "read":
		var data []byte
		data, err = os.ReadFile(args[2])
		if err == nil {
			_, err = os.Stdout.Write(data)
		}
	case "read-dir":
		_, err = os.ReadDir(args[2])
	case "stat":
		_, err = os.Stat(args[2])
	case "lstat":
		_, err = os.Lstat(args[2])
	case "readlink-missing":
		_, err = os.Readlink(args[2])
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		} else if err == nil {
			err = errors.New("expected a missing symbolic link")
		}
	case "write":
		err = os.WriteFile(args[2], []byte("written"), 0600)
	case "chmod":
		err = os.Chmod(args[2], 0600)
	case "remove":
		err = os.Remove(args[2])
	case "rename", "link":
		if len(args) != 4 {
			t.Fatal("missing destination")
		}
		if args[1] == "rename" {
			err = os.Rename(args[2], args[3])
		} else {
			err = os.Link(args[2], args[3])
		}
	case "send-udp":
		var conn net.Conn
		conn, err = net.DialTimeout("udp4", args[2], 2*time.Second)
		if err == nil {
			if err = conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err == nil {
				_, err = conn.Write([]byte("seatbelt-udp-test"))
			}
			err = errors.Join(err, conn.Close())
		}
	case "dial-tcp", "dial-unix":
		network := strings.TrimPrefix(args[1], "dial-")
		var conn net.Conn
		conn, err = net.DialTimeout(network, args[2], 2*time.Second)
		if err == nil {
			err = conn.Close()
		}
	case "roundtrip-unix":
		err = nativeUnixRoundTrip(args[2])
	case "listen-tcp", "listen-unix":
		network := strings.TrimPrefix(args[1], "listen-")
		var listener net.Listener
		listener, err = net.Listen(network, args[2])
		if err == nil {
			err = listener.Close()
		}
	default:
		t.Fatalf("unknown helper operation %q", args[1])
	}
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			fmt.Fprintln(os.Stderr, "seatbelt-permission-denied")
			os.Exit(permissionDeniedExit)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(24)
	}
	os.Exit(0)
}

// Exercise accept and data transfer as well as bind/connect. A listener that
// merely opens and closes cannot prove the inbound rule works.
func nativeUnixRoundTrip(address string) (err error) {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: address, Net: "unix"})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, listener.Close()) }()
	deadline := time.Now().Add(2 * time.Second)
	if err := listener.SetDeadline(deadline); err != nil {
		return err
	}
	client, err := net.DialTimeout("unix", address, 2*time.Second)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, client.Close()) }()
	if err := client.SetDeadline(deadline); err != nil {
		return err
	}
	server, err := listener.AcceptUnix()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, server.Close()) }()
	if err := server.SetDeadline(deadline); err != nil {
		return err
	}
	if _, err := client.Write([]byte("private-ipc")); err != nil {
		return err
	}
	var data [11]byte
	if _, err := io.ReadFull(server, data[:]); err != nil {
		return err
	}
	if string(data[:]) != "private-ipc" {
		return fmt.Errorf("unexpected Unix socket data %q", data)
	}
	return nil
}

func nativeRoot(t *testing.T) string {
	t.Helper()
	if err := Available(); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func nativeFile(t *testing.T, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
}

func nativeProfile(t *testing.T, p Policy) (string, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	p.ReadOnly = append(append([]string(nil), p.ReadOnly...), exe)
	profile := mustProfile(t, p)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := Check(ctx, profile); err != nil {
		t.Fatal(err)
	}
	return profile, exe
}

func nativeCommand(t *testing.T, profile string, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, Executable, append([]string{"-p", profile}, args...)...)
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C", "DEVSANDBOX_SEATBELT_TEST_HELPER=1"}
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("sandbox command timed out: %v; %s", ctx.Err(), output)
	}
	return output, err
}

func nativeOperation(t *testing.T, profile, exe string, allowed bool, args ...string) {
	t.Helper()
	output, err := nativeCommand(t, profile, append([]string{exe, "-test.run=^TestSeatbeltHelper$", "--"}, args...)...)
	if allowed {
		if err != nil {
			t.Fatalf("allowed operation %q failed: %v; %s", args, err, output)
		}
		return
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != permissionDeniedExit || !strings.Contains(string(output), "seatbelt-permission-denied") {
		t.Fatalf("operation %q did not produce a policy denial: %v; %s", args, err, output)
	}
}

func TestEnforcementFilesystem(t *testing.T) {
	root := nativeRoot(t)
	work := filepath.Join(root, "work")
	allowed := filepath.Join(work, "allowed")
	readonly := filepath.Join(work, "parent", "readonly")
	secret := filepath.Join(root, "host-secret")
	denied := filepath.Join(work, "denied", "secret")
	git := filepath.Join(work, "repo", ".git", "config")
	quoted := filepath.Join(work, `quote" (allow default) ;\résumé`)
	for _, name := range []string{allowed, readonly, secret, denied, git, quoted} {
		nativeFile(t, name)
	}
	profile, exe := nativeProfile(t, Policy{
		Writable: []string{work}, ReadOnly: []string{readonly},
		DenyRead: []string{denied}, DenyWrite: []string{filepath.Dir(git)},
	})
	for _, tc := range []struct {
		name    string
		allowed bool
		args    []string
	}{
		{"read allowed", true, []string{"read", allowed}},
		{"write allowed", true, []string{"write", allowed}},
		{"chmod writable", true, []string{"chmod", allowed}},
		{"read quoted", true, []string{"read", quoted}},
		{"write quoted", true, []string{"write", quoted}},
		{"read readonly", true, []string{"read", readonly}},
		{"write readonly", false, []string{"write", readonly}},
		{"chmod readonly", false, []string{"chmod", readonly}},
		{"read sibling", false, []string{"read", secret}},
		{"write sibling", false, []string{"write", secret}},
		{"explicit deny read", false, []string{"read", denied}},
		{"explicit deny write", false, []string{"write", denied}},
		{"read git", true, []string{"read", git}},
		{"write git", false, []string{"write", git}},
		{"rename readonly", false, []string{"rename", readonly, filepath.Join(work, "moved")}},
		{"rename readonly parent", false, []string{"rename", filepath.Dir(readonly), filepath.Join(work, "moved-parent")}},
		{"rename denied file", false, []string{"rename", denied, filepath.Join(work, "renamed-secret")}},
		{"rename denied ancestor", false, []string{"rename", filepath.Dir(denied), filepath.Join(work, "moved-secret")}},
		{"rename git parent", false, []string{"rename", filepath.Join(work, "repo"), filepath.Join(work, "moved-repo")}},
		{"create beside readonly", true, []string{"write", filepath.Join(filepath.Dir(readonly), "new")}},
		{"create beside git", true, []string{"write", filepath.Join(work, "repo", "source")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nativeOperation(t, profile, exe, tc.allowed, tc.args...)
		})
	}
	// Writable destinations must also be denied when used to replace an entry.
	nativeOperation(t, profile, exe, false, "rename", allowed, readonly)
	nativeOperation(t, profile, exe, false, "rename", allowed, denied)
	nativeOperation(t, profile, exe, false, "rename", allowed, git)
	if data, err := os.ReadFile(secret); err != nil || string(data) != "fixture" {
		t.Fatalf("host sibling was changed: %q, %v", data, err)
	}
}

func TestEnforcementAncestorMetadata(t *testing.T) {
	root := nativeRoot(t)
	work := filepath.Join(root, "host", "projects", "repo")
	source := filepath.Join(root, "host", "tools", "install", "data")
	sibling := filepath.Join(root, "host", "private", "secret")
	blocked := filepath.Join(work, "blocked")
	for _, name := range []string{filepath.Join(work, "file"), source, sibling, filepath.Join(blocked, "data")} {
		nativeFile(t, name)
	}
	profile, exe := nativeProfile(t, Policy{
		Writable: []string{work},
		// The nested grant must not make even the denied ancestor's metadata visible.
		ReadOnly: []string{source, filepath.Join(blocked, "data")},
		DenyRead: []string{blocked},
	})
	for _, name := range []string{filepath.Dir(work), filepath.Dir(source), filepath.Dir(filepath.Dir(source)), filepath.Join(root, "host")} {
		t.Run(name, func(t *testing.T) {
			nativeOperation(t, profile, exe, true, "stat", name)
			nativeOperation(t, profile, exe, true, "lstat", name)
			nativeOperation(t, profile, exe, false, "read-dir", name)
			nativeOperation(t, profile, exe, false, "chmod", name)
			nativeOperation(t, profile, exe, false, "write", filepath.Join(name, "new-file"))
			nativeOperation(t, profile, exe, false, "rename", name, name+"-moved")
		})
	}
	for _, operation := range []string{"stat", "lstat", "read", "write"} {
		nativeOperation(t, profile, exe, false, operation, sibling)
	}
	nativeOperation(t, profile, exe, false, "lstat", blocked)
	nativeOperation(t, profile, exe, false, "read", filepath.Join(blocked, "data"))
	nativeOperation(t, profile, exe, true, "read", source)
	nativeOperation(t, profile, exe, false, "write", source)
	nativeOperation(t, profile, exe, true, "read", filepath.Join(work, "file"))
	nativeOperation(t, profile, exe, true, "write", filepath.Join(work, "file"))
}

func TestEnforcementWritableRootsCannotBeReplaced(t *testing.T) {
	root := nativeRoot(t)
	parent := filepath.Join(root, "parent")
	home := filepath.Join(parent, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	profile, exe := nativeProfile(t, Policy{Writable: []string{root, home}})
	nativeOperation(t, profile, exe, false, "remove", home)
	nativeOperation(t, profile, exe, false, "rename", home, filepath.Join(root, "moved-home"))
	nativeOperation(t, profile, exe, false, "rename", parent, filepath.Join(root, "moved-parent"))
	nativeOperation(t, profile, exe, false, "rename", other, home)
	nativeOperation(t, profile, exe, true, "rename", other, filepath.Join(root, "moved-other"))
	nativeOperation(t, profile, exe, true, "write", filepath.Join(home, "new"))
	nativeOperation(t, profile, exe, true, "remove", filepath.Join(home, "new"))
}

func TestEnforcementEnvironmentFiles(t *testing.T) {
	root := nativeRoot(t)
	for _, name := range []string{".env", ".env.production", ".env.directory/secret", "nested/.env.local", ".environment", "other.env"} {
		nativeFile(t, filepath.Join(root, name))
	}
	profile, exe := nativeProfile(t, Policy{Writable: []string{root}, HideEnvFiles: true})
	for _, name := range []string{".env", ".env.production", ".env.directory/secret", "nested/.env.local"} {
		t.Run(name, func(t *testing.T) {
			nativeOperation(t, profile, exe, false, "read", filepath.Join(root, name))
			nativeOperation(t, profile, exe, false, "write", filepath.Join(root, name))
			nativeOperation(t, profile, exe, false, "rename", filepath.Join(root, name), filepath.Join(root, "renamed-env"))
		})
	}
	for _, name := range []string{".env.new", ".env.", "nested/.env.new"} {
		nativeOperation(t, profile, exe, false, "write", filepath.Join(root, name))
	}
	for _, name := range []string{".environment", "other.env"} {
		nativeOperation(t, profile, exe, true, "read", filepath.Join(root, name))
		nativeOperation(t, profile, exe, true, "write", filepath.Join(root, name))
	}
	// The regex continues to match after an enclosing directory is renamed.
	nativeOperation(t, profile, exe, true, "rename", filepath.Join(root, "nested"), filepath.Join(root, "renamed"))
	nativeOperation(t, profile, exe, false, "read", filepath.Join(root, "renamed", ".env.local"))
}

func TestEnforcementHardlinkBypass(t *testing.T) {
	root := nativeRoot(t)
	for _, name := range []string{".env", ".env.production", "secret", "allowed"} {
		nativeFile(t, filepath.Join(root, name))
	}
	// Establish that this filesystem supports hardlinks independently of policy.
	source := filepath.Join(root, "allowed")
	baseline := filepath.Join(root, "baseline-link")
	if err := os.Link(source, baseline); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(baseline); err != nil {
		t.Fatal(err)
	}
	profile, exe := nativeProfile(t, Policy{
		Writable: []string{root}, HideEnvFiles: true,
		DenyRead: []string{filepath.Join(root, "secret")},
	})
	for _, name := range []string{".env", ".env.production", "secret"} {
		t.Run(name, func(t *testing.T) {
			alias := filepath.Join(root, "public-alias")
			nativeOperation(t, profile, exe, false, "link", filepath.Join(root, name), alias)
			if _, err := os.Lstat(alias); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("hardlink alias exists or cannot be checked: %v", err)
			}
		})
	}
	nativeOperation(t, profile, exe, false, "link", source, filepath.Join(root, ".env.new"))
}

func TestEnforcementConfig(t *testing.T) {
	root := nativeRoot(t)
	name := filepath.Join(root, ".devsandbox.toml")
	nested := filepath.Join(root, "nested", ".devsandbox.toml", "child")
	nativeFile(t, name)
	nativeFile(t, nested)
	for _, hidden := range []bool{false, true} {
		t.Run(fmt.Sprintf("hidden=%t", hidden), func(t *testing.T) {
			profile, exe := nativeProfile(t, Policy{Writable: []string{root}, ProtectConfig: true, ConfigHidden: hidden})
			for _, file := range []string{name, nested} {
				nativeOperation(t, profile, exe, !hidden, "read", file)
				nativeOperation(t, profile, exe, false, "write", file)
			}
			nativeOperation(t, profile, exe, false, "write", filepath.Join(root, "nested", ".devsandbox.toml", "new"))
			nativeOperation(t, profile, exe, true, "write", filepath.Join(root, ".devsandbox.toml.backup"))
		})
	}
}

func TestEnforcementShellForkInheritance(t *testing.T) {
	root := nativeRoot(t)
	allowed := filepath.Join(root, "work", "allowed")
	secret := filepath.Join(root, "secret")
	nativeFile(t, allowed)
	nativeFile(t, secret)
	profile, _ := nativeProfile(t, Policy{Writable: []string{filepath.Dir(allowed)}})
	for _, tc := range []struct {
		name    string
		allowed bool
	}{
		{allowed, true}, {secret, false},
	} {
		output, err := nativeCommand(t, profile, "/bin/sh", "-c", `printf 'shell-started\n'; (/bin/cat "$1")`, "sh", tc.name)
		if !strings.Contains(string(output), "shell-started") {
			t.Fatalf("shell never started: %v; %s", err, output)
		}
		if tc.allowed {
			if err != nil || !strings.Contains(string(output), "fixture") {
				t.Fatalf("child failed allowed read: %v; %s", err, output)
			}
		} else if err == nil || strings.Contains(string(output), "fixture") {
			t.Fatalf("shell child escaped read denial: %v; %s", err, output)
		}
	}
}

func nativeListener(t *testing.T, network, address string) net.Listener {
	t.Helper()
	listener, err := net.Listen(network, address)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Error(err)
		}
		<-done
	})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					t.Errorf("accept test connection: %v", err)
				}
				return
			}
			if err := conn.Close(); err != nil {
				t.Error(err)
			}
		}
	}()
	// Prove that connection failures below are not caused by a dead listener.
	conn, err := net.DialTimeout(network, listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	return listener
}

func TestEnforcementNetwork(t *testing.T) {
	root := nativeRoot(t)
	proxy := nativeListener(t, "tcp4", "127.0.0.1:0")
	_, port, err := net.SplitHostPort(proxy.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	proxyPort, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	// Distinct services can occupy this same port on IPv6 and UDP. Neither is
	// the IPv4 TCP proxy, so a port-only rule must not authorize either one.
	ipv6 := nativeListener(t, "tcp6", net.JoinHostPort("::1", port))
	other4 := nativeListener(t, "tcp4", "127.0.0.1:0")
	other6 := nativeListener(t, "tcp6", "[::1]:0")
	udp, err := net.ListenPacket("udp4", proxy.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := udp.Close(); err != nil {
			t.Error(err)
		}
	})
	readPacket := func(t *testing.T) {
		t.Helper()
		if err := udp.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		var data [64]byte
		n, _, err := udp.ReadFrom(data[:])
		if err != nil || string(data[:n]) != "seatbelt-udp-test" {
			t.Fatalf("UDP fixture did not receive test packet: %q, %v", data[:n], err)
		}
	}
	// Verify UDP receipt outside Seatbelt, not just successful socket creation.
	conn, err := net.DialTimeout("udp4", udp.LocalAddr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, sendErr := conn.Write([]byte("seatbelt-udp-test"))
	if err := errors.Join(sendErr, conn.Close()); err != nil {
		t.Fatal(err)
	}
	readPacket(t)
	for _, tc := range []struct {
		name         string
		p            Policy
		proxyAllowed bool
		otherAllowed bool
	}{
		{"off", Policy{}, false, false},
		{"proxy", Policy{Network: true, ProxyPort: proxyPort}, true, false},
		{"proxy-private-unix", Policy{Network: true, ProxyPort: proxyPort, UnixSocketRoots: []string{root}}, true, false},
		{"outbound", Policy{Network: true}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile, exe := nativeProfile(t, tc.p)
			nativeOperation(t, profile, exe, tc.proxyAllowed, "dial-tcp", proxy.Addr().String())
			for _, listener := range []net.Listener{ipv6, other4, other6} {
				nativeOperation(t, profile, exe, tc.otherAllowed, "dial-tcp", listener.Addr().String())
			}
			nativeOperation(t, profile, exe, tc.otherAllowed, "send-udp", udp.LocalAddr().String())
			if tc.otherAllowed {
				readPacket(t)
			}
			for _, address := range []string{"127.0.0.1:0", "[::1]:0"} {
				nativeOperation(t, profile, exe, false, "listen-tcp", address)
			}
		})
	}
}

func nativeSocketRoot(t *testing.T) string {
	t.Helper()
	if err := Available(); err != nil {
		t.Fatal(err)
	}
	// t.TempDir includes the test name, exceeding Darwin's sockaddr_un limit.
	root, err := os.MkdirTemp("", "sb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestEnforcementUnixSocketsOutsideWritable(t *testing.T) {
	root := nativeSocketRoot(t)
	socket := filepath.Join(root, "s")
	nativeListener(t, "unix", socket)
	work := filepath.Join(root, "w")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Policy{
		{Writable: []string{work}},
		{Writable: []string{work}, Network: true},
		{Writable: []string{work}, Network: true, ProxyPort: 3128},
	} {
		profile, exe := nativeProfile(t, p)
		nativeOperation(t, profile, exe, false, "dial-unix", socket)
		nativeOperation(t, profile, exe, false, "listen-unix", filepath.Join(root, "new"))
	}
}

func TestEnforcementPrivateUnixSockets(t *testing.T) {
	root := nativeSocketRoot(t)
	home := filepath.Join(root, "h")
	private := []string{filepath.Join(home, ".runtime"), filepath.Join(home, ".tmp")}
	host := filepath.Join(root, "host")
	sibling := filepath.Join(home, ".runtime-other")
	for _, dir := range []string{private[0], private[1], host, sibling} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		nativeListener(t, "unix", filepath.Join(dir, "s"))
	}
	for i, target := range []string{host, sibling} {
		if err := os.Symlink(target, filepath.Join(private[i], "escape")); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		p    Policy
	}{
		{"default-deny", Policy{}},
		{"private-offline", Policy{UnixSocketRoots: private}},
		{"private-proxy", Policy{UnixSocketRoots: private, Network: true, ProxyPort: 3128}},
		{"private-outbound", Policy{UnixSocketRoots: private, Network: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Intentionally allow filesystem access everywhere in the fixture:
			// only socket path rules, not file denials, can block the escapes.
			tc.p.Writable = []string{root}
			profile, exe := nativeProfile(t, tc.p)
			allowed := len(tc.p.UnixSocketRoots) > 0
			for _, dir := range private {
				nativeOperation(t, profile, exe, allowed, "dial-unix", filepath.Join(dir, "s"))
				nativeOperation(t, profile, exe, allowed, "roundtrip-unix", filepath.Join(dir, "new"))
				if allowed {
					nativeOperation(t, profile, exe, false, "rename", dir, dir+"-moved")
				}
				// The path as supplied is inside an allowed root, but the
				// socket resolves outside it. Neither connect nor bind may pass.
				nativeOperation(t, profile, exe, false, "dial-unix", filepath.Join(dir, "escape", "s"))
				nativeOperation(t, profile, exe, false, "listen-unix", filepath.Join(dir, "escape", "new"))
			}
			for _, dir := range []string{host, sibling} {
				nativeOperation(t, profile, exe, false, "dial-unix", filepath.Join(dir, "s"))
				nativeOperation(t, profile, exe, false, "listen-unix", filepath.Join(dir, "new"))
			}
			for _, address := range []string{"127.0.0.1:0", "[::1]:0"} {
				nativeOperation(t, profile, exe, false, "listen-tcp", address)
			}
		})
	}
}

func TestCheckBaseline(t *testing.T) {
	profile := mustProfile(t, Policy{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := Check(ctx, profile); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		program string
		args    []string
		output  string
	}{
		{"/bin/echo", []string{"seatbelt-started"}, "seatbelt-started\n"},
		{"/usr/bin/uname", []string{"-s"}, "Darwin\n"},
		// getconf exercises libc's numeric HW_PAGESIZE query, not sysctlbyname.
		{"/usr/bin/getconf", []string{"PAGESIZE"}, strconv.Itoa(os.Getpagesize()) + "\n"},
		{"/bin/zsh", []string{"-fc", "echo seatbelt-started"}, "seatbelt-started\n"},
		{"/bin/sh", []string{"-c", "echo seatbelt-started"}, "seatbelt-started\n"},
	} {
		t.Run(filepath.Base(tc.program), func(t *testing.T) {
			output, err := nativeCommand(t, profile, append([]string{tc.program}, tc.args...)...)
			if err != nil || string(output) != tc.output {
				t.Fatalf("system program startup: %v; output %q, want %q", err, output, tc.output)
			}
		})
	}
}

func TestEnforcementBaselineRootDirectory(t *testing.T) {
	secret := filepath.Join(nativeRoot(t), "host-secret")
	nativeFile(t, secret)
	profile, exe := nativeProfile(t, Policy{})
	nativeOperation(t, profile, exe, true, "read-dir", "/")
	nativeOperation(t, profile, exe, false, "read", secret)
	nativeOperation(t, profile, exe, false, "write", secret)
}

func TestEnforcementMissingDeveloperSelector(t *testing.T) {
	missing := filepath.Join(nativeRoot(t), "developer_dir")
	profile, exe := nativeProfile(t, Policy{})
	// Exercise the production grants against an absent fixture without
	// creating or removing the host's developer-tool selection.
	for _, selector := range []string{"/var/select/developer_dir", "/private/var/select/developer_dir"} {
		profile = strings.ReplaceAll(profile, sbplString(selector), sbplString(missing))
	}
	nativeOperation(t, profile, exe, true, "readlink-missing", missing)
	readOnly := strings.ReplaceAll(profile, " file-test-existence", "")
	nativeOperation(t, readOnly, exe, false, "readlink-missing", missing)
}

func TestCheckRejectsInvalidProfile(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err := Check(ctx, "(version 1)\n(not-a-seatbelt-operation)")
	if err == nil || !strings.Contains(err.Error(), "seatbelt profile check failed") || !strings.Contains(err.Error(), "stderr:") {
		t.Fatalf("invalid profile did not report its compiler error: %v", err)
	}
}
