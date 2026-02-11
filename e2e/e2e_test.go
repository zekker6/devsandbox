package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var binaryPath string

func TestMain(m *testing.M) {
	// Build the binary before running tests
	tmpDir, err := os.MkdirTemp("", "devsandbox-e2e-*")
	if err != nil {
		panic("failed to create temp dir: " + err.Error())
	}

	binaryPath = filepath.Join(tmpDir, "devsandbox")

	// Get project root (parent of e2e directory)
	wd, err := os.Getwd()
	if err != nil {
		panic("failed to get working directory: " + err.Error())
	}
	projectRoot := filepath.Dir(wd)

	// Build the binary
	cmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/devsandbox")
	cmd.Dir = projectRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("failed to build binary: " + err.Error())
	}

	exitCode := m.Run()
	_ = os.RemoveAll(tmpDir)
	os.Exit(exitCode)
}

func TestSandbox_Help(t *testing.T) {
	cmd := exec.Command(binaryPath, "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--help failed: %v\nOutput: %s", err, output)
	}

	outputStr := string(output)
	expectedStrings := []string{
		"devsandbox",
		"Secure sandbox",
		"SSH: BLOCKED",
		".env files: BLOCKED",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(outputStr, expected) {
			t.Errorf("--help output missing %q", expected)
		}
	}
}

func TestSandbox_Version(t *testing.T) {
	cmd := exec.Command(binaryPath, "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--version failed: %v\nOutput: %s", err, output)
	}

	outputStr := string(output)
	// Check for version format: "devsandbox X.Y.Z (commit[-dirty:hash]) (built: date)"
	if !strings.Contains(outputStr, "devsandbox") {
		t.Errorf("--version output missing 'devsandbox': %s", output)
	}
	if !strings.Contains(outputStr, "(built:") {
		t.Errorf("--version output missing build date: %s", output)
	}
}

func TestSandbox_Info(t *testing.T) {
	cmd := exec.Command(binaryPath, "--info")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--info failed: %v\nOutput: %s", err, output)
	}

	outputStr := string(output)
	expectedStrings := []string{
		"Sandbox Configuration:",
		"Project:",
		"Sandbox Home:",
		"Blocked Paths:",
		"~/.ssh",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(outputStr, expected) {
			t.Errorf("--info output missing %q", expected)
		}
	}
}

func TestSandbox_EchoCommand(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	cmd := exec.Command(binaryPath, "echo", "hello from sandbox")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("echo command failed: %v\nOutput: %s", err, output)
	}

	if !strings.Contains(string(output), "hello from sandbox") {
		t.Errorf("echo output unexpected: %s", output)
	}
}

func TestSandbox_EnvironmentVariables(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	cmd := exec.Command(binaryPath, "env")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("env command failed: %v\nOutput: %s", err, output)
	}

	outputStr := string(output)
	expectedVars := []string{
		"DEVSANDBOX=1",
		"DEVSANDBOX_PROJECT=",
		"XDG_CONFIG_HOME=",
		"XDG_DATA_HOME=",
	}

	for _, expected := range expectedVars {
		if !strings.Contains(outputStr, expected) {
			t.Errorf("env output missing %q", expected)
		}
	}
}

func TestSandbox_MiseAvailable(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Check if mise is installed on host first
	if _, err := exec.LookPath("mise"); err != nil {
		t.Skip("mise not installed on host")
	}

	cmd := exec.Command(binaryPath, "mise", "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mise --version failed: %v\nOutput: %s", err, output)
	}

	// mise --version outputs version string like "2026.1.2 linux-x64 (2026-01-13)"
	// Just verify we got some output (command worked)
	if len(strings.TrimSpace(string(output))) == 0 {
		t.Errorf("mise version output empty")
	}
}

func TestSandbox_NeovimAvailable(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Check if nvim is installed on host first
	if _, err := exec.LookPath("nvim"); err != nil {
		t.Skip("nvim not installed on host")
	}

	cmd := exec.Command(binaryPath, "nvim", "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("nvim --version failed: %v\nOutput: %s", err, output)
	}

	if !strings.Contains(string(output), "NVIM") {
		t.Errorf("nvim version output unexpected: %s", output)
	}
}

func TestSandbox_ClaudeAvailable(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Check if claude is installed on host first
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed on host")
	}

	cmd := exec.Command(binaryPath, "claude", "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Claude might return non-zero for --version, check output anyway
		if !strings.Contains(string(output), "claude") && !strings.Contains(string(output), "Claude") {
			t.Fatalf("claude --version failed: %v\nOutput: %s", err, output)
		}
	}
}

func TestSandbox_ClaudeCodeFunctional(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Check if claude is installed on host first
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed on host")
	}

	t.Run("help_works", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "claude", "--help")
		output, err := cmd.CombinedOutput()
		outputStr := string(output)

		// Claude --help should work and show usage info
		if err != nil && !strings.Contains(outputStr, "Usage") && !strings.Contains(outputStr, "usage") {
			t.Errorf("claude --help failed: %v\nOutput: %s", err, output)
		}
	})

	t.Run("config_dir_accessible", func(t *testing.T) {
		// Verify ~/.claude directory is accessible inside sandbox
		home := os.Getenv("HOME")
		claudeDir := filepath.Join(home, ".claude")

		// Only test if .claude exists on host
		if _, err := os.Stat(claudeDir); os.IsNotExist(err) {
			t.Skip("~/.claude does not exist on host")
		}

		cmd := exec.Command(binaryPath, "ls", "-la", claudeDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("failed to list ~/.claude: %v\nOutput: %s", err, output)
		}

		// Should be able to see the directory contents
		if !strings.Contains(string(output), "total") {
			t.Errorf("~/.claude not properly mounted: %s", output)
		}
	})

	t.Run("config_file_accessible", func(t *testing.T) {
		// Verify ~/.claude.json is accessible if it exists
		home := os.Getenv("HOME")
		claudeConfig := filepath.Join(home, ".claude.json")

		// Only test if .claude.json exists on host
		if _, err := os.Stat(claudeConfig); os.IsNotExist(err) {
			t.Skip("~/.claude.json does not exist on host")
		}

		cmd := exec.Command(binaryPath, "cat", claudeConfig)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("failed to read ~/.claude.json: %v\nOutput: %s", err, output)
		}

		// Should contain valid JSON (at least opening brace)
		if !strings.Contains(string(output), "{") {
			t.Errorf("~/.claude.json not readable: %s", output)
		}
	})

	t.Run("can_execute_prompt", func(t *testing.T) {
		// Test that claude can run a simple print command
		// Using -p flag for print mode (non-interactive)
		cmd := exec.Command(binaryPath, "claude", "-p", "say hello")

		output, err := cmd.CombinedOutput()
		outputStr := string(output)

		// Claude should either:
		// 1. Respond successfully (contains common greeting words)
		// 2. Fail with auth/API error (still proves it started)
		validResponse := strings.Contains(strings.ToLower(outputStr), "hello") ||
			strings.Contains(strings.ToLower(outputStr), "hi") ||
			strings.Contains(strings.ToLower(outputStr), "help")

		authError := strings.Contains(outputStr, "API") ||
			strings.Contains(outputStr, "auth") ||
			strings.Contains(outputStr, "key") ||
			strings.Contains(outputStr, "login")

		if err != nil && !authError {
			t.Errorf("claude -p failed unexpectedly: %v\nOutput: %s", err, outputStr)
		}

		if !validResponse && !authError {
			t.Logf("claude responded (sandbox functional): %s", outputStr)
		}
	})
}

