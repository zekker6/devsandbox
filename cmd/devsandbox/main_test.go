package main

import (
	"testing"

	"github.com/spf13/cobra"

	"devsandbox/internal/config"
	"devsandbox/internal/proxy"
)

func TestWorktreeFlagsRegistered(t *testing.T) {
	cmd := &cobra.Command{}
	addSandboxFlags(cmd)

	wt := cmd.Flags().Lookup("worktree")
	if wt == nil {
		t.Fatal("--worktree flag missing")
	}
	if wt.NoOptDefVal == "" {
		t.Error("--worktree should have NoOptDefVal so bare --worktree is accepted")
	}
	if v, _ := cmd.Flags().GetString("worktree"); v != "" {
		t.Errorf("default --worktree = %q, want empty", v)
	}

	if cmd.Flags().Lookup("worktree-base") == nil {
		t.Fatal("--worktree-base flag missing")
	}
}

func TestBuildLogSkipConfig(t *testing.T) {
	t.Run("empty config returns nil", func(t *testing.T) {
		got := buildLogSkipConfig(&config.Config{})
		if got != nil {
			t.Errorf("expected nil for empty config, got %+v", got)
		}
	})

	t.Run("populated config maps fields verbatim", func(t *testing.T) {
		appCfg := &config.Config{Proxy: config.ProxyConfig{
			LogSkip: config.ProxyLogSkipConfig{Rules: []config.ProxyLogSkipRule{
				{Pattern: "telemetry.example.com"},
				{Pattern: "*/v1/traces", Scope: "url", Type: "glob"},
				{Pattern: "/v1/metrics", Scope: "path", Type: "exact"},
			}},
		}}
		got := buildLogSkipConfig(appCfg)
		if got == nil {
			t.Fatal("expected non-nil result")
		}
		if len(got.Rules) != 3 {
			t.Fatalf("expected 3 rules, got %d", len(got.Rules))
		}
		if got.Rules[0].Pattern != "telemetry.example.com" || got.Rules[0].Scope != "" || got.Rules[0].Type != "" {
			t.Errorf("rule[0] mismatch: %+v", got.Rules[0])
		}
		if got.Rules[1].Pattern != "*/v1/traces" || got.Rules[1].Scope != proxy.FilterScopeURL || got.Rules[1].Type != proxy.PatternTypeGlob {
			t.Errorf("rule[1] mismatch: %+v", got.Rules[1])
		}
		if got.Rules[2].Pattern != "/v1/metrics" || got.Rules[2].Scope != proxy.FilterScopePath || got.Rules[2].Type != proxy.PatternTypeExact {
			t.Errorf("rule[2] mismatch: %+v", got.Rules[2])
		}
	})
}
