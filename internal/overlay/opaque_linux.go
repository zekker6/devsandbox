package overlay

import "syscall"

// opaqueXattrs are the markers overlayfs writes on a directory that hides the
// lower layers' contents of the same path - the recreate half of a
// delete-then-recreate within one upper. A privileged mount (the shim's
// in-guest overlay, mounted as root) writes the trusted spelling; an
// unprivileged one (bwrap's overlay inside a user namespace, which the kernel
// requires be mounted with userxattr) writes the user spelling. devsandbox
// produces uppers both ways, so both are read.
var opaqueXattrs = []string{"trusted.overlay.opaque", "user.overlay.opaque"}

// isOpaqueDir reports whether dir carries an overlayfs opaque marker. A
// missing marker, an unreadable one (the trusted namespace needs
// CAP_SYS_ADMIN) and a filesystem without xattr support all mean "not opaque":
// the entry is then merged with the lower layers, which is the reading that
// keeps data rather than discarding it.
func isOpaqueDir(dir string) bool {
	// The value is a single "y"; anything longer is not a marker overlayfs
	// wrote, and an oversized value makes Getxattr return ERANGE, read here as
	// not opaque.
	var buf [4]byte
	for _, attr := range opaqueXattrs {
		n, err := syscall.Getxattr(dir, attr, buf[:])
		if err != nil || n <= 0 {
			continue
		}
		if string(buf[:n]) == "y" {
			return true
		}
	}
	return false
}
