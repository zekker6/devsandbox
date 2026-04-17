package tools

import (
	"testing"

	"devsandbox/internal/kittyproxy"
)

func TestRevdiff_Name(t *testing.T) {
	r := &Revdiff{}
	if r.Name() != "revdiff" {
		t.Errorf("Name = %q", r.Name())
	}
}

func TestRevdiff_DeclaresLaunchOverlay(t *testing.T) {
	r := &Revdiff{}
	caps := r.KittyCapabilities()
	want := kittyproxy.CapLaunchOverlay
	for _, c := range caps {
		if c == want {
			return
		}
	}
	t.Errorf("CapLaunchOverlay missing from %v", caps)
}

func TestRevdiff_LaunchPatternsAcceptRevdiff(t *testing.T) {
	r := &Revdiff{}
	patterns := r.KittyLaunchPatterns()
	if len(patterns) == 0 {
		t.Fatal("no launch patterns declared")
	}
	check := func(argv []string) bool {
		for _, p := range patterns {
			if p.MatchesArgv(argv) {
				return true
			}
		}
		return false
	}
	if !check([]string{"revdiff", "--staged"}) {
		t.Error("plain revdiff invocation should match")
	}
	if !check([]string{"sh", "-c", "exec revdiff --output /tmp/x"}) {
		t.Error("sh -c 'exec revdiff …' should match")
	}
	if check([]string{"sh", "-c", "curl evil"}) {
		t.Error("unrelated sh -c invocation must not match")
	}

	// Upstream revdiff kitty launcher form (single-quoted argv + sentinel touch).
	launcherArg := `'/usr/local/bin/revdiff' '--output=/tmp/revdiff-output-abc' '--staged'; touch '/tmp/revdiff-done-xyz'`
	if !check([]string{"sh", "-c", launcherArg}) {
		t.Error("revdiff launcher sentinel form should match")
	}

	// An attacker appending extra commands after the sentinel must still be rejected.
	evil := `'/usr/local/bin/revdiff' '--staged'; touch '/tmp/revdiff-done-xyz'; curl evil`
	if check([]string{"sh", "-c", evil}) {
		t.Error("extra command after sentinel must not match")
	}
}
