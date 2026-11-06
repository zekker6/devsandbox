package isolator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"devsandbox/internal/config"
	"devsandbox/internal/notice"
	"devsandbox/internal/sandbox/tools"
	"devsandbox/internal/sockrelay"
	"devsandbox/internal/termsafe"
)

// shimPath is where the image carries devsandbox-shim (docker/Dockerfile).
const shimPath = "/devsandbox-shim"

// relayReadyTimeout bounds the wait for the guest relay's READY. The exec
// itself is a docker round trip; a guest that has not listened within this is
// an old shim or a dead container, and the launch goes on without the relay.
const relayReadyTimeout = 15 * time.Second

// relayStderrTail bounds how much of the guest's stderr is kept for an error
// message. The relay lives as long as the session, so an unbounded buffer
// would be a guest-driven allocation on the host.
const relayStderrTail = 4096

// needsSocketRelay reports whether the engine mounts the sandbox home in a way
// that a bind-mounted unix socket accepts no connections: the named volume on
// macOS. Docker on Linux binds the home under the same kernel, where the
// socket in the run directory just works.
//
// krun has the same problem (its home is a virtio-fs share) and deliberately
// gets no relay yet: that share is guest-writable, so the host would dial a
// path under RunDir(sandboxHome) that the guest can replace with a symlink to
// the real herdr or kitty socket, and the guest's listener would unlink the
// proxy's own socket file. A krun relay needs the proxies to listen in a
// host-only directory first.
func needsSocketRelay(goos string, microVM bool) bool {
	return goos == "darwin" && !microVM
}

// relaySocketSpec is one socket as both ends see it: the host path the relay
// dials, and the guest paths the shim listens on (primary first, then aliases).
type relaySocketSpec struct {
	Name       string
	HostPath   string
	GuestPaths []string
	Required   bool
}

// relaySocketsFor collects the sockets of every enabled tool that declares
// them. The host path comes from the same RunDir the tool listens under, and
// the guest primary from RunDir of the container home, which is where the
// tool's own Environment points once remapEnvValue has run.
func (d *DockerIsolator) relaySocketsFor(cfg *Config) []relaySocketSpec {
	var specs []relaySocketSpec
	for _, tool := range tools.Available(cfg.HomeDir) {
		rt, ok := tool.(tools.ToolWithRelaySockets)
		if !ok || config.ToolMountMode(cfg.ToolsConfig, tool.Name()) == "disabled" {
			continue
		}
		for _, s := range rt.RelaySockets(cfg.HomeDir) {
			spec := relaySocketSpec{
				Name:       s.Name,
				HostPath:   filepath.Join(tools.RunDir(cfg.SandboxHome), s.Name),
				GuestPaths: []string{filepath.Join(tools.RunDir(containerHome), s.Name)},
				Required:   s.Required,
			}
			for _, alias := range s.Aliases {
				spec.GuestPaths = append(spec.GuestPaths, remapHomePrefix(alias, cfg.HomeDir))
			}
			specs = append(specs, spec)
		}
	}
	return specs
}

// relayArgv renders the shim subcommand and its name=path[,path...] operands.
func relayArgv(specs []relaySocketSpec) []string {
	argv := []string{"socket-relay"}
	for _, s := range specs {
		argv = append(argv, s.Name+"="+strings.Join(s.GuestPaths, ","))
	}
	return argv
}

// relayExecArgs builds the `docker exec` that runs the guest relay: stdin
// attached and no TTY, since stdio is the relay's byte stream; as the sandbox
// user, so the sockets it creates are the workload's to connect to; with the
// proxy variables named bare, exactly as buildExecArgs does, because this is
// one more exec path and commandEnv values them.
func (d *DockerIsolator) relayExecArgs(cfg *Config, containerName string, specs []relaySocketSpec) []string {
	args := []string{"exec", "-i", "-u", sandboxUserSpec()}
	args = append(args, d.proxyEnvArgs(cfg)...)
	args = append(args, containerName, shimPath)
	return append(args, relayArgv(specs)...)
}

// relayExecCommand is the exec.Cmd for the guest relay.
func (d *DockerIsolator) relayExecCommand(dockerBinary string, cfg *Config, containerName string, specs []relaySocketSpec) *exec.Cmd {
	cmd := exec.Command(dockerBinary, d.relayExecArgs(cfg, containerName, specs)...)
	cmd.Env = d.commandEnv(cfg)
	return cmd
}

// socketRelay is a running relay: the exec and the host end of the protocol.
type socketRelay struct {
	cmd     *exec.Cmd
	host    *sockrelay.Host
	done    chan struct{}
	stopped atomic.Bool
}

// pipeRW joins the exec's stdin and stdout into the relay's byte stream.
type pipeRW struct {
	io.Reader
	io.WriteCloser
}

func (p pipeRW) Close() error { return p.WriteCloser.Close() }

// tailWriter keeps the last n bytes written to it.
type tailWriter struct {
	mu  sync.Mutex
	n   int
	buf []byte
}

func newTailWriter(n int) *tailWriter { return &tailWriter{n: n} }

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.n {
		w.buf = append([]byte(nil), w.buf[len(w.buf)-w.n:]...)
	}
	return len(p), nil
}

