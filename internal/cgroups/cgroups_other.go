//go:build !linux

package cgroups

import "errors"

// Preflight rejects any configured limit on platforms without cgroups. Limits
// are enforced through a systemd transient scope, which exists only on Linux.
// Zero limits succeed so an unconfigured sandbox is unaffected.
func Preflight(l Limits) error {
	if l.IsZero() {
		return nil
	}
	return errors.New("resource limits are only supported on Linux")
}
