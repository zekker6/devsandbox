package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"devsandbox/internal/fsutil"
)

const MetadataFile = "metadata.json"

// IsolationType represents the isolation backend used for a sandbox
type IsolationType string

const (
	IsolationBwrap  IsolationType = "bwrap"
	IsolationDocker IsolationType = "docker"
	IsolationKrun   IsolationType = "krun"
)

// Metadata stores information about a sandbox instance
type Metadata struct {
	Name       string        `json:"name"`
	ProjectDir string        `json:"project_dir"`
	CreatedAt  time.Time     `json:"created_at"`
	LastUsed   time.Time     `json:"last_used"`
	Shell      Shell         `json:"shell"`
	Isolation  IsolationType `json:"isolation,omitempty"`
	// LastOOM records OOM kills observed in the current or most recent session,
	// and is cleared when a new session starts. Nil means none were observed —
	// which is not the same as none having happened, because a sandbox can only
	// be monitored when it runs in a cgroup of its own. See RecordOOM.
	LastOOM *OOMRecord `json:"last_oom,omitempty"`
	// Computed fields (not persisted)
	SandboxRoot string `json:"-"`
	SizeBytes   int64  `json:"-"`
	Orphaned    bool   `json:"-"` // True if project_dir no longer exists
	Active      bool   `json:"-"` // Session currently running (lock held)
	State       string `json:"-"` // For Docker: "running", "stopped", "exited"
}

// OOMRecord is the OOM activity observed for a sandbox session. It exists so an
// OOM kill leaves a trace the user can find after the fact: the killed sandbox
// cannot report anything itself, and its session file is removed on exit.
type OOMRecord struct {
	// At is when the kill was observed.
	At time.Time `json:"at"`
	// Kills is how many processes in the sandbox an OOM killer has killed during
	// the session.
	Kills int `json:"kills"`
	// Fatal reports that the sandbox itself was killed, rather than a process
	// inside it while the sandbox carried on.
	Fatal bool `json:"fatal"`
}

// Status renders the record for a listing's status column.
func (r *OOMRecord) Status() string {
	if r == nil {
		return ""
	}
	if r.Fatal {
		return "oom-killed"
	}
	return fmt.Sprintf("oom-kills(%d)", r.Kills)
}

// SaveMetadata writes metadata to the sandbox directory.
//
// The write is atomic because the file now has a mid-session writer (RecordOOM) as
// well as a start-of-session one. See fsutil.WriteFileAtomic for why a torn write
// here is worse than a failed one.
func SaveMetadata(m *Metadata, sandboxRoot string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	if err := fsutil.WriteFileAtomic(filepath.Join(sandboxRoot, MetadataFile), data, 0o644); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	return nil
}

// RecordOOM persists an OOM observation for the sandbox rooted at sandboxRoot,
// so `sandboxes list` can report it both while the session runs and after a
// sandbox that was killed outright is gone.
//
// kills is cumulative for the session and fatal reports whether the sandbox
// itself died, so repeated calls for the same session overwrite rather than
// accumulate. A fatal record is never downgraded: the final observation of a
// killed sandbox arrives after the live one that saw the same kill.
func RecordOOM(sandboxRoot string, kills int, fatal bool, at time.Time) error {
	m, err := LoadMetadata(sandboxRoot)
	if err != nil {
		return fmt.Errorf("record OOM kill: %w", err)
	}
	if m.LastOOM != nil && m.LastOOM.Fatal {
		fatal = true
	}
	m.LastOOM = &OOMRecord{At: at, Kills: kills, Fatal: fatal}
	return SaveMetadata(m, sandboxRoot)
}

// LoadMetadata reads metadata from a sandbox directory
func LoadMetadata(sandboxRoot string) (*Metadata, error) {
	path := filepath.Join(sandboxRoot, MetadataFile)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	var m Metadata
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	m.SandboxRoot = sandboxRoot

	// Check if project directory still exists
	if _, err := os.Stat(m.ProjectDir); errors.Is(err, fs.ErrNotExist) {
		m.Orphaned = true
	}

	return &m, nil
}

// CreateMetadata creates initial metadata for a new sandbox
func CreateMetadata(cfg *Config) *Metadata {
	now := time.Now()
	isolation := cfg.Isolation
	if isolation == "" {
		isolation = IsolationBwrap // Default
	}
	return &Metadata{
		Name:        cfg.ProjectName,
		ProjectDir:  cfg.ProjectDir,
		CreatedAt:   now,
		LastUsed:    now,
		Shell:       cfg.Shell,
		Isolation:   isolation,
		SandboxRoot: cfg.SandboxRoot,
	}
}

// UpdateLastUsed updates the last_used timestamp
func (m *Metadata) UpdateLastUsed() error {
	m.LastUsed = time.Now()
	return SaveMetadata(m, m.SandboxRoot)
}

// ListSandboxes returns all sandboxes in the base directory
func ListSandboxes(baseDir string) ([]*Metadata, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // No sandboxes yet
		}
		return nil, fmt.Errorf("failed to read sandbox directory: %w", err)
	}

	var sandboxes []*Metadata

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		sandboxRoot := filepath.Join(baseDir, entry.Name())
		m, err := LoadMetadata(sandboxRoot)
		if err != nil {
			// Create metadata from directory info if missing
			m = createMetadataFromDir(sandboxRoot, entry.Name())
		}

		sandboxes = append(sandboxes, m)
	}

	return sandboxes, nil
}

