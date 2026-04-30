package logging

import (
	"encoding/json"
	"testing"
	"time"
)

func TestScopeNameFor(t *testing.T) {
	cases := map[string]struct {
		entry *Entry
		want  string
	}{
		"component=proxy":   {&Entry{Fields: map[string]any{"component": "proxy"}}, "devsandbox.proxy"},
		"component=mounts":  {&Entry{Fields: map[string]any{"component": "mounts"}}, "devsandbox.mounts"},
		"component=wrapper": {&Entry{Fields: map[string]any{"component": "wrapper"}}, "devsandbox.wrapper"},
		"empty component":   {&Entry{Fields: map[string]any{"component": ""}}, "devsandbox"},
		"missing component": {&Entry{Fields: map[string]any{"event": "session.start"}}, "devsandbox"},
		"non-string":        {&Entry{Fields: map[string]any{"component": 42}}, "devsandbox"},
		"nil fields":        {&Entry{Fields: nil}, "devsandbox"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := scopeNameFor(tc.entry); got != tc.want {
				t.Errorf("scopeNameFor = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOTLPWriter_BuildJSONPayload_GroupsByScope(t *testing.T) {
	w := &OTLPWriter{cfg: OTLPConfig{}}
	now := time.Now()

	entries := []*Entry{
		{Timestamp: now, Level: LevelInfo, Message: "GET /x", Fields: map[string]any{"component": "proxy"}},
		{Timestamp: now, Level: LevelInfo, Message: "mount.decision", Fields: map[string]any{"component": "mounts", "event": "mount.decision"}},
		{Timestamp: now, Level: LevelInfo, Message: "session.start", Fields: map[string]any{"event": "session.start"}}, // no component → "devsandbox"
		{Timestamp: now, Level: LevelInfo, Message: "GET /y", Fields: map[string]any{"component": "proxy"}},            // same scope as first
		{Timestamp: now, Level: LevelInfo, Message: "starting up", Fields: map[string]any{"component": "wrapper"}},
	}

	data := w.buildJSONPayload(entries)

	var payload otlpLogsPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(payload.ResourceLogs) != 1 {
		t.Fatalf("ResourceLogs = %d, want 1", len(payload.ResourceLogs))
	}

	scopes := payload.ResourceLogs[0].ScopeLogs
	if len(scopes) != 4 {
		t.Fatalf("got %d ScopeLogs, want 4 (proxy/mounts/devsandbox/wrapper): %+v", len(scopes), scopeNames(scopes))
	}

	got := map[string]int{}
	for _, sl := range scopes {
		got[sl.Scope.Name] = len(sl.LogRecords)
	}
	want := map[string]int{
		"devsandbox.proxy":   2,
		"devsandbox.mounts":  1,
		"devsandbox":         1,
		"devsandbox.wrapper": 1,
	}
	for name, count := range want {
		if got[name] != count {
			t.Errorf("scope %q: got %d records, want %d (full: %+v)", name, got[name], count, got)
		}
	}
}

func scopeNames(scopes []otlpScopeLogs) []string {
	out := make([]string, len(scopes))
	for i, s := range scopes {
		out[i] = s.Scope.Name
	}
	return out
}
