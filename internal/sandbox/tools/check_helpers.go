// internal/sandbox/tools/check_helpers.go
package tools

import (
	"os"
	"os/exec"
)

// CheckBinary looks up a binary and returns a CheckResult.
// If found, Available is true and BinaryPath is set.
// If not found, adds an issue with the standard message format.
func CheckBinary(binaryName, installHint string) CheckResult {
	result := CheckResult{
		BinaryName:  binaryName,
		InstallHint: installHint,
	}

	path, err := exec.LookPath(binaryName)
	if err != nil {
		result.Issues = append(result.Issues, binaryName+" binary not found in PATH")
		return result
	}

	result.Available = true
	result.BinaryPath = path
	return result
}

// AddConfigPath adds a path to ConfigPaths if it exists.
func (r *CheckResult) AddConfigPath(path string) {
	if _, err := os.Stat(path); err == nil {
		r.ConfigPaths = append(r.ConfigPaths, path)
	}
}

// AddConfigPaths adds multiple paths, only those that exist.
func (r *CheckResult) AddConfigPaths(paths ...string) {
	for _, p := range paths {
		r.AddConfigPath(p)
	}
}

// AddIssue adds an issue message to the result.
func (r *CheckResult) AddIssue(issue string) {
	r.Issues = append(r.Issues, issue)
}

// AddInfo adds an informational message to the result.
// Use this for status information that is not a problem.
func (r *CheckResult) AddInfo(info string) {
	r.Info = append(r.Info, info)
}
