// Package session manages running sandbox sessions stored as JSON files
// under ~/.local/state/devsandbox/sessions/.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"devsandbox/internal/fsutil"
	"devsandbox/internal/procstate"
)

// Session represents a running sandbox instance.
type Session struct {
	Name           string          `json:"name"`
	PID            int             `json:"pid"`
	NetworkNS      string          `json:"network_ns"`
	StartedAt      time.Time       `json:"started_at"`
	WorkDir        string          `json:"work_dir"`
	ProxyPort      int             `json:"proxy_port,omitempty"`
	ForwardedPorts []ForwardedPort `json:"forwarded_ports,omitempty"`
	Worktree       *WorktreeInfo   `json:"worktree,omitempty"`
}

// WorktreeInfo records the git worktree this session is rooted at, if any.
// When RemoveOnExit is true the session's --rm teardown runs
// `git worktree remove` on Path before deleting sandbox state.
type WorktreeInfo struct {
	Path         string `json:"path"`
	Branch       string `json:"branch"`
	RepoRoot     string `json:"repo_root"`
	RemoveOnExit bool   `json:"remove_on_exit"`
}

// ForwardedPort describes a port forwarding rule active for a session.
type ForwardedPort struct {
	HostPort    int    `json:"host_port"`
	SandboxPort int    `json:"sandbox_port"`
	Bind        string `json:"bind"`
	Protocol    string `json:"protocol"`
}

// Store manages session files in a directory.
type Store struct {
	dir string
	// probe answers what the kernel says about a record's pid. It is a field
	// so a test can pin the uncertain answer: no pid produces it reliably on
	// every host - inside a PID namespace pid 1 is the runner's own init and
	// answers Live - and the uncertain answer is the only one the age backstop
	// acts on.
	probe func(int) procstate.State
}

// NewStore creates a Store rooted at dir. The directory must already exist
// for records to be written; CleanStaleErr reads a missing one as empty.
func NewStore(dir string) *Store {
	return &Store{dir: dir, probe: procstate.Probe}
}

// DefaultDir returns the session store directory:
// $XDG_STATE_HOME/devsandbox/sessions, falling back to
// <homeDir>/.local/state/devsandbox/sessions when XDG_STATE_HOME is unset. It
// does not create the directory.
func DefaultDir(homeDir string) string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(homeDir, ".local", "state")
	}
	return filepath.Join(stateHome, "devsandbox", "sessions")
}

// DefaultStore returns a Store rooted at DefaultDir for the current user. The
// directory is created if it does not exist.
func DefaultStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("could not determine home directory: %w", err)
	}
	dir := DefaultDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create session store directory: %w", err)
	}
	return NewStore(dir), nil
}

// Dir returns the directory the store reads and writes records in.
func (s *Store) Dir() string {
	return s.dir
}

// filePath returns the JSON file path for a session name.
func (s *Store) filePath(name string) string {
	return filepath.Join(s.dir, name+".json")
}

// Register persists a new session. Returns an error if a session with the same
// name already exists and its PID is still alive.
func (s *Store) Register(sess *Session) error {
	existing, err := s.Get(sess.Name)
	if err == nil {
		// File exists; reject only if the existing PID is still alive.
		if procstate.Alive(existing.PID) {
			return fmt.Errorf("session %q is already running (PID %d)", sess.Name, existing.PID)
		}
		// Stale file — overwrite it below.
	}

	return s.write(sess)
}

// Get reads and returns the session with the given name.
// Returns an error wrapping os.ErrNotExist if the session file is missing.
func (s *Store) Get(name string) (*Session, error) {
	data, err := os.ReadFile(s.filePath(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("session %q not found: %w", name, os.ErrNotExist)
		}
		return nil, fmt.Errorf("read session %q: %w", name, err)
	}

	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("parse session %q: %w", name, err)
	}
	return &sess, nil
}

// List returns all sessions in the store, including stale ones.
func (s *Store) List() ([]*Session, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read session store: %w", err)
	}

	var sessions []*Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		sess, err := s.Get(name)
		if err != nil {
			// Skip unreadable or unparseable files.
			continue
		}
		sessions = append(sessions, sess)
	}
	return sessions, nil
}

