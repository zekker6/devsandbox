// Package dockerproxy provides a filtering proxy for the Docker socket.
//
// SECURITY WARNING: Even with filtering, Docker socket access grants significant
// privileges. The proxy allows:
//   - GET/HEAD: Read access to ALL Docker state (containers, images, volumes, networks)
//   - POST exec/attach: Execute commands in ANY container on the host
//
// This is by design — the sandbox needs Docker access for container workflows.
// However, users must understand that enabling Docker socket forwarding effectively
// grants the sandbox access equivalent to the Docker group (often root-equivalent).
//
// The proxy blocks container creation, deletion, image manipulation, and other
// write operations. But exec into existing containers is intentionally allowed.
package dockerproxy

import (
	"fmt"
	"regexp"
)

// Precompiled patterns for exec/attach allowlist
var execPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^(/v[\d.]+)?/containers/[a-zA-Z0-9_.-]+/exec$`),
	regexp.MustCompile(`^(/v[\d.]+)?/exec/[a-zA-Z0-9_.-]+/start$`),
	regexp.MustCompile(`^(/v[\d.]+)?/containers/[a-zA-Z0-9_.-]+/attach$`),
}

// IsAllowed checks if a Docker API request should be allowed.
// GET and HEAD requests are always allowed (read-only).
// POST requests for exec/attach endpoints are allowed.
// All other write operations are denied.
func IsAllowed(method, path string) bool {
	// GET and HEAD requests are always allowed (read-only operations)
	if method == "GET" || method == "HEAD" {
		return true
	}

	// Check exec/attach allowlist for POST
	if method == "POST" {
		for _, pattern := range execPatterns {
			if pattern.MatchString(path) {
				return true
			}
		}
	}

	return false
}

// DenyReason returns a human-readable reason why a request was denied.
// Returns empty string if the request is allowed.
func DenyReason(method, path string) string {
	if IsAllowed(method, path) {
		return ""
	}

	return fmt.Sprintf("docker proxy: %s %s blocked (write operations not allowed)", method, path)
}
