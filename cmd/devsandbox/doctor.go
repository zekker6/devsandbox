package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"devsandbox/internal/sandbox"
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
  - Directory permissions`,
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

	printDoctorResults(results)

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
	_ = os.Remove(testFile)

	entries, _ := os.ReadDir(baseDir)
	sandboxCount := 0
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
