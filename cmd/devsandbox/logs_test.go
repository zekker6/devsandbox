package main

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devsandbox/internal/proxy"
)

// writeGzipLines writes lines as one gzip member and returns the compressed bytes.
func writeGzipLines(t *testing.T, lines ...string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	for _, line := range lines {
		if _, err := gz.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write gzip line: %v", err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return buf.Bytes()
}

func writeArchive(t *testing.T, data []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "proxy-requests_0001.jsonl.gz")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	return path
}

// readArchiveWithDeadline runs the reader on its own goroutine so a regression
// that spins forever fails the test instead of hanging the whole run.
func readArchiveWithDeadline(t *testing.T, path string, limit int) []proxy.RequestLog {
	t.Helper()

	type result struct {
		entries []proxy.RequestLog
		err     error
	}
	done := make(chan result, 1)
	go func() {
		entries, _, err := readCompressedProxyLogFile(path, limit)
		done <- result{entries: entries, err: err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("readCompressedProxyLogFile: %v", res.err)
		}
		return res.entries
	case <-time.After(10 * time.Second):
		t.Fatal("readCompressedProxyLogFile did not terminate - a latched decode error is being retried forever")
		return nil
	}
}

func methodsOf(entries []proxy.RequestLog) []string {
	methods := make([]string, len(entries))
	for i, e := range entries {
		methods[i] = e.Method
	}
	return methods
}

// A syntax error is the one decode failure encoding/json latches: the decoder
// stores it and returns it on every subsequent Decode without consuming input,
// so the old "skip malformed entries but continue" loop never advanced. One
// corrupt record in an archive hung `devsandbox logs proxy` at 100% CPU.
func TestReadCompressedProxyLogFile_SyntaxErrorTerminates(t *testing.T) {
	path := writeArchive(t, writeGzipLines(t,
		`{"ts":"2026-08-14T10:00:00Z","method":"GET","url":"https://example.com/one"}`,
		`{"ts":"2026-08-14T10:00:01Z","method":@@@}`,
		`{"ts":"2026-08-14T10:00:02Z","method":"PUT","url":"https://example.com/three"}`,
	))

	entries := readArchiveWithDeadline(t, path, 0)

	if got, want := methodsOf(entries), []string{"GET", "PUT"}; !equalStrings(got, want) {
		t.Fatalf("entries = %v, want %v - the corrupt record must cost only itself", got, want)
	}
}

