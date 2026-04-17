package kittyproxy

import (
	"encoding/json"
	"testing"
)

func mkFilter(caps []Capability, patterns []CommandPattern, owned *OwnedSet) *Filter {
	return NewFilter(FilterConfig{Capabilities: caps, LaunchPatterns: patterns, Owned: owned})
}

func mkCmd(t *testing.T, cmd string, payload any) []byte {
	t.Helper()
	body := map[string]any{"cmd": cmd, "version": []int{0, 1, 0}, "payload": payload}
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFilter_LaunchOverlay_Allowed(t *testing.T) {
	patterns := []CommandPattern{{Program: "revdiff", ArgsMatcher: MatchAny()}}
	f := mkFilter([]Capability{CapLaunchOverlay}, patterns, NewOwnedSet())

	cmd := mkCmd(t, "launch", map[string]any{
		"type": "overlay",
		"args": []string{"revdiff", "a", "b"},
	})
	if d := f.Decide(cmd); d.Allow != true {
		t.Errorf("expected allow, got deny: %s", d.Reason)
	}
}

func TestFilter_LaunchOverlay_DeniedByPattern(t *testing.T) {
	patterns := []CommandPattern{{Program: "revdiff", ArgsMatcher: MatchAny()}}
	f := mkFilter([]Capability{CapLaunchOverlay}, patterns, NewOwnedSet())

	cmd := mkCmd(t, "launch", map[string]any{
		"type": "overlay",
		"args": []string{"sh", "-c", "curl evil"},
	})
	if d := f.Decide(cmd); d.Allow {
		t.Errorf("expected deny, got allow")
	}
}

func TestFilter_LaunchOverlay_DeniedByMissingCapability(t *testing.T) {
	f := mkFilter(nil, nil, NewOwnedSet())
	cmd := mkCmd(t, "launch", map[string]any{
		"type": "overlay",
		"args": []string{"revdiff"},
	})
	if d := f.Decide(cmd); d.Allow {
		t.Errorf("expected deny without capability")
	}
}

func TestFilter_LaunchType_RequiresMatchingCapability(t *testing.T) {
	// CapLaunchOverlay does NOT cover type=window.
	f := mkFilter([]Capability{CapLaunchOverlay},
		[]CommandPattern{{Program: "revdiff", ArgsMatcher: MatchAny()}},
		NewOwnedSet())

	cmd := mkCmd(t, "launch", map[string]any{
		"type": "window",
		"args": []string{"revdiff"},
	})
	if d := f.Decide(cmd); d.Allow {
		t.Errorf("expected deny: launch_window cap missing")
	}
}

func TestFilter_CloseWindow_OwnedAllowed(t *testing.T) {
	owned := NewOwnedSet()
	owned.Add(7)
	f := mkFilter([]Capability{CapCloseOwned}, nil, owned)

	cmd := mkCmd(t, "close-window", map[string]any{
		"match": "id:7",
	})
	if d := f.Decide(cmd); !d.Allow {
		t.Errorf("expected allow, got deny: %s", d.Reason)
	}
}

func TestFilter_CloseWindow_UnownedDenied(t *testing.T) {
	owned := NewOwnedSet()
	owned.Add(7)
	f := mkFilter([]Capability{CapCloseOwned}, nil, owned)

	cmd := mkCmd(t, "close-window", map[string]any{"match": "id:99"})
	if d := f.Decide(cmd); d.Allow {
		t.Errorf("expected deny for unowned id")
	}
}

func TestFilter_CloseWindow_NonIDSelectorDenied(t *testing.T) {
	owned := NewOwnedSet()
	owned.Add(7)
	f := mkFilter([]Capability{CapCloseOwned}, nil, owned)

	for _, sel := range []string{"title:foo", "pid:1234", "recent:0", "state:focused", ""} {
		t.Run(sel, func(t *testing.T) {
			cmd := mkCmd(t, "close-window", map[string]any{"match": sel})
			if d := f.Decide(cmd); d.Allow {
				t.Errorf("expected deny for selector %q", sel)
			}
		})
	}
}

// The kitty CLI always stamps a populated `async` UUID on launch commands to
// correlate the response. The proxy must pass that through transparently —
// `async` is not a capability gate; it's opaque response-routing metadata.
// Captured wire shape from kitty 0.46.2: `"async":"DILmnl4SpZCUrX3wSeVdmc"`.
func TestFilter_AsyncUuidPassesThrough(t *testing.T) {
	f := mkFilter([]Capability{CapLaunchOverlay},
		[]CommandPattern{{Program: "revdiff", ArgsMatcher: MatchAny()}}, NewOwnedSet())

	for _, async := range []string{"", "DILmnl4SpZCUrX3wSeVdmc"} {
		t.Run("async="+async, func(t *testing.T) {
			body := map[string]any{
				"cmd":     "launch",
				"async":   async,
				"payload": map[string]any{"type": "overlay", "args": []string{"revdiff"}},
			}
			raw, _ := json.Marshal(body)
			if d := f.Decide(raw); !d.Allow {
				t.Errorf("expected allow (async=%q), got deny: %s", async, d.Reason)
			}
		})
	}
}

func TestFilter_UnknownCommandDenied(t *testing.T) {
	f := mkFilter([]Capability{CapLaunchOverlay, CapCloseOwned, CapListOwned}, nil, NewOwnedSet())
	cmd := mkCmd(t, "set-colors", map[string]any{})
	if d := f.Decide(cmd); d.Allow {
		t.Errorf("expected deny for unmodelled command")
	}
}

func TestFilter_LsAlwaysAllowedWhenCapPresent(t *testing.T) {
	f := mkFilter([]Capability{CapListOwned}, nil, NewOwnedSet())
	cmd := mkCmd(t, "ls", map[string]any{})
	if d := f.Decide(cmd); !d.Allow {
		t.Errorf("expected allow for ls with cap")
	}
}
