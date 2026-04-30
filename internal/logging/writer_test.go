package logging

import (
	"maps"
	"reflect"
	"sync"
	"testing"
	"time"
)

// memWriter captures dispatched entries for assertions.
type memWriter struct {
	mu      sync.Mutex
	entries []Entry
}

func (m *memWriter) Write(entry *Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *entry
	if entry.Fields != nil {
		cp.Fields = make(map[string]any, len(entry.Fields))
		maps.Copy(cp.Fields, entry.Fields)
	}
	m.entries = append(m.entries, cp)
	return nil
}

func (m *memWriter) Close() error { return nil }

func (m *memWriter) snapshot() []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Entry, len(m.entries))
	copy(out, m.entries)
	return out
}

func newDispatcherWithMem(t *testing.T) (*Dispatcher, *memWriter) {
	t.Helper()
	d := NewDispatcher()
	mw := &memWriter{}
	d.AddWriter(mw)
	return d, mw
}

func TestDispatcher_Write_NoSessionFields(t *testing.T) {
	d, mw := newDispatcherWithMem(t)

	in := &Entry{
		Timestamp: time.Now(),
		Level:     LevelInfo,
		Message:   "hello",
		Fields:    map[string]any{"component": "foo"},
	}
	if err := d.Write(in); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := mw.snapshot()
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].Fields["component"] != "foo" {
		t.Errorf("component = %v, want foo", got[0].Fields["component"])
	}
}

func TestDispatcher_Write_PerEventKeyWinsOverSessionField(t *testing.T) {
	d, mw := newDispatcherWithMem(t)
	d.SetSessionFields(map[string]any{
		"component":    "session-default",
		"sandbox_name": "sb",
	})

	in := &Entry{
		Level:   LevelInfo,
		Message: "x",
		Fields:  map[string]any{"component": "per-event"},
	}
	if err := d.Write(in); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := mw.snapshot()
	if got[0].Fields["component"] != "per-event" {
		t.Errorf("component = %v, want per-event (per-event key must win)", got[0].Fields["component"])
	}
	if got[0].Fields["sandbox_name"] != "sb" {
		t.Errorf("sandbox_name = %v, want sb (session field absent on entry)", got[0].Fields["sandbox_name"])
	}
}

func TestDispatcher_Write_NoEventFieldsMergesSessionFields(t *testing.T) {
	d, mw := newDispatcherWithMem(t)
	d.SetSessionFields(map[string]any{
		"sandbox_name": "sb",
		"pid":          42,
	})

	in := &Entry{Level: LevelInfo, Message: "x"}
	if err := d.Write(in); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := mw.snapshot()
	if got[0].Fields["sandbox_name"] != "sb" || got[0].Fields["pid"] != 42 {
		t.Errorf("merged fields missing or wrong: %v", got[0].Fields)
	}
}

func TestDispatcher_Write_DisjointFieldsAreUnioned(t *testing.T) {
	d, mw := newDispatcherWithMem(t)
	d.SetSessionFields(map[string]any{"sandbox_name": "sb"})

	in := &Entry{Level: LevelInfo, Message: "x", Fields: map[string]any{"event": "x.y"}}
	if err := d.Write(in); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := mw.snapshot()
	want := map[string]any{"sandbox_name": "sb", "event": "x.y"}
	if !reflect.DeepEqual(got[0].Fields, want) {
		t.Errorf("Fields = %v, want %v", got[0].Fields, want)
	}
}

func TestDispatcher_Write_DoesNotMutateCallerEntry(t *testing.T) {
	d, _ := newDispatcherWithMem(t)
	d.SetSessionFields(map[string]any{"sandbox_name": "sb"})

	original := map[string]any{"component": "foo"}
	in := &Entry{Level: LevelInfo, Message: "x", Fields: original}
	if err := d.Write(in); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if len(in.Fields) != 1 || in.Fields["component"] != "foo" {
		t.Errorf("caller entry.Fields was mutated: %v", in.Fields)
	}
}

func TestDispatcher_SetSessionFields_NilClears(t *testing.T) {
	d, mw := newDispatcherWithMem(t)
	d.SetSessionFields(map[string]any{"sandbox_name": "sb"})
	d.SetSessionFields(nil)

	in := &Entry{Level: LevelInfo, Message: "x"}
	if err := d.Write(in); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := mw.snapshot()
	if _, ok := got[0].Fields["sandbox_name"]; ok {
		t.Errorf("expected no session fields after nil clear, got %v", got[0].Fields)
	}
}

