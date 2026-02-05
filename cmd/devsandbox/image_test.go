package main

import (
	"testing"
)

func TestNewImageCmd(t *testing.T) {
	cmd := newImageCmd()
	if cmd.Use != "image" {
		t.Errorf("expected Use='image', got %q", cmd.Use)
	}

	// Should have pull subcommand
	pullCmd, _, err := cmd.Find([]string{"pull"})
	if err != nil {
		t.Fatalf("pull subcommand not found: %v", err)
	}
	if pullCmd.Use != "pull" {
		t.Errorf("expected pull subcommand, got %q", pullCmd.Use)
	}
}
