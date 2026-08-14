//go:build !linux

package overlay

// isOpaqueDir reports whether dir carries an overlayfs opaque marker.
// overlayfs is Linux-only, and the xattr syscalls differ per platform, so
// there is nothing to read anywhere else: the package still compiles for the
// darwin release build (internal/bwrap and internal/isolator/bwrap.go carry no
// build tags and reach it).
func isOpaqueDir(string) bool { return false }