func TestSandbox_SSHBlocked(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Try to access ~/.ssh inside sandbox - should not exist
	cmd := exec.Command(binaryPath, "ls", "-la", os.Getenv("HOME")+"/.ssh")
	output, err := cmd.CombinedOutput()

	// Should fail or show empty/nonexistent
	outputStr := string(output)
	if err == nil && strings.Contains(outputStr, "id_rsa") {
		t.Error("SSH keys should not be accessible in sandbox")
	}
}

func TestSandbox_EnvFileBlocked(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Create a temp .env file in a temp project directory
	tmpDir, err := os.MkdirTemp("", "sandbox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	envFile := filepath.Join(tmpDir, ".env")
	if err := os.WriteFile(envFile, []byte("SECRET=supersecret123"), 0o644); err != nil {
		t.Fatalf("failed to create .env: %v", err)
	}

	// Run sandbox from that directory and try to read .env
	cmd := exec.Command(binaryPath, "cat", ".env")
	cmd.Dir = tmpDir
	output, _ := cmd.CombinedOutput()

	// .env should be blocked (empty or error)
	if strings.Contains(string(output), "supersecret123") {
		t.Error(".env file contents should be blocked in sandbox")
	}
}

func TestSandbox_ProjectDirWritable(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	tmpDir, err := os.MkdirTemp("", "sandbox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create a file inside sandbox using touch
	testFile := "sandbox-test-file.txt"
	cmd := exec.Command(binaryPath, "touch", testFile)
	cmd.Dir = tmpDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to create file in sandbox: %v\nOutput: %s", err, output)
	}

	// Verify file exists on host
	filePath := filepath.Join(tmpDir, testFile)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatalf("file not created in sandbox")
	}
}

func TestSandbox_NetworkAvailable(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Simple network test - check if we can resolve DNS
	cmd := exec.Command(binaryPath, "cat", "/etc/resolv.conf")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read resolv.conf: %v\nOutput: %s", err, output)
	}

	if !strings.Contains(string(output), "nameserver") {
		t.Error("resolv.conf should be available for network access")
	}
}

func TestSandbox_ProxyInfo(t *testing.T) {
	// --proxy --info shows proxy configuration without actually starting the proxy
	// This works even without pasta installed
	cmd := exec.Command(binaryPath, "--proxy", "--info")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--proxy --info failed: %v\nOutput: %s", err, output)
	}

	outputStr := string(output)
	// Should show proxy mode section with port info
	expectedStrings := []string{
		"Proxy Mode:",
		"Port:",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(outputStr, expected) {
			t.Errorf("--proxy --info output missing %q", expected)
		}
	}
}

func TestSandbox_ProxyEnvironmentVariables(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	if !networkProviderAvailable() {
		t.Skip("pasta not available")
	}

	// Run sandbox with proxy and check environment variables
	cmd := exec.Command(binaryPath, "--proxy", "env")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("env with proxy failed: %v\nOutput: %s", err, output)
	}

	outputStr := string(output)
	expectedVars := []string{
		"HTTP_PROXY=",
		"HTTPS_PROXY=",
		"DEVSANDBOX_PROXY=1",
		"NODE_EXTRA_CA_CERTS=",
		"REQUESTS_CA_BUNDLE=",
	}

	for _, expected := range expectedVars {
		if !strings.Contains(outputStr, expected) {
			t.Errorf("proxy env output missing %q", expected)
		}
	}
}

func TestSandbox_ProxyCACertificateAccessible(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	if !networkProviderAvailable() {
		t.Skip("pasta not available")
	}

	// Verify CA certificate is accessible inside sandbox at /tmp
	// (we use /tmp because /etc/ssl is bind-mounted read-only from host)
	cmd := exec.Command(binaryPath, "--proxy", "cat", "/tmp/devsandbox-ca.crt")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read CA cert in sandbox: %v\nOutput: %s", err, output)
	}

	outputStr := string(output)
	// Should contain PEM certificate markers
	if !strings.Contains(outputStr, "BEGIN CERTIFICATE") {
		t.Errorf("CA certificate not properly mounted, output: %s", outputStr)
	}
}

func TestSandbox_ProxyServerRunning(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	if !networkProviderAvailable() {
		t.Skip("pasta not available")
	}

	// Check if curl is available
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed on host")
	}

	// Test that the proxy server is accessible from inside the sandbox
	// The proxy runs on the host, so we need to test connectivity to it
	// Use a simple HTTP request to a known endpoint
	cmd := exec.Command(binaryPath, "--proxy", "--proxy-port", "18888",
		"curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
		"--proxy", "http://10.0.2.2:18888",
		"--max-time", "5",
		"http://httpbin.org/get")

	output, err := cmd.CombinedOutput()
	outputStr := strings.TrimSpace(string(output))

	// Extract the HTTP status code (last 3 characters should be the code)
	// curl -w "%{http_code}" outputs just the code at the end
	if len(outputStr) >= 3 {
		statusCode := outputStr[len(outputStr)-3:]
		if statusCode == "200" {
			return // Success - proxy worked
		}
		// 000 means connection failed (proxy not reachable or network issue)
		// This is acceptable in CI/test environments without network
		if statusCode == "000" {
			t.Skip("Network not available in test environment")
		}
	}

	// If we got here with an error, the proxy infrastructure has issues
	if err != nil {
		t.Errorf("Proxy request failed: %v\nOutput: %s", err, outputStr)
	}
}

