// Package portforward provides utilities for detecting listening TCP sockets
// inside sandbox network namespaces via /proc/<pid>/net/tcp.
package portforward

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// ListenEntry represents a listening socket found in /proc/net/tcp.
type ListenEntry struct {
	IP   string
	Port int
}

// PortScanner detects listening ports in a network namespace.
type PortScanner interface {
	ListeningPorts() ([]ListenEntry, error)
}

// ProcNetScanner reads /proc/<pid>/net/tcp to find listening sockets.
type ProcNetScanner struct {
	ProcNetTCPPath string // e.g., "/proc/12345/net/tcp"
}

// NewProcNetScanner creates a ProcNetScanner for the given PID.
func NewProcNetScanner(pid int) *ProcNetScanner {
	return &ProcNetScanner{
		ProcNetTCPPath: fmt.Sprintf("/proc/%d/net/tcp", pid),
	}
}

// ListeningPorts reads ProcNetTCPPath, parses listening sockets, and returns
// only those on non-privileged ports (>= 1024).
func (s *ProcNetScanner) ListeningPorts() ([]ListenEntry, error) {
	f, err := os.Open(s.ProcNetTCPPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", s.ProcNetTCPPath, err)
	}
	defer func() { _ = f.Close() }()

	all, err := parseProcNetTCP(f)
	if err != nil {
		return nil, err
	}

	filtered := make([]ListenEntry, 0, len(all))
	for _, e := range all {
		if e.Port >= 1024 {
			filtered = append(filtered, e)
		}
	}
	return filtered, nil
}

// parseProcNetTCP parses the contents of /proc/net/tcp (or /proc/net/tcp6)
// and returns all sockets in the LISTEN state (0A).
func parseProcNetTCP(r io.Reader) ([]ListenEntry, error) {
	var entries []ListenEntry
	scanner := bufio.NewScanner(r)

	// Skip the header line.
	if !scanner.Scan() {
		return entries, scanner.Err()
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		// Minimum columns needed: sl, local_address, rem_address, st
		if len(fields) < 4 {
			continue
		}

		state := fields[3]
		if state != "0A" {
			continue
		}

		ip, port, err := parseHexAddr(fields[1])
		if err != nil {
			return nil, fmt.Errorf("parse local_address %q: %w", fields[1], err)
		}

		entries = append(entries, ListenEntry{IP: ip, Port: port})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading /proc/net/tcp: %w", err)
	}

	return entries, nil
}

// parseHexAddr parses a "HHHHHHHH:PPPP" address field from /proc/net/tcp.
// The IP is encoded as a little-endian hex uint32 (x86 byte order),
// and the port is a big-endian hex uint16.
func parseHexAddr(s string) (string, int, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("expected HHHHHHHH:PPPP, got %q", s)
	}

	ipHex := parts[0]
	portHex := parts[1]

	ipInt, err := strconv.ParseUint(ipHex, 16, 32)
	if err != nil {
		return "", 0, fmt.Errorf("parse IP hex %q: %w", ipHex, err)
	}

	portInt, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return "", 0, fmt.Errorf("parse port hex %q: %w", portHex, err)
	}

	// Bytes are stored in little-endian order on x86:
	// lowest byte of uint32 is the first octet of the IP.
	ip := fmt.Sprintf("%d.%d.%d.%d",
		ipInt&0xFF,
		(ipInt>>8)&0xFF,
		(ipInt>>16)&0xFF,
		(ipInt>>24)&0xFF,
	)

	return ip, int(portInt), nil
}
