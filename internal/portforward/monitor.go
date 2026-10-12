package portforward

import (
	"context"
	"fmt"
	"net/netip"
	"sync"
	"time"
)

// Monitor periodically scans for listening ports and reconciles forwards.
// Callbacks run serially on the scanning goroutine.
type Monitor struct {
	Scanner       PortScanner
	Interval      time.Duration
	ExcludePorts  map[int]bool
	ManualPorts   map[int]bool
	OnPortAdded   func(entry ListenEntry) error
	OnPortRemoved func(port int)
	OnError       func(error)

	mu            sync.Mutex
	known         map[int]ListenEntry
	failed        map[int]string
	lastScanError string
	wg            sync.WaitGroup
}

func (m *Monitor) Start(ctx context.Context) {
	m.known = make(map[int]ListenEntry)
	m.failed = make(map[int]string)
	m.wg.Go(func() { m.loop(ctx) })
}

func (m *Monitor) Wait() { m.wg.Wait() }

// SetManualPort marks or unmarks a port as manually forwarded.
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
		if msg := err.Error(); msg != m.lastScanError {
			m.report(fmt.Errorf("scan listening ports: %w", err))
			m.lastScanError = msg
		}
		return
	}
	m.lastScanError = ""

	current := make(map[int]ListenEntry, len(entries))
	m.mu.Lock()
	for _, entry := range entries {
		if m.ExcludePorts[entry.Port] || m.ManualPorts[entry.Port] {
			continue
		}
		entry = dialableEntry(entry)
		previous, exists := current[entry.Port]
		if !exists || preferEntry(entry, previous) {
			current[entry.Port] = entry
		}
	}
	m.mu.Unlock()

	// An address change on an existing port needs a new destination too.
	for port, entry := range m.known {
		if current[port] == entry {
			continue
		}
		delete(m.known, port)
		if m.OnPortRemoved != nil {
			m.OnPortRemoved(port)
		}
	}
	for port := range m.failed {
		if _, exists := current[port]; !exists {
			delete(m.failed, port)
		}
	}
	for port, entry := range current {
		if m.known[port] == entry {
			continue
		}
		if m.OnPortAdded != nil {
			if err := m.OnPortAdded(entry); err != nil {
				if msg := err.Error(); m.failed[port] != msg {
					m.report(fmt.Errorf("forward port %d: %w; retrying on the next scan", port, err))
					m.failed[port] = msg
				}
				continue
			}
		}
		delete(m.failed, port)
		m.known[port] = entry
	}
}

func (m *Monitor) report(err error) {
	if m.OnError != nil {
		m.OnError(err)
	}
}

// Wildcard listeners are reached through loopback in their own address family.
func dialableEntry(entry ListenEntry) ListenEntry {
	switch entry.IP {
	case "", "0.0.0.0":
		entry.IP = "127.0.0.1"
	case "::":
		entry.IP = "::1"
	}
	return entry
}

// One host port can serve only one listener. Prefer IPv4, then loopback, and
// finally lexical address order so procfs enumeration cannot flap the target.
func preferEntry(a, b ListenEntry) bool {
	ap, _ := netip.ParseAddr(a.IP)
	bp, _ := netip.ParseAddr(b.IP)
	if ap.Is4() != bp.Is4() {
		return ap.Is4()
	}
	if ap.IsLoopback() != bp.IsLoopback() {
		return ap.IsLoopback()
	}
	return a.IP < b.IP
}