func TestSandbox_ProxyEnvironmentSet(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	if !networkProviderAvailable() {
		t.Skip("pasta not available")
	}

	// Verify proxy environment variable is set correctly inside the sandbox
	cmd := exec.Command(binaryPath, "--proxy", "--proxy-port", "18889",
		"printenv", "HTTP_PROXY")

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("printenv HTTP_PROXY failed: %v\nOutput: %s", err, output)
	}

	outputStr := strings.TrimSpace(string(output))

	// Find the line containing the proxy URL (grep for http://)
	var proxyURL string
	for line := range strings.SplitSeq(outputStr, "\n") {
		if strings.HasPrefix(line, "http://") {
			proxyURL = line
			break
		}
	}

	if proxyURL == "" {
		t.Errorf("HTTP_PROXY not set correctly in sandbox, output: %s", outputStr)
		return
	}

	// Verify it points to the correct port
	if !strings.Contains(proxyURL, ":18889") {
		t.Errorf("HTTP_PROXY has wrong port, expected :18889, got: %s", proxyURL)
	}
}

func TestSandbox_ProxyBlocksDirectConnections(t *testing.T) {
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

	// Try to connect directly to an external IP - should fail
	// Using 1.1.1.1:443 (Cloudflare DNS) as a reliable external endpoint
	cmd := exec.Command(binaryPath, "--proxy",
		"nc", "-vv", "-w", "2", "1.1.1.1", "443")

	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	// The connection should fail with "Network is unreachable" or similar
	if err == nil {
		t.Error("Direct connection to external IP should be blocked in proxy mode")
	}

	// Verify it's a network error, not some other failure
	networkErrors := []string{
		"Network is unreachable",
		"No route to host",
		"network is unreachable",
		"no route to host",
		"Connection timed out",
	}

	foundNetworkError := false
	for _, errMsg := range networkErrors {
		if strings.Contains(outputStr, errMsg) {
			foundNetworkError = true
			break
		}
	}

	if !foundNetworkError {
		t.Logf("Expected network error, got: %s", outputStr)
	}
}

func TestSandbox_ProxyAllowsHTTPTraffic(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	if !networkProviderAvailable() {
		t.Skip("pasta not available")
	}

	// Check if curl is available
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed on host")
	}

	// HTTP request through proxy should work
	// Using httpbin.org as a reliable test endpoint
	cmd := exec.Command(binaryPath, "--proxy",
		"curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
		"--max-time", "10",
		"http://httpbin.org/get")

	output, err := cmd.CombinedOutput()
	outputStr := strings.TrimSpace(string(output))

	// Check for successful HTTP response (200)
	if strings.Contains(outputStr, "200") {
		return // Success
	}

	// 000 means network issue - skip in CI/restricted environments
	if strings.Contains(outputStr, "000") {
		t.Skip("Network not available in test environment")
	}

	if err != nil {
		t.Errorf("HTTP request through proxy failed: %v\nOutput: %s", err, outputStr)
	}
}

func TestSandbox_ProxyAllowsHTTPSTraffic(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	if !networkProviderAvailable() {
		t.Skip("pasta not available")
	}

	// Check if curl is available
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed on host")
	}

	// HTTPS request through proxy should work (using the CA cert)
	cmd := exec.Command(binaryPath, "--proxy",
		"curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
		"--max-time", "10",
		"https://httpbin.org/get")

	output, err := cmd.CombinedOutput()
	outputStr := strings.TrimSpace(string(output))

	// Check for successful HTTPS response (200)
	if strings.Contains(outputStr, "200") {
		return // Success
	}

	// 000 means network issue - skip in CI/restricted environments
	if strings.Contains(outputStr, "000") {
		t.Skip("Network not available in test environment")
	}

	if err != nil {
		t.Errorf("HTTPS request through proxy failed: %v\nOutput: %s", err, outputStr)
	}
}

func TestSandbox_ProxyLogsCreated(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	if !networkProviderAvailable() {
		t.Skip("pasta not available")
	}

	// Check if curl is available
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed on host")
	}

	// Create a temp project directory to have a known sandbox location
	tmpDir, err := os.MkdirTemp("", "sandbox-logs-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Make an HTTP request through the proxy
	cmd := exec.Command(binaryPath, "--proxy",
		"curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
		"--max-time", "10",
		"http://httpbin.org/get")
	cmd.Dir = tmpDir

	output, err := cmd.CombinedOutput()
	outputStr := strings.TrimSpace(string(output))

	// Check if request succeeded
	if strings.Contains(outputStr, "000") {
		t.Skip("Network not available in test environment")
	}

	if !strings.Contains(outputStr, "200") {
		if err != nil {
			t.Skipf("HTTP request failed (network issue?): %v\nOutput: %s", err, outputStr)
		}
	}

	// Now verify logs were created by using `devsandbox logs proxy`
	// The sandbox stores logs in ~/.local/share/devsandbox/<project-name>/logs/proxy/
	logsCmd := exec.Command(binaryPath, "logs", "proxy", "--last", "10", "--json")
	logsCmd.Dir = tmpDir
	logsOutput, logsErr := logsCmd.CombinedOutput()

	if logsErr != nil {
		t.Fatalf("logs proxy command failed: %v\nOutput: %s", logsErr, logsOutput)
	}

	logsStr := string(logsOutput)

	// Should contain the httpbin.org request
	if !strings.Contains(logsStr, "httpbin.org") {
		t.Errorf("logs proxy output should contain httpbin.org request, got: %s", logsStr)
	}

	// Should be valid JSON with expected fields
	if !strings.Contains(logsStr, `"method"`) {
		t.Errorf("logs proxy output should contain method field, got: %s", logsStr)
	}
	if !strings.Contains(logsStr, `"status"`) {
		t.Errorf("logs proxy output should contain status field, got: %s", logsStr)
	}
}

