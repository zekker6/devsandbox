package main

import (
	"testing"
)

func TestNewImageCmd(t *testing.T) {
	cmd := newImageCmd()
	if cmd.Use != "image" {
		t.Errorf("expected Use='image', got %q", cmd.Use)
	}

	// Should have build subcommand
	buildCmd, _, err := cmd.Find([]string{"build"})
	if err != nil {
		t.Fatalf("build subcommand not found: %v", err)
	}
	if buildCmd.Use != "build" {
		t.Errorf("expected build subcommand, got %q", buildCmd.Use)
	}
}
