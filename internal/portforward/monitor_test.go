package portforward

import (
	"context"
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
		OnPortAdded: func(port int) {
			if port == 3000 {
				added.Add(1)
			}
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
		OnPortAdded: func(port int) {},
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
		OnPortAdded: func(port int) {
			mu.Lock()
			addedPorts = append(addedPorts, port)
			mu.Unlock()
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
		OnPortAdded: func(port int) {
			added.Add(1)
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
