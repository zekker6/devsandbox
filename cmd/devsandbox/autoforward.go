package main

import (
	"context"
	"slices"

	"devsandbox/internal/config"
	"devsandbox/internal/notice"
	"devsandbox/internal/portforward"
	"devsandbox/internal/session"
	"devsandbox/internal/termsafe"
)

func reportForwardError(err error) {
	notice.Alert("[devsandbox] port forwarding: %s", termsafe.Escape(err.Error()))
}

// startAutoForward owns the monitor and its forwards for one sandbox launch.
// Its stop function joins the monitor before the session record is removed.
func startAutoForward(ctx context.Context, cfg config.PortForwardingConfig, sess *session.Session,
	store *session.Store, scanner portforward.PortScanner, dialer portforward.Dialer,
) func() {
	ctx, cancel := context.WithCancel(ctx)
	exclude := make(map[int]bool)
	for _, port := range cfg.ExcludePorts {
		exclude[port] = true
	}
	forwarders := make(map[int]*portforward.Forwarder)
	updateSession := func() {
		if err := store.Update(sess); err != nil {
			reportForwardError(err)
		}
	}
	mon := &portforward.Monitor{
		Scanner:      scanner,
		Interval:     cfg.GetScanInterval(),
		ExcludePorts: exclude,
		OnError:      reportForwardError,
		OnPortAdded: func(entry portforward.ListenEntry) error {
			fwd := &portforward.Forwarder{
				HostPort:    entry.Port,
				SandboxPort: entry.Port,
				SandboxIP:   entry.IP,
				Bind:        "127.0.0.1",
				Dialer:      dialer,
				OnError:     reportForwardError,
			}
			hostPort, fellBack, err := fwd.StartWithFallback(ctx)
			if err != nil {
				return err
			}
			forwarders[entry.Port] = fwd
			if fellBack {
				notice.Info("[devsandbox] auto-forwarding port %d to 127.0.0.1:%d (host port %d was in use)", entry.Port, hostPort, entry.Port)
			} else {
				notice.Info("[devsandbox] auto-forwarding port %d to 127.0.0.1:%d", entry.Port, hostPort)
			}
			sess.ForwardedPorts = append(sess.ForwardedPorts, session.ForwardedPort{
				HostPort: hostPort, SandboxPort: entry.Port, Bind: "127.0.0.1", Protocol: "tcp",
			})
			updateSession()
			return nil
		},
		OnPortRemoved: func(port int) {
			if fwd, ok := forwarders[port]; ok {
				fwd.Stop()
				delete(forwarders, port)
				sess.ForwardedPorts = slices.DeleteFunc(sess.ForwardedPorts, func(p session.ForwardedPort) bool {
					return p.SandboxPort == port && p.HostPort == fwd.ActualHostPort() && p.Bind == "127.0.0.1" && p.Protocol == "tcp"
				})
				updateSession()
			}
			notice.Info("[devsandbox] stopped forwarding port %d", port)
		},
	}
	mon.Start(ctx)
	return func() {
		cancel()
		mon.Wait()
		for _, fwd := range forwarders {
			fwd.Stop()
		}
	}
}
