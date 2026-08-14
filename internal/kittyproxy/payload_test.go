package kittyproxy

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"testing"
)

func launchFilter(t *testing.T) *Filter {
	t.Helper()
	return NewFilter(FilterConfig{
		Capabilities:   []Capability{CapLaunchOverlay},
		LaunchPatterns: []CommandPattern{{Program: "revdiff", ArgsMatcher: MatchAny()}},
		Owned:          NewOwnedSet(),
	})
}

// Every field kitty's `launch` accepts beyond type/args used to ride through
// unvalidated: the payload was decoded for type and args only and then
// forwarded verbatim to the host socket.
func TestFilter_LaunchPayload_RejectsUnvettedOptions(t *testing.T) {
	cases := map[string]map[string]any{
		"env":                     {"env": []string{"EDITOR=/tmp/evil"}},
		"cwd path":                {"cwd": "/etc"},
		"watcher":                 {"watcher": []string{"/tmp/w.py"}},
		"stdin_source":            {"stdin_source": "@screen_scrollback"},
		"copy_env list":           {"copy_env": []string{"LD_PRELOAD=/tmp/x.so"}},
		"copy_env true":           {"copy_env": true},
		"copy_cmdline":            {"copy_cmdline": true},
		"allow_remote_control":    {"allow_remote_control": true},
		"remote_control_password": {"remote_control_password": []string{"pw"}},
		"hold_after_ssh":          {"hold_after_ssh": true},
		"os_panel":                {"os_panel": []string{"lines=2"}},
		"source_window":           {"source_window": "title:secrets"},
		"next_to":                 {"next_to": "title:secrets"},
		"add_to_session":          {"add_to_session": "."},
		"foreign match":           {"match": "title:secrets"},
	}
	f := launchFilter(t)
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			payload := map[string]any{"type": "overlay", "args": []string{"revdiff"}}
			maps.Copy(payload, extra)
			if d := f.Decide(mkCmd(t, "launch", payload)); d.Allow {
				t.Fatalf("expected deny for %s, got allow", name)
			}
		})
	}
}

// A field the struct does not declare means a kitty version this filter was
// never checked against - deny rather than forward it blind.
func TestFilter_LaunchPayload_RejectsUnknownField(t *testing.T) {
	f := launchFilter(t)
	cmd := mkCmd(t, "launch", map[string]any{
		"type": "overlay", "args": []string{"revdiff"},
		"exec_the_thing": "whatever",
	})
	if d := f.Decide(cmd); d.Allow {
		t.Fatalf("expected deny for undeclared field, got allow")
	}
}

// Fields kitty resolves against host-side state, or that cannot reach the
// launched process, stay allowed - the filter must not deny a legitimate
// launch just because an option is present.
func TestFilter_LaunchPayload_AllowsInertOptions(t *testing.T) {
	cases := map[string]map[string]any{
		"cwd current":       {"cwd": "current"},
		"cwd last_reported": {"cwd": "last_reported"},
		"window title":      {"window_title": "revdiff"},
		"hold":              {"hold": true},
		"keep_focus":        {"keep_focus": true},
		"colors":            {"color": []string{"background=white"}},
		"logo":              {"logo": "l.png", "logo_alpha": -1.0},
		"stdin_source none": {"stdin_source": "none"},
		"null strings":      {"marker": nil, "tab_title": nil},
	}
	f := launchFilter(t)
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			payload := map[string]any{"type": "overlay", "args": []string{"revdiff"}}
			maps.Copy(payload, extra)
			if d := f.Decide(mkCmd(t, "launch", payload)); !d.Allow {
				t.Fatalf("expected allow for %s, got deny: %s", name, d.Reason)
			}
		})
	}
}

// `match` selects the tab the new window lands in, so it is anchored to the
// host window devsandbox itself is running in (KITTY_WINDOW_ID) rather than
// left free-form.
func TestFilter_LaunchPayload_MatchAnchoredToHostWindow(t *testing.T) {
	f := NewFilter(FilterConfig{
		Capabilities:   []Capability{CapLaunchOverlay},
		LaunchPatterns: []CommandPattern{{Program: "revdiff", ArgsMatcher: MatchAny()}},
		Owned:          NewOwnedSet(),
		HostWindowID:   "3",
	})
	tests := []struct {
		match string
		allow bool
	}{
		{"window_id:3", true},
		{"", true},
		{"window_id:4", false},
		{"id:3", false},
		{"title:secrets", false},
	}
	for _, tc := range tests {
		t.Run(tc.match, func(t *testing.T) {
			cmd := mkCmd(t, "launch", map[string]any{
				"type": "overlay", "args": []string{"revdiff"}, "match": tc.match,
			})
			if d := f.Decide(cmd); d.Allow != tc.allow {
				t.Fatalf("match=%q: allow=%v want %v (%s)", tc.match, d.Allow, tc.allow, d.Reason)
			}
		})
	}
}

// Without a host window id there is nothing to anchor `match` against, so any
// selector is refused rather than accepted on the sandbox's word.
func TestFilter_LaunchPayload_MatchDeniedWithoutHostWindow(t *testing.T) {
	f := launchFilter(t)
	cmd := mkCmd(t, "launch", map[string]any{
		"type": "overlay", "args": []string{"revdiff"}, "match": "window_id:3",
	})
	if d := f.Decide(cmd); d.Allow {
		t.Fatalf("expected deny when no host window id is known")
	}
}

