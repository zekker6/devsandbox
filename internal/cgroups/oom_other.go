//go:build !linux

package cgroups

import "context"

// ScopeCgroupDir cannot resolve a cgroup on a platform that has none.
func ScopeCgroupDir(_ context.Context, _ int) (string, error) {
	return "", ErrOOMUnsupported
}

// ContainerCgroupDir cannot resolve a cgroup on a platform that has none. On
// macOS the engine's PIDs belong to the VM its daemon runs in, so there would be
// nothing local to resolve them against even if there were a cgroup hierarchy.
func ContainerCgroupDir(_ int, _ string) (string, error) {
	return "", ErrOOMUnsupported
}

// WatchOOM reports the platform limitation rather than returning a watcher that
// would silently never fire.
func WatchOOM(_ string, _ Ownership, _ func(OOMStats)) (*OOMWatcher, error) {
	return nil, ErrOOMUnsupported
}
