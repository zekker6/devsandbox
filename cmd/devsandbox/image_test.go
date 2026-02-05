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

	// Should have pull subcommand
	pullCmd, _, err := cmd.Find([]string{"pull"})
	if err != nil {
		t.Fatalf("pull subcommand not found: %v", err)
	}
	if pullCmd.Use != "pull" {
		t.Errorf("expected pull subcommand, got %q", pullCmd.Use)
	}
}

func TestGetImageInfo_NotExists(t *testing.T) {
	info, err := getImageInfo("nonexistent-image:v999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Error("expected nil info for non-existent image")
	}
}

func TestGetConfiguredImage_Default(t *testing.T) {
	// When no config exists, should return default image
	// Change to a temp directory and set XDG_CONFIG_HOME to avoid picking up global config
	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	// Set XDG_CONFIG_HOME to temp dir to avoid global config
	origXDG := os.Getenv("XDG_CONFIG_HOME")
	if err := os.Setenv("XDG_CONFIG_HOME", tmpDir); err != nil {
		t.Fatalf("failed to set XDG_CONFIG_HOME: %v", err)
	}
	defer func() { _ = os.Setenv("XDG_CONFIG_HOME", origXDG) }()

	image := getConfiguredImage()
	expected := "ghcr.io/zekker6/devsandbox:latest"
	if image != expected {
		t.Errorf("expected %q, got %q", expected, image)
	}
}
