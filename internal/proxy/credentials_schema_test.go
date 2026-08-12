package proxy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devsandbox/internal/config"
	"devsandbox/internal/notice"
)

// load writes cfg to a file and loads it, returning what devsandbox reported on
// stderr along the way.
func load(t *testing.T, cfg string) (string, error) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stderr bytes.Buffer
	if err := notice.Setup("", true, &stderr); err != nil {
		t.Fatalf("notice.Setup: %v", err)
	}
	t.Cleanup(func() { _ = notice.Setup("", false, nil) })

	_, err := config.LoadFrom(path)
	return stderr.String(), err
}

// loadWarnings returns what devsandbox reports on stderr while loading cfg.
func loadWarnings(t *testing.T, cfg string) string {
	t.Helper()
	out, err := load(t, cfg)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	return out
}

func TestCredentialSchema_RejectsMistypedValues(t *testing.T) {
	_, err := load(t, "[proxy.credentials.github]\nenabled = \"yes\"\n")
	if err == nil {
		t.Fatal("a mistyped injector setting was accepted")
	}
	if !strings.Contains(err.Error(), "enabled: expected a boolean, got a string") {
		t.Errorf("error = %q, want it to name the key and both types", err)
	}

	_, err = load(t, "[proxy.credentials.github]\nenabled = true\n[proxy.credentials.github.source]\nenv = 5\n")
	if err == nil {
		t.Fatal("a mistyped source setting was accepted")
	}
	if !strings.Contains(err.Error(), "source.env: expected a string, got an integer") {
		t.Errorf("error = %q, want it to name the sub-table key", err)
	}
}

// Every key buildOne reads must be in the schema, or configuring an injector
// the documented way reports keys that do work.
func TestCredentialSchema_AcceptsEveryKeyBuildOneReads(t *testing.T) {
	cfg := `[proxy.credentials.internal]
preset = "github"
host = "api.internal"
header = "Authorization"
value_format = "Bearer {token}"
overwrite = true
enabled = true

[proxy.credentials.internal.source]
env = "TOKEN"
`
	if out := loadWarnings(t, cfg); strings.Contains(out, "unknown config key") {
		t.Errorf("a fully configured injector reported unknown keys: %q", out)
	}
}

func TestCredentialSchema_ReportsTypos(t *testing.T) {
	cfg := `[proxy.credentials.github]
enabled = true
overwritte = true

[proxy.credentials.github.source]
envv = "TOKEN"
`
	out := loadWarnings(t, cfg)
	for _, want := range []string{"proxy.credentials.github.overwritte", "proxy.credentials.github.source.envv"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning missing %q: %q", want, out)
		}
	}
}
