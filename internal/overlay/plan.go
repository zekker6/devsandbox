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
}

// Plan is the full set of operations to apply, plus per-sandbox grouping
// for preview output.
type Plan struct {
	Operations []Operation
	BySandbox  map[string][]Operation // keyed by sandbox name
	HostPath   string                 // the target host path
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
