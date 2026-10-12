package portforward

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseProcNetTCP(t *testing.T) {
	data := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12346 1 0000000000000000 100 0 0 10 0
   2: 00000000:0050 0100007F:C350 01 00000000:00000000 00:00000000 00000000  1000        0 12347 1 0000000000000000 100 0 0 10 0
`
	data = strings.ReplaceAll(data, "0100007F", procNetHexIP("127.0.0.1"))
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
	data = strings.ReplaceAll(data, "0100007F", procNetHexIP("127.0.0.1"))
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

// procNetHexIP renders each address word as procfs does on the current host.
func procNetHexIP(ip string) string {
	addr := netip.MustParseAddr(ip).AsSlice()
	var result strings.Builder
	for i := 0; i < len(addr); i += 4 {
		fmt.Fprintf(&result, "%08X", binary.NativeEndian.Uint32(addr[i:i+4]))
	}
	return result.String()
}

func TestParseHexAddr(t *testing.T) {
	for _, tt := range []struct {
		name string
		addr string
		want string
	}{
		{name: "IPv4", addr: "127.0.0.1", want: "127.0.0.1"},
		{name: "IPv6 loopback", addr: "::1", want: "::1"},
		{name: "IPv6 wildcard", addr: "::", want: "::"},
		{name: "IPv6 word order", addr: "2001:db8:1234:5678:90ab:cdef:1122:3344", want: "2001:db8:1234:5678:90ab:cdef:1122:3344"},
		{name: "mapped IPv4", addr: "::ffff:127.0.0.1", want: "127.0.0.1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ip, port, err := parseHexAddr(procNetHexIP(tt.addr) + ":1F90")
			if err != nil {
				t.Fatal(err)
			}
			if ip != tt.want || port != 8080 {
				t.Fatalf("got %s:%d, want %s:8080", ip, port, tt.want)
			}
		})
	}
}

func TestParseHexAddr_Malformed(t *testing.T) {
	for _, addr := range []string{
		"00000000",
		"0000000:1F90",
		strings.Repeat("0", 31) + ":1F90",
		strings.Repeat("0", 33) + ":1F90",
		"0000000000000000000000000000000G:1F90",
		"00000000000000000000000000000000:10000",
		"00000000000000000000000000000000:ZZZZ",
	} {
		t.Run(addr, func(t *testing.T) {
			if _, _, err := parseHexAddr(addr); err == nil {
				t.Fatal("expected malformed address error")
			}
		})
	}
}

func TestParseProcNetTCP6(t *testing.T) {
	data := fmt.Sprintf(`sl local_address rem_address st
0: %s:1F90 00000000000000000000000000000000:0000 0A
1: %s:0BB8 00000000000000000000000000000000:0000 0A
2: %s:2328 00000000000000000000000000000000:0000 0A
3: %s:0050 00000000000000000000000000000000:0000 01
`, procNetHexIP("::1"), procNetHexIP("::"), procNetHexIP("::ffff:127.0.0.1"), procNetHexIP("::1"))
	entries, err := parseProcNetTCP(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	want := []ListenEntry{{IP: "::1", Port: 8080}, {IP: "::", Port: 3000}, {IP: "127.0.0.1", Port: 9000}}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("got %v, want %v", entries, want)
	}
}

func TestNewProcNetScanner(t *testing.T) {
	scanner := NewProcNetScanner(12345)
	if scanner.ProcNetTCPPath != "/proc/12345/net/tcp" || scanner.ProcNetTCP6Path != "/proc/12345/net/tcp6" {
		t.Fatalf("unexpected paths: %+v", scanner)
	}
}

func TestProcNetScanner_TCP6(t *testing.T) {
	for _, name := range []string{"both files", "missing tcp6", "open failure", "read failure", "malformed IPv6", "oversized line", "missing tcp"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			tcpPath := filepath.Join(dir, "tcp")
			tcp6Path := filepath.Join(dir, "tcp6")
			if name != "missing tcp" {
				data := fmt.Sprintf("sl local_address rem_address st\n0: %s:1F90 00000000:0000 0A\n", procNetHexIP("127.0.0.1"))
				if err := os.WriteFile(tcpPath, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want := []ListenEntry{{IP: "127.0.0.1", Port: 8080}}
			wantErr := false
			data := fmt.Sprintf("sl local_address rem_address st\n0: %s:0400 0:0 0A\n1: %s:03FF 0:0 0A\n", procNetHexIP("::1"), procNetHexIP("::"))
			switch name {
			case "both files":
				want = append(want, ListenEntry{IP: "::1", Port: 1024})
			case "missing tcp6":
			case "open failure":
				tcp6Path = filepath.Join(tcpPath, "tcp6")
				wantErr = true
			case "read failure":
				if err := os.Mkdir(tcp6Path, 0o700); err != nil {
					t.Fatal(err)
				}
				wantErr = true
			case "malformed IPv6":
				data = "sl local_address rem_address st\n0: 0000000000000000000000000000000G:1F90 0:0 0A\n"
				wantErr = true
			case "oversized line":
				data += strings.Repeat("x", 128*1024)
				wantErr = true
			case "missing tcp":
				wantErr = true
			}
			if name != "missing tcp6" && name != "open failure" && name != "read failure" {
				if err := os.WriteFile(tcp6Path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			scanner := &ProcNetScanner{ProcNetTCPPath: tcpPath, ProcNetTCP6Path: tcp6Path}
			entries, err := scanner.ListeningPorts()
			if wantErr {
				if err == nil || entries != nil {
					t.Fatalf("expected error without partial entries, got %v, %v", entries, err)
				}
				path := tcp6Path
				if name == "missing tcp" {
					path = tcpPath
				}
				if !strings.Contains(err.Error(), path) {
					t.Fatalf("error does not name failing path %q: %v", path, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(entries, want) {
				t.Fatalf("got %v, want %v", entries, want)
			}
		})
	}
}