// ListLive returns only sessions whose PID is still alive.
func (s *Store) ListLive() ([]*Session, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}

	live := make([]*Session, 0, len(all))
	for _, sess := range all {
		if procstate.Alive(sess.PID) {
			live = append(live, sess)
		}
	}
	return live, nil
}

// ListForSandbox returns sessions whose WorkDir or registered worktree path
// is inside sandboxRoot.
func (s *Store) ListForSandbox(sandboxRoot string) ([]*Session, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	return FilterForSandbox(all, sandboxRoot), nil
}

// FilterForSandbox returns the sessions in all whose WorkDir or registered
// worktree path is inside sandboxRoot. Paths are symlink-resolved before
// comparison so /tmp/x and /private/tmp/x match on macOS.
//
// The snapshot lets `sandboxes prune` keep its worktree cleanup candidates
// across sweeps and confirmation. It must not authorize removing session
// records by name, because another launch can reuse a stale session's name.
func FilterForSandbox(all []*Session, sandboxRoot string) []*Session {
	normRoot := resolvePath(sandboxRoot)
	prefix := normRoot + string(os.PathSeparator)
	var out []*Session
	for _, sess := range all {
		normWork := resolvePath(sess.WorkDir)
		if normWork == normRoot || strings.HasPrefix(normWork, prefix) {
			out = append(out, sess)
			continue
		}
		if sess.Worktree != nil {
			normWt := resolvePath(sess.Worktree.Path)
			if normWt == normRoot || strings.HasPrefix(normWt, prefix) {
				out = append(out, sess)
			}
		}
	}
	return out
}

// Update overwrites the session file with the provided data.
func (s *Store) Update(sess *Session) error {
	return s.write(sess)
}

// Remove deletes the session file for the given name.
func (s *Store) Remove(name string) error {
	if err := os.Remove(s.filePath(name)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("session %q not found: %w", name, os.ErrNotExist)
		}
		return fmt.Errorf("remove session %q: %w", name, err)
	}
	return nil
}

// sessionStaleAge bounds how long a record survives once nothing has written
// it. The pid probe answers an uncertain result as alive, so a record whose
// pid was recycled by another user's process is never reclaimed on the pid
// alone - and while it survives, Register refuses its name and AutoName counts
// the name as taken. Thirty days sits far past any plausible session while
// still bounding that. A live session rewrites its record whenever its state
// changes, so the clock runs from the last write, not from the start.
const sessionStaleAge = 30 * 24 * time.Hour

// CleanStale removes stale session files and returns how many it removed. It
// is CleanStaleErr without the error, for callers that only want the count;
// a store that cannot be read counts as nothing removed.
func (s *Store) CleanStale() int {
	removed, _ := s.CleanStaleErr()
	return removed
}

