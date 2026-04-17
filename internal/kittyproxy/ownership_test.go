package kittyproxy

import (
	"sync"
	"testing"
)

func TestOwnedSet_AddContains(t *testing.T) {
	s := NewOwnedSet()
	if s.Contains(7) {
		t.Error("empty set should not contain 7")
	}
	s.Add(7)
	if !s.Contains(7) {
		t.Error("set should contain 7 after Add")
	}
}

func TestOwnedSet_Concurrent(t *testing.T) {
	s := NewOwnedSet()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			s.Add(id)
		}(i)
	}
	wg.Wait()
	for i := 0; i < 100; i++ {
		if !s.Contains(i) {
			t.Errorf("missing id %d after concurrent Add", i)
		}
	}
}

func TestExtractLaunchedWindowID(t *testing.T) {
	// Kitty wraps responses as: {"ok": true, "data": <command-result>}.
	// `launch` returns the new window id as the data payload (an int).
	cases := []struct {
		name    string
		body    string
		want    int
		wantErr bool
	}{
		{"int data", `{"ok":true,"data":42}`, 42, false},
		{"int data with extra fields", `{"ok":true,"data":42,"async_id":""}`, 42, false},
		{"error response", `{"ok":false,"error":"nope"}`, 0, true},
		{"non-int data", `{"ok":true,"data":"foo"}`, 0, true},
		{"missing data", `{"ok":true}`, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExtractLaunchedWindowID([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got id %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFilterLsResponse_KeepsOnlyOwned(t *testing.T) {
	owned := NewOwnedSet()
	owned.Add(2)
	owned.Add(4)

	// Kitty's ls returns: {"ok":true,"data":"<JSON-string of OS-window list>"}.
	// Each OS window has tabs; each tab has windows; each window has an int "id".
	body := `{"ok":true,"data":"[{\"tabs\":[{\"windows\":[{\"id\":1},{\"id\":2}]},{\"windows\":[{\"id\":3},{\"id\":4},{\"id\":5}]}]}]"}`
	got, err := FilterLsResponse([]byte(body), owned)
	if err != nil {
		t.Fatalf("FilterLsResponse: %v", err)
	}
	// Result should be a valid kitty response containing only ids 2 and 4.
	// Inner data is a JSON-encoded string, so quotes appear escaped on the wire.
	if !contains(string(got), `\"id\":2`) || !contains(string(got), `\"id\":4`) {
		t.Errorf("filtered response missing owned ids: %s", got)
	}
	for _, leaked := range []string{`\"id\":1`, `\"id\":3`, `\"id\":5`} {
		if contains(string(got), leaked) {
			t.Errorf("filtered response leaked %s: %s", leaked, got)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
