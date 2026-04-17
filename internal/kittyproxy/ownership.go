package kittyproxy

import (
	"encoding/json"
	"fmt"
	"sync"
)

// OwnedSet is a concurrent-safe set of window IDs the sandbox has launched.
type OwnedSet struct {
	mu  sync.RWMutex
	ids map[int]struct{}
}

func NewOwnedSet() *OwnedSet {
	return &OwnedSet{ids: make(map[int]struct{})}
}

func (s *OwnedSet) Add(id int) {
	s.mu.Lock()
	s.ids[id] = struct{}{}
	s.mu.Unlock()
}

func (s *OwnedSet) Contains(id int) bool {
	s.mu.RLock()
	_, ok := s.ids[id]
	s.mu.RUnlock()
	return ok
}

// kittyResponse is the envelope returned by every kitty remote-control command.
type kittyResponse struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// ExtractLaunchedWindowID parses a launch-command response and returns the new
// window id. Returns an error if the response is unsuccessful or shaped unexpectedly.
func ExtractLaunchedWindowID(body []byte) (int, error) {
	var resp kittyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("parse launch response: %w", err)
	}
	if !resp.OK {
		return 0, fmt.Errorf("launch failed: %s", resp.Error)
	}
	if len(resp.Data) == 0 {
		return 0, fmt.Errorf("launch response missing data")
	}
	var id int
	if err := json.Unmarshal(resp.Data, &id); err != nil {
		return 0, fmt.Errorf("launch response data not an int: %w", err)
	}
	return id, nil
}

// FilterLsResponse takes a kitty `ls` response body and returns a body with the
// same shape but with all windows whose id is not in owned removed. OS windows
// and tabs that become empty are dropped.
func FilterLsResponse(body []byte, owned *OwnedSet) ([]byte, error) {
	var resp kittyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse ls response: %w", err)
	}
	if !resp.OK {
		// Forward errors verbatim.
		return body, nil
	}

	// kitty packs the inner data as a JSON-encoded *string*, not a nested object.
	var dataStr string
	if err := json.Unmarshal(resp.Data, &dataStr); err != nil {
		return nil, fmt.Errorf("ls data not a string: %w", err)
	}

	var osWindows []map[string]any
	if err := json.Unmarshal([]byte(dataStr), &osWindows); err != nil {
		return nil, fmt.Errorf("ls inner not a list: %w", err)
	}

	filtered := filterOSWindows(osWindows, owned)

	innerBytes, err := json.Marshal(filtered)
	if err != nil {
		return nil, fmt.Errorf("marshal filtered ls: %w", err)
	}
	dataField, err := json.Marshal(string(innerBytes))
	if err != nil {
		return nil, fmt.Errorf("marshal data string: %w", err)
	}
	out := kittyResponse{OK: true, Data: dataField}
	return json.Marshal(out)
}

func filterOSWindows(osWindows []map[string]any, owned *OwnedSet) []map[string]any {
	var keptOS []map[string]any
	for _, osw := range osWindows {
		tabs, _ := osw["tabs"].([]any)
		var keptTabs []any
		for _, t := range tabs {
			tab, _ := t.(map[string]any)
			windows, _ := tab["windows"].([]any)
			var keptWindows []any
			for _, w := range windows {
				win, _ := w.(map[string]any)
				idF, ok := win["id"].(float64)
				if !ok {
					continue
				}
				if owned.Contains(int(idF)) {
					keptWindows = append(keptWindows, win)
				}
			}
			if len(keptWindows) == 0 {
				continue
			}
			tab["windows"] = keptWindows
			keptTabs = append(keptTabs, tab)
		}
		if len(keptTabs) == 0 {
			continue
		}
		osw["tabs"] = keptTabs
		keptOS = append(keptOS, osw)
	}
	if keptOS == nil {
		// Preserve empty-list shape rather than encoding null.
		return []map[string]any{}
	}
	return keptOS
}
