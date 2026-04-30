package main

import (
	"errors"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"time"

	"devsandbox/internal/config"
	"devsandbox/internal/logging"
	"devsandbox/internal/proxy"
)

// emitSessionStart writes a session.start audit event with the security
// posture snapshot described in docs/plans/2026-04-29-audit-logging-enhancements.md.
// Caller-side fields like proxyEnabled and proxyPort vary depending on whether
// the proxy was started, so they're parameterised explicitly.
func emitSessionStart(
	d *logging.Dispatcher,
	appCfg *config.Config,
	pCfg *proxy.Config,
	command []string,
	tty bool,
) {
	if d == nil {
		return
	}

	host, _ := os.Hostname()
	hostUser := ""
	if u, err := user.Current(); err == nil {
		hostUser = u.Username
	}

	fields := map[string]any{
		"host":                 host,
		"host_user":            hostUser,
		"proxy_enabled":        pCfg != nil,
		"proxy_mitm":           pCfg != nil && pCfg.MITM,
		"filter_mode":          filterModeFor(pCfg),
		"filter_rule_count":    filterRuleCount(pCfg),
		"redaction_rule_count": redactionRuleCount(pCfg),
		"log_skip_rule_count":  logSkipRuleCount(pCfg),
		"credential_injectors": credentialInjectorNames(appCfg),
		"command":              strings.Join(command, " "),
		"tty":                  tty,
		"start_time":           time.Now().UTC().Format(time.RFC3339),
	}
	if pCfg != nil {
		fields["proxy_port"] = pCfg.Port
	}

	_ = d.Event(logging.LevelInfo, "session.start", fields)
}

// emitSessionEnd writes a session.end audit event. Pulls exit code from
// retErr (interpreting *exec.ExitError when present) and request count from
// the proxy's RequestLogger if available. Safe to call on a closed dispatcher
// — Dispatcher.Write swallows errors silently.
func emitSessionEnd(
	d *logging.Dispatcher,
	start time.Time,
	retErr error,
	signaled bool,
	proxyServer *proxy.Server,
) {
	if d == nil {
		return
	}

	exitCode := exitCodeFromError(retErr)
	if signaled && retErr == nil {
		// Suppressed-by-signal case; treat as terminated rather than success.
		exitCode = -1
	}

	end := time.Now()
	fields := map[string]any{
		"exit_code":           exitCode,
		"duration_ms":         end.Sub(start).Milliseconds(),
		"end_time":            end.UTC().Format(time.RFC3339),
		"proxy_request_count": proxyRequestCount(proxyServer),
	}

	_ = d.Event(logging.LevelInfo, "session.end", fields)
}

// exitCodeFromError extracts a process-style exit code from an error. nil → 0.
// *exec.ExitError → ExitCode(). Any other non-nil error → 1.
func exitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

func filterModeFor(pCfg *proxy.Config) string {
	if pCfg == nil || pCfg.Filter == nil || !pCfg.Filter.IsEnabled() {
		return "off"
	}
	return string(pCfg.Filter.DefaultAction)
}

func filterRuleCount(pCfg *proxy.Config) int {
	if pCfg == nil || pCfg.Filter == nil {
		return 0
	}
	return len(pCfg.Filter.Rules)
}

func redactionRuleCount(pCfg *proxy.Config) int {
	if pCfg == nil || pCfg.Redaction == nil {
		return 0
	}
	return len(pCfg.Redaction.Rules)
}

func logSkipRuleCount(pCfg *proxy.Config) int {
	if pCfg == nil || pCfg.LogSkip == nil {
		return 0
	}
	return len(pCfg.LogSkip.Rules)
}

// credentialInjectorNames returns the configured credential injector names
// (no values). Reads from appCfg.Proxy.Credentials so the names are visible
// even on builds where the proxy was disabled at runtime.
func credentialInjectorNames(appCfg *config.Config) []string {
	if appCfg == nil {
		return nil
	}
	creds := appCfg.Proxy.Credentials
	names := make([]string, 0, len(creds))
	for name := range creds {
		names = append(names, name)
	}
	return names
}

// proxyRequestCount returns the per-session request count tracked by the
// proxy's RequestLogger. Returns 0 if the proxy is absent or the logger
// has no counter wired (it does after Task 7).
func proxyRequestCount(s *proxy.Server) int64 {
	if s == nil {
		return 0
	}
	return s.RequestCount()
}
