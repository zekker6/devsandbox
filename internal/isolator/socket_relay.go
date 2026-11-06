package isolator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

// needsSocketRelay reports whether the engine mounts the sandbox home in a way
// that a bind-mounted unix socket accepts no connections: the named volume on
// macOS and the virtio-fs share of a microVM. Docker on Linux binds the home
// under the same kernel, where the socket in the run directory just works.
func needsSocketRelay(goos string, microVM bool) bool {
	return goos == "darwin" || microVM
}

// relaySocketSpec is one socket as both ends see it: the host path the relay
// dials, and the guest paths the shim listens on (primary first, then aliases).
type relaySocketSpec struct {
	Name       string
	HostPath   string
	GuestPaths []string
}

// relaySocketsFor collects the sockets of every available tool that declares
// them. The host path comes from the same RunDir the tool listens under, and
// the guest primary from RunDir of the container home, which is where the
// tool's own Environment points once remapEnvValue has run.
func (d *DockerIsolator) relaySocketsFor(cfg *Config) []relaySocketSpec {
	var specs []relaySocketSpec
	for _, tool := range tools.Available(cfg.HomeDir) {
		rt, ok := tool.(tools.ToolWithRelaySockets)
		if !ok {
			continue
		}
		for _, s := range rt.RelaySockets(cfg.HomeDir) {
			spec := relaySocketSpec{
				Name:       s.Name,
				HostPath:   filepath.Join(tools.RunDir(cfg.SandboxHome), s.Name),
				GuestPaths: []string{filepath.Join(tools.RunDir(containerHome), s.Name)},
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
	cmd  *exec.Cmd
	host *sockrelay.Host
	done chan struct{}
}

// pipeRW joins the exec's stdin and stdout into the relay's byte stream.
type pipeRW struct {
	io.Reader
	io.WriteCloser
}

func (p pipeRW) Close() error { return p.WriteCloser.Close() }

// startSocketRelay starts cmd as the guest relay and waits for its READY, so
// the sockets exist before the workload that will connect to them is exec'd.
// The host end dials only the HostPath registered under each name.
func startSocketRelay(ctx context.Context, cmd *exec.Cmd, specs []relaySocketSpec, timeout time.Duration) (*socketRelay, error) {
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
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("socket relay: start: %w", err)
	}

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
		_ = r.host.Run(ctx)
		exited <- cmd.Wait()
	}()

	select {
	case <-r.host.Ready():
		return r, nil
	case err := <-exited:
		return nil, fmt.Errorf("socket relay: guest exited before ready (%v): %s", err, lastStderrLine(stderr.String()))
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
// on without it: the sockets are optional integrations, and the warning has to
// reach the terminal, which in the running phase means Alert.
func (d *DockerIsolator) startSocketRelayIfNeeded(ctx context.Context, dockerBinary string, cfg *Config, containerName string) func() {
	noop := func() {}
	if !d.relaySockets {
		return noop
	}
	specs := d.relaySocketsFor(cfg)
	if len(specs) == 0 {
		return noop
	}
	names := make([]string, 0, len(specs))
	for _, s := range specs {
		names = append(names, s.Name)
	}
	relay, err := startSocketRelay(ctx, d.relayExecCommand(dockerBinary, cfg, containerName, specs), specs, relayReadyTimeout)
	if err != nil {
		notice.Alert("%v; %s unreachable from the sandbox. If the image predates this devsandbox: `docker pull %s`, `devsandbox image build`, then remove the kept container so it is recreated",
			err, strings.Join(names, ", "), DefaultImage)
		return noop
	}
	d.logInfo("socket relay up for %s", strings.Join(names, ", "))
	return relay.Stop
}

// errNoRelayForRun is the reason the ephemeral `docker run --rm` path gets no
// relay: an anonymous container cannot be exec'd into, and a named one would
// change that path's collision semantics on Docker.
var errNoRelayForRun = errors.New("socket relay needs a kept container; set [sandbox.docker] keep_container = true")
