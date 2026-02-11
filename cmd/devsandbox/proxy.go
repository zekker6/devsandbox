package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"devsandbox/internal/config"
	"devsandbox/internal/proxy"
	"devsandbox/internal/sandbox"
)

func newProxyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Proxy-related commands",
		Long:  `Commands for managing the HTTP proxy, including the ask mode monitor and filter configuration.`,
	}

	cmd.AddCommand(newProxyMonitorCmd())
	cmd.AddCommand(newFilterCmd())

	return cmd
}

func newProxyMonitorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "monitor [socket-path]",
		Short: "Monitor and approve HTTP requests in ask mode",
		Long: `Interactive terminal for approving/denying HTTP requests when proxy is running in ask mode.

Run this command in a separate terminal while the sandbox is running with --filter-default=ask.
If no socket path is provided, it will be auto-detected from the current directory's sandbox.

Keys (instant response, no Enter needed):
  a - Allow this request
  b - Block this request
  s - Allow and remember for session
  n - Block and remember for session

Requests that don't receive a response within 30 seconds are automatically rejected.`,
		Example: `  # Auto-detect socket from current project
  devsandbox proxy monitor

  # Explicit socket path
  devsandbox proxy monitor /path/to/ask.sock`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return runProxyMonitor(args[0])
			}
			socketPath, err := detectAskSocket()
			if err != nil {
				return err
			}
			return runProxyMonitor(socketPath)
		},
	}
}

// resolveSandboxBase returns the sandbox base path for the current directory's project.
func resolveSandboxBase() (string, error) {
	appCfg, _, projectDir, err := config.LoadConfig()
	if err != nil {
		return "", err
	}

	basePath := appCfg.Sandbox.BasePath
	if basePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		basePath = sandbox.SandboxBasePath(home)
	}

	projectName := sandbox.GenerateSandboxName(projectDir)
	return filepath.Join(basePath, projectName), nil
}

// detectAskSocket finds the ask socket for the current directory's sandbox.
func detectAskSocket() (string, error) {
	sandboxBase, err := resolveSandboxBase()
	if err != nil {
		return "", err
	}

	socketPath := proxy.AskSocketPath(sandboxBase)
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return "", fmt.Errorf("no ask socket found at %s\nMake sure the sandbox is running with --filter-default=ask", socketPath)
	}

	return socketPath, nil
}

func runProxyMonitor(socketPath string) error {
	// Connect to the ask server
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("failed to connect to ask server at %s: %w\nMake sure the sandbox is running with --filter-default=ask", socketPath, err)
	}
	defer func() { _ = conn.Close() }()

	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(conn)

	// Set terminal to raw mode for single-key input
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw terminal mode: %w", err)
	}

	// Cleanup function
	cleanup := func() {
		_ = term.Restore(int(os.Stdin.Fd()), oldState)
	}
	defer cleanup()

	// Channel for keyboard input
	keyChan := make(chan byte, 10)

	// Read keyboard in background goroutine
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 {
				continue
			}
			key := buf[0]
			// Handle Ctrl+C immediately
			if key == 3 {
				cleanup()
				fmt.Print("\r\nExiting monitor...\r\n")
				os.Exit(0)
			}
			keyChan <- key
		}
	}()

	// Handle signals for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		cleanup()
		fmt.Print("\r\nExiting monitor...\r\n")
		os.Exit(0)
	}()

	printHeader()

	// Read requests from the server
	for {
		var req proxy.AskRequest
		if err := decoder.Decode(&req); err != nil {
			fmt.Printf("\r\nConnection closed: %v\r\n", err)
			return nil
		}

		// Display request details
		displayRequest(&req)

		// Get user decision (single keypress)
		resp := getUserDecisionFromChan(&req, keyChan)

		// Send response back to proxy
		if err := encoder.Encode(&resp); err != nil {
			fmt.Printf("\r\nFailed to send response: %v\r\n", err)
		}

		fmt.Print("\r\n")
	}
}

func printHeader() {
	fmt.Print("╔════════════════════════════════════════════════════════════════╗\r\n")
	fmt.Print("║            devsandbox HTTP Request Monitor                     ║\r\n")
	fmt.Print("╠════════════════════════════════════════════════════════════════╣\r\n")
	fmt.Print("║  Waiting for requests...                                       ║\r\n")
	fmt.Print("║  Keys: [a]llow  [b]lock  [s]ession-allow  [n]ever-allow        ║\r\n")
	fmt.Print("║  Requests timeout after 30 seconds (auto-reject)               ║\r\n")
	fmt.Print("╚════════════════════════════════════════════════════════════════╝\r\n")
	fmt.Print("\r\n")
}

func displayRequest(req *proxy.AskRequest) {
	fmt.Print("┌──────────────────────────────────────────────────────────────────┐\r\n")
	fmt.Printf("│  %-64s│\r\n", fmt.Sprintf("Request #%s", req.ID))
	fmt.Print("├──────────────────────────────────────────────────────────────────┤\r\n")
	fmt.Printf("│  Method: %-55s│\r\n", req.Method)
	fmt.Printf("│  Host:   %-55s│\r\n", truncate(req.Host, 55))
	fmt.Printf("│  Path:   %-55s│\r\n", truncate(req.Path, 55))

	if len(req.Headers) > 0 {
		fmt.Print("├──────────────────────────────────────────────────────────────────┤\r\n")
		for k, v := range req.Headers {
			line := fmt.Sprintf("%s: %s", k, v)
			fmt.Printf("│  %-62s│\r\n", truncate(line, 62))
		}
	}

	if req.Body != "" {
		fmt.Print("├──────────────────────────────────────────────────────────────────┤\r\n")
		fmt.Print("│  Body preview:                                                   │\r\n")
		preview := truncate(req.Body, 60)
		fmt.Printf("│  %-62s│\r\n", preview)
	}

	fmt.Print("├──────────────────────────────────────────────────────────────────┤\r\n")
	fmt.Print("│  [a]llow    [b]lock    [s]ession-allow    [n]ever-allow         │\r\n")
	fmt.Print("└──────────────────────────────────────────────────────────────────┘\r\n")
}

func getUserDecisionFromChan(req *proxy.AskRequest, keyChan <-chan byte) proxy.AskResponse {
	fmt.Print("Decision: ")

	for key := range keyChan {
		resp := proxy.AskResponse{ID: req.ID}

		switch key {
		case 'a', 'A', 'y', 'Y':
			resp.Action = proxy.FilterActionAllow
			fmt.Printf("%c\r\n✓ Allowed: %s\r\n", key, req.Host)
			return resp

		case 'b', 'B':
			resp.Action = proxy.FilterActionBlock
			fmt.Printf("%c\r\n✗ Blocked: %s\r\n", key, req.Host)
			return resp

		case 's', 'S':
			resp.Action = proxy.FilterActionAllow
			resp.Remember = true
			fmt.Printf("%c\r\n✓ Allowed for session: %s\r\n", key, req.Host)
			return resp

		case 'n', 'N':
			resp.Action = proxy.FilterActionBlock
			resp.Remember = true
			fmt.Printf("%c\r\n✗ Blocked for session: %s\r\n", key, req.Host)
			return resp

		default:
			// Ignore unknown keys, wait for valid input
			continue
		}
	}

	// Channel closed, return block as default
	return proxy.AskResponse{ID: req.ID, Action: proxy.FilterActionBlock}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
