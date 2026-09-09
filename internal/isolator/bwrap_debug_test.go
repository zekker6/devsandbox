package isolator

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devsandbox/internal/notice"
)

func TestBwrapDebugArgs_OmitsPrivateValues(t *testing.T) {
	t.Setenv("DEVSANDBOX_DEBUG", "1")
	var stderr bytes.Buffer
	logPath := filepath.Join(t.TempDir(), "wrapper.log")
	if err := notice.Setup(logPath, true, &stderr); err != nil {
		t.Fatal(err)
	}
	notice.SetRunning()
	t.Cleanup(func() {
		notice.Flush()
		notice.SetStartup()
		if err := notice.Setup("", false, io.Discard); err != nil {
			t.Errorf("reset notice: %v", err)
		}
	})
	var sink []string
	if dropped := notice.AttachSink(func(_ notice.Level, msg string, _ time.Time) {
		sink = append(sink, msg)
	}); dropped != 0 {
		t.Fatalf("notice sink dropped %d entries", dropped)
	}

	const token = "private-proxy-token-0123456789"
	secretArgs := []string{"--setenv", "HTTP_PROXY", "http://devsandbox:" + token + "@10.0.2.2:8080"}
	iso := NewBwrapIsolator(BwrapConfig{})
	if err := iso.debugArgs([]string{"--unshare-pid", "--bind", "/src", "/src"}, secretArgs, []string{"/bin/sh"}); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(sink) != 1 {
		t.Fatalf("sink entries = %d, want one debug entry", len(sink))
	}
	for name, output := range map[string]string{
		"stderr":      stderr.String(),
		"wrapper log": string(log),
		"notice sink": sink[0],
	} {
		if strings.Contains(output, token) || strings.Contains(output, "HTTP_PROXY") {
			t.Errorf("%s contains private arguments: %q", name, output)
		}
		for _, want := range []string{"=== Sandbox Debug ===", "bwrap", "--unshare-pid", "--bind", "/src", "-- [/bin/sh]"} {
			if !strings.Contains(output, want) {
				t.Errorf("%s missing ordinary debug output %q: %q", name, want, output)
			}
		}
	}
}
