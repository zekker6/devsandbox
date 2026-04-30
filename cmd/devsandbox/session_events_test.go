package main

import (
	"errors"
	"os/exec"
	"testing"
	"time"

	"devsandbox/internal/config"
	"devsandbox/internal/logging"
	"devsandbox/internal/proxy"
)

func TestEmitSessionStart_PayloadShape(t *testing.T) {
	d := logging.NewDispatcher()
	mw := &captureWriter{}
	d.AddWriter(mw)

	appCfg := &config.Config{}
	appCfg.Proxy.Credentials = map[string]any{
		"openai": map[string]any{},
		"github": map[string]any{},
	}

	pCfg := &proxy.Config{
		Port: 8888,
		MITM: true,
		Filter: &proxy.FilterConfig{
			DefaultAction: proxy.FilterActionAllow,
			Rules:         []proxy.FilterRule{{Pattern: "x.example", Action: proxy.FilterActionBlock}},
		},
		Redaction: &proxy.RedactionConfig{
			Rules: []proxy.RedactionRule{{Name: "r1"}},
		},
		LogSkip: &proxy.LogSkipConfig{
			Rules: []proxy.LogSkipRule{{Pattern: "x.example"}},
		},
	}

	emitSessionStart(d, appCfg, pCfg, []string{"claude", "--model", "opus"}, true)

	got := mw.snapshot()
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Fields["event"] != "session.start" {
		t.Errorf("event = %v, want session.start", e.Fields["event"])
	}
	if e.Fields["proxy_enabled"] != true {
		t.Errorf("proxy_enabled = %v, want true", e.Fields["proxy_enabled"])
	}
	if e.Fields["proxy_port"] != 8888 {
		t.Errorf("proxy_port = %v, want 8888", e.Fields["proxy_port"])
	}
	if e.Fields["proxy_mitm"] != true {
		t.Errorf("proxy_mitm = %v, want true", e.Fields["proxy_mitm"])
	}
	if e.Fields["filter_mode"] != "allow" {
		t.Errorf("filter_mode = %v, want allow", e.Fields["filter_mode"])
	}
	if e.Fields["filter_rule_count"] != 1 {
		t.Errorf("filter_rule_count = %v, want 1", e.Fields["filter_rule_count"])
	}
	if e.Fields["redaction_rule_count"] != 1 {
		t.Errorf("redaction_rule_count = %v, want 1", e.Fields["redaction_rule_count"])
	}
	if e.Fields["log_skip_rule_count"] != 1 {
		t.Errorf("log_skip_rule_count = %v, want 1", e.Fields["log_skip_rule_count"])
	}
	injectors, ok := e.Fields["credential_injectors"].([]string)
	if !ok || len(injectors) != 2 {
		t.Errorf("credential_injectors = %v, want 2-element slice", e.Fields["credential_injectors"])
	}
	if e.Fields["command"] != "claude --model opus" {
		t.Errorf("command = %v, want claude --model opus", e.Fields["command"])
	}
	if e.Fields["tty"] != true {
		t.Errorf("tty = %v, want true", e.Fields["tty"])
	}
	if _, ok := e.Fields["start_time"].(string); !ok {
		t.Errorf("start_time should be a string (RFC3339), got %T", e.Fields["start_time"])
	}
}

func TestEmitSessionStart_ProxyDisabledOmitsPort(t *testing.T) {
	d := logging.NewDispatcher()
	mw := &captureWriter{}
	d.AddWriter(mw)

	emitSessionStart(d, &config.Config{}, nil, []string{"sh"}, false)

	got := mw.snapshot()
	e := got[0]
	if e.Fields["proxy_enabled"] != false {
		t.Errorf("proxy_enabled = %v, want false", e.Fields["proxy_enabled"])
	}
	if _, ok := e.Fields["proxy_port"]; ok {
		t.Errorf("proxy_port should be omitted when proxy disabled, got %v", e.Fields["proxy_port"])
	}
	if e.Fields["filter_mode"] != "off" {
		t.Errorf("filter_mode = %v, want off", e.Fields["filter_mode"])
	}
}

func TestEmitSessionStart_NilDispatcherIsNoop(t *testing.T) {
	// Just must not panic.
	emitSessionStart(nil, &config.Config{}, nil, []string{"x"}, false)
}

func TestEmitSessionEnd_NormalExit(t *testing.T) {
	d := logging.NewDispatcher()
	mw := &captureWriter{}
	d.AddWriter(mw)

	start := time.Now().Add(-50 * time.Millisecond)
	emitSessionEnd(d, start, nil, false, nil)

	got := mw.snapshot()
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Fields["event"] != "session.end" {
		t.Errorf("event = %v, want session.end", e.Fields["event"])
	}
	if e.Fields["exit_code"] != 0 {
		t.Errorf("exit_code = %v, want 0", e.Fields["exit_code"])
	}
	dur, ok := e.Fields["duration_ms"].(int64)
	if !ok || dur < 50 {
		t.Errorf("duration_ms = %v, want >= 50", e.Fields["duration_ms"])
	}
	if e.Fields["proxy_request_count"] != int64(0) {
		t.Errorf("proxy_request_count = %v, want 0", e.Fields["proxy_request_count"])
	}
}

func TestEmitSessionEnd_SignaledExit(t *testing.T) {
	d := logging.NewDispatcher()
	mw := &captureWriter{}
	d.AddWriter(mw)

	emitSessionEnd(d, time.Now(), nil, true, nil)

	got := mw.snapshot()
	if got[0].Fields["exit_code"] != -1 {
		t.Errorf("signaled exit_code = %v, want -1", got[0].Fields["exit_code"])
	}
}

func TestEmitSessionEnd_GenericError(t *testing.T) {
	d := logging.NewDispatcher()
	mw := &captureWriter{}
	d.AddWriter(mw)

	emitSessionEnd(d, time.Now(), errors.New("boom"), false, nil)

	got := mw.snapshot()
	if got[0].Fields["exit_code"] != 1 {
		t.Errorf("generic-error exit_code = %v, want 1", got[0].Fields["exit_code"])
	}
}

func TestEmitSessionEnd_ExitErrorPropagatesCode(t *testing.T) {
	// Build a real *exec.ExitError by running a failing command.
	cmd := exec.Command("sh", "-c", "exit 42")
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}

	d := logging.NewDispatcher()
	mw := &captureWriter{}
	d.AddWriter(mw)

	emitSessionEnd(d, time.Now(), exitErr, false, nil)

	got := mw.snapshot()
	if got[0].Fields["exit_code"] != 42 {
		t.Errorf("exit_code = %v, want 42 from *exec.ExitError", got[0].Fields["exit_code"])
	}
}

func TestEmitSessionEnd_DispatcherClosedDoesNotPanic(t *testing.T) {
	d := logging.NewDispatcher()
	mw := &captureWriter{}
	d.AddWriter(mw)
	_ = d.Close()

	// Must not panic; Dispatcher.Write swallows errors silently after close.
	emitSessionEnd(d, time.Now(), nil, false, nil)
}

func TestEmitSessionEnd_NilDispatcherIsNoop(t *testing.T) {
	emitSessionEnd(nil, time.Now(), nil, false, nil)
}

func TestExitCodeFromError(t *testing.T) {
	if got := exitCodeFromError(nil); got != 0 {
		t.Errorf("nil error → %d, want 0", got)
	}
	if got := exitCodeFromError(errors.New("x")); got != 1 {
		t.Errorf("generic error → %d, want 1", got)
	}
}
