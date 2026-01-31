package tools

import (
	"os/exec"
	"path/filepath"
)

func init() {
	Register(&Go{})
}

// Go provides Go language environment isolation.
// Creates isolated GOPATH, GOCACHE, and GOMODCACHE in the sandbox.
type Go struct{}

func (g *Go) Name() string {
	return "go"
}

func (g *Go) Description() string {
	return "Go language environment isolation"
}

func (g *Go) Available(homeDir string) bool {
	// Go is always "available" - we set up the environment regardless
	// of whether Go is installed, so tools work correctly when added later
	_, err := exec.LookPath("go")
	return err == nil
}

func (g *Go) Bindings(homeDir, sandboxHome string) []Binding {
	// No bindings needed - Go uses directories in sandbox home
	// which are created by EnsureSandboxDirs()
	return nil
}

func (g *Go) Environment(homeDir, sandboxHome string) []EnvVar {
	return []EnvVar{
		// Isolated Go workspace
		{Name: "GOPATH", Value: filepath.Join(sandboxHome, "go")},
		// Isolated build cache
		{Name: "GOCACHE", Value: filepath.Join(sandboxHome, ".cache", "go-build")},
		// Isolated module cache
		{Name: "GOMODCACHE", Value: filepath.Join(sandboxHome, ".cache", "go-mod")},
		// Prevent auto-downloading different toolchains (causes version conflicts)
		{Name: "GOTOOLCHAIN", Value: "local"},
	}
}

func (g *Go) ShellInit(shell string) string {
	return ""
}

func (g *Go) Check(homeDir string) CheckResult {
	result := CheckResult{
		BinaryName:  "go",
		InstallHint: "mise install go",
	}

	path, err := exec.LookPath("go")
	if err != nil {
		result.Issues = append(result.Issues, "go binary not found in PATH")
		return result
	}

	result.Available = true
	result.BinaryPath = path

	return result
}
