package portforward

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mockScanner struct {
	mu    sync.Mutex
	ports []ListenEntry
}

func (m *mockScanner) ListeningPorts() ([]ListenEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]ListenEntry, len(m.ports))
	copy(result, m.ports)
	return result, nil
}

func (m *mockScanner) setPorts(ports []ListenEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ports = ports
}

func TestMonitor_DetectsNewPort(t *testing.T) {
	scanner := &mockScanner{}

	var added atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mon := &Monitor{
		Scanner:  scanner,
		Interval: 50 * time.Millisecond,
		OnPortAdded: func(entry ListenEntry) error {
			if entry.Port == 3000 {
				added.Add(1)
			}
			return nil
		},
	}
	mon.Start(ctx)

	// Initially no ports — wait one cycle.
	time.Sleep(150 * time.Millisecond)
	if added.Load() != 0 {
		t.Fatalf("expected 0 added, got %d", added.Load())
	}

	// Add port 3000.
	scanner.setPorts([]ListenEntry{{IP: "127.0.0.1", Port: 3000}})
	time.Sleep(150 * time.Millisecond)

	if added.Load() == 0 {
		t.Fatal("OnPortAdded not called after port 3000 appeared")
	}

	cancel()
	mon.Wait()
}

func TestMonitor_DetectsRemovedPort(t *testing.T) {
	scanner := &mockScanner{
		ports: []ListenEntry{{IP: "127.0.0.1", Port: 3000}},
	}

	var removed atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mon := &Monitor{
		Scanner:     scanner,
		Interval:    50 * time.Millisecond,
		OnPortAdded: func(ListenEntry) error { return nil },
		OnPortRemoved: func(port int) {
			if port == 3000 {
				removed.Add(1)
			}
		},
	}
	mon.Start(ctx)

	// Let the initial scan pick up port 3000.
	time.Sleep(150 * time.Millisecond)

	// Remove the port.
	scanner.setPorts(nil)
	time.Sleep(150 * time.Millisecond)

	if removed.Load() == 0 {
		t.Fatal("OnPortRemoved not called after port 3000 disappeared")
	}

	cancel()
	mon.Wait()
}

func TestMonitor_ExcludePorts(t *testing.T) {
	scanner := &mockScanner{
		ports: []ListenEntry{
			{IP: "0.0.0.0", Port: 22},
			{IP: "127.0.0.1", Port: 3000},
		},
	}

	var mu sync.Mutex
	addedPorts := make([]int, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mon := &Monitor{
		Scanner:      scanner,
		Interval:     50 * time.Millisecond,
		ExcludePorts: map[int]bool{22: true},
		OnPortAdded: func(entry ListenEntry) error {
			mu.Lock()
			addedPorts = append(addedPorts, entry.Port)
			mu.Unlock()
			return nil
		},
	}
	mon.Start(ctx)

	time.Sleep(150 * time.Millisecond)
	cancel()
	mon.Wait()

	mu.Lock()
	defer mu.Unlock()

	for _, p := range addedPorts {
		if p == 22 {
			t.Fatal("excluded port 22 triggered OnPortAdded")
		}
	}
	found3000 := false
	for _, p := range addedPorts {
		if p == 3000 {
			found3000 = true
		}
	}
	if !found3000 {
		t.Fatal("expected port 3000 to trigger OnPortAdded")
	}
}

func TestMonitor_SkipsManuallyForwarded(t *testing.T) {
	scanner := &mockScanner{
		ports: []ListenEntry{{IP: "127.0.0.1", Port: 3000}},
	}

	var added atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mon := &Monitor{
		Scanner:     scanner,
		Interval:    50 * time.Millisecond,
		ManualPorts: map[int]bool{3000: true},
		OnPortAdded: func(ListenEntry) error {
			added.Add(1)
			return nil
		},
	}
	mon.Start(ctx)

	time.Sleep(150 * time.Millisecond)
	cancel()
	mon.Wait()

	if added.Load() != 0 {
		t.Fatalf("expected no callbacks for manually-forwarded port, got %d", added.Load())
	}
}

func TestMonitor_RetriesFailedForward(t *testing.T) {
	scanner := &mockScanner{ports: []ListenEntry{{IP: "127.0.0.1", Port: 3000}}}
	attempts, reports := 0, 0
	mon := &Monitor{
		Scanner: scanner,
		known:   make(map[int]ListenEntry),
		failed:  make(map[int]string),
		OnPortAdded: func(ListenEntry) error {
			attempts++
			if attempts < 3 {
				return errors.New("temporary bind failure")
			}
			return nil
		},
		OnError: func(error) { reports++ },
	}
	for range 4 {
		mon.scan()
	}
	if attempts != 3 || reports != 1 {
		t.Fatalf("attempts=%d reports=%d, want 3 and 1", attempts, reports)
	}
	if mon.known[3000].IP != "127.0.0.1" {
		t.Fatal("successful retry was not recorded")
	}
}

func TestMonitor_DestinationsAndAddressChanges(t *testing.T) {
	scanner := &mockScanner{}
	var added []ListenEntry
	var removed []int
	mon := &Monitor{
		Scanner:       scanner,
		known:         make(map[int]ListenEntry),
		failed:        make(map[int]string),
		OnPortAdded:   func(entry ListenEntry) error { added = append(added, entry); return nil },
		OnPortRemoved: func(port int) { removed = append(removed, port) },
	}
	for _, entries := range [][]ListenEntry{
		{{IP: "::", Port: 3000}},
		{{IP: "127.0.0.2", Port: 3000}},
		{{IP: "::1", Port: 3000}, {IP: "0.0.0.0", Port: 3000}},
		{{IP: "0.0.0.0", Port: 3000}, {IP: "::1", Port: 3000}},
	} {
		scanner.setPorts(entries)
		mon.scan()
	}
	want := []ListenEntry{{IP: "::1", Port: 3000}, {IP: "127.0.0.2", Port: 3000}, {IP: "127.0.0.1", Port: 3000}}
	if !reflect.DeepEqual(added, want) || !reflect.DeepEqual(removed, []int{3000, 3000}) {
		t.Fatalf("added=%v removed=%v", added, removed)
	}
}

type errorScanner struct{ err error }

func (s *errorScanner) ListeningPorts() ([]ListenEntry, error) { return nil, s.err }

func TestMonitor_ReportsScanErrorsAndRecovery(t *testing.T) {
	scanner := &errorScanner{err: errors.New("procfs unavailable")}
	reports := 0
	mon := &Monitor{Scanner: scanner, OnError: func(error) { reports++ }}
	mon.scan()
	mon.scan()
	if reports != 1 {
		t.Fatalf("reports=%d, want 1", reports)
	}
	scanner.err = nil
	mon.scan()
	scanner.err = errors.New("procfs unavailable")
	mon.scan()
	if reports != 2 {
		t.Fatalf("reports after recovery=%d, want 2", reports)
	}
}
