package herdrproxy

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"devsandbox/internal/cmdpattern"
)

// Relocator copies a sandbox-supplied launch script to a host-only location
// after validating it, and rewrites the command to point at the copy.
//
// The problem it solves: herdr's `pane run` is invoked as `sh <path>`, so the
// payload is a file rather than an inline command. That file lives in a
// directory the sandbox can write, yet the script executes on the host outside
// the sandbox. Validating the file in place would leave a TOCTOU window — the
// sandbox could rewrite it between the check and herdr's exec.
//
// Instead the bytes are read exactly once, validated in memory, and written to
// a directory only the host can write. The command then names that copy, so
// what was validated and what runs are the same bytes. The sandbox has no way
// to influence the script after validation.
type Relocator struct {
	dir string

	mu      sync.Mutex
	written []string
}

// NewRelocator creates a relocator over dir, which must not be reachable from
// inside the sandbox. forbidden lists sandbox-visible mount paths; construction
// fails if dir is equal to or beneath any of them, because relocating into a
// sandbox-writable directory would defeat the entire mechanism.
func NewRelocator(dir string, forbidden []string) (*Relocator, error) {
	if dir == "" {
		return nil, fmt.Errorf("herdr relocator: directory is empty")
	}
	clean := filepath.Clean(dir)
	if !filepath.IsAbs(clean) {
		return nil, fmt.Errorf("herdr relocator: directory %q is not absolute", dir)
	}
	for _, f := range forbidden {
		if f == "" {
			continue
		}
		fc := filepath.Clean(f)
		if clean == fc || strings.HasPrefix(clean, fc+string(filepath.Separator)) {
			return nil, fmt.Errorf(
				"herdr relocator: directory %q is inside sandbox-visible path %q; "+
					"relocated scripts would remain writable by the sandbox", clean, fc)
		}
	}
	if err := os.MkdirAll(clean, 0o700); err != nil {
		return nil, fmt.Errorf("herdr relocator: create %q: %w", clean, err)
	}
	if err := os.Chmod(clean, 0o700); err != nil {
		return nil, fmt.Errorf("herdr relocator: chmod %q: %w", clean, err)
	}
	return &Relocator{dir: clean}, nil
}

// maxScriptBytes bounds how much a sandbox can make the proxy read and copy.
const maxScriptBytes = 64 << 10

// Relocate validates and relocates the script referenced by text.
//
// text is a shell command line as it would be typed into a pane. When it has
// the form `sh <path>`, the referenced file is read, validated against pat, and
// copied; the returned string names the copy. Any other form is returned
// unchanged with ok=false so the caller falls back to inline argv matching.
//
// An error means the command must be denied: the script was unreadable, too
// large, or did not match the pattern.
func (r *Relocator) Relocate(text string, pat cmdpattern.ScriptPattern) (rewritten string, ok bool, err error) {
	path, isScript := parseShScript(text)
	if !isScript {
		return text, false, nil
	}

	body, err := readScript(path)
	if err != nil {
		return "", true, err
	}
	if !pat.MatchesBody(body) {
		return "", true, fmt.Errorf("launch script does not match the declared pattern")
	}

	dest, err := r.write(body)
	if err != nil {
		return "", true, err
	}
	return "sh " + dest, true, nil
}

// parseShScript recognizes the `sh <path>` form and returns the path. The path
// must be absolute and free of characters the shell would act on, since it is
// re-emitted into a command line.
func parseShScript(text string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) != 2 {
		return "", false
	}
	if fields[0] != "sh" && fields[0] != "/bin/sh" {
		return "", false
	}
	path := unquoteSingle(fields[1])
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", false
	}
	// Reject anything that would change meaning when re-emitted, and any quote
	// the caller might use to escape the argument position.
	if strings.ContainsAny(path, ";&|`$()<>*?[]{}\\\n\r\t\"'") {
		return "", false
	}
	return path, true
}

// unquoteSingle removes one layer of single quotes, which is how the launcher
// shell-quotes the script path before sending `sh '<path>'` to `herdr pane run`.
//
// Only the trivial case is unwrapped: a remainder that still contains a quote
// is left as-is, so shell escaping of an embedded quote never round-trips into
// a path the caller would treat as literal and re-emit unquoted. Such a field
// then fails the absolute-path and metacharacter checks above, as it must.
func unquoteSingle(field string) string {
	if len(field) < 2 || field[0] != '\'' || field[len(field)-1] != '\'' {
		return field
	}
	inner := field[1 : len(field)-1]
	if strings.ContainsRune(inner, '\'') {
		return field
	}
	return inner
}

// readScript reads the script once, refusing symlinks and oversized files.
//
// The read is the single point at which the sandbox's bytes enter the proxy;
// everything downstream operates on the returned slice.
func readScript(path string) ([]byte, error) {
	// O_NOFOLLOW makes the open itself fail with ELOOP on a symlink, so a
	// sandbox cannot point the proxy at a file elsewhere on the host. Checking
	// with Lstat first would leave a window in which the path is swapped
	// between the check and the open; refusing at open time has no such gap.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("read launch script: %w", err)
	}
	defer func() { _ = f.Close() }()

	// Stat the opened descriptor rather than the path, so what is inspected is
	// exactly what was opened.
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat launch script: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("launch script %q is not a regular file", path)
	}
	if info.Size() > maxScriptBytes {
		return nil, fmt.Errorf("launch script %q exceeds %d bytes", path, maxScriptBytes)
	}

	body := make([]byte, info.Size())
	if _, err := readFull(f, body); err != nil {
		return nil, fmt.Errorf("read launch script %q: %w", path, err)
	}
	return body, nil
}

// readFull fills buf from f, tolerating short reads.
func readFull(f *os.File, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := f.Read(buf[total:])
		total += n
		if err != nil {
			if total == len(buf) {
				return total, nil
			}
			return total, err
		}
		if n == 0 {
			break
		}
	}
	return total, nil
}

// write stores body in the host-only directory and returns its path.
func (r *Relocator) write(body []byte) (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("herdr relocator: generate name: %w", err)
	}
	dest := filepath.Join(r.dir, "herdr-launch-"+hex.EncodeToString(suffix[:])+".sh")

	// 0500: readable and executable by the owner, writable by nobody. The
	// script is run as `sh <path>`, so execute permission is not strictly
	// required, but denying write is.
	if err := os.WriteFile(dest, body, 0o500); err != nil {
		return "", fmt.Errorf("herdr relocator: write %q: %w", dest, err)
	}

	r.mu.Lock()
	r.written = append(r.written, dest)
	r.mu.Unlock()

	return dest, nil
}

// Cleanup removes every relocated script and the directory itself.
func (r *Relocator) Cleanup() error {
	r.mu.Lock()
	written := r.written
	r.written = nil
	r.mu.Unlock()

	var firstErr error
	for _, p := range written {
		// Files are 0500; the owner can still unlink them because the
		// containing directory is writable.
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = fmt.Errorf("herdr relocator: remove %q: %w", p, err)
		}
	}
	if err := os.RemoveAll(r.dir); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("herdr relocator: remove dir %q: %w", r.dir, err)
	}
	return firstErr
}