func TestSandbox_ProxyLogsFiltering(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	if !networkProviderAvailable() {
		t.Skip("pasta not available")
	}

	// Check if curl is available
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed on host")
	}

	// Create a temp project directory
	tmpDir, err := os.MkdirTemp("", "sandbox-logs-filter-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Make multiple requests - one GET and one POST
	cmds := []struct {
		method string
		args   []string
	}{
		{"GET", []string{"--proxy", "curl", "-s", "-o", "/dev/null", "--max-time", "10", "http://httpbin.org/get"}},
		{"POST", []string{"--proxy", "curl", "-s", "-o", "/dev/null", "-X", "POST", "--max-time", "10", "http://httpbin.org/post"}},
	}

	for _, c := range cmds {
		cmd := exec.Command(binaryPath, c.args...)
		cmd.Dir = tmpDir
		output, _ := cmd.CombinedOutput()
		// Skip if network unavailable
		if strings.Contains(string(output), "000") {
			t.Skip("Network not available in test environment")
		}
	}

	t.Run("filter_by_method", func(t *testing.T) {
		// Filter for POST only
		logsCmd := exec.Command(binaryPath, "logs", "proxy", "--method", "POST", "--json")
		logsCmd.Dir = tmpDir
		logsOutput, err := logsCmd.CombinedOutput()

		if err != nil {
			t.Fatalf("logs proxy --method POST failed: %v\nOutput: %s", err, logsOutput)
		}

		logsStr := string(logsOutput)
		// Should contain POST request (JSON has space after colon)
		if !strings.Contains(logsStr, `"method": "POST"`) {
			t.Errorf("filtered logs should contain POST method, got: %s", logsStr)
		}
	})

	t.Run("filter_by_url", func(t *testing.T) {
		// Filter by URL substring
		logsCmd := exec.Command(binaryPath, "logs", "proxy", "--url", "/get", "--json")
		logsCmd.Dir = tmpDir
		logsOutput, err := logsCmd.CombinedOutput()

		if err != nil {
			t.Fatalf("logs proxy --url /get failed: %v\nOutput: %s", err, logsOutput)
		}

		logsStr := string(logsOutput)
		// Should contain /get URL
		if !strings.Contains(logsStr, "/get") {
			t.Errorf("filtered logs should contain /get URL, got: %s", logsStr)
		}
	})

	t.Run("compact_output", func(t *testing.T) {
		// Test compact output format
		logsCmd := exec.Command(binaryPath, "logs", "proxy", "--compact", "--last", "5")
		logsCmd.Dir = tmpDir
		logsOutput, err := logsCmd.CombinedOutput()

		if err != nil {
			t.Fatalf("logs proxy --compact failed: %v\nOutput: %s", err, logsOutput)
		}

		logsStr := string(logsOutput)
		// Compact format should have method and status visible
		if !strings.Contains(logsStr, "GET") && !strings.Contains(logsStr, "POST") {
			t.Errorf("compact output should show HTTP method, got: %s", logsStr)
		}
	})

	t.Run("stats_output", func(t *testing.T) {
		// Test stats summary
		logsCmd := exec.Command(binaryPath, "logs", "proxy", "--stats")
		logsCmd.Dir = tmpDir
		logsOutput, err := logsCmd.CombinedOutput()

		if err != nil {
			t.Fatalf("logs proxy --stats failed: %v\nOutput: %s", err, logsOutput)
		}

		logsStr := string(logsOutput)
		// Stats should show summary
		if !strings.Contains(logsStr, "Total") {
			t.Errorf("stats output should show Total, got: %s", logsStr)
		}
	})
}

func TestLogs_CommandHelp(t *testing.T) {
	// Test that logs command help works
	cmd := exec.Command(binaryPath, "logs", "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("logs --help failed: %v\nOutput: %s", err, output)
	}

	outputStr := string(output)
	expectedStrings := []string{
		"proxy",
		"internal",
		"View proxy and internal logs",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(outputStr, expected) {
			t.Errorf("logs --help output missing %q", expected)
		}
	}
}

func TestLogs_ProxyHelp(t *testing.T) {
	// Test that logs proxy help shows all filter options
	cmd := exec.Command(binaryPath, "logs", "proxy", "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("logs proxy --help failed: %v\nOutput: %s", err, output)
	}

	outputStr := string(output)
	expectedFlags := []string{
		"--last",
		"--follow",
		"--json",
		"--url",
		"--method",
		"--status",
		"--since",
		"--errors",
		"--compact",
		"--stats",
	}

	for _, expected := range expectedFlags {
		if !strings.Contains(outputStr, expected) {
			t.Errorf("logs proxy --help output missing %q", expected)
		}
	}
}

func TestLogs_InternalHelp(t *testing.T) {
	// Test that logs internal help shows options
	cmd := exec.Command(binaryPath, "logs", "internal", "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("logs internal --help failed: %v\nOutput: %s", err, output)
	}

	outputStr := string(output)
	expectedFlags := []string{
		"--last",
		"--follow",
		"--type",
		"--since",
	}

	for _, expected := range expectedFlags {
		if !strings.Contains(outputStr, expected) {
			t.Errorf("logs internal --help output missing %q", expected)
		}
	}
}

func TestSandboxes_DeprecatedLogsRemoved(t *testing.T) {
	// Verify that the deprecated 'sandboxes logs' command no longer exists
	// by checking that 'logs' is not listed as a subcommand
	cmd := exec.Command(binaryPath, "sandboxes", "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandboxes --help failed: %v\nOutput: %s", err, output)
	}

	outputStr := string(output)
	// 'logs' should NOT be in the available commands
	// The help shows "Available Commands:" followed by command names
	if strings.Contains(outputStr, "  logs") {
		t.Errorf("'sandboxes logs' should be removed, but found 'logs' in help output: %s", outputStr)
	}
}

// Git mode tests

func TestSandbox_GitReadOnlyMode_BlocksCommits(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Check if git is available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed on host")
	}

	// Create a temp config directory to ensure clean config (no proxy)
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-readonly-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	// Create minimal config with readonly git (default)
	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("[tools.git]\nmode = \"readonly\"\n"), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Create a temp project directory with a git repo
	tmpDir, err := os.MkdirTemp("", "sandbox-git-readonly-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Initialize git repo
	initCmd := exec.Command("git", "init")
	initCmd.Dir = tmpDir
	if output, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to init git repo: %v\nOutput: %s", err, output)
	}

	// Configure git user (needed for commits)
	configCmds := [][]string{
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test User"},
	}
	for _, args := range configCmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tmpDir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to configure git: %v\nOutput: %s", err, output)
		}
	}

	// Create a file to commit
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Stage the file outside sandbox first
	addCmd := exec.Command("git", "add", "test.txt")
	addCmd.Dir = tmpDir
	if output, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to stage file: %v\nOutput: %s", err, output)
	}

	// Try to commit inside sandbox with readonly mode
	// This should fail because .git is read-only
	cmd := exec.Command(binaryPath, "git", "commit", "-m", "test commit")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	// Commit should fail with a read-only filesystem error
	if err == nil {
		t.Error("git commit should fail in readonly mode, but succeeded")
	}

	// Should contain error about read-only or permission denied
	readOnlyErrors := []string{
		"read-only",
		"Read-only",
		"permission denied",
		"Permission denied",
		"cannot lock",
		"fatal:",
	}

	foundError := false
	for _, errMsg := range readOnlyErrors {
		if strings.Contains(outputStr, errMsg) {
			foundError = true
			break
		}
	}

	if !foundError {
		t.Errorf("expected read-only error, got: %s", outputStr)
	}
}

