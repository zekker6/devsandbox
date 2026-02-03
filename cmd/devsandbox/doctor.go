package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"devsandbox/internal/config"
	"devsandbox/internal/sandbox"
	"devsandbox/internal/sandbox/tools"
)

type checkResult struct {
	name    string
	status  string // "ok", "warn", "error"
	message string
}

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check installation and dependencies",
		Long: `Verify that all required dependencies are installed and configured correctly.

Checks:
  - Required binaries (bwrap, shell)
  - Optional binaries (pasta for proxy mode)
  - User namespace support
  - Directory permissions
  - Overlayfs support (for tool writable layers)
  - Recent errors in internal logs`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor()
		},
	}

	return cmd
}

func runDoctor() error {
	var results []checkResult

	results = append(results, checkBinary("bwrap", true, "bubblewrap - required for sandboxing"))
	results = append(results, checkShell())

	results = append(results, checkBinary("pasta", false, "passt - required for proxy mode"))

	results = append(results, checkUserNamespaces())

	results = append(results, checkDirectories())

	results = append(results, checkKernelVersion())

	results = append(results, checkConfigFile())

	results = append(results, checkOverlayfs())

	results = append(results, checkRecentLogs())

	printDoctorResults(results)

	// Print detected tools
	printDetectedTools()

	hasError := false
	for _, r := range results {
		if r.status == "error" {
			hasError = true
			break
		}
	}

	if hasError {
		fmt.Println("\nSome checks failed. Please install missing dependencies.")
		return fmt.Errorf("doctor found issues")
	}

	fmt.Println("\nAll checks passed!")
	return nil
}

func checkBinary(name string, required bool, description string) checkResult {
	path, err := exec.LookPath(name)
	if err != nil {
		status := "warn"
		if required {
			status = "error"
		}
		return checkResult{
			name:    name,
			status:  status,
			message: fmt.Sprintf("not found - %s", description),
		}
	}

	// Try to get version
	version := getBinaryVersion(name)
	msg := fmt.Sprintf("found at %s", path)
	if version != "" {
		msg = fmt.Sprintf("%s (%s)", version, path)
	}

	return checkResult{
		name:    name,
		status:  "ok",
		message: msg,
	}
}

func getBinaryVersion(name string) string {
	var cmd *exec.Cmd
	switch name {
	case "bwrap":
		cmd = exec.Command(name, "--version")
	case "pasta":
		cmd = exec.Command(name, "--version")
	case "mise":
		cmd = exec.Command(name, "--version")
	case "fish":
		cmd = exec.Command(name, "--version")
	case "bash":
		cmd = exec.Command(name, "--version")
	case "zsh":
		cmd = exec.Command(name, "--version")
	default:
		return ""
	}

	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	version := strings.TrimSpace(strings.Split(string(output), "\n")[0])
	return version
}

func checkShell() checkResult {
	shell, shellPath := sandbox.DetectShell()

	if _, err := os.Stat(shellPath); os.IsNotExist(err) {
		return checkResult{
			name:    "shell",
			status:  "error",
			message: fmt.Sprintf("%s not found at %s", shell, shellPath),
		}
	}

	version := getBinaryVersion(string(shell))
	msg := fmt.Sprintf("%s at %s", shell, shellPath)
	if version != "" {
		msg = fmt.Sprintf("%s (%s)", shell, version)
	}

	return checkResult{
		name:    "shell",
		status:  "ok",
		message: msg,
	}
}

func checkUserNamespaces() checkResult {
	data, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone")
	if err == nil {
		value := strings.TrimSpace(string(data))
		if value == "0" {
			return checkResult{
				name:    "userns",
				status:  "error",
				message: "user namespaces disabled (unprivileged_userns_clone=0)",
			}
		}
	}

	cmd := exec.Command("unshare", "--user", "true")
	if err := cmd.Run(); err != nil {
		return checkResult{
			name:    "userns",
			status:  "error",
			message: "cannot create user namespace - check kernel config",
		}
	}

	return checkResult{
		name:    "userns",
		status:  "ok",
		message: "user namespaces enabled",
	}
}

