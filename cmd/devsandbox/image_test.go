package main

import (
	"os"
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

func TestGetConfiguredDockerfile_Default(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	origXDG := os.Getenv("XDG_CONFIG_HOME")
	if err := os.Setenv("XDG_CONFIG_HOME", tmpDir); err != nil {
		t.Fatalf("failed to set XDG_CONFIG_HOME: %v", err)
	}
	defer func() { _ = os.Setenv("XDG_CONFIG_HOME", origXDG) }()

	df := getConfiguredDockerfile()
	// Should return empty (meaning use default)
	if df != "" {
		t.Errorf("expected empty string (default), got %q", df)
	}
}
