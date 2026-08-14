package kittyproxy

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"devsandbox/internal/socketproxy"
)

// Decision is the outcome of filtering a single kitty command.
type Decision struct {
	Allow   bool
	Reason  string // populated for both allow and deny (deny: why; allow: short summary for logs)
	Cmd     string // command name extracted from the payload (best effort)
	Program string // for launch: argv[0] of the launched command
}

// FilterConfig configures a Filter.
type FilterConfig struct {
	Capabilities   []Capability
	LaunchPatterns []CommandPattern
	Owned          *OwnedSet

	// HostWindowID is the host's KITTY_WINDOW_ID, read on the host side. It
	// anchors the `match` selector of a launch: the sandbox may place a window
	// against the kitty window devsandbox itself runs in, and nothing else.
	// Empty means no selector is accepted.
	HostWindowID string
}

// Filter decides whether a single kitty remote-control command should be
// forwarded to the upstream socket.
type Filter struct {
	caps         map[Capability]struct{}
	patterns     []CommandPattern
	owned        *OwnedSet
	hostWindowID string
}

func NewFilter(cfg FilterConfig) *Filter {
	caps := make(map[Capability]struct{}, len(cfg.Capabilities))
	for _, c := range cfg.Capabilities {
		caps[c] = struct{}{}
	}
	owned := cfg.Owned
	if owned == nil {
		owned = NewOwnedSet()
	}
	return &Filter{
		caps:         caps,
		patterns:     cfg.LaunchPatterns,
		owned:        owned,
		hostWindowID: strings.TrimSpace(cfg.HostWindowID),
	}
}

func (f *Filter) hasCap(c Capability) bool {
	_, ok := f.caps[c]
	return ok
}

// command is the parsed shape of a kitty remote-control request envelope,
// as kitty's own client builds it (create_basic_command in
// kitty/remote_control.py, kitty KittyPayloadVersion).
//
// The `async` field carries an opaque response-correlation UUID that the kitty
// CLI populates on most calls (including every `launch`). It is NOT a capability
// gate — the proxy is 1-request/1-response per connection, so the UUID flows
// through to the upstream request and the upstream-stamped `async_id` flows
// back on the response verbatim. `no_response` is a fire-and-forget flag; we
// pass it through too.
//
// The encryption fields are declared only so a denial can name them: kitty
// executes the command inside `encrypted` and ignores the outer `cmd`, so an
// encrypted envelope forwarded on the strength of its outer name would run
// something this filter never saw.
type command struct {
	Cmd        string          `json:"cmd"`
	Version    []int           `json:"version,omitempty"`
	Async      string          `json:"async,omitempty"`
	NoResponse bool            `json:"no_response,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`

	Encrypted json.RawMessage `json:"encrypted,omitempty"`
	EncProto  string          `json:"enc_proto,omitempty"`
	PubKey    string          `json:"pubkey,omitempty"`
	IV        string          `json:"iv,omitempty"`
	Tag       string          `json:"tag,omitempty"`
	Password  json.RawMessage `json:"password,omitempty"`
}

// Decide inspects raw and returns an allow/deny decision.
func (f *Filter) Decide(raw []byte) Decision {
	var c command
	if err := socketproxy.StrictUnmarshal(raw, &c); err != nil {
		return Decision{Reason: fmt.Sprintf("malformed command: %v", err)}
	}
	if len(c.Encrypted) > 0 {
		return Decision{Cmd: c.Cmd, Reason: "encrypted commands are forbidden (the proxy cannot see what would run)"}
	}
	if len(c.Password) > 0 {
		return Decision{Cmd: c.Cmd, Reason: "password-authenticated commands are forbidden (use allow_remote_control = socket-only)"}
	}
	switch c.Cmd {
	case "launch":
		return f.decideLaunch(c)
	case "close-window":
		return f.decideOwnedMutation(c, CapCloseOwned)
	case "wait":
		return f.decideOwnedMutation(c, CapWaitOwned)
	case "focus-window":
		return f.decideOwnedMutation(c, CapFocusOwned)
	case "send-text":
		return f.decideOwnedMutation(c, CapSendTextOwned)
	case "get-text":
		return f.decideOwnedMutation(c, CapGetTextOwned)
	case "set-window-title":
		return f.decideOwnedMutation(c, CapSetTitleOwned)
	case "ls":
		return f.decideLs(c)
	default:
		return Decision{Cmd: c.Cmd, Reason: fmt.Sprintf("command %q not supported by proxy", c.Cmd)}
	}
}