func checkDirectories() checkResult {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return checkResult{
			name:    "directories",
			status:  "error",
			message: fmt.Sprintf("cannot determine home directory: %v", err),
		}
	}

	baseDir := sandbox.SandboxBasePath(homeDir)

	info, err := os.Stat(baseDir)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(baseDir, 0o755); err != nil {
			return checkResult{
				name:    "directories",
				status:  "error",
				message: fmt.Sprintf("cannot create %s: %v", baseDir, err),
			}
		}
		return checkResult{
			name:    "directories",
			status:  "ok",
			message: fmt.Sprintf("created %s", baseDir),
		}
	}
	if err != nil {
		return checkResult{
			name:    "directories",
			status:  "error",
			message: fmt.Sprintf("cannot access %s: %v", baseDir, err),
		}
	}

	if !info.IsDir() {
		return checkResult{
			name:    "directories",
			status:  "error",
			message: fmt.Sprintf("%s exists but is not a directory", baseDir),
		}
	}

	testFile := filepath.Join(baseDir, ".doctor-test")
	if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
		return checkResult{
			name:    "directories",
			status:  "error",
			message: fmt.Sprintf("%s is not writable: %v", baseDir, err),
		}
	}
	_ = os.Remove(testFile) // cleanup, best effort

	sandboxCount := 0
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return checkResult{
			name:    "sandboxes",
			status:  "warning",
			message: fmt.Sprintf("%s exists but cannot list contents: %v", baseDir, err),
		}
	}
	for _, e := range entries {
		if e.IsDir() {
			sandboxCount++
		}
	}

	return checkResult{
		name:    "directories",
		status:  "ok",
		message: fmt.Sprintf("%s (%d sandboxes)", baseDir, sandboxCount),
	}
}

func checkKernelVersion() checkResult {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return checkResult{
			name:    "kernel",
			status:  "warn",
			message: "cannot read kernel version",
		}
	}

	version := strings.TrimSpace(string(data))
	parts := strings.Fields(version)
	if len(parts) >= 3 {
		version = parts[2]
	}

	return checkResult{
		name:    "kernel",
		status:  "ok",
		message: version,
	}
}

func checkConfigFile() checkResult {
	configPath := config.ConfigPath()

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return checkResult{
			name:    "config",
			status:  "ok",
			message: "not found (using defaults) - run 'devsandbox config init' to create",
		}
	}

	cfg, _, _, err := config.LoadConfig()
	if err != nil {
		return checkResult{
			name:    "config",
			status:  "error",
			message: fmt.Sprintf("failed to load: %v", err),
		}
	}

	// Build a summary of non-default settings
	var settings []string
	if cfg.Proxy.IsEnabled() {
		settings = append(settings, "proxy=enabled")
	}
	if cfg.Proxy.Port != 8080 {
		settings = append(settings, fmt.Sprintf("port=%d", cfg.Proxy.Port))
	}
	if cfg.Sandbox.BasePath != "" {
		settings = append(settings, fmt.Sprintf("base_path=%s", cfg.Sandbox.BasePath))
	}

	msg := configPath
	if len(settings) > 0 {
		msg = fmt.Sprintf("%s (%s)", configPath, strings.Join(settings, ", "))
	}

	return checkResult{
		name:    "config",
		status:  "ok",
		message: msg,
	}
}

func printDoctorResults(results []checkResult) {
	table := tablewriter.NewWriter(os.Stdout)
	table.Header("CHECK", "STATUS", "DETAILS")

	for _, r := range results {
		status := r.status
		switch r.status {
		case "ok":
			status = "✓ ok"
		case "warn":
			status = "⚠ warn"
		case "error":
			status = "✗ error"
		}

		_ = table.Append(r.name, status, r.message)
	}

	_ = table.Render()
}

func printDetectedTools() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}

	allTools := tools.All()
	availableTools := tools.Available(homeDir)

	// Build a set of available tool names for quick lookup
	availableSet := make(map[string]bool)
	for _, t := range availableTools {
		availableSet[t.Name()] = true
	}

	fmt.Println("\nDetected Tools:")

	table := tablewriter.NewWriter(os.Stdout)
	table.Header("TOOL", "STATUS", "DESCRIPTION")

	for _, t := range allTools {
		status := "✗ not found"
		if availableSet[t.Name()] {
			status = "✓ available"
		}

		_ = table.Append(t.Name(), status, t.Description())
	}

	_ = table.Render()

	fmt.Printf("\n%d of %d tools available\n", len(availableTools), len(allTools))
}