// CleanStaleErr removes session files whose process is gone, or whose process
// is uncertain and file has not been written for sessionStaleAge, and reports
// how many it removed. Records with actionable worktree cleanup information
// are kept until their checkout is absent; a failed stat is reported and the
// record is kept. A missing store directory holds nothing and is not an error; a
// store that cannot be read is. A file that cannot be removed is reported and
// the sweep continues, so one stuck record never hides the rest.
//
// It enumerates the directory itself rather than going through List, which
// skips a record it cannot read or parse. Such a record has no identifiable
// owner, so it is kept - but it is reported, because nothing else would ever
// reclaim it and this location is catalogued as one that is swept.
func (s *Store) CleanStaleErr() (int, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read session store: %w", err)
	}

	cutoff := time.Now().Add(-sessionStaleAge)
	removed := 0
	var errs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		// The name comes from the file, never from the record inside it: the
		// two are written together but nothing enforces that they still agree,
		// and acting on the record's own name would stat and remove a
		// different session's file - reporting it removed while leaving this
		// one to be found again by every later sweep.
		name := strings.TrimSuffix(e.Name(), ".json")
		sess, err := s.Get(name)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// Removed between the listing and this read - every launch and
				// every `devsandbox sessions` sweeps this directory too. The
				// record is gone either way, which is what this sweep wanted,
				// so reporting it fails a prune for someone else's success.
				continue
			}
			errs = append(errs, err)
			continue
		}
		if !s.stale(name, sess, cutoff) {
			continue
		}
		if wt := sess.Worktree; wt != nil && wt.RepoRoot != "" && wt.Path != "" {
			_, err := os.Stat(wt.Path)
			if err == nil {
				continue
			}
			if !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("stat worktree %q for session %q: %w", wt.Path, name, err))
				continue
			}
		}
		if err := s.Remove(name); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// Another cleanup got there first; the record is gone either way.
				continue
			}
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// stale reports whether the record stored under name may be removed: its pid
// is gone, or the pid cannot be inspected and the file has not been written
// since cutoff. A file whose age cannot be read is kept, because an unknown
// age must not authorize a deletion.
//
// The age rule is reached only for a pid the probe cannot resolve - one now
// belonging to another user's process, which is kept forever on the pid alone
// and is the leak the backstop exists for. A pid the kernel confirms is
// running keeps its record whatever its age: the record is rewritten only when
// the session's forwarded ports change, so age says nothing about whether the
// session is still there, and removing it frees the name of a running session
// for the next launch to take.
func (s *Store) stale(name string, sess *Session, cutoff time.Time) bool {
	switch s.probe(sess.PID) {
	case procstate.Dead:
		return true
	case procstate.Live:
		return false
	}
	info, err := os.Stat(s.filePath(name))
	if err != nil {
		return false
	}
	return info.ModTime().Before(cutoff)
}

// AutoName generates a unique session name derived from the basename of workDir.
// If the base name is already taken by a live session, it appends -2, -3, etc.
func (s *Store) AutoName(workDir string) string {
	base := filepath.Base(workDir)

	live, err := s.ListLive()
	if err != nil {
		return base
	}

	taken := make(map[string]bool, len(live))
	for _, sess := range live {
		taken[sess.Name] = true
	}

	if !taken[base] {
		return base
	}

	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if !taken[candidate] {
			return candidate
		}
	}
}

// FindSingle returns the single live session. Returns an error if there are
// zero or more than one live sessions.
func (s *Store) FindSingle() (*Session, error) {
	live, err := s.ListLive()
	if err != nil {
		return nil, err
	}
	switch len(live) {
	case 0:
		return nil, errors.New("no active sandbox sessions found")
	case 1:
		return live[0], nil
	default:
		names := make([]string, len(live))
		for i, sess := range live {
			names[i] = sess.Name
		}
		return nil, fmt.Errorf("multiple active sessions (%s): specify a name", strings.Join(names, ", "))
	}
}

// FindByWorkDir returns all live sessions whose WorkDir refers to the same
// directory as cwd. Both paths are normalized via filepath.EvalSymlinks so
// that /tmp/foo and /private/tmp/foo (or symlink aliases) match. When
// EvalSymlinks fails on either side for a given comparison, that comparison
// falls back to a raw string compare so a single bad path does not cause the
// whole call to error out.
func (s *Store) FindByWorkDir(cwd string) ([]*Session, error) {
	live, err := s.ListLive()
	if err != nil {
		return nil, err
	}

	target := resolvePath(cwd)
	matches := make([]*Session, 0, len(live))
	for _, sess := range live {
		if resolvePath(sess.WorkDir) == target {
			matches = append(matches, sess)
		}
	}
	return matches, nil
}

// resolvePath returns filepath.EvalSymlinks(p) when it succeeds, otherwise p
// unchanged. It is used to canonicalize paths for equality comparisons
// without turning a missing path into a hard error.
func resolvePath(p string) string {
	if p == "" {
		return p
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return resolved
}

// write marshals sess and writes it atomically to the store directory.
func (s *Store) write(sess *Session) error {
	data, err := json.MarshalIndent(sess, "", "\t")
	if err != nil {
		return fmt.Errorf("marshal session %q: %w", sess.Name, err)
	}

	if err := fsutil.WriteFileAtomic(s.filePath(sess.Name), data, 0o600); err != nil {
		return fmt.Errorf("write session %q: %w", sess.Name, err)
	}
	return nil
}
