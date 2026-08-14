package kittyproxy

import (
	"encoding/json"
	"fmt"
	"strings"

	"devsandbox/internal/socketproxy"
)

// KittyPayloadVersion is the kitty release the payload field lists below were
// derived from. kitty's remote-control client copies its whole parsed option
// set into the payload rather than only the options the caller typed
// (kitty/rc/launch.py message_to_kitty), so these structs must enumerate every
// field that version emits: a missing one denies a legitimate launch, and an
// undeclared one used to reach the host terminal unvalidated.
//
// A newer kitty that adds an option is denied with "unknown field" - the
// fail-closed direction. Re-derive the lists from kitty/rc/*.py against that
// version when that happens.
const KittyPayloadVersion = "0.46.2"

// launchPayload is the `launch` payload. Fields are grouped by how they are
// treated, because the argv allowlist alone does not bound a launch: several
// options run host code, feed the launched process host data, or replace the
// vetted command line outright.
type launchPayload struct {
	// Vetted by decideLaunch: capability for the type, allowlist for the argv.
	Args []string `json:"args"`
	Type string   `json:"type"`

	// Pinned to their kitty defaults. env/copy_env set the environment of a
	// host process, watcher makes kitty import a Python file, stdin_source
	// pipes other windows' screen contents in, copy_cmdline discards the argv
	// that was just vetted, allow_remote_control/remote_control_password hand
	// the launched process unfiltered control of kitty, hold_after_ssh runs a
	// local shell, and os_panel/source_window/next_to/add_to_session all
	// reposition the launch against host state the sandbox chose.
	//
	// marker and logo are here for the same reason as watcher, not for the
	// cosmetic reason their names suggest: a `function <path>` marker spec has
	// kitty runpy.run_path the file (kitty/marks.py), and both resolve their
	// path argument through resolve_custom_file, which takes an absolute path
	// verbatim. The shared temp directory is bind-mounted read-write at an
	// identical path on host and sandbox, so a path the sandbox writes is a path
	// the host reads and executes.
	Env                   []string        `json:"env"`
	CopyEnv               json.RawMessage `json:"copy_env"`
	CopyCmdline           bool            `json:"copy_cmdline"`
	Watcher               []string        `json:"watcher"`
	StdinSource           string          `json:"stdin_source"`
	AllowRemoteControl    bool            `json:"allow_remote_control"`
	RemoteControlPassword []string        `json:"remote_control_password"`
	HoldAfterSSH          bool            `json:"hold_after_ssh"`
	OSPanel               []string        `json:"os_panel"`
	SourceWindow          string          `json:"source_window"`
	NextTo                string          `json:"next_to"`
	AddToSession          string          `json:"add_to_session"`
	Marker                string          `json:"marker"`
	Logo                  string          `json:"logo"`

	// Anchored to host-derived values rather than trusted as sent.
	Cwd   string `json:"cwd"`
	Match string `json:"match"`

	// Placement, titles and decoration: they cannot influence what runs or
	// what the launched process can read.
	WindowTitle             string   `json:"window_title"`
	TabTitle                string   `json:"tab_title"`
	OSWindowTitle           string   `json:"os_window_title"`
	OSWindowName            string   `json:"os_window_name"`
	OSWindowClass           string   `json:"os_window_class"`
	OSWindowState           string   `json:"os_window_state"`
	KeepFocus               bool     `json:"keep_focus"`
	CopyColors              bool     `json:"copy_colors"`
	Location                string   `json:"location"`
	Hold                    bool     `json:"hold"`
	Var                     []string `json:"var"`
	LogoPosition            string   `json:"logo_position"`
	LogoAlpha               float64  `json:"logo_alpha"`
	Color                   []string `json:"color"`
	Spacing                 []string `json:"spacing"`
	Bias                    float64  `json:"bias"`
	StdinAddFormatting      bool     `json:"stdin_add_formatting"`
	StdinAddLineWrapMarkers bool     `json:"stdin_add_line_wrap_markers"`
	Self                    bool     `json:"self"`
	NoResponse              bool     `json:"no_response"`
	WaitForChildToExit      bool     `json:"wait_for_child_to_exit"`
	ResponseTimeout         float64  `json:"response_timeout"`
}

