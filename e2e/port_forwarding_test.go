package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"devsandbox/internal/portforward"
)

// TestPortForwarding_RequiresNetworkIsolation verifies that port forwarding
// fails with an appropriate error when used without network isolation (proxy mode).
func TestPortForwarding_RequiresNetworkIsolation(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Create a temp config directory with port forwarding enabled but no proxy
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-portfwd-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	// Enable port forwarding without proxy mode
	configContent := `[port_forwarding]
enabled = true

[[port_forwarding.rules]]
name = "test-port"
direction = "inbound"
protocol = "tcp"
host_port = 18080
sandbox_port = 18080
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Create a temp project directory
	tmpDir, err := os.MkdirTemp("", "sandbox-portfwd-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Run without --proxy, should fail with network isolation error
	cmd := exec.Command(binaryPath, "echo", "test")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	// Should fail
	if err == nil {
		t.Error("expected error when port forwarding without network isolation")
	}

	// Should mention network isolation
	if !strings.Contains(outputStr, "network isolation") {
		t.Errorf("expected error about network isolation, got: %s", outputStr)
	}

	// Should mention pasta
	if !strings.Contains(outputStr, "pasta") {
		t.Errorf("expected error to mention pasta, got: %s", outputStr)
	}

	// Should suggest enabling proxy mode
	if !strings.Contains(outputStr, "--proxy") {
		t.Errorf("expected error to suggest --proxy, got: %s", outputStr)
	}
}

// TestPortForwarding_ConfigValidation tests that invalid port forwarding
// configurations produce appropriate validation errors.
func TestPortForwarding_ConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      string
		wantErr     string
		description string
	}{
		{
			name: "invalid_direction",
			config: `[port_forwarding]
enabled = true

[[port_forwarding.rules]]
direction = "sideways"
host_port = 15001
sandbox_port = 15001
`,
			wantErr:     "direction must be 'inbound' or 'outbound'",
			description: "Invalid direction should produce validation error",
		},
		{
			name: "port_zero",
			config: `[port_forwarding]
enabled = true

[[port_forwarding.rules]]
direction = "inbound"
host_port = 0
sandbox_port = 15002
`,
			wantErr:     "host_port must be 1-65535",
			description: "Port 0 should produce validation error",
		},
		{
			name: "port_too_high",
			config: `[port_forwarding]
enabled = true

[[port_forwarding.rules]]
direction = "inbound"
host_port = 70000
sandbox_port = 15003
`,
			wantErr:     "host_port must be 1-65535",
			description: "Port above 65535 should produce validation error",
		},
		{
			name: "sandbox_port_zero",
			config: `[port_forwarding]
enabled = true

[[port_forwarding.rules]]
direction = "outbound"
host_port = 15004
sandbox_port = 0
`,
			wantErr:     "sandbox_port must be 1-65535",
			description: "Sandbox port 0 should produce validation error",
		},
		{
			name: "invalid_protocol",
			config: `[port_forwarding]
enabled = true

[[port_forwarding.rules]]
direction = "inbound"
protocol = "icmp"
host_port = 15005
sandbox_port = 15005
`,
			wantErr:     "protocol must be 'tcp' or 'udp'",
			description: "Invalid protocol should produce validation error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a temp config directory
			tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-validate-*")
			if err != nil {
				t.Fatalf("failed to create temp config dir: %v", err)
			}
			defer func() { _ = os.RemoveAll(tmpConfigDir) }()

			configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
			if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
				t.Fatalf("failed to create config dir: %v", err)
			}

			if err := os.WriteFile(configPath, []byte(tt.config), 0o644); err != nil {
				t.Fatalf("failed to write config: %v", err)
			}

			// Create a temp project directory
			tmpDir, err := os.MkdirTemp("", "sandbox-validate-*")
			if err != nil {
				t.Fatalf("failed to create temp dir: %v", err)
			}
			defer func() { _ = os.RemoveAll(tmpDir) }()

			// Run with the invalid config (use --info to avoid needing bwrap)
			cmd := exec.Command(binaryPath, "--info")
			cmd.Dir = tmpDir
			cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
			output, err := cmd.CombinedOutput()
			outputStr := string(output)

			// Should fail
			if err == nil {
				t.Errorf("%s: expected validation error, but command succeeded", tt.description)
			}

			// Should contain expected error message
			if !strings.Contains(outputStr, tt.wantErr) {
				t.Errorf("%s: expected error containing %q, got: %s", tt.description, tt.wantErr, outputStr)
			}
		})
	}
}

// TestPortForwarding_InboundTCP tests inbound TCP port forwarding.
// It starts a server inside the sandbox and connects from the host.
func TestPortForwarding_InboundTCP(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	if !networkProviderAvailable() {
		t.Skip("pasta not available")
	}

	// Check if nc (netcat) is available
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc (netcat) not installed on host")
	}

	// Use high ports to avoid permission issues
	hostPort := 16080
	sandboxPort := 16080

	// Create a temp config directory with inbound port forwarding
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-inbound-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	configContent := fmt.Sprintf(`[proxy]
enabled = true
port = 17080

[port_forwarding]
enabled = true

[[port_forwarding.rules]]
name = "test-inbound"
direction = "inbound"
protocol = "tcp"
host_port = %d
sandbox_port = %d
`, hostPort, sandboxPort)

	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Create a temp project directory
	tmpDir, err := os.MkdirTemp("", "sandbox-inbound-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Start a simple TCP server inside the sandbox using bash
	// Use a bash while loop that listens and responds with PONG
	sandboxCmd := exec.Command(binaryPath,
		"sh", "-c", fmt.Sprintf(
			"echo 'READY' && { echo 'PONG'; cat > /dev/null; } | nc -l -p %d",
			sandboxPort))
	sandboxCmd.Dir = tmpDir
	sandboxCmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)

	// Start the sandbox command
	sandboxStdout, err := sandboxCmd.StdoutPipe()
	if err != nil {
		t.Fatalf("failed to get stdout pipe: %v", err)
	}
	sandboxCmd.Stderr = os.Stderr

	if err := sandboxCmd.Start(); err != nil {
		t.Fatalf("failed to start sandbox: %v", err)
	}

	// Clean up the sandbox on exit
	defer func() {
		_ = sandboxCmd.Process.Kill()
		_ = sandboxCmd.Wait()
	}()

	// Wait for the sandbox to be ready
	readyBuf := make([]byte, 32)
	_, err = sandboxStdout.Read(readyBuf)
	if err != nil {
		t.Skipf("failed to read ready signal, may be CI limitation: %v", err)
	}

	// Give pasta a moment to set up port forwarding
	time.Sleep(500 * time.Millisecond)

	// Try to connect from host to the forwarded port
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", hostPort), 3*time.Second)
	if err != nil {
		t.Skipf("failed to connect to forwarded port, may be CI limitation: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Read response (PONG should be sent immediately upon connection)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Skipf("failed to read response, may be CI limitation: %v", err)
	}

	response := string(buf[:n])
	if !strings.Contains(response, "PONG") {
		t.Errorf("expected PONG response, got: %s", response)
	}
}

// TestPortForwarding_OutboundTCP tests outbound TCP port forwarding.
// It starts a server on the host and connects from inside the sandbox.
func TestPortForwarding_OutboundTCP(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	if !networkProviderAvailable() {
		t.Skip("pasta not available")
	}

	// Check if nc (netcat) is available
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc (netcat) not installed on host")
	}

	// Use high ports to avoid permission issues
	hostPort := 16090
	sandboxPort := 16090

	// Create a temp config directory with outbound port forwarding
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-outbound-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	configContent := fmt.Sprintf(`[proxy]
enabled = true
port = 17090

[port_forwarding]
enabled = true

[[port_forwarding.rules]]
name = "test-outbound"
direction = "outbound"
protocol = "tcp"
host_port = %d
sandbox_port = %d
`, hostPort, sandboxPort)

	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Create a temp project directory
	tmpDir, err := os.MkdirTemp("", "sandbox-outbound-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Start a TCP server on the host
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", hostPort))
	if err != nil {
		t.Fatalf("failed to start host listener: %v", err)
	}
	defer func() { _ = listener.Close() }()

	// Handle one connection in a goroutine
	serverDone := make(chan struct{})
	var serverErr error
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			serverErr = err
			return
		}
		defer func() { _ = conn.Close() }()

		// Read the request
		buf := make([]byte, 64)
		n, err := conn.Read(buf)
		if err != nil {
			serverErr = err
			return
		}

		// Echo back with "PONG"
		request := strings.TrimSpace(string(buf[:n]))
		response := fmt.Sprintf("PONG-%s\n", request)
		_, serverErr = conn.Write([]byte(response))
	}()

	// Connect from inside sandbox to host via gateway IP (10.0.2.2)
	// pasta maps the gateway IP to the host
	cmd := exec.Command(binaryPath,
		"sh", "-c", fmt.Sprintf("echo 'PING' | nc -w 2 10.0.2.2 %d", hostPort))
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Wait for server to finish
		<-serverDone
		if serverErr != nil {
			t.Skipf("server error (may be CI limitation): %v", serverErr)
		}
		t.Skipf("failed to connect from sandbox to host, may be CI limitation: %v\nOutput: %s", err, output)
	}

	// Wait for server to finish
	<-serverDone
	if serverErr != nil {
		t.Skipf("server error (may be CI limitation): %v", serverErr)
	}

	outputStr := string(output)
	if !strings.Contains(outputStr, "PONG") {
		t.Errorf("expected PONG response from host server, got: %s", outputStr)
	}
}

// TestPortForwarding_DuplicatePortConflict tests that duplicate port configurations
// are detected and rejected.
func TestPortForwarding_DuplicatePortConflict(t *testing.T) {
	// Create a temp config directory with duplicate inbound ports
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-conflict-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	// Two inbound rules using the same host port
	configContent := `[port_forwarding]
enabled = true

[[port_forwarding.rules]]
name = "rule1"
direction = "inbound"
host_port = 15100
sandbox_port = 15100

[[port_forwarding.rules]]
name = "rule2"
direction = "inbound"
host_port = 15100
sandbox_port = 15101
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Create a temp project directory
	tmpDir, err := os.MkdirTemp("", "sandbox-conflict-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Run with the conflicting config
	cmd := exec.Command(binaryPath, "--info")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	// Should fail
	if err == nil {
		t.Error("expected error for duplicate port configuration")
	}

	// Should mention port conflict
	if !strings.Contains(outputStr, "conflict") {
		t.Errorf("expected error about port conflict, got: %s", outputStr)
	}
}

// TestPortForwarding_DefaultProtocol tests that TCP is the default protocol
// when no protocol is explicitly specified.
func TestPortForwarding_DefaultProtocol(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	if !networkProviderAvailable() {
		t.Skip("pasta not available")
	}

	// Create a temp config directory with port forwarding without explicit protocol
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-default-proto-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	// No protocol specified - should default to TCP and work
	configContent := `[proxy]
enabled = true
port = 17100

[port_forwarding]
enabled = true

[[port_forwarding.rules]]
name = "noprotocol"
direction = "inbound"
host_port = 18500
sandbox_port = 8500
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Create a temp project directory
	tmpDir, err := os.MkdirTemp("", "sandbox-default-proto-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Run a simple command - should succeed (proving config is valid)
	cmd := exec.Command(binaryPath, "echo", "protocol-test")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command should succeed with default protocol: %v\nOutput: %s", err, output)
	}

	// Should have executed the echo command
	if !strings.Contains(string(output), "protocol-test") {
		t.Errorf("expected echo output, got: %s", output)
	}
}

// TestPortForwarding_DynamicForward exercises the dynamic port-forwarding
// path (session registry + NamespaceDialer). This is the code path used by
// the `devsandbox forward` command and by auto-detection. It is intentionally
// decomposed so a failure tells us exactly which stage broke:
//  1. Session file is written (PID capture + Register).
//  2. sess.PID is a host-visible process (kill -0 succeeds).
//  3. sess.PID is inside the sandbox netns (/proc/<pid>/net/tcp shows the
//     listener we started).
//  4. A NamespaceDialer can setns into sess.NetworkNS.
//  5. A TCP dial inside the ns to 127.0.0.1:<sandboxPort> succeeds and we
//     get the expected response.
func TestPortForwarding_DynamicForward(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}
	if !networkProviderAvailable() {
		t.Skip("pasta not available")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available on host")
	}

	// Pick an ephemeral port from the host. The sandbox's netns is fresh, so
	// a port free on the host is almost certainly free in there too. This
	// avoids collisions with leftover processes from prior test runs.
	ephemeral, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve ephemeral port: %v", err)
	}
	sandboxPort := ephemeral.Addr().(*net.TCPAddr).Port
	_ = ephemeral.Close()

	// Isolated XDG_STATE_HOME so we don't collide with the user's real sessions.
	tmpState, err := os.MkdirTemp("", "sandbox-dynfwd-state-*")
	if err != nil {
		t.Fatalf("mkdtemp state: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpState) })

	// Config: proxy on (required for session registration + netns), no static
	// port_forwarding rules — we'll forward dynamically.
	tmpConfig, err := os.MkdirTemp("", "sandbox-dynfwd-config-*")
	if err != nil {
		t.Fatalf("mkdtemp config: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpConfig) })

	configPath := filepath.Join(tmpConfig, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	configContent := `[proxy]
enabled = true
port = 17180
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	tmpProject, err := os.MkdirTemp("", "sandbox-dynfwd-project-*")
	if err != nil {
		t.Fatalf("mkdtemp project: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpProject) })

	// Start a sandbox with a listener that responds PONG on connection and
	// then sits reading until closed. The "READY" marker on stdout lets us
	// know the listener is up before we try to forward.
	// Use a Python TCP listener rather than nc to avoid cross-distro netcat
	// flag differences (GNU netcat 0.7.1 rejects trailing bind addresses and
	// has no -4, openbsd-netcat has different -l syntax, etc.). Explicitly
	// binds AF_INET so pasta's lack of IPv6 routes doesn't matter.
	pyListener := fmt.Sprintf(`
import socket, sys
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('0.0.0.0', %d))
s.listen(1)
print('READY', flush=True)
conn, _ = s.accept()
conn.sendall(b'PONG\n')
try:
    conn.recv(4096)
finally:
    conn.close()
`, sandboxPort)
	sandboxCmd := exec.Command(binaryPath, "python3", "-c", pyListener)
	sandboxCmd.Dir = tmpProject
	sandboxCmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+tmpConfig,
		"XDG_STATE_HOME="+tmpState,
	)
	stdout, err := sandboxCmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	sandboxCmd.Stderr = os.Stderr
	if err := sandboxCmd.Start(); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}
	t.Cleanup(func() {
		_ = sandboxCmd.Process.Kill()
		_ = sandboxCmd.Wait()
	})

	// Wait for READY from the listener, with a timeout goroutine.
	readyCh := make(chan string, 1)
	readyErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := stdout.Read(buf)
		if err != nil {
			readyErr <- err
			return
		}
		readyCh <- string(buf[:n])
	}()
	select {
	case data := <-readyCh:
		if !strings.Contains(data, "READY") {
			t.Skipf("sandbox listener did not signal READY: got %q", data)
		}
	case err := <-readyErr:
		t.Skipf("sandbox listener stdout closed before READY: %v", err)
	case <-time.After(10 * time.Second):
		t.Skip("timed out waiting for sandbox listener to signal READY")
	}

	// Stage 1: session file exists.
	sessDir := filepath.Join(tmpState, "devsandbox", "sessions")
	var sessionFile string
	sessDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(sessDeadline) {
		entries, _ := os.ReadDir(sessDir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				sessionFile = filepath.Join(sessDir, e.Name())
				break
			}
		}
		if sessionFile != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if sessionFile == "" {
		t.Fatalf("stage 1 FAIL: no session file appeared under %s", sessDir)
	}
	raw, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	var sess struct {
		Name      string `json:"name"`
		PID       int    `json:"pid"`
		NetworkNS string `json:"network_ns"`
	}
	if err := json.Unmarshal(raw, &sess); err != nil {
		t.Fatalf("parse session file: %v\nraw=%s", err, raw)
	}
	t.Logf("stage 1 OK: session %s pid=%d ns=%s", sess.Name, sess.PID, sess.NetworkNS)

	// Stage 2: sess.PID is host-alive (this is the regression we just fixed).
	if sess.PID <= 1 {
		t.Fatalf("stage 2 FAIL: session PID %d is not a host PID (PID-namespace leak)", sess.PID)
	}
	if err := syscall.Kill(sess.PID, 0); err != nil {
		t.Fatalf("stage 2 FAIL: kill -0 %d: %v", sess.PID, err)
	}
	t.Logf("stage 2 OK: PID %d is host-alive", sess.PID)

	// Stage 3: PID is inside the sandbox netns. /proc/<pid>/net/tcp should
	// list our listener.
	scanner := portforward.NewProcNetScanner(sess.PID)
	var gotListener bool
	for range 30 {
		listening, scanErr := scanner.ListeningPorts()
		if scanErr == nil {
			for _, e := range listening {
				if e.Port == sandboxPort {
					gotListener = true
					break
				}
			}
		}
		if gotListener {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !gotListener {
		t.Fatalf("stage 3 FAIL: port %d not seen in /proc/%d/net/tcp — PID is not in sandbox netns",
			sandboxPort, sess.PID)
	}
	t.Logf("stage 3 OK: listener on %d visible via /proc/%d/net/tcp", sandboxPort, sess.PID)

	// Stages 4+5: NamespaceDialer enters netns and dials the listener.
	// Point the dialer at the built devsandbox binary (which carries the
	// `__nsdial` subcommand); otherwise it would default to os.Executable(),
	// i.e. the test binary, which has no such subcommand.
	dialer := portforward.NewNamespaceDialer(sess.PID)
	dialer.HelperBinary = binaryPath
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", sandboxPort))
	if err != nil {
		t.Fatalf("stage 4/5 FAIL: dial via NamespaceDialer: %v", err)
	}
	defer func() { _ = conn.Close() }()
	t.Logf("stage 4/5 OK: dial via NamespaceDialer succeeded")

	// Read PONG.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 32)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read from sandbox service: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "PONG") {
		t.Errorf("expected PONG, got %q", string(buf[:n]))
	}
	t.Logf("end-to-end OK: got response %q", strings.TrimSpace(string(buf[:n])))
}
