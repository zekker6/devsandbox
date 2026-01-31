// Package version provides build version information.
package version

// These variables are set via ldflags at build time.
// Example: go build -ldflags "-X devsandbox/internal/version.Commit=abc123"
var (
	// Version is the semantic version (e.g., "1.0.0").
	Version = "0.0.0"

	// Commit is the git commit hash.
	Commit = "dev"

	// Date is the build date.
	Date = "unknown"
)
