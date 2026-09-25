package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "12h", want: 12 * time.Hour},
		{in: "30d", want: 30 * 24 * time.Hour},
		{in: "2w", want: 14 * 24 * time.Hour},
		{in: "d", wantErr: true},
		{in: "3xd", wantErr: true},
		{in: "5y", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseDuration(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseDuration(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseDuration(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestValidate_MaxAge(t *testing.T) {
	tests := []struct {
		value   string
		wantErr string
	}{
		{value: ""},
		{value: "30d"},
		{value: "banana", wantErr: "sandbox.max_age: invalid duration"},
		{value: "0d", wantErr: "must be positive"},
		{value: "-1h", wantErr: "must be positive"},
	}
	for _, tt := range tests {
		err := (&Config{Sandbox: SandboxConfig{MaxAge: tt.value}}).Validate()
		if tt.wantErr == "" {
			if err != nil {
				t.Errorf("max_age %q: unexpected error %v", tt.value, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("max_age %q: err = %v, want containing %q", tt.value, err, tt.wantErr)
		}
	}
}

func TestSandboxConfig_GetMaxAge(t *testing.T) {
	if got := (SandboxConfig{}).GetMaxAge(); got != 0 {
		t.Errorf("unset max_age = %v, want 0 (disabled)", got)
	}
	if got := (SandboxConfig{MaxAge: "2w"}).GetMaxAge(); got != 14*24*time.Hour {
		t.Errorf("max_age 2w = %v", got)
	}
}

// max_age decides the fate of every sandbox on the host, and the project file
// is sandbox-writable: a project must be able neither to set nor to change it.
func TestMergeProjectConfig_IgnoresLocalMaxAge(t *testing.T) {
	for _, host := range []string{"", "30d"} {
		merged := mergeProjectConfig(
			&Config{Sandbox: SandboxConfig{MaxAge: host}},
			&Config{Sandbox: SandboxConfig{MaxAge: "1s", Isolation: IsolationBwrap}},
		)
		if merged.Sandbox.MaxAge != host {
			t.Errorf("host max_age %q: merged = %q, want the host value", host, merged.Sandbox.MaxAge)
		}
		if merged.Sandbox.Isolation != IsolationBwrap {
			t.Errorf("isolation = %q, want the project override to still apply", merged.Sandbox.Isolation)
		}
	}
}

// An include is host-owned config the user wrote, so it may set max_age.
func TestApplyIncludes_IncludeSetsMaxAge(t *testing.T) {
	dir := t.TempDir()
	includePath := filepath.Join(dir, "include.toml")
	if err := os.WriteFile(includePath, []byte("[sandbox]\nmax_age = \"1d\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		Sandbox: SandboxConfig{MaxAge: "30d"},
		Include: []Include{{If: "dir:" + dir, Path: includePath}},
	}
	merged, err := applyIncludes(cfg, dir)
	if err != nil {
		t.Fatalf("applyIncludes: %v", err)
	}
	if merged.Sandbox.MaxAge != "1d" {
		t.Errorf("max_age = %q, want the include's %q", merged.Sandbox.MaxAge, "1d")
	}
}