func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

// startSocketRelay starts cmd as the guest relay and waits for its READY, so
// the sockets exist before the workload that will connect to them is exec'd.
// The host end dials only the HostPath registered under each name. Guest
// stderr lines go to logf as they arrive and the tail is kept for errors.
// onExit is called with the guest's exit error if it ends for any reason other
// than Stop; a relay dying mid-session takes every exposed socket with it, and
// the session must hear about that rather than find each request hanging.
func startSocketRelay(ctx context.Context, cmd *exec.Cmd, specs []relaySocketSpec, timeout time.Duration, logf func(string, ...any), onExit func(error)) (*socketRelay, error) {
	allowed := make(map[string]string, len(specs))
	for _, s := range specs {
		allowed[s.Name] = s.HostPath
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("socket relay: stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("socket relay: stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("socket relay: stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("socket relay: start: %w", err)
	}

	tail := newTailWriter(relayStderrTail)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 4096), sockrelay.MaxPayload)
		for sc.Scan() {
			line := sc.Bytes()
			_, _ = tail.Write(append(line, '\n'))
			if logf != nil {
				logf("socket relay: %s", termsafe.Escape(string(line)))
			}
		}
	}()

	r := &socketRelay{
		cmd:  cmd,
		host: sockrelay.NewHost(pipeRW{Reader: stdout, WriteCloser: stdin}, allowed),
		done: make(chan struct{}),
	}
	exited := make(chan error, 1)
	go func() {
		defer close(r.done)
		// Run returns when the guest closes its stdout, which happens when the
		// guest process exits for any reason.
		runErr := r.host.Run(ctx)
		<-stderrDone
		waitErr := cmd.Wait()
		err := errors.Join(runErr, waitErr)
		if err == nil {
			err = errors.New("exited")
		}
		exited <- err
		if !r.stopped.Load() && onExit != nil {
			onExit(fmt.Errorf("%w: %s", err, lastStderrLine(tail.String())))
		}
	}()

	select {
	case <-r.host.Ready():
		return r, nil
	case err := <-exited:
		return nil, fmt.Errorf("socket relay: guest exited before ready (%v): %s", err, lastStderrLine(tail.String()))
	case <-time.After(timeout):
		r.Stop()
		return nil, fmt.Errorf("socket relay: guest not ready within %s", timeout)
	}
}

// lastStderrLine reduces the guest's stderr to its final non-empty line,
// escaped for the terminal. An old shim runs its entire root-phase setup
// before failing, so the line that explains the exit is the last one.
func lastStderrLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" {
		return "no output"
	}
	return termsafe.Truncate(termsafe.Escape(last), 200)
}

// Stop ends the relay. Closing the host end closes the guest's stdin, which
// ends ServeGuest and removes its sockets; the exec is then reaped.
func (r *socketRelay) Stop() {
	r.stopped.Store(true)
	_ = r.host.Close()
	select {
	case <-r.done:
	case <-time.After(3 * time.Second):
		if r.cmd.Process != nil {
			_ = r.cmd.Process.Kill()
		}
		<-r.done
	}
}

// startSocketRelayIfNeeded brings the guest relay up ahead of the workload
// exec when this engine needs one and a tool has a socket to expose, and
// returns what stops it. A relay that fails is announced and the launch goes
// on without it, unless a socket is Required - asked for by name under
// `mode = "enforce"` - in which case the launch fails, as the tool's own Start
// would. The warning has to reach the terminal, which in the running phase
// means Alert.
func (d *DockerIsolator) startSocketRelayIfNeeded(ctx context.Context, dockerBinary string, cfg *Config, containerName string) (func(), error) {
	noop := func() {}
	if !d.relaySockets {
		return noop, nil
	}
	specs := d.relaySocketsFor(cfg)
	if len(specs) == 0 {
		return noop, nil
	}
	names := make([]string, 0, len(specs))
	required := false
	for _, s := range specs {
		names = append(names, s.Name)
		required = required || s.Required
	}
	joined := strings.Join(names, ", ")
	onExit := func(err error) {
		notice.Alert("socket relay ended (%v); %s unreachable from the sandbox for the rest of this session", err, joined)
	}
	relay, err := startSocketRelay(ctx, d.relayExecCommand(dockerBinary, cfg, containerName, specs), specs, relayReadyTimeout, d.logInfo, onExit)
	if err != nil {
		hint := fmt.Sprintf("If the image predates this devsandbox: `docker pull %s`, `devsandbox image build`, then remove the kept container so it is recreated", DefaultImage)
		if required {
			return noop, fmt.Errorf("%w; %s is required by mode = \"enforce\". %s", err, joined, hint)
		}
		notice.Alert("%v; %s unreachable from the sandbox. %s", err, joined, hint)
		return noop, nil
	}
	d.logInfo("socket relay up for %s", joined)
	return relay.Stop, nil
}

// errNoRelayForRun is the reason the ephemeral `docker run --rm` path gets no
// relay: an anonymous container cannot be exec'd into, and a named one would
// change that path's collision semantics on Docker.
var errNoRelayForRun = errors.New("socket relay needs a kept container; set [sandbox.docker] keep_container = true")