func TestSandbox_GitReadOnlyMode_AllowsRead(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Check if git is available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed on host")
	}

	// Create a temp project directory with a git repo
	tmpDir, err := os.MkdirTemp("", "sandbox-git-read-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Initialize git repo
	initCmd := exec.Command("git", "init")
	initCmd.Dir = tmpDir
	if output, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to init git repo: %v\nOutput: %s", err, output)
	}

	// Configure and create initial commit outside sandbox
	configCmds := [][]string{
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test User"},
	}
	for _, args := range configCmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tmpDir
		if _, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to configure git: %v", err)
		}
	}

	// Create and commit a file outside sandbox
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial content"), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	commitCmds := [][]string{
		{"git", "add", "test.txt"},
		{"git", "commit", "-m", "initial commit"},
	}
	for _, args := range commitCmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tmpDir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed: %v\nOutput: %s", err, output)
		}
	}

	// Read operations should work in readonly mode
	t.Run("git_status", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "git", "status")
		cmd.Dir = tmpDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("git status should work in readonly mode: %v\nOutput: %s", err, output)
		}
	})

	t.Run("git_log", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "git", "log", "--oneline")
		cmd.Dir = tmpDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("git log should work in readonly mode: %v\nOutput: %s", err, output)
		}
		if !strings.Contains(string(output), "initial commit") {
			t.Errorf("git log should show commit, got: %s", output)
		}
	})

	t.Run("git_diff", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "git", "diff", "HEAD")
		cmd.Dir = tmpDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("git diff should work in readonly mode: %v\nOutput: %s", err, output)
		}
	})

	t.Run("git_branch", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "git", "branch", "-a")
		cmd.Dir = tmpDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("git branch should work in readonly mode: %v\nOutput: %s", err, output)
		}
	})
}

func TestSandbox_GitDisabledMode_AllowsCommits(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Check if git is available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed on host")
	}

	// Create a temp config directory with git mode = disabled
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	configContent := `[tools.git]
mode = "disabled"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Create a temp project directory with a git repo
	tmpDir, err := os.MkdirTemp("", "sandbox-git-disabled-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Initialize git repo and configure
	setupCmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test User"},
	}
	for _, args := range setupCmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tmpDir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed: %v\nOutput: %s", err, output)
		}
	}

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Stage and commit inside sandbox with disabled mode
	// Use XDG_CONFIG_HOME to point to our custom config
	cmd := exec.Command(binaryPath, "git", "add", "test.txt")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\nOutput: %s", err, output)
	}

	// Commit should work in disabled mode
	// Note: git config is set in the repo itself, so commit should work
	cmd = exec.Command(binaryPath, "git", "commit", "-m", "test commit")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Errorf("git commit should succeed in disabled mode: %v\nOutput: %s", err, output)
	}

	// Verify commit was created
	logCmd := exec.Command("git", "log", "--oneline")
	logCmd.Dir = tmpDir
	logOutput, _ := logCmd.CombinedOutput()
	if !strings.Contains(string(logOutput), "test commit") {
		t.Errorf("commit should be visible in git log, got: %s", logOutput)
	}
}

// Custom mounts tests

func TestSandbox_CustomMounts_ReadOnly(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Create a temp directory with a file to mount readonly
	mountSourceDir, err := os.MkdirTemp("", "sandbox-mount-source-*")
	if err != nil {
		t.Fatalf("failed to create mount source dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(mountSourceDir) }()

	// Create a config file in the source dir
	configFile := filepath.Join(mountSourceDir, "app.conf")
	if err := os.WriteFile(configFile, []byte("setting=value123"), 0o644); err != nil {
		t.Fatalf("failed to create config file: %v", err)
	}

	// Create a temp config directory with custom mount rule
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-mounts-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	configContent := `[[sandbox.mounts.rules]]
pattern = "` + mountSourceDir + `"
mode = "readonly"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Create a temp project directory
	tmpDir, err := os.MkdirTemp("", "sandbox-project-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	t.Run("can_read_mounted_file", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "cat", configFile)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("failed to read mounted file: %v\nOutput: %s", err, output)
		}
		if !strings.Contains(string(output), "setting=value123") {
			t.Errorf("mounted file should be readable, got: %s", output)
		}
	})

	t.Run("cannot_write_to_mounted_file", func(t *testing.T) {
		// Try to write to the readonly mounted file
		cmd := exec.Command(binaryPath, "sh", "-c", "echo 'new content' > "+configFile)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()

		// Should fail with permission denied or read-only error
		if err == nil {
			t.Error("writing to readonly mount should fail, but succeeded")
		}
		outputStr := string(output)
		if !strings.Contains(outputStr, "Read-only") && !strings.Contains(outputStr, "read-only") &&
			!strings.Contains(outputStr, "Permission denied") && !strings.Contains(outputStr, "permission denied") {
			t.Errorf("expected read-only or permission error, got: %s", outputStr)
		}
	})
}

