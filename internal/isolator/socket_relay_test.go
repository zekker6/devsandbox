package isolator

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"devsandbox/internal/sandbox"
	"devsandbox/internal/sandbox/tools"
)

// A unix socket bind-mounted into a named-volume home accepts no connections,
// so the relay replaces it on macOS. Docker on Linux keeps the bind. krun is
// deliberately excluded: its home is shared read-write over virtio-fs, so the
// host would dial a path the guest can replace with a symlink; the relay
// there needs a host-only socket directory first.
func TestNeedsSocketRelay(t *testing.T) {
	tests := []struct {
		goos    string
		microVM bool
		want    bool
	}{
		{"darwin", false, true},
		{"linux", false, false},
		{"linux", true, false},
	}
	for _, tt := range tests {
		if got := needsSocketRelay(tt.goos, tt.microVM); got != tt.want {
			t.Errorf("needsSocketRelay(%s, microVM=%v) = %v, want %v", tt.goos, tt.microVM, got, tt.want)
		}
	}
}

// relayProbeTool stands in for herdr/kitty: a proxy socket in the run
// directory plus a default-path alias, and the bind a tool emits for the alias.
type relayProbeTool struct {
	socket   string
	required bool
}

func (r *relayProbeTool) Name() string          { return "relay-probe" }
func (r *relayProbeTool) Description() string   { return "test tool" }
func (r *relayProbeTool) Available(string) bool { return true }
func (r *relayProbeTool) Bindings(homeDir, _ string) []tools.Binding {
	return []tools.Binding{{
		Source:           r.socket,
		Dest:             filepath.Join(homeDir, ".config", "probe", "probe.sock"),
		HomeRelativeDest: true,
		Type:             tools.MountBind,
		Optional:         true,
		Category:         tools.CategoryRuntime,
	}}
}
func (r *relayProbeTool) Environment(_, _ string) []tools.EnvVar { return nil }
func (r *relayProbeTool) ShellInit(string) string                { return "" }
func (r *relayProbeTool) RelaySockets(homeDir string) []tools.RelaySocket {
	return []tools.RelaySocket{{
		Name:     "probe.sock",
		Aliases:  []string{filepath.Join(homeDir, ".config", "probe", "probe.sock")},
		Required: r.required,
	}}
}

func TestRelaySocketsFor_HonorsDisabledMountMode(t *testing.T) {
	probe := &relayProbeTool{socket: "/nonexistent/probe.sock"}
	tools.Register(probe)
	defer tools.Unregister(probe.Name())
	iso := NewDockerIsolator(DockerConfig{})
	cfg := &Config{
		HomeDir: t.TempDir(), SandboxHome: filepath.Join(t.TempDir(), "home"), ProjectDir: t.TempDir(), Shell: "bash",
		ToolsConfig: map[string]any{"relay-probe": map[string]any{"mount_mode": "disabled"}},
	}
	for _, s := range iso.relaySocketsFor(cfg) {
		if s.Name == "probe.sock" {
			t.Fatalf("relaySocketsFor includes a disabled tool: %+v", s)
		}
	}
}

// A relay the user asked for by name (`mode = "enforce"`) that cannot start
// fails the launch, as Herdr.Start does for an unreachable host socket. In
// auto mode the same failure is announced and the launch goes on.
func TestStartSocketRelayIfNeeded_RequiredFailureAborts(t *testing.T) {
	for _, required := range []bool{false, true} {
		probe := &relayProbeTool{socket: "/nonexistent/probe.sock", required: required}
		tools.Register(probe)
		iso := NewDockerIsolator(DockerConfig{})
		iso.relaySockets = true
		cfg := &Config{HomeDir: t.TempDir(), SandboxHome: filepath.Join(t.TempDir(), "home"), ProjectDir: t.TempDir(), Shell: "bash"}
		// /usr/bin/false stands in for an engine whose exec exits before READY.
		stop, err := iso.startSocketRelayIfNeeded(context.Background(), "/usr/bin/false", cfg, "container")
		stop()
		tools.Unregister(probe.Name())
		if (err != nil) != required {
			t.Fatalf("required=%v: err = %v", required, err)
		}
	}
}