// The payload the kitty CLI actually puts on the wire for the revdiff overlay
// launch must still be allowed: kitty serializes its whole option set, not
// just the options the launcher passed, so a field list that is merely
// plausible would deny every real launch.
func TestFilter_LaunchPayload_KittyCLIPayloadAllowed(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "launch-overlay-kitty-0.46.2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	f := NewFilter(FilterConfig{
		Capabilities:   []Capability{CapLaunchOverlay},
		LaunchPatterns: []CommandPattern{{Program: "sh", ArgsMatcher: MatchPrefix("-c")}},
		Owned:          NewOwnedSet(),
		HostWindowID:   "3",
	})
	if d := f.Decide(mkCmd(t, "launch", payload)); !d.Allow {
		t.Fatalf("expected allow for the real kitty payload, got deny: %s", d.Reason)
	}
}

// kitty executes the command inside `encrypted` and ignores the outer `cmd`,
// so the envelope needs the same deny-by-default decode as the payload.
func TestFilter_Envelope_RejectsBypassFields(t *testing.T) {
	f := launchFilter(t)
	allowed := map[string]any{"type": "overlay", "args": []string{"revdiff"}}

	cases := map[string]map[string]any{
		"encrypted": {"cmd": "launch", "version": []int{0, 46, 2}, "payload": allowed,
			"encrypted": "Zm9v", "enc_proto": "1", "pubkey": "k", "iv": "i", "tag": "t"},
		"password": {"cmd": "launch", "version": []int{0, 46, 2}, "payload": allowed, "password": "hunter2"},
		"unknown field": {"cmd": "launch", "version": []int{0, 46, 2}, "payload": allowed,
			"stream_id": "s"},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			if d := f.Decide(raw); d.Allow {
				t.Fatalf("expected deny for envelope with %s", name)
			}
		})
	}

	// The envelope kitty's own client builds must still pass.
	raw, err := json.Marshal(map[string]any{
		"cmd": "launch", "version": []int{0, 46, 2}, "no_response": false,
		"async": "DILmnl4SpZCUrX3wSeVdmc", "payload": allowed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d := f.Decide(raw); !d.Allow {
		t.Fatalf("expected allow for the CLI envelope, got deny: %s", d.Reason)
	}
}

// send-text's `all` and `match_tab` both widen past the matched window
// (kitty/rc/base.py windows_for_payload), so an owned-id selector on its own
// does not bound where the text lands.
func TestFilter_OwnedMutation_RejectsWideningFields(t *testing.T) {
	owned := NewOwnedSet()
	owned.Add(7)
	f := mkFilter([]Capability{CapSendTextOwned, CapGetTextOwned, CapCloseOwned}, nil, owned)

	cases := []struct {
		name    string
		cmd     string
		payload map[string]any
	}{
		{"send-text all", "send-text", map[string]any{"match": "id:7", "data": "text:hi", "all": true}},
		{"send-text match_tab", "send-text", map[string]any{"match": "id:7", "data": "text:hi", "match_tab": "title:x"}},
		{"send-text session", "send-text", map[string]any{"match": "id:7", "data": "text:hi", "session_id": "s"}},
		{"send-text unknown", "send-text", map[string]any{"match": "id:7", "data": "text:hi", "sneak": 1}},
		{"close-window unknown", "close-window", map[string]any{"match": "id:7", "sneak": 1}},
		{"get-text unknown", "get-text", map[string]any{"match": "id:7", "sneak": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if d := f.Decide(mkCmd(t, tc.cmd, tc.payload)); d.Allow {
				t.Fatalf("expected deny for %s", tc.name)
			}
		})
	}
}

// The fields the kitty CLI does emit for these commands must still pass.
func TestFilter_OwnedMutation_AllowsCLIFields(t *testing.T) {
	owned := NewOwnedSet()
	owned.Add(7)
	f := mkFilter([]Capability{CapSendTextOwned, CapGetTextOwned, CapCloseOwned, CapSetTitleOwned}, nil, owned)

	cases := []struct {
		name    string
		cmd     string
		payload map[string]any
	}{
		{"close-window", "close-window", map[string]any{"match": "id:7", "self": false, "ignore_no_match": false}},
		{"send-text", "send-text", map[string]any{
			"match": "id:7", "data": "base64:aGk=", "match_tab": nil,
			"all": false, "exclude_active": false, "bracketed_paste": "auto",
		}},
		{"get-text", "get-text", map[string]any{
			"match": "id:7", "extent": "screen", "ansi": false,
			"cursor": false, "wrap_markers": false, "clear_selection": false, "self": false,
		}},
		// set-window-title's CLI always stamps self=true; the match selector
		// takes precedence over it server-side, so it must not be a denial.
		{"set-window-title", "set-window-title", map[string]any{
			"match": "id:7", "title": "revdiff", "temporary": false, "self": true,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if d := f.Decide(mkCmd(t, tc.cmd, tc.payload)); !d.Allow {
				t.Fatalf("expected allow for %s, got deny: %s", tc.name, d.Reason)
			}
		})
	}
}

// `ls --output-format session` returns a shape FilterLsResponse cannot parse,
// which used to mean the unfiltered upstream body reached the sandbox.
func TestFilter_Ls_RejectsNonJSONOutputFormat(t *testing.T) {
	f := mkFilter([]Capability{CapListOwned}, nil, NewOwnedSet())

	if d := f.Decide(mkCmd(t, "ls", map[string]any{"output_format": "session"})); d.Allow {
		t.Fatalf("expected deny for output_format=session")
	}
	if d := f.Decide(mkCmd(t, "ls", map[string]any{"sneak": true})); d.Allow {
		t.Fatalf("expected deny for undeclared ls field")
	}
	allowed := map[string]any{"all_env_vars": false, "match": nil, "match_tab": nil, "output_format": "json"}
	if d := f.Decide(mkCmd(t, "ls", allowed)); !d.Allow {
		t.Fatalf("expected allow for the CLI's own ls payload: %s", d.Reason)
	}
}
