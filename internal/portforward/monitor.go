package portforward

import (
	"context"
	"sync"
	"time"
)

// Monitor periodically scans for listening ports and fires callbacks when
// ports appear or disappear.
type Monitor struct {
	Scanner       PortScanner // From procnet.go
	Interval      time.Duration
	ExcludePorts  map[int]bool // Ports to never auto-forward
	ManualPorts   map[int]bool // Ports already manually forwarded (skip these)
	OnPortAdded   func(port int)
	OnPortRemoved func(port int)

	mu    sync.Mutex
	known map[int]bool
	wg    sync.WaitGroup
}

// Start initialises the known port set and starts the background scanning goroutine.
func (m *Monitor) Start(ctx context.Context) {
	m.known = make(map[int]bool)
	m.wg.Go(func() {
		m.loop(ctx)
	})
}

// Wait blocks until the background goroutine exits.
func (m *Monitor) Wait() {
	m.wg.Wait()
}

// SetManualPort marks or unmarks a port as manually forwarded. Ports marked
// manual are skipped during auto-forward decisions but are still removed from
// known when they disappear.
func (m *Monitor) SetManualPort(port int, active bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ManualPorts == nil {
		m.ManualPorts = make(map[int]bool)
	}
	if active {
		m.ManualPorts[port] = true
	} else {
		delete(m.ManualPorts, port)
	}
}

func (m *Monitor) loop(ctx context.Context) {
	m.scan()
	ticker := time.NewTicker(m.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.scan()
		}
	}
}

func (m *Monitor) scan() {
	entries, err := m.Scanner.ListeningPorts()
	if err != nil {
		// Sandbox may have exited; silently return.
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	current := make(map[int]bool, len(entries))
	for _, e := range entries {
		current[e.Port] = true
	}

	// Detect new ports.
	for port := range current {
		if m.known[port] {
			continue
		}
		if m.ExcludePorts[port] || m.ManualPorts[port] {
			continue
		}
		m.known[port] = true
		if m.OnPortAdded != nil {
			m.OnPortAdded(port)
		}
	}

	// Detect removed ports.
	for port := range m.known {
		if current[port] {
			continue
		}
		delete(m.known, port)
		if m.OnPortRemoved != nil {
			m.OnPortRemoved(port)
		}
	}
}
