// Package portforward provides utilities for detecting listening TCP sockets
// inside sandbox network namespaces via /proc/<pid>/net/tcp and tcp6.
package portforward

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
)

// ListenEntry represents a listening socket found in /proc/net/tcp or tcp6.
type ListenEntry struct {
	IP   string
	Port int
}

// PortScanner detects listening ports in a network namespace.
type PortScanner interface {
	ListeningPorts() ([]ListenEntry, error)
}

// ProcNetScanner reads /proc/<pid>/net/tcp and tcp6 to find listening sockets.
type ProcNetScanner struct {
	ProcNetTCPPath  string // e.g., "/proc/12345/net/tcp"
	ProcNetTCP6Path string // Empty skips IPv6 scanning.
}

// NewProcNetScanner creates a ProcNetScanner for the given PID.
func NewProcNetScanner(pid int) *ProcNetScanner {
	return &ProcNetScanner{
		ProcNetTCPPath:  fmt.Sprintf("/proc/%d/net/tcp", pid),
		ProcNetTCP6Path: fmt.Sprintf("/proc/%d/net/tcp6", pid),
	}
}

// ListeningPorts reads both configured paths and returns listening sockets on
// non-privileged ports (>= 1024). A missing tcp6 file means IPv6 is unavailable.
func (s *ProcNetScanner) ListeningPorts() ([]ListenEntry, error) {
	filtered := make([]ListenEntry, 0)
	for i, path := range []string{s.ProcNetTCPPath, s.ProcNetTCP6Path} {
		if i == 1 && path == "" {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			if i == 1 && os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("open %s: %w", path, err)
		}

		all, parseErr := parseProcNetTCP(f)
		closeErr := f.Close()
		if parseErr != nil {
			return nil, fmt.Errorf("parse %s: %w", path, parseErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close %s: %w", path, closeErr)
		}
		for _, e := range all {
			if e.Port >= 1024 {
				filtered = append(filtered, e)
			}
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
		return nil, fmt.Errorf("reading proc network table: %w", err)
	}

	return entries, nil
}

// parseHexAddr parses a local_address field from /proc/net/tcp or tcp6.
// The IP is encoded as one or four native-endian hex uint32 words, and the
// port is a hex uint16. IPv4-mapped IPv6 addresses become plain IPv4 strings.
func parseHexAddr(s string) (string, int, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("expected IPHEX:PORTHEX, got %q", s)
	}

	ipHex := parts[0]
	portHex := parts[1]

	if len(ipHex) != 8 && len(ipHex) != 32 {
		return "", 0, fmt.Errorf("expected 8 or 32 IP hex digits, got %q", ipHex)
	}
	ip := make(net.IP, len(ipHex)/2)
	for offset := 0; offset < len(ipHex); offset += 8 {
		word, err := strconv.ParseUint(ipHex[offset:offset+8], 16, 32)
		if err != nil {
			return "", 0, fmt.Errorf("parse IP hex %q: %w", ipHex, err)
		}
		binary.NativeEndian.PutUint32(ip[offset/2:offset/2+4], uint32(word))
	}

	portInt, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return "", 0, fmt.Errorf("parse port hex %q: %w", portHex, err)
	}

	return ip.String(), int(portInt), nil
}