// A relay that dies after READY takes herdr and kitty with it; the session
// has to hear about that rather than find every request hanging.
func TestSocketRelay_ExitAfterReadyIsReported(t *testing.T) {
	shim := buildShim(t)
	sandboxHome := shortSandboxHome(t)
	hostSock := listenProbeSocket(t, sandboxHome)
	guestDir := filepath.Join(filepath.Dir(sandboxHome), "guest")
	specs := []relaySocketSpec{{Name: "probe.sock", HostPath: hostSock, GuestPaths: []string{filepath.Join(guestDir, "probe.sock")}}}

	reported := make(chan error, 1)
	cmd := exec.Command(shim, relayArgv(specs)...)
	relay, err := startSocketRelay(context.Background(), cmd, specs, 5*time.Second, nil, func(err error) { reported <- err })
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-reported:
		if err == nil {
			t.Fatal("exit reported with a nil error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay death after READY was not reported")
	}

	// A deliberate Stop is not an incident.
	cmd = exec.Command(shim, relayArgv(specs)...)
	relay, err = startSocketRelay(context.Background(), cmd, specs, 5*time.Second, nil, func(err error) { reported <- err })
	if err != nil {
		t.Fatal(err)
	}
	relay.Stop()
	select {
	case err := <-reported:
		t.Fatalf("Stop was reported as an exit: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
}

// The ephemeral run path cannot host a relay; under enforce that is a failed
// launch, otherwise an announcement.
func TestRunPathRelayCheck_RequiredAborts(t *testing.T) {
	for _, required := range []bool{false, true} {
		probe := &relayProbeTool{socket: "/nonexistent/probe.sock", required: required}
		tools.Register(probe)
		iso := NewDockerIsolator(DockerConfig{})
		iso.relaySockets = true
		cfg := &Config{HomeDir: t.TempDir(), SandboxHome: filepath.Join(t.TempDir(), "home"), ProjectDir: t.TempDir(), Shell: "bash"}
		err := iso.checkRunPathRelay(cfg)
		tools.Unregister(probe.Name())
		if (err != nil) != required {
			t.Fatalf("required=%v: err = %v", required, err)
		}
	}
}

// A guest that never reaches READY is reported once, by the start error;
// onExit is for a relay that was up and died.
func TestSocketRelay_NoExitReportBeforeReady(t *testing.T) {
	cmd := exec.Command("sh", "-c", "echo 'unknown command' >&2; exit 2")
	exits := make(chan error, 1)
	_, err := startSocketRelay(context.Background(), cmd, []relaySocketSpec{{Name: "x", HostPath: "/nonexistent", GuestPaths: []string{"/tmp/x"}}}, 5*time.Second, nil, func(err error) { exits <- err })
	if err == nil {
		t.Fatal("startSocketRelay succeeded without a READY")
	}
	select {
	case err := <-exits:
		t.Fatalf("onExit fired for a guest that never became ready: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
}

// A stderr line over the scanner's limit stops the scanner; the pipe still has
// to be drained or the guest blocks on a full pipe and the exit never arrives.
func TestSocketRelay_DrainsStderrPastScannerLimit(t *testing.T) {
	cmd := exec.Command("sh", "-c", "head -c 300000 /dev/zero | tr '\\0' a >&2; echo >&2; echo 'last line' >&2; exit 1")
	start := time.Now()
	_, err := startSocketRelay(context.Background(), cmd, []relaySocketSpec{{Name: "x", HostPath: "/nonexistent", GuestPaths: []string{"/tmp/x"}}}, 10*time.Second, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "exited before ready") {
		t.Fatalf("err = %v, want the guest's exit (elapsed %s)", err, time.Since(start))
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("exit took %s; the stderr pipe was not drained", time.Since(start))
	}
}

// A guest that dies straight after READY races the start's select against the
// exit path. Whichever wins, the relay was up, so the session hears exactly one
// report and it comes from onExit: a start error would call a relay that became
// ready "exited before ready", and a skipped report would lose the death.
func TestSocketRelay_ExitRightAfterReadyIsReportedOnce(t *testing.T) {
	const readyFrame = `\005\000\000\000\000\000\000\000\000`
	specs := []relaySocketSpec{{Name: "x", HostPath: "/nonexistent", GuestPaths: []string{"/tmp/x"}}}
	for i := range 200 {
		exits := make(chan error, 2)
		cmd := exec.Command("sh", "-c", "exec 2>&-; printf '"+readyFrame+"'")
		relay, err := startSocketRelay(context.Background(), cmd, specs, 5*time.Second, nil, func(err error) { exits <- err })
		if err != nil {
			t.Fatalf("iteration %d: start failed for a guest that sent READY: %v", i, err)
		}
		<-relay.done
		if len(exits) != 1 {
			t.Fatalf("iteration %d: got %d exit reports, want 1", i, len(exits))
		}
	}
}

func TestTailWriter_KeepsLastBytes(t *testing.T) {
	w := newTailWriter(8)
	_, _ = w.Write([]byte("0123456789"))
	_, _ = w.Write([]byte("ab"))
	if got := w.String(); got != "456789ab" {
		t.Fatalf("tail = %q, want the last 8 bytes", got)
	}
}

func listenProbeSocket(t *testing.T, sandboxHome string) string {
	t.Helper()
	dir := tools.RunDir(sandboxHome)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "probe.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return path
}

// shortSandboxHome returns a sandbox home short enough for a unix socket path
// under its run directory on macOS.
func shortSandboxHome(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "sr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return filepath.Join(root, "home")
}

func TestGetToolBindings_DropsSocketBindsWhenRelaying(t *testing.T) {
	sandboxHome := shortSandboxHome(t)
	sock := listenProbeSocket(t, sandboxHome)
	probe := &relayProbeTool{socket: sock}
	tools.Register(probe)
	defer tools.Unregister(probe.Name())
	cfg := &Config{HomeDir: t.TempDir(), SandboxHome: sandboxHome, ProjectDir: t.TempDir(), Shell: "bash"}

	for _, relaying := range []bool{false, true} {
		iso := NewDockerIsolator(DockerConfig{})
		iso.relaySockets = relaying
		mounts, _, _ := iso.getToolBindings(cfg)
		has := slices.ContainsFunc(mounts, func(m string) bool { return strings.HasPrefix(m, sock+":") })
		if has == relaying {
			t.Errorf("relaying=%v: socket bind present=%v; a bind-mounted socket is dead on a relay backend and must stay on the others", relaying, has)
		}
	}
}

func TestRelaySocketsFor_MapsGuestPathsToContainerHome(t *testing.T) {
	probe := &relayProbeTool{socket: "/nonexistent/probe.sock"}
	tools.Register(probe)
	defer tools.Unregister(probe.Name())
	homeDir := t.TempDir()
	iso := NewDockerIsolator(DockerConfig{})
	cfg := &Config{HomeDir: homeDir, SandboxHome: filepath.Join(t.TempDir(), "home"), ProjectDir: t.TempDir(), Shell: "bash"}

	got := iso.relaySocketsFor(cfg)
	guestRun := tools.RunDir(containerHome)
	want := relaySocketSpec{
		Name:       "probe.sock",
		HostPath:   filepath.Join(tools.RunDir(cfg.SandboxHome), "probe.sock"),
		GuestPaths: []string{filepath.Join(guestRun, "probe.sock"), containerHome + "/.config/probe/probe.sock"},
	}
	idx := slices.IndexFunc(got, func(s relaySocketSpec) bool { return s.Name == "probe.sock" })
	if idx < 0 {
		t.Fatalf("relaySocketsFor did not include the probe: %+v", got)
	}
	if g := got[idx]; g.HostPath != want.HostPath || !slices.Equal(g.GuestPaths, want.GuestPaths) {
		t.Fatalf("spec = %+v, want %+v", g, want)
	}
}

// The relay exec is one more argv path that must carry the session's proxy
// credential by name only, and must run as the sandbox user so the sockets it
// creates in the sandbox home are connectable by the workload.
func TestRelayExecArgs_ShapeAndNoCredentialInArgv(t *testing.T) {
	const token = "fedcba9876543210fedcba9876543210"
	iso := NewDockerIsolator(DockerConfig{})
	iso.imageTag = "test:latest"
	cfg := &Config{
		ProjectDir: "/tmp/test-project", SandboxHome: "/tmp/test-sandbox", HomeDir: "/home/testuser", Shell: "/bin/bash",
		ProxyEnabled: true, ProxyHost: "10.0.2.2", ProxyPort: 8081, ProxyAuthToken: token,
	}
	specs := []relaySocketSpec{{Name: "herdr.sock", GuestPaths: []string{"/home/sandboxuser/.run/7/herdr.sock", "/home/sandboxuser/.config/herdr/herdr.sock"}}}
	argv := iso.relayExecArgs(cfg, "container", specs)

	for _, arg := range argv {
		if strings.Contains(arg, token) {
			t.Errorf("argv carries the session token in %q", arg)
		}
	}
	if argv[0] != "exec" || !slices.Contains(argv, "-i") || slices.Contains(argv, "-it") {
		t.Errorf("argv = %q; want a non-tty exec", argv)
	}
	uid := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	if i := slices.Index(argv, "-u"); i < 0 || argv[i+1] != uid {
		t.Errorf("argv = %q; want -u %s", argv, uid)
	}
	tail := []string{"container", shimPath, "socket-relay", "herdr.sock=/home/sandboxuser/.run/7/herdr.sock,/home/sandboxuser/.config/herdr/herdr.sock"}
	if len(argv) < len(tail) || !slices.Equal(argv[len(argv)-len(tail):], tail) {
		t.Errorf("argv tail = %q, want %q", argv, tail)
	}
}

// buildShim compiles the real shim so the host end is exercised against the
// guest end it will drive in production, over real pipes.
func buildShim(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "devsandbox-shim")
	cmd := exec.Command("go", "build", "-o", out, "devsandbox/cmd/devsandbox-shim")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build shim: %v\n%s", err, msg)
	}
	return out
}

func TestSocketRelay_EndToEndThroughShim(t *testing.T) {
	shim := buildShim(t)
	sandboxHome := shortSandboxHome(t)
	hostSock := listenProbeSocket(t, sandboxHome)
	guestDir := filepath.Join(filepath.Dir(sandboxHome), "guest")
	specs := []relaySocketSpec{{
		Name:       "probe.sock",
		HostPath:   hostSock,
		GuestPaths: []string{filepath.Join(guestDir, "probe.sock"), filepath.Join(guestDir, "alias", "probe.sock")},
	}}

	cmd := exec.Command(shim, relayArgv(specs)...)
	relay, err := startSocketRelay(context.Background(), cmd, specs, 5*time.Second, nil, nil)
	if err != nil {
		t.Fatalf("startSocketRelay: %v", err)
	}

	for _, p := range specs[0].GuestPaths {
		c, err := net.Dial("unix", p)
		if err != nil {
			t.Fatalf("dial guest %s: %v", p, err)
		}
		if _, err := c.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
			t.Fatalf("echo via %s = %q, %v", p, buf, err)
		}
		_ = c.Close()
	}

	relay.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for _, p := range specs[0].GuestPaths {
		for {
			if _, err := os.Lstat(p); os.IsNotExist(err) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("guest socket %s survived Stop", p)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// An image older than this devsandbox has a shim without the subcommand. The
// exec then exits with a usage error before READY, which must surface as a
// named error rather than a bare timeout.
func TestSocketRelay_OldShimFailsBeforeReady(t *testing.T) {
	// An old shim treats the subcommand as a workload and runs its whole
	// root-phase setup first, so stderr is several lines of noise ending in
	// the one that matters.
	cmd := exec.Command("sh", "-c", "echo 'noise line' >&2; echo 'unknown command' >&2; exit 2")
	_, err := startSocketRelay(context.Background(), cmd, []relaySocketSpec{{Name: "x", HostPath: "/nonexistent", GuestPaths: []string{"/tmp/x"}}}, 5*time.Second, nil, nil)
	if err == nil {
		t.Fatal("startSocketRelay succeeded without a READY")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("error does not carry the guest's last stderr line: %v", err)
	}
	if strings.Contains(err.Error(), "noise line") {
		t.Fatalf("error carries every stderr line instead of the last: %v", err)
	}
}

// execEnvProbeTool exports a per-process socket path, as herdr and kitty do.
type execEnvProbeTool struct{}

func (execEnvProbeTool) Name() string                         { return "env-probe" }
func (execEnvProbeTool) Description() string                  { return "test tool" }
func (execEnvProbeTool) Available(string) bool                { return true }
func (execEnvProbeTool) Bindings(_, _ string) []tools.Binding { return nil }
func (execEnvProbeTool) ShellInit(string) string              { return "" }
func (execEnvProbeTool) Environment(homeDir, _ string) []tools.EnvVar {
	return []tools.EnvVar{
		{Name: "ENV_PROBE_SOCK", Value: filepath.Join(tools.RunDir(homeDir), "probe.sock"), Session: true},
		{Name: "ENV_PROBE_EDITOR", Value: "nvim"},
	}
}

// A kept container carries the tool environment of the session that created
// it, and a socket path keyed on that session's pid is dead for every later
// one. The workload exec has to carry the current values, remapped for the
// container home exactly as `docker create` remaps them.
func TestBuildExecArgs_CarriesCurrentToolEnv(t *testing.T) {
	probe := execEnvProbeTool{}
	tools.Register(probe)
	defer tools.Unregister(probe.Name())
	homeDir := t.TempDir()
	iso := NewDockerIsolator(DockerConfig{})
	cfg := &Config{HomeDir: homeDir, SandboxHome: filepath.Join(t.TempDir(), "home"), ProjectDir: t.TempDir(), Shell: "bash"}

	argv := iso.buildExecArgs(cfg, "container")
	want := "ENV_PROBE_SOCK=" + filepath.Join(tools.RunDir(containerHome), "probe.sock")
	i := slices.Index(argv, want)
	if i < 1 || argv[i-1] != "-e" {
		t.Fatalf("exec argv %q does not carry -e %s", argv, want)
	}
	if c := slices.Index(argv, "container"); c < i {
		t.Fatalf("tool env %q placed after the container name in %q", want, argv)
	}
	// A value that is stable across sessions was set at create time and is
	// still right; re-sending it would override the user's own setting for
	// the same name, which docker create emitted after the tool's.
	if slices.ContainsFunc(argv, func(a string) bool { return strings.HasPrefix(a, "ENV_PROBE_EDITOR=") }) {
		t.Fatalf("exec argv %q re-sends a non-session tool variable", argv)
	}
}

// The user's own value for a session variable wins at create time and must
// keep winning on exec, which `-e` would otherwise override.
func TestBuildExecArgs_SessionEnvYieldsToUserConfig(t *testing.T) {
	probe := execEnvProbeTool{}
	tools.Register(probe)
	defer tools.Unregister(probe.Name())
	iso := NewDockerIsolator(DockerConfig{})
	cfg := &Config{
		HomeDir: t.TempDir(), SandboxHome: filepath.Join(t.TempDir(), "home"), ProjectDir: t.TempDir(), Shell: "bash",
		EnvVars: map[string]string{"ENV_PROBE_SOCK": "/custom.sock"},
	}
	argv := iso.buildExecArgs(cfg, "container")
	if slices.ContainsFunc(argv, func(a string) bool { return strings.HasPrefix(a, "ENV_PROBE_SOCK=") }) {
		t.Fatalf("exec argv %q overrides a user-configured variable", argv)
	}
}

// An empty home directory must not turn the substring replace into an insert
// between every byte of the value.
func TestRemapEnvValue_EmptyHomeIsNoop(t *testing.T) {
	if got := remapEnvValue("/opt/go", "", "/proj"); got != "/opt/go" {
		t.Fatalf("remapEnvValue with empty home = %q, want the value unchanged", got)
	}
}

func TestGuestHomeFor(t *testing.T) {
	if got := GuestHomeFor(sandbox.IsolationDocker); got != containerHome {
		t.Errorf("docker = %q, want %q", got, containerHome)
	}
	if got := GuestHomeFor(sandbox.IsolationKrun); got != containerHome {
		t.Errorf("krun = %q, want %q", got, containerHome)
	}
	if got := GuestHomeFor(sandbox.IsolationBwrap); got != "" {
		t.Errorf("bwrap = %q, want empty: the home is bound at the host path", got)
	}
}