// hostResolvedCwds are the kitty keywords that resolve against a host window's
// own working directory. An explicit path is refused: it is the one cwd value
// the sandbox picks, and it decides which tree the launched program reads.
var hostResolvedCwds = map[string]struct{}{
	"":              {},
	"current":       {},
	"oldest":        {},
	"last_reported": {},
	"root":          {},
}

// vet reports why the payload must be denied, or "" when it may be forwarded.
// hostWindowID is the host's KITTY_WINDOW_ID, the only window the sandbox is
// entitled to place a launch against.
func (p *launchPayload) vet(hostWindowID string) string {
	switch {
	case len(p.Env) > 0:
		return "launch env= is not permitted (sets environment variables for a host process)"
	case !copyEnvIsDefault(p.CopyEnv):
		return "launch copy_env is not permitted (sets environment variables for a host process)"
	case p.CopyCmdline:
		return "launch copy_cmdline is not permitted (would replace the vetted command line)"
	case len(p.Watcher) > 0:
		return "launch watcher= is not permitted (loads host code into kitty)"
	case p.StdinSource != "" && p.StdinSource != "none":
		return fmt.Sprintf("launch stdin_source=%q is not permitted (pipes host window contents in)", p.StdinSource)
	case p.AllowRemoteControl:
		return "launch allow_remote_control is not permitted (would bypass this proxy)"
	case len(p.RemoteControlPassword) > 0:
		return "launch remote_control_password is not permitted (would bypass this proxy)"
	case p.HoldAfterSSH:
		return "launch hold_after_ssh is not permitted (runs a host shell)"
	case len(p.OSPanel) > 0:
		return "launch os_panel is not permitted"
	case p.SourceWindow != "":
		return fmt.Sprintf("launch source_window=%q is not permitted", p.SourceWindow)
	case p.NextTo != "":
		return fmt.Sprintf("launch next_to=%q is not permitted", p.NextTo)
	case p.AddToSession != "":
		return fmt.Sprintf("launch add_to_session=%q is not permitted", p.AddToSession)
	case p.Marker != "":
		return "launch marker= is not permitted (a function spec loads host code into kitty)"
	case p.Logo != "":
		return "launch logo= is not permitted (reads a host file the sandbox names)"
	}
	if _, ok := hostResolvedCwds[p.Cwd]; !ok {
		return fmt.Sprintf("launch cwd=%q is not permitted (only kitty's host-resolved keywords are)", p.Cwd)
	}
	if p.Match != "" {
		if hostWindowID == "" || p.Match != "window_id:"+hostWindowID {
			return fmt.Sprintf("launch match=%q forbidden (only the host window devsandbox runs in)", p.Match)
		}
	}
	return ""
}

// copyEnvIsDefault reports whether copy_env carries kitty's default. The field
// is a bool from the CLI and a list of NAME=VALUE strings from clone-in-kitty,
// so it is decoded raw and both spellings of "unset" are accepted.
func copyEnvIsDefault(raw json.RawMessage) bool {
	switch strings.TrimSpace(string(raw)) {
	case "", "null", "false", "[]":
		return true
	}
	return false
}

// The owned-mutation payloads. Each enumerates what the kitty CLI emits for
// that command; fields that widen past the matched window are refused, since
// the id selector alone does not bound them.

// closeWindowPayload: kitty/rc/close_window.py.
type closeWindowPayload struct {
	Match         string `json:"match"`
	Self          bool   `json:"self"`
	IgnoreNoMatch bool   `json:"ignore_no_match"`
}

