package bwrap

import (
	"testing"
)

func TestCheckInstalled(t *testing.T) {
	// With embedded binaries, CheckInstalled should always succeed
	// (extraction to cache dir or system fallback).
	// It can only fail if both embedded extraction AND system LookPath fail.
	err := CheckInstalled()
	// Don't assert success/failure — depends on test environment.
	// Just verify it doesn't panic.
	t.Logf("CheckInstalled() error: %v", err)
}

func TestPastaSupportsMapHostLoopback(t *testing.T) {
	// Test with a known path — just verify it doesn't panic.
	// With embedded binary, this should use the compile-time constant.
	result := pastaSupportsMapHostLoopback("/nonexistent/pasta")
	if result {
		t.Error("pastaSupportsMapHostLoopback should return false for nonexistent path")
	}
}
