// Package herdrstate records which devsandbox launch owns a herdr pane, so a
// later `devsandbox run-agent` invocation in that pane can tell whether
// re-entering the sandbox from the current directory would reach the same
// synthetic home the session was created in.
//
// The store is host-owned: it lives under $XDG_STATE_HOME, which is repointed
// at the synthetic home inside the sandbox, so sandboxed code cannot write it.
package herdrstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"devsandbox/internal/fsutil"
	"devsandbox/internal/sandbox"
)

// Version is the schema version of a Record. A record written by a different
// version is rejected rather than migrated.
const Version = 1

// ErrNotFound is returned by Load when no record exists for a pane.
var ErrNotFound = errors.New("herdrstate: no record for pane")

// Record maps a herdr pane to the sandbox launch that owns it.
//
// PaneID is stored as well as hashed into the filename so a caller can confirm
// the record it opened is the one it asked for.
type Record struct {
	Version int    `json:"version"`
	PaneID  string `json:"pane_id"`
	Agent   string `json:"agent"`
	// ProjectDir is the directory the sandbox was launched from. Under
	// --worktree this is the worktree path, not the repo root.
	ProjectDir string `json:"project_dir"`
	// SandboxRoot is the per-project state dir the session used. Under
	// --worktree it is derived from the repo root, so it does not match what
	// a plain re-entry from ProjectDir would derive - which is exactly the
	// case DerivesSameSandboxRoot exists to detect.
	SandboxRoot string    `json:"sandbox_root"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Store holds pane records as JSON files in a directory.
type Store struct {
	dir string
}

// NewStore creates a Store rooted at dir. The directory must already exist.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Dir returns the directory the store writes to.
func (s *Store) Dir() string { return s.dir }

// DefaultStore returns a Store rooted at DefaultDir for the current user,
// creating the directory 0700.
func DefaultStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("could not determine home directory: %w", err)
	}
	dir := DefaultDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create herdr pane store directory: %w", err)
	}
	return NewStore(dir), nil
}

// DefaultDir returns the store directory without creating it:
// $XDG_STATE_HOME/devsandbox/herdr-panes, falling back to
// <homeDir>/.local/state/devsandbox/herdr-panes when XDG_STATE_HOME is unset.
func DefaultDir(homeDir string) string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(homeDir, ".local", "state")
	}
	return filepath.Join(stateHome, "devsandbox", "herdr-panes")
}

// filePath returns the record path for a pane ID. The ID is hashed so an
// opaque, herdr-chosen string can never influence the path.
func (s *Store) filePath(paneID string) string {
	sum := sha256.Sum256([]byte(paneID))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
}

// Save writes rec atomically, so a concurrent Load sees either the old record or
// the new one but never a partial write.
//
// UpdatedAt is stamped when the caller left it zero.
func (s *Store) Save(rec Record) error {
	if rec.PaneID == "" {
		return errors.New("herdrstate: pane ID is required")
	}
	if rec.Agent == "" {
		return errors.New("herdrstate: agent is required")
	}
	if !filepath.IsAbs(rec.ProjectDir) {
		return fmt.Errorf("herdrstate: project dir %q is not absolute", rec.ProjectDir)
	}
	if !filepath.IsAbs(rec.SandboxRoot) {
		return fmt.Errorf("herdrstate: sandbox root %q is not absolute", rec.SandboxRoot)
	}

	rec.Version = Version
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = time.Now().UTC()
	}

	data, err := json.MarshalIndent(rec, "", "\t")
	if err != nil {
		return fmt.Errorf("herdrstate: marshal record: %w", err)
	}

	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("herdrstate: create %s: %w", s.dir, err)
	}

	if err := fsutil.WriteFileAtomic(s.filePath(rec.PaneID), data, 0o600); err != nil {
		return fmt.Errorf("herdrstate: write record: %w", err)
	}
	return nil
}

// Load returns the record for paneID. It reports ErrNotFound when no record
// exists, which is the ordinary case for a pane that never launched an agent.
func (s *Store) Load(paneID string) (Record, error) {
	if paneID == "" {
		return Record{}, fmt.Errorf("%w: empty pane ID", ErrNotFound)
	}
	rec, err := readRecord(s.filePath(paneID))
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, ErrNotFound
	}
	return rec, err
}

// readRecord reads and parses the record at path. A read or parse failure
// names the path: through Load it fails resume closed for the pane, and
// deleting the file is the user's only way out, which they cannot find from
// the pane ID alone; through Prune it is the file the sweep kept. A missing
// file is reported wrapping fs.ErrNotExist.
func readRecord(path string) (Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, fmt.Errorf("herdrstate: read record %s: %w", path, err)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, fmt.Errorf("herdrstate: parse record %s: %w", path, err)
	}
	return rec, nil
}

// Prune removes the records under dir whose sandbox state root no longer
// exists and reports how many it removed. A missing dir is not an error: it
// is the normal state of a host that has never launched an agent from a
// herdr pane.
//
// The root's absence is the only signal. There is no pid to probe - the pane
// outlives the launch by design - and deliberately no age backstop: a record
// is a few hundred bytes, herdr panes routinely stay open for weeks, and
// deleting the record of a live pane silently disables the resume guard in
// run-agent, which reads a missing record as a pane that never launched a
// sandboxed agent. The launch creates the sandbox root before it writes the
// record, so a record never names a root that does not exist yet.
//
// A record the sweep cannot interpret - unreadable, not JSON, another schema
// version, a non-absolute root - is kept and reported with its path, as is
// one whose root cannot be checked for any reason other than not existing:
// an unknown answer must never authorize a deletion. A removal failure is
// reported and the sweep continues, so one stuck entry never hides the rest.
func Prune(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("herdrstate: read %s: %w", dir, err)
	}
	removed := 0
	var errs []error
	for _, e := range entries {
		if !e.Type().IsRegular() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		orphaned, err := recordOrphaned(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !orphaned {
			continue
		}
		if err := os.Remove(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Another prune got there first; the record is gone either way.
				continue
			}
			errs = append(errs, fmt.Errorf("herdrstate: remove record %s: %w", path, err))
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// recordOrphaned reports whether the record at path may be removed: it is a
// record of this schema naming an absolute sandbox root, and that root does
// not exist. A file that vanished since it was listed is neither an error nor
// an orphan.
func recordOrphaned(path string) (bool, error) {
	rec, err := readRecord(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if rec.Version != Version {
		return false, fmt.Errorf("herdrstate: record %s: version %d, want %d", path, rec.Version, Version)
	}
	if !filepath.IsAbs(rec.SandboxRoot) {
		return false, fmt.Errorf("herdrstate: record %s: sandbox root %q is not absolute", path, rec.SandboxRoot)
	}
	_, err = os.Stat(rec.SandboxRoot)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	return false, fmt.Errorf("herdrstate: record %s: sandbox root %q: %w", path, rec.SandboxRoot, err)
}

// Validate reports whether rec can be trusted to describe the caller's pane and
// agent. A record that fails validation is treated as absent by the caller.
func Validate(rec Record, paneID, agent string) error {
	if rec.Version != Version {
		return fmt.Errorf("herdrstate: record version %d, want %d", rec.Version, Version)
	}
	if paneID == "" || rec.PaneID != paneID {
		return fmt.Errorf("herdrstate: record pane %q does not match %q", rec.PaneID, paneID)
	}
	if agent == "" || rec.Agent != agent {
		return fmt.Errorf("herdrstate: record agent %q does not match %q", rec.Agent, agent)
	}
	if !filepath.IsAbs(rec.ProjectDir) {
		return fmt.Errorf("herdrstate: record project dir %q is not absolute", rec.ProjectDir)
	}
	if !filepath.IsAbs(rec.SandboxRoot) {
		return fmt.Errorf("herdrstate: record sandbox root %q is not absolute", rec.SandboxRoot)
	}
	info, err := os.Stat(rec.ProjectDir)
	if err != nil {
		return fmt.Errorf("herdrstate: record project dir %q: %w", rec.ProjectDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("herdrstate: record project dir %q is not a directory", rec.ProjectDir)
	}
	return nil
}

// DerivesSameSandboxRoot reports whether re-entering the sandbox from cwd would
// land in the sandbox root the record was written with. It is false for a
// session launched with --worktree, where the root is derived from the repo
// root while the project dir is the worktree path.
//
// The base directory is taken from the record rather than recomputed, so a
// session launched with a custom sandbox base path still compares correctly.
func DerivesSameSandboxRoot(rec Record, cwd string) bool {
	if !filepath.IsAbs(rec.SandboxRoot) || !filepath.IsAbs(cwd) {
		return false
	}
	root := filepath.Clean(rec.SandboxRoot)
	derived := filepath.Join(filepath.Dir(root), sandbox.GenerateSandboxName(filepath.Clean(cwd)))
	return derived == root
}
