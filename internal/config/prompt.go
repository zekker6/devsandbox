// Package config provides configuration file support for devsandbox.
package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// isInteractive returns true if stdin is a terminal.
func isInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// promptTrust prompts the user to trust a local config file.
// Returns true if the user accepts, false otherwise.
// The changed parameter indicates if this is a hash change (vs new file).
func promptTrust(input io.Reader, output io.Writer, projectDir, configContent string, changed bool) (bool, error) {
	// Show header
	if changed {
		_, _ = fmt.Fprintf(output, "Local config changed: .devsandbox.toml\n\n")
	} else {
		_, _ = fmt.Fprintf(output, "Local config found: .devsandbox.toml\n\n")
	}

	// Show config content with indentation
	lines := strings.Split(strings.TrimSpace(configContent), "\n")
	for _, line := range lines {
		_, _ = fmt.Fprintf(output, "  %s\n", line)
	}
	_, _ = fmt.Fprintln(output)

	// Prompt
	if changed {
		_, _ = fmt.Fprintf(output, "Trust updated configuration? [y/N]: ")
	} else {
		_, _ = fmt.Fprintf(output, "Trust this configuration? [y/N]: ")
	}

	// Read response
	reader := bufio.NewReader(input)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, fmt.Errorf("failed to read response: %w", err)
	}

	response = strings.TrimSpace(strings.ToLower(response))

	return response == "y" || response == "yes", nil
}

// PromptTrustStdio is a convenience wrapper that uses os.Stdin/os.Stderr.
func PromptTrustStdio(projectDir, configContent string, changed bool) (bool, error) {
	if !isInteractive() {
		_, _ = fmt.Fprintf(os.Stderr, "Warning: skipping .devsandbox.toml (non-interactive, run 'devsandbox trust' to approve)\n")
		return false, nil
	}
	return promptTrust(os.Stdin, os.Stderr, projectDir, configContent, changed)
}