func TestSandbox_CustomMounts_Hidden(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Hidden mode overlays a file with /dev/null, making it appear empty.
	// The file's parent directory must be mounted first for hidden to work.
	// Note: Custom mounts under $HOME conflict with sandbox home, so we use /tmp.

	// Create a directory with files (in /tmp which allows custom mounts)
	secretDir, err := os.MkdirTemp("", "sandbox-hidden-source-*")
	if err != nil {
		t.Fatalf("failed to create secret dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(secretDir) }()

	secretFile := filepath.Join(secretDir, "credentials.key")
	if err := os.WriteFile(secretFile, []byte("SUPER_SECRET_KEY_12345"), 0o644); err != nil {
		t.Fatalf("failed to create secret file: %v", err)
	}

	// Also create a visible file in the same directory
	visibleFile := filepath.Join(secretDir, "readme.txt")
	if err := os.WriteFile(visibleFile, []byte("visible content"), 0o644); err != nil {
		t.Fatalf("failed to create visible file: %v", err)
	}

	// Create a temp project directory (separate from secret dir)
	tmpDir, err := os.MkdirTemp("", "sandbox-hidden-project-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create a temp config directory with custom mount rules
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-hidden-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	// Mount the directory first, then hide the specific file using glob
	configContent := `[[sandbox.mounts.rules]]
pattern = "` + secretDir + `"
mode = "readonly"

[[sandbox.mounts.rules]]
pattern = "` + secretDir + `/*.key"
mode = "hidden"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	t.Run("hidden_file_content_not_exposed", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "cat", secretFile)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, _ := cmd.CombinedOutput()

		// The key test: secret content should NOT be exposed
		// Hidden mode may return empty content or permission denied,
		// but it must not reveal the actual secret
		if strings.Contains(string(output), "SUPER_SECRET_KEY") {
			t.Error("hidden file should not expose its contents")
		}
	})

	t.Run("visible_file_readable", func(t *testing.T) {
		// The visible file in the same directory should still be readable
		cmd := exec.Command(binaryPath, "cat", visibleFile)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("failed to read visible file: %v\nOutput: %s", err, output)
		}
		if !strings.Contains(string(output), "visible content") {
			t.Errorf("visible file should be readable, got: %s", output)
		}
	})
}

func TestSandbox_CustomMounts_ReadWrite(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Create a temp directory to mount as readwrite
	mountSourceDir, err := os.MkdirTemp("", "sandbox-rw-source-*")
	if err != nil {
		t.Fatalf("failed to create mount source dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(mountSourceDir) }()

	// Create a file that will be modified
	dataFile := filepath.Join(mountSourceDir, "data.txt")
	if err := os.WriteFile(dataFile, []byte("original"), 0o644); err != nil {
		t.Fatalf("failed to create data file: %v", err)
	}

	// Create a temp config directory with custom mount rule
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-rw-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	configContent := `[[sandbox.mounts.rules]]
pattern = "` + mountSourceDir + `"
mode = "readwrite"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Create a temp project directory
	tmpDir, err := os.MkdirTemp("", "sandbox-project-rw-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	t.Run("can_write_to_mounted_dir", func(t *testing.T) {
		// Write to the readwrite mounted file
		cmd := exec.Command(binaryPath, "sh", "-c", "echo 'modified' > "+dataFile)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("writing to readwrite mount should succeed: %v\nOutput: %s", err, output)
		}

		// Verify the change persisted on host
		content, err := os.ReadFile(dataFile)
		if err != nil {
			t.Fatalf("failed to read file on host: %v", err)
		}
		if !strings.Contains(string(content), "modified") {
			t.Errorf("changes should persist to host, got: %s", content)
		}
	})
}

func TestSandbox_CustomMounts_TmpOverlay(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Create a temp directory to mount with tmpoverlay
	mountSourceDir, err := os.MkdirTemp("", "sandbox-tmpoverlay-source-*")
	if err != nil {
		t.Fatalf("failed to create mount source dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(mountSourceDir) }()

	// Create a file that will be "modified" but changes discarded
	dataFile := filepath.Join(mountSourceDir, "temp.txt")
	if err := os.WriteFile(dataFile, []byte("original content"), 0o644); err != nil {
		t.Fatalf("failed to create data file: %v", err)
	}

	// Create a temp config directory with custom mount rule
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-tmpoverlay-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	configContent := `[[sandbox.mounts.rules]]
pattern = "` + mountSourceDir + `"
mode = "tmpoverlay"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Create a temp project directory
	tmpDir, err := os.MkdirTemp("", "sandbox-project-tmpoverlay-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	t.Run("can_write_inside_sandbox", func(t *testing.T) {
		// Write to the tmpoverlay mounted file - should succeed
		cmd := exec.Command(binaryPath, "sh", "-c", "echo 'sandbox modified' > "+dataFile+" && cat "+dataFile)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("writing in tmpoverlay should succeed: %v\nOutput: %s", err, output)
		}
		if !strings.Contains(string(output), "sandbox modified") {
			t.Errorf("should see modified content inside sandbox, got: %s", output)
		}
	})

	t.Run("changes_not_persisted_to_host", func(t *testing.T) {
		// After sandbox exits, host file should be unchanged
		content, err := os.ReadFile(dataFile)
		if err != nil {
			t.Fatalf("failed to read file on host: %v", err)
		}
		if !strings.Contains(string(content), "original content") {
			t.Errorf("host file should be unchanged, got: %s", content)
		}
		if strings.Contains(string(content), "sandbox modified") {
			t.Error("tmpoverlay changes should NOT persist to host")
		}
	})
}

func TestSandbox_CustomMounts_InfoShowsMounts(t *testing.T) {
	// Create a temp config directory with custom mount rules
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-info-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	configContent := `[[sandbox.mounts.rules]]
pattern = "~/.config/testapp"
mode = "readonly"

[[sandbox.mounts.rules]]
pattern = "**/secrets/**"
mode = "hidden"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Create the directories so they get expanded
	home := os.Getenv("HOME")
	testAppDir := filepath.Join(home, ".config", "testapp")
	_ = os.MkdirAll(testAppDir, 0o755)
	defer func() { _ = os.RemoveAll(testAppDir) }()

	// Run --info with custom config
	cmd := exec.Command(binaryPath, "--info")
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--info failed: %v\nOutput: %s", err, output)
	}

	outputStr := string(output)

	// Should show custom mounts section
	if !strings.Contains(outputStr, "Custom Mounts:") {
		t.Errorf("--info should show Custom Mounts section, got: %s", outputStr)
	}

	// Should show the testapp mount
	if !strings.Contains(outputStr, "testapp") {
		t.Errorf("--info should show testapp mount, got: %s", outputStr)
	}
}

func TestSandbox_CustomMounts_ProjectInternalHidden(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Test that custom mounts can hide files INSIDE the project directory.
	// This tests the new two-phase mount implementation where project-internal
	// custom mounts are applied after the project is bound.

	// Create a project directory with some files
	tmpDir, err := os.MkdirTemp("", "sandbox-project-internal-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create a secrets subdirectory with sensitive files
	secretsDir := filepath.Join(tmpDir, "secrets")
	if err := os.MkdirAll(secretsDir, 0o755); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	secretFile := filepath.Join(secretsDir, "api.key")
	if err := os.WriteFile(secretFile, []byte("SUPER_SECRET_API_KEY_12345"), 0o644); err != nil {
		t.Fatalf("failed to create secret file: %v", err)
	}

	// Create a normal file that should be readable
	normalFile := filepath.Join(tmpDir, "readme.txt")
	if err := os.WriteFile(normalFile, []byte("readable content"), 0o644); err != nil {
		t.Fatalf("failed to create normal file: %v", err)
	}

	// Create a temp config directory with rule to hide secrets/**
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-project-internal-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	// Use glob pattern to hide all files in secrets directory
	configContent := `[[sandbox.mounts.rules]]
pattern = "**/secrets/**"
mode = "hidden"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	t.Run("project_internal_files_hidden", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "cat", "secrets/api.key")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, _ := cmd.CombinedOutput()

		// The secret content should NOT be exposed
		if strings.Contains(string(output), "SUPER_SECRET_API_KEY") {
			t.Error("hidden project-internal file should not expose its contents")
		}
	})

	t.Run("project_normal_files_readable", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "cat", "readme.txt")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("failed to read normal file: %v\nOutput: %s", err, output)
		}
		if !strings.Contains(string(output), "readable content") {
			t.Errorf("normal file should be readable, got: %s", output)
		}
	})

	t.Run("project_secrets_dir_listing_shows_hidden", func(t *testing.T) {
		// The secrets directory should still be listable, but files appear empty/inaccessible
		cmd := exec.Command(binaryPath, "ls", "-la", "secrets/")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("failed to list secrets dir: %v\nOutput: %s", err, output)
		}
		// The file should appear in the listing (hidden mode uses /dev/null overlay, not removal)
		if !strings.Contains(string(output), "api.key") {
			t.Errorf("hidden file should still appear in directory listing, got: %s", output)
		}
	})
}

func TestSandbox_CustomMounts_ProjectInternalReadOnly(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Test that custom mounts can make project subdirectories readonly

	// Create a project directory
	tmpDir, err := os.MkdirTemp("", "sandbox-project-readonly-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create a vendor directory that should be readonly
	vendorDir := filepath.Join(tmpDir, "vendor")
	if err := os.MkdirAll(vendorDir, 0o755); err != nil {
		t.Fatalf("failed to create vendor dir: %v", err)
	}

	vendorFile := filepath.Join(vendorDir, "library.js")
	if err := os.WriteFile(vendorFile, []byte("// original vendor code"), 0o644); err != nil {
		t.Fatalf("failed to create vendor file: %v", err)
	}

	// Create a normal file that should be writable
	normalFile := filepath.Join(tmpDir, "app.js")
	if err := os.WriteFile(normalFile, []byte("// app code"), 0o644); err != nil {
		t.Fatalf("failed to create normal file: %v", err)
	}

	// Create a temp config directory with rule to make vendor readonly
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-project-ro-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	configContent := `[[sandbox.mounts.rules]]
pattern = "**/vendor/**"
mode = "readonly"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	t.Run("vendor_readable", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "cat", "vendor/library.js")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("failed to read vendor file: %v\nOutput: %s", err, output)
		}
		if !strings.Contains(string(output), "original vendor code") {
			t.Errorf("vendor file should be readable, got: %s", output)
		}
	})

	t.Run("vendor_not_writable", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "sh", "-c", "echo 'modified' > vendor/library.js")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()

		if err == nil {
			t.Error("writing to readonly vendor should fail, but succeeded")
		}
		outputStr := string(output)
		if !strings.Contains(outputStr, "Read-only") && !strings.Contains(outputStr, "read-only") &&
			!strings.Contains(outputStr, "Permission denied") && !strings.Contains(outputStr, "permission denied") {
			t.Errorf("expected read-only or permission error, got: %s", outputStr)
		}
	})

	t.Run("normal_project_file_writable", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "sh", "-c", "echo '// modified' > app.js")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("writing to normal project file should succeed: %v\nOutput: %s", err, output)
		}

		// Verify change persisted on host
		content, err := os.ReadFile(normalFile)
		if err != nil {
			t.Fatalf("failed to read file on host: %v", err)
		}
		if !strings.Contains(string(content), "modified") {
			t.Errorf("changes should persist to host, got: %s", content)
		}
	})
}

