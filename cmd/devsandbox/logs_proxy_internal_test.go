package main

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"devsandbox/internal/proxy"
)

func writeGz(t *testing.T, path, content string) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestReadProxyInternalLogs_ReadsActiveAndArchived is the regression for a
// reader that could never return anything: the writer named its active,
// uncompressed file `.log.gz`, and this reader globbed that one name and opened
// it with a gzip reader, then discarded the resulting error with `continue`. No
// entries, no warning, including for the lifecycle lines the proxy's own
// comments tell the user to read this way.
func TestReadProxyInternalLogs_ReadsActiveAndArchived(t *testing.T) {
	dir := t.TempDir()

	active := filepath.Join(dir, proxy.ProxyLogPrefix+"_20260814_0001"+proxy.ProxyLogSuffix)
	if err := os.WriteFile(active, []byte("2026/08/14 10:00:00 active line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archived := filepath.Join(dir, proxy.ProxyLogPrefix+"_20260814_0000"+proxy.ProxyLogArchiveSuffix)
	writeGz(t, archived, "2026/08/14 09:00:00 archived line\n")

	lines, err := readProxyInternalLogs(dir, time.Time{})
	if err != nil {
		t.Fatalf("readProxyInternalLogs: %v", err)
	}
	for _, want := range []string{"2026/08/14 09:00:00 archived line", "2026/08/14 10:00:00 active line"} {
		if !slices.Contains(lines, want) {
			t.Errorf("missing %q; got %v", want, lines)
		}
	}
}

// TestReadProxyInternalLogs_ReadsMislabeledPlainArchive covers what is already
// on disk from the broken writer: plain text under a `.log.gz` name. The
// encoding is sniffed rather than taken from the name, so those files still
// read.
func TestReadProxyInternalLogs_ReadsMislabeledPlainArchive(t *testing.T) {
	dir := t.TempDir()
	mislabeled := filepath.Join(dir, proxy.ProxyLogPrefix+"_20260814_0000"+proxy.ProxyLogArchiveSuffix)
	if err := os.WriteFile(mislabeled, []byte("2026/08/14 09:00:00 legacy line\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	lines, err := readProxyInternalLogs(dir, time.Time{})
	if err != nil {
		t.Fatalf("readProxyInternalLogs: %v", err)
	}
	if !slices.Contains(lines, "2026/08/14 09:00:00 legacy line") {
		t.Errorf("plain text under a .gz name was not read; got %v", lines)
	}
}

func TestReadProxyInternalLogs_FiltersBySince(t *testing.T) {
	dir := t.TempDir()
	active := filepath.Join(dir, proxy.ProxyLogPrefix+"_20260814_0000"+proxy.ProxyLogSuffix)
	body := "2026/08/14 09:00:00 old line\n2026/08/14 11:00:00 new line\n"
	if err := os.WriteFile(active, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	since := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	lines, err := readProxyInternalLogs(dir, since)
	if err != nil {
		t.Fatalf("readProxyInternalLogs: %v", err)
	}
	if slices.Contains(lines, "2026/08/14 09:00:00 old line") {
		t.Errorf("line before since was kept; got %v", lines)
	}
	if !slices.Contains(lines, "2026/08/14 11:00:00 new line") {
		t.Errorf("line after since was dropped; got %v", lines)
	}
}
