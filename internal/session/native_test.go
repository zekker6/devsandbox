package session_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"devsandbox/internal/session"
)

func TestNativeSessionLifecycle(t *testing.T) {
	store := newTestStore(t)
	sess := makeSession("native")
	sess.Backend = "seatbelt"
	sess.NetworkNS = ""
	if err := store.Register(sess); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(sess.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != "seatbelt" || got.PID != os.Getpid() || got.NetworkNS != "" || got.CanForward() {
		t.Fatalf("unexpected native session: %+v", got)
	}
	if got.WorkDir != sess.WorkDir || got.ProxyPort != sess.ProxyPort || !got.StartedAt.Equal(sess.StartedAt) {
		t.Fatalf("lost session metadata: %+v", got)
	}
	live, err := store.ListLive()
	if err != nil || len(live) != 1 || live[0].Backend != "seatbelt" {
		t.Fatalf("ListLive: %+v, %v", live, err)
	}
	data, err := os.ReadFile(filepath.Join(store.Dir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"network_ns", "forwarded_ports"} {
		if _, ok := fields[absent]; ok {
			t.Errorf("native JSON contains %s", absent)
		}
	}
	if err := store.Remove(sess.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(sess.Name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("record survived removal: %v", err)
	}
}

func TestSessionCanForward(t *testing.T) {
	for _, tc := range []struct {
		name, backend, namespace string
		want                     bool
	}{
		{"legacy namespace", "", "/proc/42/ns/net", true},
		{"bwrap", "bwrap", "/proc/42/ns/net", true},
		{"krun", "krun", "/proc/42/ns/net", true},
		{"native", "seatbelt", "", false},
		{"native with invalid namespace", "seatbelt", "/proc/42/ns/net", false},
		{"missing namespace", "bwrap", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := &session.Session{Backend: tc.backend, NetworkNS: tc.namespace}
			if got := sess.CanForward(); got != tc.want {
				t.Fatalf("CanForward = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLegacySessionWithoutBackend(t *testing.T) {
	store := newTestStore(t)
	sess := makeSession("legacy")
	if err := store.Register(sess); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(store.Dir(), "legacy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["backend"]; ok {
		t.Fatal("legacy record should omit backend")
	}
	got, err := store.Get("legacy")
	if err != nil || !got.CanForward() {
		t.Fatalf("legacy forwarding lost: %+v, %v", got, err)
	}
}