func (f *Filter) decideLaunch(c command) Decision {
	var p launchPayload
	if err := socketproxy.StrictUnmarshal(c.Payload, &p); err != nil {
		return Decision{Cmd: c.Cmd, Reason: fmt.Sprintf("malformed launch payload: %v", err)}
	}
	if reason := p.vet(f.hostWindowID); reason != "" {
		return Decision{Cmd: c.Cmd, Reason: reason}
	}
	required, ok := capForLaunchType(p.Type)
	if !ok {
		return Decision{Cmd: c.Cmd, Reason: fmt.Sprintf("unsupported launch type %q", p.Type)}
	}
	if !f.hasCap(required) {
		return Decision{Cmd: c.Cmd, Reason: fmt.Sprintf("%s capability not granted", required)}
	}
	if len(p.Args) == 0 {
		return Decision{Cmd: c.Cmd, Reason: "launch with no args (would open default shell) is denied"}
	}
	for _, pat := range f.patterns {
		if pat.MatchesArgv(p.Args) {
			return Decision{
				Allow:   true,
				Cmd:     c.Cmd,
				Program: p.Args[0],
				Reason:  fmt.Sprintf("launch %s program=%s", p.Type, p.Args[0]),
			}
		}
	}
	return Decision{Cmd: c.Cmd, Program: p.Args[0],
		Reason: fmt.Sprintf("no launch pattern matched program=%s args=%v", p.Args[0], p.Args[1:])}
}

// decideLs gates `ls` on the capability and on the response staying filterable:
// an output format FilterLsResponse cannot parse would put every host window,
// tab and per-window env in front of the sandbox.
func (f *Filter) decideLs(c command) Decision {
	if !f.hasCap(CapListOwned) {
		return Decision{Cmd: c.Cmd, Reason: "list_owned capability not granted"}
	}
	var p lsPayload
	if err := socketproxy.StrictUnmarshal(c.Payload, &p); err != nil {
		return Decision{Cmd: c.Cmd, Reason: fmt.Sprintf("malformed ls payload: %v", err)}
	}
	if p.OutputFormat != "" && p.OutputFormat != "json" {
		return Decision{Cmd: c.Cmd, Reason: fmt.Sprintf("ls output_format=%q forbidden (response would not be filterable)", p.OutputFormat)}
	}
	return Decision{Allow: true, Cmd: c.Cmd, Reason: "ls (response will be filtered to owned ids)"}
}

func capForLaunchType(t string) (Capability, bool) {
	switch t {
	case "overlay":
		return CapLaunchOverlay, true
	case "window", "":
		// kitty defaults to "window" when type is omitted
		return CapLaunchWindow, true
	case "tab":
		return CapLaunchTab, true
	case "os-window":
		return CapLaunchOSWindow, true
	}
	return "", false
}

func (f *Filter) decideOwnedMutation(c command, required Capability) Decision {
	if !f.hasCap(required) {
		return Decision{Cmd: c.Cmd, Reason: fmt.Sprintf("%s capability not granted", required)}
	}
	match, reason := vetOwnedMutation(c.Cmd, c.Payload)
	if reason != "" {
		return Decision{Cmd: c.Cmd, Reason: reason}
	}
	if match == "" {
		return Decision{Cmd: c.Cmd, Reason: "match selector required (default-focused-window is denied)"}
	}
	rest, ok := strings.CutPrefix(match, "id:")
	if !ok {
		return Decision{Cmd: c.Cmd, Reason: fmt.Sprintf("non-id selector %q forbidden", match)}
	}
	id, err := strconv.Atoi(rest)
	if err != nil {
		return Decision{Cmd: c.Cmd, Reason: fmt.Sprintf("malformed id selector %q", match)}
	}
	if !f.owned.Contains(id) {
		return Decision{Cmd: c.Cmd, Reason: fmt.Sprintf("id %d not in OwnedSet", id)}
	}
	return Decision{Allow: true, Cmd: c.Cmd, Reason: fmt.Sprintf("%s match=id:%d", c.Cmd, id)}
}
