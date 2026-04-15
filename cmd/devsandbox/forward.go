package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"devsandbox/internal/notice"
	"devsandbox/internal/portforward"
	"devsandbox/internal/session"
)

func newForwardCmd() *cobra.Command {
	var name string
	var bind string

	cmd := &cobra.Command{
		Use:   "forward [flags] <port_spec> [port_spec...]",
		Short: "Forward ports to a running sandbox",
		Long: `Forward one or more ports from the host into a running sandbox.

Port spec format: <sandbox_port>[:<host_port>]

If host_port is omitted, it defaults to sandbox_port. The forwarder runs
in the foreground until Ctrl+C is pressed or the sandbox exits.`,
		Example: `  devsandbox forward 3000
  devsandbox forward 3000:8080
  devsandbox forward --name myproject 3000 5432:5433
  devsandbox forward --bind 0.0.0.0 3000`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runForward(cmd.Context(), name, bind, args)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Target sandbox by name (auto-select if one running)")
	cmd.Flags().StringVarP(&bind, "bind", "b", "127.0.0.1", "Bind address for host-side listeners")

	return cmd
}

// parsePortSpec parses a port spec of the form "<sandbox_port>" or
// "<sandbox_port>:<host_port>". Both ports must be in the range 1–65535.
func parsePortSpec(spec string) (sandboxPort, hostPort int, err error) {
	parts := strings.Split(spec, ":")
	switch len(parts) {
	case 1:
		p, e := parsePort(parts[0])
		if e != nil {
			return 0, 0, fmt.Errorf("invalid port spec %q: %w", spec, e)
		}
		return p, p, nil
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return 0, 0, fmt.Errorf("invalid port spec %q: both sandbox_port and host_port must be specified", spec)
		}
		sp, e := parsePort(parts[0])
		if e != nil {
			return 0, 0, fmt.Errorf("invalid port spec %q: sandbox port: %w", spec, e)
		}
		hp, e := parsePort(parts[1])
		if e != nil {
			return 0, 0, fmt.Errorf("invalid port spec %q: host port: %w", spec, e)
		}
		return sp, hp, nil
	default:
		return 0, 0, fmt.Errorf("invalid port spec %q: expected <sandbox_port>[:<host_port>]", spec)
	}
}

// parsePort parses a single port number string and validates it is in range 1–65535.
func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("not a number: %q", s)
	}
	if n < 1 || n > 65535 {
		return 0, fmt.Errorf("port %d out of range (1–65535)", n)
	}
	return n, nil
}

// resolveSession picks the session to operate on.
//
// When name is non-empty, it is looked up directly. Otherwise, all live
// sessions whose WorkDir matches cwd are considered:
//   - exactly one match: that session is returned.
//   - zero matches: an error with a hint listing any live sessions elsewhere.
//   - more than one match: an error asking the user to pass --name.
func resolveSession(store *session.Store, name, cwd string) (*session.Session, error) {
	if name != "" {
		return store.Get(name)
	}

	matches, err := store.FindByWorkDir(cwd)
	if err != nil {
		return nil, err
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, noSandboxInCWDError(store, cwd)
	default:
		return nil, fmt.Errorf("multiple sandboxes running in %s: %s; specify --name",
			cwd, strings.Join(sessionNames(matches), ", "))
	}
}

// noSandboxInCWDError builds a helpful error for the zero-match case. When
// other live sessions exist, their names are appended as a hint so the user
// knows what they could target with --name.
func noSandboxInCWDError(store *session.Store, cwd string) error {
	live, err := store.ListLive()
	if err != nil || len(live) == 0 {
		return fmt.Errorf("no sandbox running in %s", cwd)
	}
	return fmt.Errorf("no sandbox running in %s (live elsewhere: %s); pass --name to target one",
		cwd, strings.Join(sessionNames(live), ", "))
}

func sessionNames(sessions []*session.Session) []string {
	names := make([]string, len(sessions))
	for i, s := range sessions {
		names[i] = s.Name
	}
	return names
}

func runForward(_ context.Context, name, bind string, portSpecs []string) error {
	// 1. Open store and clean stale sessions.
	store, err := session.DefaultStore()
	if err != nil {
		return fmt.Errorf("open session store: %w", err)
	}
	store.CleanStale()

	// 2. Find target session.
	var sess *session.Session
	if name != "" {
		sess, err = store.Get(name)
		if err != nil {
			return err
		}
	} else {
		sess, err = store.FindSingle()
		if err != nil {
			return err
		}
	}

	// 3. Parse all port specs.
	type portMapping struct{ sandboxPort, hostPort int }
	mappings := make([]portMapping, 0, len(portSpecs))
	for _, spec := range portSpecs {
		sp, hp, e := parsePortSpec(spec)
		if e != nil {
			return e
		}
		mappings = append(mappings, portMapping{sp, hp})
	}

	// 4. Create namespace dialer and start forwarders.
	dialer := portforward.NewNamespaceDialer(sess.PID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var forwarders []*portforward.Forwarder
	for _, m := range mappings {
		fwd := &portforward.Forwarder{
			HostPort:    m.hostPort,
			SandboxPort: m.sandboxPort,
			Bind:        bind,
			Dialer:      dialer,
		}
		if err := fwd.Start(ctx); err != nil {
			for _, started := range forwarders {
				started.Stop()
			}
			return fmt.Errorf("failed to forward port %d: %w", m.hostPort, err)
		}
		forwarders = append(forwarders, fwd)
		notice.Info("Forwarding %s:%d → sandbox:%d (%s)",
			bind, fwd.ActualHostPort(), m.sandboxPort, sess.Name)
	}

	// 5. Update session registry with forwarded ports.
	for _, m := range mappings {
		sess.ForwardedPorts = append(sess.ForwardedPorts, session.ForwardedPort{
			HostPort:    m.hostPort,
			SandboxPort: m.sandboxPort,
			Bind:        bind,
			Protocol:    "tcp",
		})
	}
	_ = store.Update(sess)

	// 6. Wait for signal or sandbox exit.
	notice.Info("\nPress Ctrl+C to stop forwarding.")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				proc, e := os.FindProcess(sess.PID)
				if e != nil {
					return
				}
				if proc.Signal(syscall.Signal(0)) != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	select {
	case <-sigChan:
		notice.Info("\nStopping port forwards...")
	case <-done:
		notice.Info("\nSandbox exited, stopping port forwards...")
	}

	// 7. Stop all forwarders concurrently.
	cancel()
	var wg sync.WaitGroup
	for _, fwd := range forwarders {
		wg.Go(func() {
			fwd.Stop()
		})
	}
	wg.Wait()
	return nil
}
