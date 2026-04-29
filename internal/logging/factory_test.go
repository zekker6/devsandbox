package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devsandbox/internal/config"
	"devsandbox/internal/source"
)

func TestResolveOTLPHeaders_StaticOnly(t *testing.T) {
	headers, err := resolveOTLPHeaders(map[string]string{"X-Team": "platform"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := headers["X-Team"]; got != "platform" {
		t.Errorf("X-Team = %q, want %q", got, "platform")
	}
}

func TestResolveOTLPHeaders_FromEnv(t *testing.T) {
	t.Setenv("OTLP_TEST_TOKEN", "Bearer abc123")

	headers, err := resolveOTLPHeaders(nil, map[string]source.Source{
		"Authorization": {Env: "OTLP_TEST_TOKEN"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := headers["Authorization"]; got != "Bearer abc123" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer abc123")
	}
}

func TestResolveOTLPHeaders_EnvUnsetIsError(t *testing.T) {
	if err := os.Unsetenv("OTLP_TEST_MISSING"); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}

	_, err := resolveOTLPHeaders(nil, map[string]source.Source{
		"Authorization": {Env: "OTLP_TEST_MISSING"},
	})
	if err == nil {
		t.Fatal("expected error for unset env var, got nil")
	}
	if !strings.Contains(err.Error(), "Authorization") {
		t.Errorf("error %q should mention header name", err.Error())
	}
}

func TestResolveOTLPHeaders_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("Bearer file-token\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	headers, err := resolveOTLPHeaders(nil, map[string]source.Source{
		"Authorization": {File: path},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := headers["Authorization"]; got != "Bearer file-token" {
		t.Errorf("Authorization = %q, want trimmed file content", got)
	}
}

func TestResolveOTLPHeaders_SourceWinsOverStatic(t *testing.T) {
	t.Setenv("OTLP_TEST_TOKEN", "from-source")

	headers, err := resolveOTLPHeaders(
		map[string]string{"Authorization": "from-static"},
		map[string]source.Source{"Authorization": {Env: "OTLP_TEST_TOKEN"}},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := headers["Authorization"]; got != "from-source" {
		t.Errorf("Authorization = %q, want source value to win", got)
	}
}

func TestResolveOTLPHeaders_NilWhenEmpty(t *testing.T) {
	headers, err := resolveOTLPHeaders(nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if headers != nil {
		t.Errorf("expected nil headers, got %v", headers)
	}
}

func TestNewWriterFromConfig_OTLPHeaderSources(t *testing.T) {
	t.Setenv("OTLP_TEST_TOKEN", "Bearer xyz")

	r := config.ReceiverConfig{
		Type:     "otlp",
		Endpoint: "http://127.0.0.1:4318/v1/logs",
		Protocol: "http",
		Headers:  map[string]string{"X-Team": "platform"},
		HeaderSources: map[string]source.Source{
			"Authorization": {Env: "OTLP_TEST_TOKEN"},
		},
	}

	w, err := newWriterFromConfig(r, nil, nil)
	if err != nil {
		t.Fatalf("newWriterFromConfig: %v", err)
	}
	defer func() { _ = w.Close() }()

	otlp, ok := w.(*OTLPWriter)
	if !ok {
		t.Fatalf("expected *OTLPWriter, got %T", w)
	}
	if got := otlp.cfg.Headers["Authorization"]; got != "Bearer xyz" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer xyz")
	}
	if got := otlp.cfg.Headers["X-Team"]; got != "platform" {
		t.Errorf("X-Team header = %q, want %q", got, "platform")
	}
}

func TestNewWriterFromConfig_OTLPHeaderSourceUnsetEnv(t *testing.T) {
	if err := os.Unsetenv("OTLP_TEST_MISSING"); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}

	r := config.ReceiverConfig{
		Type:     "otlp",
		Endpoint: "http://127.0.0.1:4318/v1/logs",
		Protocol: "http",
		HeaderSources: map[string]source.Source{
			"Authorization": {Env: "OTLP_TEST_MISSING"},
		},
	}

	if _, err := newWriterFromConfig(r, nil, nil); err == nil {
		t.Fatal("expected error when header source env var is unset")
	}
}
