package main

import "testing"

func TestCheckGit(t *testing.T) {
	r := checkGit()
	if r.name != "git" {
		t.Errorf("name = %q, want %q", r.name, "git")
	}
	switch r.status {
	case "ok", "warn", "error":
	default:
		t.Errorf("unexpected status %q", r.status)
	}
	if r.message == "" {
		t.Error("message is empty")
	}
}
