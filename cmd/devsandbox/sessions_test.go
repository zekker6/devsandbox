package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"devsandbox/internal/session"
)

func captureSessionOutput(t *testing.T, fn func() error) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = f
	t.Cleanup(func() {
		os.Stdout = previous
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := fn(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = previous
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSessionsTableBackendAndForwarding(t *testing.T) {
	for _, tc := range []struct {
		name, backend, namespace, wantBackend, wantPorts string
	}{
		{"native", "seatbelt", "", "seatbelt", "(unavailable)"},
		{"namespace", "bwrap", "/proc/self/ns/net", "bwrap", "(none)"},
		{"legacy", "", "/proc/self/ns/net", "(unknown)", "(none)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := captureSessionOutput(t, func() error {
				return printSessionsTable([]*session.Session{{
					Name: tc.name, Backend: tc.backend, NetworkNS: tc.namespace,
					PID: os.Getpid(), StartedAt: time.Now(), WorkDir: "/work/project",
				}})
			})
			for _, want := range []string{"BACKEND", tc.name, tc.wantBackend, tc.wantPorts} {
				if !strings.Contains(out, want) {
					t.Errorf("table missing %q: %s", want, out)
				}
			}
		})
	}
}

func TestSessionsJSONIncludesNative(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	store, err := session.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Register(&session.Session{Name: "native", PID: os.Getpid(), Backend: "seatbelt"}); err != nil {
		t.Fatal(err)
	}
	out := captureSessionOutput(t, func() error {
		cmd := newSessionsCmd()
		cmd.SetArgs([]string{"--json"})
		return cmd.Execute()
	})
	var records []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || string(records[0]["backend"]) != `"seatbelt"` {
		t.Fatalf("unexpected JSON: %s", out)
	}
	if _, ok := records[0]["network_ns"]; ok {
		t.Fatalf("native JSON contains namespace: %s", out)
	}
}