// Corruption at the very start of the stream must terminate too: there is no
// readable entry to return, but the reader still has to come back.
func TestReadCompressedProxyLogFile_LeadingSyntaxErrorTerminates(t *testing.T) {
	path := writeArchive(t, writeGzipLines(t,
		`}{ not json at all`,
		`{"ts":"2026-08-14T10:00:02Z","method":"GET","url":"https://example.com/one"}`,
	))

	entries := readArchiveWithDeadline(t, path, 0)

	if got, want := methodsOf(entries), []string{"GET"}; !equalStrings(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
}

// A wrong-typed field does not latch, so this case survived before the fix. It
// stays covered so the line-based rewrite keeps skipping the record rather than
// recording a half-populated entry.
func TestReadCompressedProxyLogFile_TypeErrorSkipped(t *testing.T) {
	path := writeArchive(t, writeGzipLines(t,
		`{"ts":"2026-08-14T10:00:00Z","method":"GET","url":"https://example.com/one"}`,
		`{"ts":"2026-08-14T10:00:01Z","method":42,"url":"https://example.com/two"}`,
		`{"ts":"2026-08-14T10:00:02Z","method":"PUT","url":"https://example.com/three"}`,
	))

	entries := readArchiveWithDeadline(t, path, 0)

	if got, want := methodsOf(entries), []string{"GET", "PUT"}; !equalStrings(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
}

// A writer killed mid-rotation leaves an archive without its trailer. The
// entries already flushed must still be readable, and the missing trailer must
// not surface as an error.
func TestReadCompressedProxyLogFile_TruncatedStream(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	for _, method := range []string{"GET", "POST", "PUT"} {
		if _, err := gz.Write([]byte(`{"ts":"2026-08-14T10:00:00Z","method":"` + method + `"}` + "\n")); err != nil {
			t.Fatalf("write gzip line: %v", err)
		}
		if err := gz.Flush(); err != nil {
			t.Fatalf("flush gzip: %v", err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}

	// Drop the gzip trailer so the stream ends without its CRC and length.
	truncated := buf.Bytes()[:buf.Len()-8]
	path := writeArchive(t, truncated)

	entries := readArchiveWithDeadline(t, path, 0)

	if got, want := methodsOf(entries), []string{"GET", "POST", "PUT"}; !equalStrings(got, want) {
		t.Fatalf("entries = %v, want %v - flushed records must survive a missing trailer", got, want)
	}
}

// Rotation appends gzip members, so an archive can hold more than one.
func TestReadCompressedProxyLogFile_ConcatenatedMembers(t *testing.T) {
	first := writeGzipLines(t, `{"ts":"2026-08-14T10:00:00Z","method":"GET"}`)
	second := writeGzipLines(t, `{"ts":"2026-08-14T10:00:01Z","method":"POST"}`)
	path := writeArchive(t, append(first, second...))

	entries := readArchiveWithDeadline(t, path, 0)

	if got, want := methodsOf(entries), []string{"GET", "POST"}; !equalStrings(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
}

// The limit is a sliding window over the whole archive, keeping the newest
// entries. The rewrite must not change which end it keeps.
func TestReadCompressedProxyLogFile_LimitKeepsNewest(t *testing.T) {
	lines := make([]string, 0, 10)
	methods := []string{"M0", "M1", "M2", "M3", "M4", "M5", "M6", "M7", "M8", "M9"}
	for _, m := range methods {
		lines = append(lines, `{"ts":"2026-08-14T10:00:00Z","method":"`+m+`"}`)
	}
	path := writeArchive(t, writeGzipLines(t, lines...))

	if got, want := methodsOf(readArchiveWithDeadline(t, path, 3)), []string{"M7", "M8", "M9"}; !equalStrings(got, want) {
		t.Fatalf("limit 3 = %v, want %v", got, want)
	}
	if got := readArchiveWithDeadline(t, path, 0); len(got) != len(methods) {
		t.Fatalf("limit 0 returned %d entries, want %d", len(got), len(methods))
	}
	if got, want := methodsOf(readArchiveWithDeadline(t, path, 20)), methods; !equalStrings(got, want) {
		t.Fatalf("limit above the entry count = %v, want %v", got, want)
	}
}

// An entry carries captured request and response bodies, which the proxy bounds
// at 256KiB each by default - well past bufio.Scanner's 64KiB default token
// size. A line-based reader that kept that default would silently stop at the
// first such record.
func TestReadCompressedProxyLogFile_LongEntry(t *testing.T) {
	body := strings.Repeat("a", 300*1024)
	path := writeArchive(t, writeGzipLines(t,
		`{"ts":"2026-08-14T10:00:00Z","method":"GET","url":"https://example.com/`+body+`"}`,
		`{"ts":"2026-08-14T10:00:01Z","method":"POST"}`,
	))

	entries := readArchiveWithDeadline(t, path, 0)

	if got, want := methodsOf(entries), []string{"GET", "POST"}; !equalStrings(got, want) {
		t.Fatalf("entries = %v, want %v - a long record must not end the scan", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// oversizedEntry builds a JSON log line longer than proxyLogMaxLineBytes, which
// is what a proxy.max_log_body_bytes near the reader's own bound produces.
func oversizedEntry(t *testing.T) string {
	t.Helper()
	return `{"ts":"2026-08-14T10:00:02Z","method":"POST","request_body":"` +
		strings.Repeat("A", proxyLogMaxLineBytes) + `"}`
}

// TestReadCompressedProxyLogFile_SkipsOversizedLine pins what an oversized
// record costs: itself, and nothing else. Response headers are attacker-chosen
// - the sandbox picks the upstream - so one such record used to end the scan
// and drop every later entry in the archive.
func TestReadCompressedProxyLogFile_SkipsOversizedLine(t *testing.T) {
	path := writeArchive(t, writeGzipLines(t,
		`{"ts":"2026-08-14T10:00:00Z","method":"GET"}`,
		oversizedEntry(t),
		`{"ts":"2026-08-14T10:00:03Z","method":"PUT"}`,
	))

	entries, oversized, err := readCompressedProxyLogFile(path, 0)
	if err != nil {
		t.Fatalf("readCompressedProxyLogFile: %v", err)
	}
	if oversized != 1 {
		t.Errorf("oversized = %d, want 1 - the skip must be reported, not silent", oversized)
	}
	if got, want := methodsOf(entries), []string{"GET", "PUT"}; !equalStrings(got, want) {
		t.Errorf("entries = %v, want %v - entries after an oversized record were dropped", got, want)
	}
}

// The uncompressed reader is the other half of the same contract.
func TestReadUncompressedProxyLogFile_SkipsOversizedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy-requests_0001.jsonl")
	content := `{"ts":"2026-08-14T10:00:00Z","method":"GET"}` + "\n" +
		oversizedEntry(t) + "\n" +
		`{"ts":"2026-08-14T10:00:03Z","method":"PUT"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	entries, oversized, err := readUncompressedProxyLogFile(path, 0)
	if err != nil {
		t.Fatalf("readUncompressedProxyLogFile: %v", err)
	}
	if oversized != 1 {
		t.Errorf("oversized = %d, want 1", oversized)
	}
	if got, want := methodsOf(entries), []string{"GET", "PUT"}; !equalStrings(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

// A record longer than the reader's buffer but inside the line bound must come
// back whole: readBoundedLine reassembles it across ReadSlice calls.
func TestReadUncompressedProxyLogFile_LongEntryReassembled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy-requests_0001.jsonl")
	url := "https://example.com/" + strings.Repeat("a", 300*1024)
	content := `{"ts":"2026-08-14T10:00:00Z","method":"GET","url":"` + url + `"}` + "\n" +
		`{"ts":"2026-08-14T10:00:01Z","method":"POST"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	entries, oversized, err := readUncompressedProxyLogFile(path, 0)
	if err != nil {
		t.Fatalf("readUncompressedProxyLogFile: %v", err)
	}
	if oversized != 0 {
		t.Errorf("oversized = %d, want 0", oversized)
	}
	if got, want := methodsOf(entries), []string{"GET", "POST"}; !equalStrings(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	if entries[0].URL != url {
		t.Errorf("URL was not reassembled: got %d bytes, want %d", len(entries[0].URL), len(url))
	}
}

// Consecutive oversized records must each cost only themselves, including one
// that ends the file without a trailing newline.
func TestReadUncompressedProxyLogFile_ConsecutiveOversizedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy-requests_0001.jsonl")
	content := oversizedEntry(t) + "\n" +
		oversizedEntry(t) + "\n" +
		`{"ts":"2026-08-14T10:00:03Z","method":"PUT"}` + "\n" +
		oversizedEntry(t)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	entries, oversized, err := readUncompressedProxyLogFile(path, 0)
	if err != nil {
		t.Fatalf("readUncompressedProxyLogFile: %v", err)
	}
	if oversized != 3 {
		t.Errorf("oversized = %d, want 3", oversized)
	}
	if got, want := methodsOf(entries), []string{"PUT"}; !equalStrings(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

// A well-formed file must still read clean through the shared scanner.
func TestReadUncompressedProxyLogFile_ReadsAllEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy-requests_0001.jsonl")
	content := `{"ts":"2026-08-14T10:00:00Z","method":"GET"}` + "\n" +
		`not json at all` + "\n" +
		`{"ts":"2026-08-14T10:00:01Z","method":"POST"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	entries, _, err := readUncompressedProxyLogFile(path, 0)
	if err != nil {
		t.Fatalf("readUncompressedProxyLogFile: %v", err)
	}
	// A corrupt record costs only itself.
	if got, want := methodsOf(entries), []string{"GET", "POST"}; !equalStrings(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
}
