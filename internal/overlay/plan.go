package overlay

import (
	"os"
	"time"
)

// OpKind is the kind of filesystem operation in a Plan.
type OpKind int

const (
	OpCreate OpKind = iota
	OpOverwrite
	OpDelete
)

func (k OpKind) String() string {
	switch k {
	case OpCreate:
		return "create"
	case OpOverwrite:
		return "overwrite"
	case OpDelete:
		return "delete"
	}
	return "unknown"
}

// Operation describes a single change to the host filesystem.
type Operation struct {
	Kind        OpKind
	RelPath     string      // path relative to the mount point (host path)
	HostPath    string      // absolute host path (hostMount + RelPath)
	Source      string      // absolute path in the winning upper (empty for OpDelete)
	SourceLabel string      // human-readable label, e.g. "sandbox=wip-graph:primary"
	Bytes       int64       // file size (0 for dirs, 0 for deletes)
	Mode        os.FileMode // for Create/Overwrite
	ModTime     time.Time   // for Create/Overwrite
	IsDir       bool
	IsSymlink   bool
	LinkTarget  string // for symlinks

	// ReplacesHostDir marks an operation whose destination is a host directory
	// the applier has to remove recursively before it can put a file or a
	// symlink there. The preview says so: without it a `~` line reads as an
	// ordinary file overwrite while the apply deletes a whole host subtree,
	// including entries no operation in the plan puts back.
	ReplacesHostDir bool
}

// Plan is the full set of operations to apply, plus per-sandbox grouping
// for preview output.
type Plan struct {
	Operations []Operation
	BySandbox  map[string][]Operation // keyed by sandbox name
	HostPath   string                 // the target host path
}

// DirReplacements counts the operations that delete a host directory
// recursively to put a file or a symlink in its place.
func (p Plan) DirReplacements() int {
	n := 0
	for _, op := range p.Operations {
		if op.ReplacesHostDir {
			n++
		}
	}
	return n
}

// Totals returns aggregated counts and byte total for preview summaries.
func (p Plan) Totals() (create, overwrite, del int, bytes int64) {
	for _, op := range p.Operations {
		switch op.Kind {
		case OpCreate:
			create++
			bytes += op.Bytes
		case OpOverwrite:
			overwrite++
			bytes += op.Bytes
		case OpDelete:
			del++
		}
	}
	return
}
