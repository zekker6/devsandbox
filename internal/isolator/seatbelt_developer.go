package isolator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func nativeDeveloperDirCommand(ctx context.Context) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/usr/bin/xcode-select", "-p")
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	// Honor the host's selected toolchain without inheriting loader variables
	// or unrelated credentials into the host-side query.
	if value := os.Getenv("DEVELOPER_DIR"); value != "" {
		cmd.Env = append(cmd.Env, "DEVELOPER_DIR="+value)
	}
	return cmd
}

func prepareNativeDeveloperDir(env map[string]string, cmd *exec.Cmd) error {
	if _, explicit := env["DEVELOPER_DIR"]; explicit {
		return nil
	}
	output, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("query xcode-select: %w; stderr: %q", err, exit.Stderr)
		}
		return fmt.Errorf("query xcode-select: %w", err)
	}
	// Remove the output terminator, not whitespace belonging to the path.
	dir := strings.TrimSuffix(string(output), "\n")
	if strings.ContainsAny(dir, "\x00\r\n") {
		return fmt.Errorf("xcode-select did not return a single path: %q", dir)
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("xcode-select directory is not absolute: %q", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat developer directory %q: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("selected developer path %q is not a directory", dir)
	}
	env["DEVELOPER_DIR"] = dir
	return nil
}
