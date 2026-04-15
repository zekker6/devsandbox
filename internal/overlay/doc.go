// Package overlay provides helpers for locating, planning, and applying
// migrations of sandbox overlay upper directories to host paths.
//
// Overlay upper layout under a sandbox's sandboxHome:
//
//	<sandboxHome>/overlay/<safePath>/upper            — primary persistent upper
//	<sandboxHome>/overlay/sessions/<sid>/<safePath>/upper — per-session upper
//
// where safePath is the destination mount path with the leading "/" stripped
// and remaining "/" replaced with "_".
package overlay