func TestSandbox_CustomMounts_GlobPattern(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Create an external directory with multiple conf files
	// (custom mounts with hidden mode work on external paths, not project paths)
	externalDir, err := os.MkdirTemp("", "sandbox-external-glob-*")
	if err != nil {
		t.Fatalf("failed to create external dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(externalDir) }()

	// Create multiple .secret files that should be hidden
	secretFiles := []string{"api.secret", "db.secret"}
	for _, name := range secretFiles {
		path := filepath.Join(externalDir, name)
		if err := os.WriteFile(path, []byte("SECRET_"+name), 0o644); err != nil {
			t.Fatalf("failed to create %s: %v", name, err)
		}
	}

	// Create a normal file that should NOT be hidden
	normalFile := filepath.Join(externalDir, "readme.txt")
	if err := os.WriteFile(normalFile, []byte("normal content"), 0o644); err != nil {
		t.Fatalf("failed to create normal file: %v", err)
	}

	// Create a temp project directory (separate from external dir)
	tmpDir, err := os.MkdirTemp("", "sandbox-glob-project-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create a temp config directory with glob pattern
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-glob-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	// Mount the external directory as readonly, then use glob to hide .secret files
	configContent := `[[sandbox.mounts.rules]]
pattern = "` + externalDir + `"
mode = "readonly"

[[sandbox.mounts.rules]]
pattern = "` + externalDir + `/*.secret"
mode = "hidden"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	t.Run("glob_hides_matching_files", func(t *testing.T) {
		for _, name := range secretFiles {
			path := filepath.Join(externalDir, name)
			cmd := exec.Command(binaryPath, "cat", path)
			cmd.Dir = tmpDir
			cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
			output, _ := cmd.CombinedOutput()

			if strings.Contains(string(output), "SECRET_") {
				t.Errorf("%s should be hidden, but got content: %s", name, output)
			}
		}
	})

	t.Run("glob_does_not_hide_non_matching", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "cat", normalFile)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("failed to read normal file: %v\nOutput: %s", err, output)
		}
		if !strings.Contains(string(output), "normal content") {
			t.Errorf("normal file should be readable, got: %s", output)
		}
	})
}

// Docker proxy tests

func TestSandbox_DockerProxy_ReadOperations(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	if !dockerAvailable() {
		t.Skip("docker not available")
	}

	// Create a temp config directory with docker enabled
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-docker-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	configContent := `[tools.docker]
enabled = true
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Create a temp project directory
	tmpDir, err := os.MkdirTemp("", "sandbox-docker-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	t.Run("docker_ps", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "docker", "ps")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("docker ps should work: %v\nOutput: %s", err, output)
		}
		// Should show header at minimum
		if !strings.Contains(string(output), "CONTAINER ID") {
			t.Errorf("docker ps should show container list header, got: %s", output)
		}
	})

	t.Run("docker_images", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "docker", "images")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("docker images should work: %v\nOutput: %s", err, output)
		}
		// Should show header at minimum (may be "REPOSITORY" or "IMAGE" depending on docker version)
		outputStr := string(output)
		if !strings.Contains(outputStr, "REPOSITORY") && !strings.Contains(outputStr, "IMAGE") {
			t.Errorf("docker images should show image list header, got: %s", output)
		}
	})

	t.Run("docker_info", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "docker", "info", "--format", "{{.ServerVersion}}")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("docker info should work: %v\nOutput: %s", err, output)
		}
		// Should return a version string
		outputStr := strings.TrimSpace(string(output))
		if len(outputStr) == 0 {
			t.Error("docker info should return server version")
		}
	})

	t.Run("docker_version", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "docker", "version", "--format", "{{.Server.Version}}")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("docker version should work: %v\nOutput: %s", err, output)
		}
		outputStr := strings.TrimSpace(string(output))
		if len(outputStr) == 0 {
			t.Error("docker version should return server version")
		}
	})
}

