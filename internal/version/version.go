// Package version provides build version information.
package version

// These variables are set via ldflags at build time.
// Example: go build -ldflags "-X devsandbox/internal/version.Commit=abc123"
var (
	// Version is the semantic version (e.g., "1.0.0").
	// This is set from git tags if available.
	Version = "dev"

	// Commit is the git commit hash.
	Commit = "unknown"

	// Date is the build date.
	Date = "unknown"

	// Dirty indicates if there were uncommitted changes at build time.
	// Set to "true" or "false".
	Dirty = "false"

	// DirtyHash is a short hash of the uncommitted changes (if dirty).
	// This helps identify which modifications were included in the build.
	DirtyHash = ""
)

// IsDirty returns true if the build was made with uncommitted changes.
func IsDirty() bool {
	return Dirty == "true"
}

// FullVersion returns the version string with commit and dirty state.
// Examples:
//   - Tagged release: "v1.0.0 (abc1234)"
//   - Tagged dirty:   "v1.0.0 (abc1234-dirty:f3e2a1b)"
//   - Untagged:       "dev (abc1234)"
//   - Untagged dirty: "dev (abc1234-dirty:f3e2a1b)"
func FullVersion() string {
	v := Version + " (" + Commit
	if IsDirty() {
		v += "-dirty"
		if DirtyHash != "" {
			v += ":" + DirtyHash
		}
	}
	v += ")"
	return v
}
