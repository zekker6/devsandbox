package main

import (
	"os"
	"strings"
	"testing"

	"devsandbox/internal/session"
)

func TestForwardRejectsNativeAndMissingNamespace(t *testing.T) {
	for _, tc := range []struct {
		name, backend, namespace string
	}{
		{"native", "seatbelt", ""},
		{"native with namespace", "seatbelt", "/proc/self/ns/net"},
		{"no namespace", "bwrap", ""},
		{"legacy without namespace", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			store, err := session.DefaultStore()
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Register(&session.Session{
				Name: "target", PID: os.Getpid(), Backend: tc.backend, NetworkNS: tc.namespace,
			}); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"target", ""} {
				// Invalid input also proves rejection precedes port parsing and
				// any namespace dialer or host listener construction.
				err := runForward(t.Context(), name, "127.0.0.1", []string{"not-a-port"})
				if err == nil || !strings.Contains(err.Error(), "port forwarding is unavailable") {
					t.Fatalf("unexpected forward result: %v", err)
				}
			}
			got, err := store.Get("target")
			if err != nil || len(got.ForwardedPorts) != 0 {
				t.Fatalf("forward mutated record: %+v, %v", got, err)
			}
		})
	}
}