func TestSandbox_DockerProxy_WriteOperationsBlocked(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	if !dockerAvailable() {
		t.Skip("docker not available")
	}

	// Create a temp config directory with docker enabled
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-docker-block-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	configContent := `[tools.docker]
enabled = true
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Create a temp project directory
	tmpDir, err := os.MkdirTemp("", "sandbox-docker-block-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	t.Run("docker_run_blocked", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "docker", "run", "--rm", "hello-world")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()

		// Should fail
		if err == nil {
			t.Error("docker run should be blocked, but succeeded")
		}

		outputStr := string(output)
		// Should contain 403/blocked message or connection reset (proxy rejecting the request)
		if !strings.Contains(outputStr, "403") && !strings.Contains(outputStr, "blocked") &&
			!strings.Contains(outputStr, "Forbidden") && !strings.Contains(outputStr, "forbidden") &&
			!strings.Contains(outputStr, "connection reset by peer") {
			t.Errorf("docker run should show blocked/403 error, got: %s", outputStr)
		}
	})

	t.Run("docker_pull_blocked", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "docker", "pull", "hello-world")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()

		// Should fail
		if err == nil {
			t.Error("docker pull should be blocked, but succeeded")
		}

		outputStr := string(output)
		// Should contain 403/blocked message or connection reset (proxy rejecting the request)
		if !strings.Contains(outputStr, "403") && !strings.Contains(outputStr, "blocked") &&
			!strings.Contains(outputStr, "Forbidden") && !strings.Contains(outputStr, "forbidden") &&
			!strings.Contains(outputStr, "connection reset by peer") {
			t.Errorf("docker pull should show blocked/403 error, got: %s", outputStr)
		}
	})

	t.Run("docker_build_blocked", func(t *testing.T) {
		// Create a simple Dockerfile
		dockerfile := filepath.Join(tmpDir, "Dockerfile")
		if err := os.WriteFile(dockerfile, []byte("FROM alpine\n"), 0o644); err != nil {
			t.Fatalf("failed to create Dockerfile: %v", err)
		}

		cmd := exec.Command(binaryPath, "docker", "build", "-t", "test-blocked", ".")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
		output, err := cmd.CombinedOutput()

		// Should fail
		if err == nil {
			t.Error("docker build should be blocked, but succeeded")
		}

		outputStr := string(output)
		// Should contain 403/blocked message or connection reset (proxy rejecting the request)
		if !strings.Contains(outputStr, "403") && !strings.Contains(outputStr, "blocked") &&
			!strings.Contains(outputStr, "Forbidden") && !strings.Contains(outputStr, "forbidden") &&
			!strings.Contains(outputStr, "connection reset by peer") {
			t.Errorf("docker build should show blocked/403 error, got: %s", outputStr)
		}
	})
}

func TestSandbox_DockerProxy_EnvironmentVariable(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	if !dockerAvailable() {
		t.Skip("docker not available")
	}

	// Create a temp config directory with docker enabled
	tmpConfigDir, err := os.MkdirTemp("", "sandbox-config-docker-env-*")
	if err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpConfigDir) }()

	configPath := filepath.Join(tmpConfigDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	configContent := `[tools.docker]
enabled = true
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Create a temp project directory
	tmpDir, err := os.MkdirTemp("", "sandbox-docker-env-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cmd := exec.Command(binaryPath, "printenv", "DOCKER_HOST")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpConfigDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("printenv DOCKER_HOST failed: %v\nOutput: %s", err, output)
	}

	outputStr := strings.TrimSpace(string(output))

	// Find the unix:// line (stderr warnings may precede it)
	var dockerHost string
	for line := range strings.SplitSeq(outputStr, "\n") {
		if strings.HasPrefix(line, "unix://") {
			dockerHost = line
			break
		}
	}

	if dockerHost == "" {
		t.Fatalf("DOCKER_HOST should contain unix:// URL, got: %s", outputStr)
	}
	if !strings.Contains(dockerHost, "docker.sock") {
		t.Errorf("DOCKER_HOST should point to docker.sock, got: %s", dockerHost)
	}
}

func TestSandbox_DockerProxy_DisabledByDefault(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap not available")
	}

	// Create a temp project directory (no special config)
	tmpDir, err := os.MkdirTemp("", "sandbox-docker-disabled-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Without docker enabled, DOCKER_HOST should not be set to proxy
	cmd := exec.Command(binaryPath, "printenv", "DOCKER_HOST")
	cmd.Dir = tmpDir
	output, _ := cmd.CombinedOutput()

	outputStr := strings.TrimSpace(string(output))
	// Should either be empty or not contain devsandbox proxy path
	if strings.Contains(outputStr, "devsandbox-docker") {
		t.Errorf("DOCKER_HOST should not be set to proxy when disabled, got: %s", outputStr)
	}
}

// dockerAvailable checks if Docker daemon is running and accessible.
func dockerAvailable() bool {
	// Check if docker CLI is installed
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}

	// Check if Docker daemon is running
	cmd := exec.Command("docker", "info")
	err := cmd.Run()
	return err == nil
}

// bwrapAvailable checks if bwrap is installed AND functional.
// GitHub Actions and some CI environments don't allow user namespaces,
// so we need to test if bwrap actually works, not just if it's installed.
func bwrapAvailable() bool {
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		return false
	}

	// Try to run a simple bwrap command to verify user namespaces work
	cmd := exec.Command(bwrapPath,
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--unshare-user",
		"--", "true")
	err = cmd.Run()
	return err == nil
}

// networkProviderAvailable checks if pasta is installed AND functional.
func networkProviderAvailable() bool {
	pastaPath, err := exec.LookPath("pasta")
	if err != nil {
		return false
	}

	// Check if pasta can at least show help (doesn't require namespaces)
	cmd := exec.Command(pastaPath, "--help")
	err = cmd.Run()
	return err == nil
}