// checkOverlayfs tests if bwrap's overlayfs support works.
// This is needed for tool overlay features (e.g., mise with writable layers).
func checkOverlayfs() checkResult {
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		return checkResult{
			name:    "overlayfs",
			status:  "warn",
			message: "cannot test (bwrap not found)",
		}
	}

	// Create a temporary directory structure for testing
	tmpDir, err := os.MkdirTemp("", "devsandbox-overlay-test")
	if err != nil {
		return checkResult{
			name:    "overlayfs",
			status:  "warn",
			message: fmt.Sprintf("cannot create temp dir: %v", err),
		}
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create test directories
	lowerDir := filepath.Join(tmpDir, "lower")
	if err := os.MkdirAll(lowerDir, 0o755); err != nil {
		return checkResult{
			name:    "overlayfs",
			status:  "warn",
			message: fmt.Sprintf("cannot create test dirs: %v", err),
		}
	}

	// Create a test file in lower dir
	testFile := filepath.Join(lowerDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
		return checkResult{
			name:    "overlayfs",
			status:  "warn",
			message: fmt.Sprintf("cannot create test file: %v", err),
		}
	}

	// Test bwrap with tmp-overlay (writes go to tmpfs, discarded on exit)
	// Need to mount /tmp as tmpfs so overlay mount point can be created
	cmd := exec.Command(bwrapPath,
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--unshare-user",
		"--overlay-src", lowerDir,
		"--tmp-overlay", "/tmp/overlay-test",
		"--", "cat", "/tmp/overlay-test/test.txt",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check for common failure reasons
		errStr := string(output)
		if strings.Contains(errStr, "fuse: unknown option") || strings.Contains(errStr, "fusermount") {
			return checkResult{
				name:    "overlayfs",
				status:  "warn",
				message: "not supported (fuse-overlayfs not installed)",
			}
		}
		if strings.Contains(errStr, "Operation not permitted") {
			return checkResult{
				name:    "overlayfs",
				status:  "warn",
				message: "not supported (requires user namespace or fuse-overlayfs)",
			}
		}
		return checkResult{
			name:    "overlayfs",
			status:  "warn",
			message: fmt.Sprintf("not working: %v", strings.TrimSpace(errStr)),
		}
	}

	if strings.TrimSpace(string(output)) != "test" {
		return checkResult{
			name:    "overlayfs",
			status:  "warn",
			message: "overlay mount works but content mismatch",
		}
	}

	return checkResult{
		name:    "overlayfs",
		status:  "ok",
		message: "supported (bwrap overlay works)",
	}
}

// checkRecentLogs checks for recent errors in internal logs.
// Looks at logs from the last 24 hours and summarizes any issues.
func checkRecentLogs() checkResult {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return checkResult{
			name:    "logs",
			status:  "warn",
			message: "cannot determine home directory",
		}
	}

	logPath := filepath.Join(homeDir, ".local", "share", "devsandbox", "logs", "internal", "logging-errors.log")

	info, err := os.Stat(logPath)
	if os.IsNotExist(err) {
		return checkResult{
			name:    "logs",
			status:  "ok",
			message: "no error log found (good)",
		}
	}
	if err != nil {
		return checkResult{
			name:    "logs",
			status:  "warn",
			message: fmt.Sprintf("cannot access log: %v", err),
		}
	}

	// Check file age
	if time.Since(info.ModTime()) > 7*24*time.Hour {
		return checkResult{
			name:    "logs",
			status:  "ok",
			message: fmt.Sprintf("no recent errors (last entry: %s)", info.ModTime().Format("2006-01-02")),
		}
	}

	// Read and parse recent entries
	file, err := os.Open(logPath)
	if err != nil {
		return checkResult{
			name:    "logs",
			status:  "warn",
			message: fmt.Sprintf("cannot open log: %v", err),
		}
	}
	defer func() { _ = file.Close() }()

	cutoff := time.Now().Add(-24 * time.Hour)
	var recentErrors []string
	componentCounts := make(map[string]int)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Parse timestamp (format: 2006-01-02T15:04:05Z07:00)
		if len(line) < 25 {
			continue
		}

		timestamp, err := time.Parse(time.RFC3339, line[:25])
		if err != nil {
			// Try without timezone (some entries might have shorter timestamps)
			timestamp, err = time.Parse("2006-01-02T15:04:05", line[:19])
			if err != nil {
				continue
			}
		}

		if timestamp.After(cutoff) {
			recentErrors = append(recentErrors, line)

			// Extract component from [component] format
			start := strings.Index(line, "[")
			end := strings.Index(line, "]")
			if start > 0 && end > start {
				component := line[start+1 : end]
				componentCounts[component]++
			}
		}
	}

	if len(recentErrors) == 0 {
		return checkResult{
			name:    "logs",
			status:  "ok",
			message: "no errors in last 24h",
		}
	}

	// Build summary
	var parts []string
	for component, count := range componentCounts {
		parts = append(parts, fmt.Sprintf("%s:%d", component, count))
	}

	msg := fmt.Sprintf("%d errors in last 24h", len(recentErrors))
	if len(parts) > 0 {
		msg += fmt.Sprintf(" (%s)", strings.Join(parts, ", "))
	}

	// Show last error if there's only one, or just the count
	status := "warn"
	if len(recentErrors) > 10 {
		status = "error"
	}

	return checkResult{
		name:    "logs",
		status:  status,
		message: msg,
	}
}
