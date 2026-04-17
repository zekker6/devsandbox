// Package kittyproxy provides a filtering proxy for the kitty terminal remote-control socket.
//
// SECURITY MODEL: The proxy enforces an allowlist of capabilities declared by enabled tools.
// Mutating operations are scoped to windows the sandbox itself created (ownership tracking).
// The host kitty socket is NOT bind-mounted into the sandbox; only the proxy socket is.
package kittyproxy

// Capability identifies a single kitty operation a tool may request.
// Mutating capabilities are suffixed `_owned` to make ownership scoping explicit.
type Capability string

const (
	CapLaunchOverlay  Capability = "launch_overlay"   // launch --type=overlay
	CapLaunchWindow   Capability = "launch_window"    // launch --type=window
	CapLaunchTab      Capability = "launch_tab"       // launch --type=tab
	CapLaunchOSWindow Capability = "launch_os_window" // launch --type=os-window

	CapCloseOwned    Capability = "close_owned"     // close-window, scoped to owned ids
	CapWaitOwned     Capability = "wait_owned"      // wait, scoped to owned ids
	CapFocusOwned    Capability = "focus_owned"     // focus-window, scoped to owned ids
	CapSendTextOwned Capability = "send_text_owned" // send-text, scoped to owned ids
	CapGetTextOwned  Capability = "get_text_owned"  // get-text, scoped to owned ids
	CapSetTitleOwned Capability = "set_title_owned" // set-window-title, scoped to owned ids
	CapListOwned     Capability = "list_owned"      // ls — response filtered to owned ids only
)

// IsLaunch reports whether c is one of the launch_* capabilities.
// Launch capabilities are special: they imply arbitrary host code execution and
// must be paired with a CommandPattern allowlist.
func IsLaunch(c Capability) bool {
	switch c {
	case CapLaunchOverlay, CapLaunchWindow, CapLaunchTab, CapLaunchOSWindow:
		return true
	}
	return false
}
