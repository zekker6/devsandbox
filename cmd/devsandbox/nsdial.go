package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"github.com/spf13/cobra"
)

// newNSDialCmd creates a hidden helper subcommand used by the in-process
// NamespaceDialer. The parent process invokes us under `nsenter -U -n` so
// that we execute inside the target sandbox's user+network namespaces. Our
// only job is to dial the given TCP address (which is already namespace-
// local because nsenter set us up that way) and bridge the connection to
// our stdin/stdout. The parent wraps our pipes as a net.Conn.
//
// This indirection exists because setns(CLONE_NEWUSER) requires the calling
// process to be single-threaded, which Go processes never are.
func newNSDialCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "__nsdial <host:port>",
		Short:  "Internal helper: bridge stdio to a TCP connection (no namespace ops here)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runNSDial(args[0])
		},
	}
}

func runNSDial(addr string) error {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	var wg sync.WaitGroup
	wg.Add(2)

	// stdin → conn
	go func() {
		defer wg.Done()
		_, _ = io.Copy(conn, os.Stdin)
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()

	// conn → stdout
	go func() {
		defer wg.Done()
		_, _ = io.Copy(os.Stdout, conn)
		_ = os.Stdout.Close()
	}()

	wg.Wait()
	return nil
}
