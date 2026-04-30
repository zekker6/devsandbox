package main

import (
	"strings"
	"testing"
	"time"

	"devsandbox/internal/config"
	"devsandbox/internal/logging"
	"devsandbox/internal/notice"
	"devsandbox/internal/proxy"
)

// TestAuditPipeline_EndToEnd wires together every component touched by the
// audit-logging plan: notice buffer + AttachSink, dispatcher SetSessionFields,
// Event helper, and the lifecycle event helpers from session_events.go.
//
// Because e2e tests against a real sandbox cannot run inside an existing
// devsandbox session (recursive-sandbox detection), this test stands in as
// the integration-level guarantee that:
//   - session.start arrives before any subsequent events
//   - all entries share the same session_id
//   - all entries carry the per-session metadata (sandbox_name, sandbox_path,
//     project_dir, isolator, pid, devsandbox_version)
//   - notice entries buffered before AttachSink are drained with full session
//     fields once the sink is attached
//   - session.end is the last lifecycle/security event and reports
//     proxy_request_count
//
// Running in a real sandbox would add the actual proxy hop, which is what
// the e2e/ folder's existing tests cover. The audit contract is independent
// of that hop and is fully verifiable here.
func TestAuditPipeline_EndToEnd(t *testing.T) {
	d, mw := newPipelineDispatcher(t)

	// Step 1: simulate the early-startup window where notice fires before
	// the dispatcher is attached. These should be buffered.
	resetNoticeForTest(t)
	notice.Info("starting up — proxy on :8888")
	notice.Warn("MITM disabled — credential injection limited to plain HTTP")

	// Step 2: build session context and attach to dispatcher + notice. This
	// mirrors cmd/devsandbox/main.go's wiring sequence.
	sessionCtx, err := buildSessionContext("audit-sb", "/sandbox/audit-sb", "/work/api", "bwrap")
	if err != nil {
		t.Fatalf("buildSessionContext: %v", err)
	}
	d.SetSessionFields(sessionCtx.Fields())
	dropped := notice.AttachSink(noticeSinkFor(d))
	if dropped > 0 {
		t.Errorf("dropped = %d, want 0 (buffer not expected to overflow in this test)", dropped)
	}

	// Step 3: emit session.start.
	appCfg := &config.Config{}
	pCfg := &proxy.Config{
		Port: 8888,
		MITM: false,
		Filter: &proxy.FilterConfig{
			DefaultAction: proxy.FilterActionAllow,
			Rules:         []proxy.FilterRule{{Pattern: "evil.example", Action: proxy.FilterActionBlock}},
		},
	}
	emitSessionStart(d, appCfg, pCfg, []string{"claude"}, true)
	start := time.Now()

	// Step 4: emit a security event mid-session.
	server := &proxy.Server{}
	// Use the proxy package's emit helper directly via a constructed Server
	// is hard without exporting fields, so just dispatch the event by hand
	// to validate the cross-component shape.
	_ = d.Event(logging.LevelWarn, "proxy.filter.decision", map[string]any{
		"host":                "evil.example",
		"method":              "GET",
		"path":                "/x",
		"rule_action":         "block",
		"rule_id":             "evil.example",
		"default_action_used": false,
	})

	// Step 5: live notice (post-attach) — should dispatch immediately.
	notice.Info("running phase")

	// Step 6: emit session.end.
	emitSessionEnd(d, start, nil, false, server)

	// --- Assertions ---

	got := mw.snapshot()
	if len(got) < 5 {
		// Expected at minimum: 2 buffered notice + session.start + filter.decision + live notice + session.end = 6
		t.Fatalf("got %d entries, want at least 5: %+v", len(got), got)
	}

	// Find lifecycle indices.
	var startIdx, endIdx = -1, -1
	for i, e := range got {
		switch e.Fields["event"] {
		case "session.start":
			if startIdx != -1 {
				t.Fatalf("multiple session.start entries at indices %d and %d", startIdx, i)
			}
			startIdx = i
		case "session.end":
			endIdx = i
		}
	}
	if startIdx == -1 {
		t.Fatal("session.start not found")
	}
	if endIdx == -1 {
		t.Fatal("session.end not found")
	}
	if endIdx <= startIdx {
		t.Errorf("session.end index %d not after session.start index %d", endIdx, startIdx)
	}

	// All entries past startIdx — including session.start itself — must
	// carry the session fields (the early notice buffered entries are
	// drained at AttachSink with current session fields too).
	wantSessionID := sessionCtx.SessionID
	wantSandbox := "audit-sb"
	for i, e := range got {
		if e.Fields["session_id"] != wantSessionID {
			t.Errorf("entry %d session_id = %v, want %v", i, e.Fields["session_id"], wantSessionID)
		}
		if e.Fields["sandbox_name"] != wantSandbox {
			t.Errorf("entry %d sandbox_name = %v, want %v", i, e.Fields["sandbox_name"], wantSandbox)
		}
		if e.Fields["isolator"] != "bwrap" {
			t.Errorf("entry %d isolator = %v, want bwrap", i, e.Fields["isolator"])
		}
		if e.Fields["sandbox_path"] != "/sandbox/audit-sb" {
			t.Errorf("entry %d sandbox_path = %v, want /sandbox/audit-sb", i, e.Fields["sandbox_path"])
		}
		if e.Fields["project_dir"] != "/work/api" {
			t.Errorf("entry %d project_dir = %v, want /work/api", i, e.Fields["project_dir"])
		}
		if _, ok := e.Fields["pid"]; !ok {
			t.Errorf("entry %d missing pid", i)
		}
		if _, ok := e.Fields["devsandbox_version"]; !ok {
			t.Errorf("entry %d missing devsandbox_version", i)
		}
	}

	// session.end is the last LIFECYCLE/SECURITY event (entries with `event` set).
	for i := endIdx + 1; i < len(got); i++ {
		if _, ok := got[i].Fields["event"]; ok {
			t.Errorf("found event entry %v after session.end at index %d", got[i].Fields["event"], i)
		}
	}

	// Buffered notice entries must arrive before session.start.
	foundBufferedNotice := false
	for i := 0; i < startIdx; i++ {
		if got[i].Fields["component"] == "wrapper" && strings.Contains(got[i].Message, "starting up") {
			foundBufferedNotice = true
			break
		}
	}
	if !foundBufferedNotice {
		t.Errorf("buffered notice entries not drained before session.start; entries pre-start: %+v", got[:startIdx])
	}

	// session.end carries proxy_request_count.
	endEntry := got[endIdx]
	if _, ok := endEntry.Fields["proxy_request_count"]; !ok {
		t.Errorf("session.end missing proxy_request_count")
	}
}

// newPipelineDispatcher wires a fresh dispatcher with the local captureWriter
// from session_context_test.go.
func newPipelineDispatcher(t *testing.T) (*logging.Dispatcher, *captureWriter) {
	t.Helper()
	d := logging.NewDispatcher()
	mw := &captureWriter{}
	d.AddWriter(mw)
	return d, mw
}

// resetNoticeForTest forces the notice singleton to a known state. We reach
// into package internals via the test-scoped Setup helper — calling Setup
// again replaces stderr and clears state. The buffered ring resets via
// AttachSink-then-replace pattern documented in internal/notice/notice_test.go.
func resetNoticeForTest(t *testing.T) {
	t.Helper()
	// The notice package exposes Setup which clears state. We cannot import
	// resetForTest from the notice package's _test file, so rely on a fresh
	// Setup call to reset stderr/log. Buffer state is wiped naturally because
	// AttachSink earlier in this run drained it; before that, there's nothing
	// to wipe.
	if err := notice.Setup("", false, &noopWriter{}); err != nil {
		t.Fatalf("notice.Setup: %v", err)
	}
}

type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }
