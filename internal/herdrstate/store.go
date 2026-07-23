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
	"os"
	"path/filepath"
	"time"

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

// DefaultStore returns a Store rooted at $XDG_STATE_HOME/devsandbox/herdr-panes
// (falling back to ~/.local/state/devsandbox/herdr-panes), creating it 0700.
func DefaultStore() (*Store, error) {
	dir, err := DefaultDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create herdr pane store directory: %w", err)
	}
	return NewStore(dir), nil
}

// DefaultDir returns the store directory without creating it.
func DefaultDir() (string, error) {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not determine home directory: %w", err)
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "devsandbox", "herdr-panes"), nil
}

// filePath returns the record path for a pane ID. The ID is hashed so an
// opaque, herdr-chosen string can never influence the path.
func (s *Store) filePath(paneID string) string {
	sum := sha256.Sum256([]byte(paneID))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
}

// Save writes rec atomically: a temp file in the same directory, fsync'd, then
// renamed over the destination, so a concurrent Load sees either the old
// record or the new one but never a partial write.
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

	tmp, err := os.CreateTemp(s.dir, ".pane-*.tmp")
	if err != nil {
		return fmt.Errorf("herdrstate: create temp record: %w", err)
	}
	tmpPath := tmp.Name()
	if err := writeAndSync(tmp, data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("herdrstate: write record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("herdrstate: close record: %w", err)
	}
	if err := os.Rename(tmpPath, s.filePath(rec.PaneID)); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("herdrstate: commit record: %w", err)
	}
	return nil
}

func writeAndSync(f *os.File, data []byte) error {
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

// Load returns the record for paneID. It reports ErrNotFound when no record
// exists, which is the ordinary case for a pane that never launched an agent.
func (s *Store) Load(paneID string) (Record, error) {
	if paneID == "" {
		return Record{}, fmt.Errorf("%w: empty pane ID", ErrNotFound)
	}
	path := s.filePath(paneID)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Record{}, ErrNotFound
		}
		return Record{}, fmt.Errorf("herdrstate: read record %s: %w", path, err)
	}

	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		// A read or parse failure fails resume closed for this pane, so the
		// path is part of the error: deleting the file is the user's only way
		// out, and they cannot find it from the pane ID alone.
		return Record{}, fmt.Errorf("herdrstate: parse record %s: %w", path, err)
	}
	return rec, nil
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
