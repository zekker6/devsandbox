// Package config provides configuration file support for devsandbox.
package config

import (
	"fmt"
	"io"
	"os"
	"strings"

	"devsandbox/internal/notice"
	"devsandbox/internal/prompt"
)

// isInteractive reports whether the trust prompt has a human at both ends. It
// is asked on stderr and answered on stdin, so a terminal on only one of them
// is nobody who can answer - see [prompt.IsInteractive].
func isInteractive() bool {
	return prompt.IsInteractive(os.Stdin, os.Stderr)
}

// promptTrust prompts the user to trust a local config file.
// Returns true if the user accepts, false otherwise.
// The changed parameter indicates if this is a hash change (vs new file).
// configContent holds the recognized settings only - see loadLocalConfig.
func promptTrust(input io.Reader, output io.Writer, projectDir, configContent string, changed bool) (bool, error) {
	// Show header
	if changed {
		_, _ = fmt.Fprintf(output, "Local config changed: .devsandbox.toml\n\n")
	} else {
		_, _ = fmt.Fprintf(output, "Local config found: .devsandbox.toml\n\n")
	}

	// Show config content with indentation
	if content := strings.TrimSpace(configContent); content == "" {
		_, _ = fmt.Fprintf(output, "  (no recognized settings)\n")
	} else {
		for line := range strings.SplitSeq(content, "\n") {
			_, _ = fmt.Fprintf(output, "  %s\n", line)
		}
	}
	_, _ = fmt.Fprintln(output)

	// Prompt
	if changed {
		_, _ = fmt.Fprintf(output, "Trust updated configuration? [y/N]: ")
	} else {
		_, _ = fmt.Fprintf(output, "Trust this configuration? [y/N]: ")
	}

	// Read response. The sandbox workload inherits this stdin, so the answer is
	// read a byte at a time - see prompt.ReadLine.
	response, err := prompt.ReadLine(input)
	if err != nil {
		return false, fmt.Errorf("failed to read response: %w", err)
	}

	return prompt.IsYes(response), nil
}

// PromptTrustStdio is a convenience wrapper that uses os.Stdin/os.Stderr.
func PromptTrustStdio(projectDir, configContent string, changed bool) (bool, error) {
	if !isInteractive() {
		notice.Warn("skipping .devsandbox.toml (non-interactive, run 'devsandbox trust' to approve)")
		return false, nil
	}
	return promptTrust(os.Stdin, os.Stderr, projectDir, configContent, changed)
}
