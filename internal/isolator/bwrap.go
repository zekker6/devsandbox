package isolator

import (
	"context"
	"errors"
	"os/exec"
)

// BwrapIsolator implements Isolator using bubblewrap.
type BwrapIsolator struct{}

// NewBwrapIsolator creates a new bwrap isolator.
func NewBwrapIsolator() *BwrapIsolator {
	return &BwrapIsolator{}
}

// Name returns the backend name.
func (b *BwrapIsolator) Name() Backend {
	return BackendBwrap
}

// Available checks if bwrap is installed and provides helpful installation instructions if not.
func (b *BwrapIsolator) Available() error {
	_, err := exec.LookPath("bwrap")
	if err != nil {
		return errors.New("bubblewrap (bwrap) is not installed\n" +
			"Install with:\n" +
			"  Arch:   pacman -S bubblewrap\n" +
			"  Debian: apt install bubblewrap\n" +
			"  Fedora: dnf install bubblewrap\n\n" +
			"Or use Docker backend: devsandbox --isolation=docker")
	}
	return nil
}

// Build constructs the command to run the sandbox.
// This is a thin wrapper - actual bwrap args are built by sandbox.Builder.
// For now, returns the bwrap path; integration happens in Phase 3.
func (b *BwrapIsolator) Build(ctx context.Context, cfg *Config) (string, []string, error) {
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		return "", nil, err
	}
	return bwrapPath, nil, nil
}

// Cleanup performs any post-sandbox cleanup.
// BwrapIsolator has no cleanup requirements.
func (b *BwrapIsolator) Cleanup() error {
	return nil
}
