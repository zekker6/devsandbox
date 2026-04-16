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
	"syscall"
	"time"
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
}

// NewStore creates a Store rooted at dir. The directory must already exist.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// DefaultStore returns a Store rooted at $XDG_STATE_HOME/devsandbox/sessions/
// (falling back to ~/.local/state/devsandbox/sessions/). The directory is
// created if it does not exist.
func DefaultStore() (*Store, error) {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("could not determine home directory: %w", err)
		}
		stateHome = filepath.Join(home, ".local", "state")
	}

	dir := filepath.Join(stateHome, "devsandbox", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create session store directory: %w", err)
	}
	return NewStore(dir), nil
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
		if isAlive(existing.PID) {
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
		if isAlive(sess.PID) {
			live = append(live, sess)
		}
	}
	return live, nil
}

// ListForSandbox returns sessions whose WorkDir or registered worktree path
// is inside sandboxRoot. Used by `sandboxes prune` to find worktrees to
// remove alongside sandbox state. Paths are symlink-resolved before comparison
// so /tmp/x and /private/tmp/x match on macOS.
func (s *Store) ListForSandbox(sandboxRoot string) ([]*Session, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
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
	return out, nil
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

// CleanStale removes all session files whose PID no longer exists.
// Returns the number of files removed.
func (s *Store) CleanStale() int {
	all, err := s.List()
	if err != nil {
		return 0
	}

	removed := 0
	for _, sess := range all {
		if !isAlive(sess.PID) {
			if err := s.Remove(sess.Name); err == nil {
				removed++
			}
		}
	}
	return removed
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

	path := s.filePath(sess.Name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write session %q: %w", sess.Name, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit session %q: %w", sess.Name, err)
	}
	return nil
}

// isAlive reports whether the process with the given PID is alive.
// It uses signal 0 to probe the process without affecting it.
func isAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}
