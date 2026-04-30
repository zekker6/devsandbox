package main

import (
	"os"
	"testing"
	"time"

	"devsandbox/internal/logging"
	"devsandbox/internal/notice"
	"devsandbox/internal/version"
)

func TestBuildSessionContext_PopulatesAllFields(t *testing.T) {
	ctx, err := buildSessionContext("sb", "/sb/path", "/proj", "bwrap")
	if err != nil {
		t.Fatalf("buildSessionContext: %v", err)
	}
	if ctx.SandboxName != "sb" {
		t.Errorf("SandboxName = %q, want sb", ctx.SandboxName)
	}
	if ctx.SandboxPath != "/sb/path" {
		t.Errorf("SandboxPath = %q, want /sb/path", ctx.SandboxPath)
	}
	if ctx.ProjectDir != "/proj" {
		t.Errorf("ProjectDir = %q, want /proj", ctx.ProjectDir)
	}
	if ctx.Isolator != "bwrap" {
		t.Errorf("Isolator = %q, want bwrap", ctx.Isolator)
	}
	if ctx.Version != version.Version {
		t.Errorf("Version = %q, want %q", ctx.Version, version.Version)
	}
	if ctx.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d", ctx.PID, os.Getpid())
	}
	if ctx.SessionID == "" {
		t.Error("SessionID is empty")
	}
}

func TestBuildSessionContext_AllowsEmptySandboxName(t *testing.T) {
	// Proxy-disabled flow: sandbox_name may be empty if --name not passed.
	ctx, err := buildSessionContext("", "/sb", "/proj", "bwrap")
	if err != nil {
		t.Fatalf("buildSessionContext: %v", err)
	}
	if ctx.SandboxName != "" {
		t.Errorf("SandboxName = %q, want empty", ctx.SandboxName)
	}
	if ctx.SessionID == "" {
		t.Error("SessionID is empty even when sandbox_name is empty")
	}
}

func TestNoticeSinkFor_DispatchesWithWrapperComponent(t *testing.T) {
	d := logging.NewDispatcher()
	mw := &captureWriter{}
	d.AddWriter(mw)

	sink := noticeSinkFor(d)
	now := time.Now()
	sink(notice.LevelInfo, "hello", now)

	got := mw.snapshot()
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].Message != "hello" {
		t.Errorf("Message = %q, want hello", got[0].Message)
	}
	if got[0].Level != logging.LevelInfo {
		t.Errorf("Level = %q, want info", got[0].Level)
	}
	if got[0].Fields["component"] != "wrapper" {
		t.Errorf("component = %v, want wrapper", got[0].Fields["component"])
	}
	if !got[0].Timestamp.Equal(now) {
		t.Errorf("Timestamp = %v, want %v", got[0].Timestamp, now)
	}
}

func TestNoticeSinkFor_AppliesDispatcherSessionFields(t *testing.T) {
	d := logging.NewDispatcher()
	mw := &captureWriter{}
	d.AddWriter(mw)
	d.SetSessionFields(map[string]any{"sandbox_name": "sb"})

	sink := noticeSinkFor(d)
	sink(notice.LevelWarn, "warn-msg", time.Now())

	got := mw.snapshot()
	if got[0].Fields["sandbox_name"] != "sb" {
		t.Errorf("session field missing: %v", got[0].Fields)
	}
	if got[0].Fields["component"] != "wrapper" {
		t.Errorf("wrapper component overridden: %v", got[0].Fields)
	}
}

// captureWriter mirrors the memWriter helper from internal/logging tests but
// lives in this package because internal test helpers are not exported.
type captureWriter struct {
	entries []logging.Entry
}

func (c *captureWriter) Write(entry *logging.Entry) error {
	cp := *entry
	if entry.Fields != nil {
		cp.Fields = make(map[string]any, len(entry.Fields))
		for k, v := range entry.Fields {
			cp.Fields[k] = v
		}
	}
	c.entries = append(c.entries, cp)
	return nil
}

func (c *captureWriter) Close() error { return nil }

func (c *captureWriter) snapshot() []logging.Entry {
	out := make([]logging.Entry, len(c.entries))
	copy(out, c.entries)
	return out
}
