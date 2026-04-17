package kittyproxy

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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
}

// Filter decides whether a single kitty remote-control command should be
// forwarded to the upstream socket.
type Filter struct {
	caps     map[Capability]struct{}
	patterns []CommandPattern
	owned    *OwnedSet
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
	return &Filter{caps: caps, patterns: cfg.LaunchPatterns, owned: owned}
}

func (f *Filter) hasCap(c Capability) bool {
	_, ok := f.caps[c]
	return ok
}

// command is the parsed shape of a kitty remote-control request.
// The `async` field carries an opaque response-correlation UUID that the kitty
// CLI populates on most calls (including every `launch`). It is NOT a capability
// gate — the proxy is 1-request/1-response per connection, so the UUID flows
// through to the upstream request and the upstream-stamped `async_id` flows
// back on the response verbatim. `no_response` is a fire-and-forget flag; we
// pass it through too.
type command struct {
	Cmd        string          `json:"cmd"`
	Async      string          `json:"async,omitempty"`
	NoResponse bool            `json:"no_response,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// Decide inspects raw and returns an allow/deny decision.
func (f *Filter) Decide(raw []byte) Decision {
	var c command
	if err := json.Unmarshal(raw, &c); err != nil {
		return Decision{Reason: fmt.Sprintf("malformed command: %v", err)}
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
		if !f.hasCap(CapListOwned) {
			return Decision{Cmd: c.Cmd, Reason: "list_owned capability not granted"}
		}
		return Decision{Allow: true, Cmd: c.Cmd, Reason: "ls (response will be filtered to owned ids)"}
	default:
		return Decision{Cmd: c.Cmd, Reason: fmt.Sprintf("command %q not supported by proxy", c.Cmd)}
	}
}

type launchPayload struct {
	Type string   `json:"type"`
	Args []string `json:"args"`
}

func (f *Filter) decideLaunch(c command) Decision {
	var p launchPayload
	if err := json.Unmarshal(c.Payload, &p); err != nil {
		return Decision{Cmd: c.Cmd, Reason: fmt.Sprintf("malformed launch payload: %v", err)}
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

type matchPayload struct {
	Match string `json:"match"`
}

func (f *Filter) decideOwnedMutation(c command, required Capability) Decision {
	if !f.hasCap(required) {
		return Decision{Cmd: c.Cmd, Reason: fmt.Sprintf("%s capability not granted", required)}
	}
	var p matchPayload
	if err := json.Unmarshal(c.Payload, &p); err != nil {
		return Decision{Cmd: c.Cmd, Reason: fmt.Sprintf("malformed payload: %v", err)}
	}
	if p.Match == "" {
		return Decision{Cmd: c.Cmd, Reason: "match selector required (default-focused-window is denied)"}
	}
	rest, ok := strings.CutPrefix(p.Match, "id:")
	if !ok {
		return Decision{Cmd: c.Cmd, Reason: fmt.Sprintf("non-id selector %q forbidden", p.Match)}
	}
	id, err := strconv.Atoi(rest)
	if err != nil {
		return Decision{Cmd: c.Cmd, Reason: fmt.Sprintf("malformed id selector %q", p.Match)}
	}
	if !f.owned.Contains(id) {
		return Decision{Cmd: c.Cmd, Reason: fmt.Sprintf("id %d not in OwnedSet", id)}
	}
	return Decision{Allow: true, Cmd: c.Cmd, Reason: fmt.Sprintf("%s match=id:%d", c.Cmd, id)}
}
