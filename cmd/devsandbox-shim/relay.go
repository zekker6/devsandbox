package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"devsandbox/internal/sockrelay"
)

// relaySubcommand is the shim's second entry point: `devsandbox-shim
// socket-relay name=path[,path...] ...`, started by the host with `docker exec
// -i` as the sandbox user once the container is ready. It exposes the host's
// filtered proxy sockets inside a guest whose home is not a native bind mount
// (Docker on macOS, krun), where a bind-mounted unix socket accepts nothing.
// The host end lives in internal/isolator; the protocol in internal/sockrelay.
const relaySubcommand = "socket-relay"

// parseRelayArgs turns `name=path[,path...]` arguments into guest sockets.
// Every path must be absolute: the relay runs in the sandbox home and a
// relative path would resolve against whatever cwd docker exec chose.
func parseRelayArgs(args []string) ([]sockrelay.GuestSocket, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("%s: no sockets given", relaySubcommand)
	}
	seen := make(map[string]struct{}, len(args))
	sockets := make([]sockrelay.GuestSocket, 0, len(args))
	for _, arg := range args {
		name, list, ok := strings.Cut(arg, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("%s: expected name=path[,path...], got %q", relaySubcommand, arg)
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("%s: socket %q given twice", relaySubcommand, name)
		}
		seen[name] = struct{}{}
		var paths []string
		for _, p := range strings.Split(list, ",") {
			if !filepath.IsAbs(p) {
				return nil, fmt.Errorf("%s: path %q for %q is not absolute", relaySubcommand, p, name)
			}
			paths = append(paths, p)
		}
		sockets = append(sockets, sockrelay.GuestSocket{Name: name, Paths: paths})
	}
	return sockets, nil
}

// stdio is the relay's byte stream: stdin from the host, stdout to it. Closing
// it closes both, which is what ends the accept loops when the host goes away.
type stdio struct {
	in  io.ReadCloser
	out io.WriteCloser
}

func (s stdio) Read(p []byte) (int, error)  { return s.in.Read(p) }
func (s stdio) Write(p []byte) (int, error) { return s.out.Write(p) }
func (s stdio) Close() error {
	_ = s.in.Close()
	return s.out.Close()
}

// runSocketRelay serves the guest end until the host closes the pipe. Exit
// status 2 is a usage error the host reports as a version mismatch hint.
func runSocketRelay(args []string) int {
	sockets, err := parseRelayArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := sockrelay.ServeGuest(context.Background(), stdio{in: os.Stdin, out: os.Stdout}, sockets); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", relaySubcommand, err)
		return 1
	}
	return 0
}
