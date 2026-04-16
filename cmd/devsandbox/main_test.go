package main

import (
	"testing"

	"github.com/spf13/cobra"
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
