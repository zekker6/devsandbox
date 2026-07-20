package herdrproxy

import (
	"fmt"
	"sync"
	"testing"
)

func TestExtractTabCreateIDs(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		tabID   string
		paneID  string
		wantErr bool
	}{
		{
			name:   "well-formed response",
			body:   `{"id":"a","result":{"tab":{"tab_id":"tab3"},"root_pane":{"pane_id":"pane7"}}}`,
			tabID:  "tab3",
			paneID: "pane7",
		},
		{
			name:   "extra fields are tolerated",
			body:   `{"id":"a","result":{"tab":{"tab_id":"t","label":"rev"},"root_pane":{"pane_id":"p","cwd":"/w"},"extra":1}}`,
			tabID:  "t",
			paneID: "p",
		},
		{name: "missing root_pane", body: `{"id":"a","result":{"tab":{"tab_id":"tab3"}}}`, wantErr: true},
		{name: "missing tab", body: `{"id":"a","result":{"root_pane":{"pane_id":"pane7"}}}`, wantErr: true},
		{name: "empty ids", body: `{"id":"a","result":{"tab":{"tab_id":""},"root_pane":{"pane_id":""}}}`, wantErr: true},
		{name: "error response", body: `{"id":"a","error":{"code":"x","message":"nope"}}`, wantErr: true},
		{name: "malformed json", body: `{"id":`, wantErr: true},
		{name: "empty body", body: ``, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tabID, paneID, err := ExtractTabCreateIDs([]byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ExtractTabCreateIDs(%q) = nil error, want an error", tt.body)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExtractTabCreateIDs returned error: %v", err)
			}
			if tabID != tt.tabID || paneID != tt.paneID {
				t.Errorf("got (%q, %q), want (%q, %q)", tabID, paneID, tt.tabID, tt.paneID)
			}
		})
	}
}

func TestCorrelatorTrackAndResolve(t *testing.T) {
	c := newCorrelator()

	if err := c.Track("a", methodTabCreate); err != nil {
		t.Fatalf("Track returned error: %v", err)
	}
	if err := c.Track("b", methodTabClose); err != nil {
		t.Fatalf("Track returned error: %v", err)
	}

	// Responses can arrive in any order; each must resolve to its own method.
	if m, ok := c.Resolve("b"); !ok || m != methodTabClose {
		t.Errorf(`Resolve("b") = (%q, %v), want (%q, true)`, m, ok, methodTabClose)
	}
	if m, ok := c.Resolve("a"); !ok || m != methodTabCreate {
		t.Errorf(`Resolve("a") = (%q, %v), want (%q, true)`, m, ok, methodTabCreate)
	}

	// Resolving consumes the entry.
	if _, ok := c.Resolve("a"); ok {
		t.Error("Resolve returned an entry twice, want it consumed")
	}
	if c.len() != 0 {
		t.Errorf("correlator retains %d entries after resolution, want 0", c.len())
	}
}

func TestCorrelatorRejectsDuplicateID(t *testing.T) {
	c := newCorrelator()

	if err := c.Track("dup", methodTabCreate); err != nil {
		t.Fatalf("first Track returned error: %v", err)
	}
	// The client picks its own ids; a repeat must not silently replace the
	// pending correlation.
	if err := c.Track("dup", methodTabClose); err == nil {
		t.Error("Track accepted a duplicate in-flight id, want an error")
	}

	if m, ok := c.Resolve("dup"); !ok || m != methodTabCreate {
		t.Errorf("Resolve returned %q, want the original method to survive", m)
	}
}

func TestCorrelatorRejectsEmptyID(t *testing.T) {
	c := newCorrelator()
	if err := c.Track("", methodTabCreate); err == nil {
		t.Error("Track accepted an empty id, want an error")
	}
	if _, ok := c.Resolve(""); ok {
		t.Error("Resolve matched an empty id")
	}
}

func TestCorrelatorEnforcesCap(t *testing.T) {
	c := newCorrelator()

	for i := range maxInFlight {
		if err := c.Track(fmt.Sprintf("id-%d", i), methodTabCreate); err != nil {
			t.Fatalf("Track %d returned error: %v", i, err)
		}
	}
	if err := c.Track("one-too-many", methodTabCreate); err == nil {
		t.Error("Track exceeded maxInFlight without an error, want the cap enforced")
	}

	// Resolving frees a slot.
	c.Resolve("id-0")
	if err := c.Track("now-fits", methodTabCreate); err != nil {
		t.Errorf("Track returned error after a slot freed: %v", err)
	}
}

// TestCorrelatorConcurrent exercises the mutex under -race: the request pump
// calls Track while the response pump calls Resolve.
func TestCorrelatorConcurrent(t *testing.T) {
	c := newCorrelator()
	const n = 200

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := range n {
			_ = c.Track(fmt.Sprintf("id-%d", i), methodTabCreate)
		}
	}()
	go func() {
		defer wg.Done()
		for i := range n {
			c.Resolve(fmt.Sprintf("id-%d", i))
		}
	}()

	wg.Wait()

	// Drain whatever the reader missed; the point is the absence of a race,
	// not a particular interleaving.
	for i := range n {
		c.Resolve(fmt.Sprintf("id-%d", i))
	}
	if c.len() != 0 {
		t.Errorf("correlator retains %d entries after draining, want 0", c.len())
	}
}
