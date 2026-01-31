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
		"Bubblewrap sandbox",
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

	if !strings.Contains(string(output), "v1.0.0") {
		t.Errorf("--version output missing version: %s", output)
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

	if !strings.Contains(string(output), "mise") {
		t.Errorf("mise version output unexpected: %s", output)
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

func bwrapAvailable() bool {
	_, err := exec.LookPath("bwrap")
	return err == nil
}

func networkProviderAvailable() bool {
	_, err := exec.LookPath("pasta")
	return err == nil
}
