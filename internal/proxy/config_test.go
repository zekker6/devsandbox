package proxy

import (
	"testing"

	"devsandbox/internal/config"
)

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

// TestGetMaxLogBodyBytes covers the three answers the accessor gives, including
// the nil-field default every default-configured launch runs on.
func TestGetMaxLogBodyBytes(t *testing.T) {
	explicit := func(n int) *Config {
		c := &Config{}
		c.MaxLogBodyBytes = &n
		return c
	}

	tests := []struct {
		name string
		cfg  *Config
		want int
	}{
		{"nil receiver", nil, config.DefaultMaxLogBodyBytes},
		{"unset field", &Config{}, config.DefaultMaxLogBodyBytes},
		{"explicit zero opts out", explicit(0), 0},
		{"explicit value", explicit(4096), 4096},
		{"negative clamps to zero", explicit(-1), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.GetMaxLogBodyBytes(); got != tt.want {
				t.Errorf("GetMaxLogBodyBytes() = %d, want %d", got, tt.want)
			}
		})
	}
}