func TestDispatcher_SetSessionFields_DefensiveCopy(t *testing.T) {
	d, mw := newDispatcherWithMem(t)
	src := map[string]any{"sandbox_name": "sb"}
	d.SetSessionFields(src)

	src["sandbox_name"] = "mutated"

	in := &Entry{Level: LevelInfo, Message: "x"}
	if err := d.Write(in); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := mw.snapshot()
	if got[0].Fields["sandbox_name"] != "sb" {
		t.Errorf("dispatcher session fields mutated externally: %v", got[0].Fields)
	}
}

func TestDispatcher_Event_BasicShape(t *testing.T) {
	d, mw := newDispatcherWithMem(t)

	if err := d.Event(LevelInfo, "session.start", map[string]any{"host": "h1"}); err != nil {
		t.Fatalf("Event: %v", err)
	}

	got := mw.snapshot()
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Message != "session.start" {
		t.Errorf("Message = %q, want session.start", e.Message)
	}
	if e.Fields["event"] != "session.start" {
		t.Errorf("Fields[event] = %v, want session.start", e.Fields["event"])
	}
	if e.Fields["host"] != "h1" {
		t.Errorf("Fields[host] = %v, want h1", e.Fields["host"])
	}
	if e.Level != LevelInfo {
		t.Errorf("Level = %v, want info", e.Level)
	}
	if e.Timestamp.IsZero() {
		t.Error("Timestamp is zero")
	}
}

func TestDispatcher_Event_HelperEventKeyOverridesCaller(t *testing.T) {
	d, mw := newDispatcherWithMem(t)

	caller := map[string]any{"event": "caller-event-leak", "x": 1}
	if err := d.Event(LevelInfo, "session.end", caller); err != nil {
		t.Fatalf("Event: %v", err)
	}

	got := mw.snapshot()
	if got[0].Fields["event"] != "session.end" {
		t.Errorf("Fields[event] = %v, want session.end (helper-controlled)", got[0].Fields["event"])
	}
}

func TestDispatcher_Event_DoesNotMutateCallerFields(t *testing.T) {
	d, _ := newDispatcherWithMem(t)

	caller := map[string]any{"x": 1}
	if err := d.Event(LevelInfo, "x.y", caller); err != nil {
		t.Fatalf("Event: %v", err)
	}

	if _, ok := caller["event"]; ok {
		t.Errorf("caller fields mutated: %v", caller)
	}
	if len(caller) != 1 {
		t.Errorf("caller fields length = %d, want 1", len(caller))
	}
}

func TestDispatcher_Event_AppliesSessionFields(t *testing.T) {
	d, mw := newDispatcherWithMem(t)
	d.SetSessionFields(map[string]any{"sandbox_name": "sb"})

	if err := d.Event(LevelWarn, "proxy.filter.decision", map[string]any{"host": "h"}); err != nil {
		t.Fatalf("Event: %v", err)
	}

	got := mw.snapshot()
	f := got[0].Fields
	if f["event"] != "proxy.filter.decision" || f["host"] != "h" || f["sandbox_name"] != "sb" {
		t.Errorf("merged fields wrong: %v", f)
	}
}

// BenchmarkDispatcherWrite exercises the merge hot path with a representative
// 6 session fields + 2 per-event fields shape (proxy request log entry).
func BenchmarkDispatcherWrite(b *testing.B) {
	d := NewDispatcher()
	d.AddWriter(&memWriter{})
	d.SetSessionFields(map[string]any{
		"session_id":         "01HF000000000000000000",
		"sandbox_name":       "sb",
		"sandbox_path":       "/sb",
		"project_dir":        "/proj",
		"isolator":           "bwrap",
		"devsandbox_version": "0.16.0",
	})
	entry := &Entry{
		Timestamp: time.Now(),
		Level:     LevelInfo,
		Message:   "x",
		Fields:    map[string]any{"component": "proxy", "event": "proxy.filter.decision"},
	}

	b.ReportAllocs()
	for b.Loop() {
		_ = d.Write(entry)
	}
}