// createMetadataFromDir creates metadata for a sandbox without metadata.json
func createMetadataFromDir(sandboxRoot, name string) *Metadata {
	info, err := os.Stat(sandboxRoot)
	var createdAt time.Time
	if err == nil {
		createdAt = info.ModTime()
	} else {
		createdAt = time.Now()
	}

	return &Metadata{
		Name:        name,
		ProjectDir:  "(unknown)",
		CreatedAt:   createdAt,
		LastUsed:    createdAt,
		Shell:       ShellBash,
		SandboxRoot: sandboxRoot,
		Orphaned:    true,
	}
}

// GetSandboxSize calculates the total size of a sandbox directory
func GetSandboxSize(sandboxRoot string) (int64, error) {
	var size int64

	err := filepath.WalkDir(sandboxRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip inaccessible files
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err == nil {
				size += info.Size()
			}
		}
		return nil
	})

	return size, err
}

// SortBy defines sorting options for sandbox list
type SortBy string

const (
	SortByName    SortBy = "name"
	SortByCreated SortBy = "created"
	SortByUsed    SortBy = "used"
	SortBySize    SortBy = "size"
)

// SortSandboxes sorts sandboxes by the specified field
func SortSandboxes(sandboxes []*Metadata, by SortBy) {
	sort.Slice(sandboxes, func(i, j int) bool {
		switch by {
		case SortByCreated:
			return sandboxes[i].CreatedAt.Before(sandboxes[j].CreatedAt)
		case SortByUsed:
			return sandboxes[i].LastUsed.Before(sandboxes[j].LastUsed)
		case SortBySize:
			return sandboxes[i].SizeBytes < sandboxes[j].SizeBytes
		default: // SortByName
			return sandboxes[i].Name < sandboxes[j].Name
		}
	})
}

// PruneOptions configures sandbox pruning behavior
type PruneOptions struct {
	All       bool          // Remove all sandboxes
	Keep      int           // Keep N most recently used sandboxes
	OlderThan time.Duration // Remove sandboxes not used in this duration
	Orphaned  bool          // Restrict pruning to orphaned sandboxes only
	DryRun    bool          // Don't actually remove, just report
}

// SelectForPruning returns sandboxes that should be pruned based on options
func SelectForPruning(sandboxes []*Metadata, opts PruneOptions) []*Metadata {
	if len(sandboxes) == 0 {
		return nil
	}

	// Sort by last used (most recent first)
	sorted := make([]*Metadata, len(sandboxes))
	copy(sorted, sandboxes)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].LastUsed.After(sorted[j].LastUsed)
	})

	var toPrune []*Metadata
	cutoff := time.Now().Add(-opts.OlderThan)

	for i, m := range sorted {
		// Skip active sessions (lock held)
		if m.Active {
			continue
		}

		// --orphaned restricts the candidate set across all other selectors.
		if opts.Orphaned && !m.Orphaned {
			continue
		}

		// If --all (or --orphaned alone), include everything that survived
		// the filters above.
		if opts.All || (opts.Orphaned && opts.Keep == 0 && opts.OlderThan == 0) {
			toPrune = append(toPrune, m)
			continue
		}

		// Keep N most recently used
		if opts.Keep > 0 && i < opts.Keep {
			continue
		}

		// Filter by age if specified
		if opts.OlderThan > 0 && m.LastUsed.After(cutoff) {
			continue
		}

		// If neither --keep nor --older-than specified, only prune orphaned
		if opts.Keep == 0 && opts.OlderThan == 0 && !m.Orphaned {
			continue
		}

		toPrune = append(toPrune, m)
	}

	return toPrune
}

// RemoveSandbox deletes a sandbox directory.
//
// Sandboxes can contain Go module caches ($GOPATH/pkg/mod), which lay down
// directories with mode 0555. A plain os.RemoveAll fails on such trees with
// "permission denied" on unlinkat, because removing a directory entry needs
// write permission on the *parent* directory. fsutil.RemoveAllForce walks the
// tree first and chmods every directory writable before removal.
func RemoveSandbox(sandboxRoot string) error {
	return fsutil.RemoveAllForce(sandboxRoot)
}

// FormatSize formats bytes as human-readable string
func FormatSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/GB)
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/MB)
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/KB)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// ListAllSandboxes returns all sandboxes (both bwrap and docker)
func ListAllSandboxes(baseDir string) ([]*Metadata, error) {
	// Get bwrap sandboxes
	bwrapSandboxes, err := ListSandboxes(baseDir)
	if err != nil {
		return nil, err
	}

	// Set isolation type for bwrap sandboxes
	for _, m := range bwrapSandboxes {
		if m.Isolation == "" {
			m.Isolation = IsolationBwrap
		}
	}

	// Get docker sandboxes
	dockerSandboxes, err := ListDockerSandboxes()
	if err != nil {
		// Don't fail if docker is not available, just skip
		return bwrapSandboxes, nil
	}

	return append(bwrapSandboxes, dockerSandboxes...), nil
}
