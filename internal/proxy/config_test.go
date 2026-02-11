package proxy

import "testing"

func TestAskSocketPath(t *testing.T) {
	path := AskSocketPath("/tmp/sandbox-test")
	expected := "/tmp/sandbox-test/logs/proxy/.ask/ask.sock"
	if path != expected {
		t.Errorf("AskSocketPath = %q, want %q", path, expected)
	}
}

func TestAskSocketDir(t *testing.T) {
	dir := AskSocketDir("/tmp/sandbox-test")
	expected := "/tmp/sandbox-test/logs/proxy/.ask"
	if dir != expected {
		t.Errorf("AskSocketDir = %q, want %q", dir, expected)
	}
}

func TestAskLockPath(t *testing.T) {
	path := AskLockPath("/tmp/sandbox-test")
	expected := "/tmp/sandbox-test/logs/proxy/.ask/ask.lock"
	if path != expected {
		t.Errorf("AskLockPath = %q, want %q", path, expected)
	}
}
