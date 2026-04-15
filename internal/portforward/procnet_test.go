package portforward

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseProcNetTCP(t *testing.T) {
	data := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12346 1 0000000000000000 100 0 0 10 0
   2: 00000000:0050 0100007F:C350 01 00000000:00000000 00:00000000 00000000  1000        0 12347 1 0000000000000000 100 0 0 10 0
`
	r := strings.NewReader(data)
	entries, err := parseProcNetTCP(r)
	if err != nil {
		t.Fatalf("parseProcNetTCP returned error: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(entries), entries)
	}

	// Entry 0: port 3000 (0x0BB8), IP 0.0.0.0
	if entries[0].Port != 3000 {
		t.Errorf("entry[0]: expected port 3000, got %d", entries[0].Port)
	}
	if entries[0].IP != "0.0.0.0" {
		t.Errorf("entry[0]: expected IP 0.0.0.0, got %s", entries[0].IP)
	}

	// Entry 1: port 8080 (0x1F90), IP 127.0.0.1
	if entries[1].Port != 8080 {
		t.Errorf("entry[1]: expected port 8080, got %d", entries[1].Port)
	}
	if entries[1].IP != "127.0.0.1" {
		t.Errorf("entry[1]: expected IP 127.0.0.1, got %s", entries[1].IP)
	}
}

func TestParseProcNetTCP_Empty(t *testing.T) {
	data := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
`
	r := strings.NewReader(data)
	entries, err := parseProcNetTCP(r)
	if err != nil {
		t.Fatalf("parseProcNetTCP returned error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d: %v", len(entries), entries)
	}
}

func TestProcNetScanner_ListeningPorts(t *testing.T) {
	// Write a fake /proc/net/tcp file in a temp dir.
	// Port 80 (0x0050) is privileged and must be filtered out.
	// Port 3000 (0x0BB8) and 8080 (0x1F90) must be returned.
	data := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 0000000000000000 100 0 0 10 0
   1: 00000000:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12346 1 0000000000000000 100 0 0 10 0
   2: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12347 1 0000000000000000 100 0 0 10 0
`
	dir := t.TempDir()
	fpath := filepath.Join(dir, "tcp")
	if err := os.WriteFile(fpath, []byte(data), 0o600); err != nil {
		t.Fatalf("writing fake tcp file: %v", err)
	}

	scanner := &ProcNetScanner{ProcNetTCPPath: fpath}
	entries, err := scanner.ListeningPorts()
	if err != nil {
		t.Fatalf("ListeningPorts returned error: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (port 80 filtered), got %d: %v", len(entries), entries)
	}

	// Verify port 80 is absent.
	for _, e := range entries {
		if e.Port < 1024 {
			t.Errorf("privileged port %d leaked through filter", e.Port)
		}
	}

	// Verify port 3000 present.
	found3000 := false
	found8080 := false
	for _, e := range entries {
		if e.Port == 3000 {
			found3000 = true
		}
		if e.Port == 8080 {
			found8080 = true
		}
	}
	if !found3000 {
		t.Error("expected port 3000 in entries")
	}
	if !found8080 {
		t.Error("expected port 8080 in entries")
	}
}
