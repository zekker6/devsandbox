package herdrproxy

import (
	"encoding/json"
	"fmt"
	"sync"
)

// maxInFlight bounds how many un-answered requests the proxy will track for a
// single connection. The client controls request ids and can pipeline freely,
// so without a cap it could grow the map without bound by sending requests the
// server never answers.
const maxInFlight = 1024

// tabCreateResponse is the shape herdr returns for tab.create. Only the two
// identifiers the proxy needs for ownership tracking are decoded.
type tabCreateResponse struct {
	Result struct {
		Tab struct {
			TabID string `json:"tab_id"`
		} `json:"tab"`
		RootPane struct {
			PaneID string `json:"pane_id"`
		} `json:"root_pane"`
	} `json:"result"`
	Error *errorBody `json:"error,omitempty"`
}

// ExtractTabCreateIDs pulls the tab and pane identifiers out of a tab.create
// response, so the proxy can record what this sandbox is allowed to touch.
//
// Ownership is derived from the server's reply and never from anything the
// client said: that is what makes it a trustworthy basis for scoping later
// mutations.
func ExtractTabCreateIDs(body []byte) (tabID, paneID string, err error) {
	var resp tabCreateResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", "", fmt.Errorf("parse tab.create response: %w", err)
	}
	if resp.Error != nil {
		return "", "", fmt.Errorf("tab.create failed: %s", resp.Error.Message)
	}
	tabID = resp.Result.Tab.TabID
	paneID = resp.Result.RootPane.PaneID
	if tabID == "" || paneID == "" {
		return "", "", fmt.Errorf("tab.create response missing tab_id or pane_id")
	}
	return tabID, paneID, nil
}

// correlator remembers which method each in-flight request used, so a response
// can be attributed to the call that produced it.
//
// It carries its own mutex: the request pump writes it while the response pump
// reads it, and the OwnedSet's lock covers a different structure entirely.
type correlator struct {
	mu       sync.Mutex
	inFlight map[string]string // request id -> method
}

func newCorrelator() *correlator {
	return &correlator{inFlight: make(map[string]string)}
}

// Track records that a request with the given id and method was forwarded.
//
// A duplicate id is refused rather than overwritten: ids come from the client,
// and silently replacing one would let a second request inherit the first's
// pending correlation.
func (c *correlator) Track(id, method string) error {
	if id == "" {
		return fmt.Errorf("request has no id to correlate")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.inFlight[id]; exists {
		return fmt.Errorf("request id %q is already in flight", id)
	}
	if len(c.inFlight) >= maxInFlight {
		return fmt.Errorf("too many in-flight requests (limit %d)", maxInFlight)
	}
	c.inFlight[id] = method
	return nil
}

// Resolve returns the method for id and forgets it. A response arrives once, so
// holding the entry afterwards would only grow the map.
func (c *correlator) Resolve(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	method, ok := c.inFlight[id]
	if ok {
		delete(c.inFlight, id)
	}
	return method, ok
}

// len reports how many requests are outstanding. Test helper.
func (c *correlator) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.inFlight)
}