// focusWindowPayload: kitty/rc/focus_window.py.
type focusWindowPayload struct {
	Match string `json:"match"`
}

// waitPayload: kitty 0.46.2 ships no `wait` remote command, so only the match
// selector every other owned mutation carries is modelled. A future kitty that
// adds one is denied until its payload is enumerated here.
type waitPayload struct {
	Match string `json:"match"`
}

// sendTextPayload: kitty/rc/send_text.py. `all` and `match_tab` both replace
// the matched-window set in kitty/rc/base.py windows_for_payload, so an owned
// id in `match` does not bound where the text is typed.
type sendTextPayload struct {
	Data           string `json:"data"`
	Match          string `json:"match"`
	MatchTab       string `json:"match_tab"`
	All            bool   `json:"all"`
	ExcludeActive  bool   `json:"exclude_active"`
	SessionID      string `json:"session_id"`
	BracketedPaste string `json:"bracketed_paste"`
}

// getTextPayload: kitty/rc/get_text.py.
type getTextPayload struct {
	Match          string `json:"match"`
	Extent         string `json:"extent"`
	Ansi           bool   `json:"ansi"`
	Cursor         bool   `json:"cursor"`
	WrapMarkers    bool   `json:"wrap_markers"`
	ClearSelection bool   `json:"clear_selection"`
	Self           bool   `json:"self"`
}

// setWindowTitlePayload: kitty/rc/set_window_title.py. Its CLI always stamps
// self=true; a non-empty match takes precedence server-side, and this proxy
// requires one, so the flag is inert here.
type setWindowTitlePayload struct {
	Title     string `json:"title"`
	Match     string `json:"match"`
	Temporary bool   `json:"temporary"`
	Self      bool   `json:"self"`
}

// lsPayload: kitty/rc/ls.py. output_format=session returns a shape
// FilterLsResponse cannot filter, so only the JSON form is accepted.
type lsPayload struct {
	AllEnvVars   bool   `json:"all_env_vars"`
	Match        string `json:"match"`
	MatchTab     string `json:"match_tab"`
	Self         bool   `json:"self"`
	OutputFormat string `json:"output_format"`
}

// vetOwnedMutation strict-decodes the payload of an owned-scoped command and
// returns its match selector, or the reason it must be denied.
func vetOwnedMutation(cmd string, raw json.RawMessage) (match, reason string) {
	decode := func(dst any) string {
		if err := socketproxy.StrictUnmarshal(raw, dst); err != nil {
			return fmt.Sprintf("malformed %s payload: %v", cmd, err)
		}
		return ""
	}
	switch cmd {
	case "close-window":
		var p closeWindowPayload
		if r := decode(&p); r != "" {
			return "", r
		}
		return p.Match, ""
	case "focus-window":
		var p focusWindowPayload
		if r := decode(&p); r != "" {
			return "", r
		}
		return p.Match, ""
	case "wait":
		var p waitPayload
		if r := decode(&p); r != "" {
			return "", r
		}
		return p.Match, ""
	case "send-text":
		var p sendTextPayload
		if r := decode(&p); r != "" {
			return "", r
		}
		switch {
		case p.All:
			return "", "send-text all=true forbidden (reaches every host window)"
		case p.MatchTab != "":
			return "", fmt.Sprintf("send-text match_tab=%q forbidden (reaches every window of a host tab)", p.MatchTab)
		case p.SessionID != "":
			return "", "send-text session_id forbidden (broadcast sessions are not scoped to owned windows)"
		}
		return p.Match, ""
	case "get-text":
		var p getTextPayload
		if r := decode(&p); r != "" {
			return "", r
		}
		return p.Match, ""
	case "set-window-title":
		var p setWindowTitlePayload
		if r := decode(&p); r != "" {
			return "", r
		}
		return p.Match, ""
	}
	// Unreachable via Decide's switch; deny rather than guess a payload shape.
	return "", fmt.Sprintf("command %q has no payload validator", cmd)
}
