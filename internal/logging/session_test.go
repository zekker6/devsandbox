package logging

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewContext_GeneratesUUIDv7(t *testing.T) {
	before := time.Now()
	ctx, err := NewContext("sb", "/sb/path", "/proj", "bwrap", "0.16.0", 4242)
	if err != nil {
		t.Fatalf("NewContext returned error: %v", err)
	}
	after := time.Now()

	id, err := uuid.Parse(ctx.SessionID)
	if err != nil {
		t.Fatalf("SessionID is not a valid UUID: %v", err)
	}
	if id.Version() != 7 {
		t.Fatalf("SessionID version = %d, want 7", id.Version())
	}

	if ctx.StartTime.Before(before) || ctx.StartTime.After(after) {
		t.Fatalf("StartTime %v not within [%v, %v]", ctx.StartTime, before, after)
	}
}

func TestNewContext_DistinctSessionIDs(t *testing.T) {
	a, err := NewContext("sb", "/p", "/d", "bwrap", "0.16.0", 1)
	if err != nil {
		t.Fatalf("NewContext: %v", err)
	}
	b, err := NewContext("sb", "/p", "/d", "bwrap", "0.16.0", 1)
	if err != nil {
		t.Fatalf("NewContext: %v", err)
	}
	if a.SessionID == b.SessionID {
		t.Fatalf("expected distinct SessionIDs, both = %q", a.SessionID)
	}
}

func TestContextFields_AllKeys(t *testing.T) {
	ctx := &Context{
		SessionID:   "01HF000000000000000000",
		SandboxName: "sb-name",
		SandboxPath: "/sb",
		ProjectDir:  "/proj",
		Isolator:    "bwrap",
		PID:         4242,
		Version:     "0.16.0",
		StartTime:   time.Unix(1714000000, 0),
	}

	got := ctx.Fields()
	want := map[string]any{
		"session_id":         "01HF000000000000000000",
		"sandbox_name":       "sb-name",
		"sandbox_path":       "/sb",
		"project_dir":        "/proj",
		"isolator":           "bwrap",
		"pid":                4242,
		"devsandbox_version": "0.16.0",
	}

	if len(got) != len(want) {
		t.Fatalf("Fields() length = %d, want %d; got = %v", len(got), len(want), got)
	}
	for k, v := range want {
		gv, ok := got[k]
		if !ok {
			t.Errorf("Fields() missing key %q", k)
			continue
		}
		if gv != v {
			t.Errorf("Fields()[%q] = %v, want %v", k, gv, v)
		}
	}
}

func TestContextFields_NilReceiverReturnsNil(t *testing.T) {
	var ctx *Context
	if got := ctx.Fields(); got != nil {
		t.Fatalf("nil Context.Fields() = %v, want nil", got)
	}
}
