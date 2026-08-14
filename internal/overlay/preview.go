package overlay

import (
	"fmt"
	"io"
	"sort"
)

// FormatPreview writes a human-readable preview of plan to w. If applied is
// false, the output is labeled as a dry-run. Entries are grouped by sandbox.
func FormatPreview(w io.Writer, plan Plan, applied bool) error {
	if applied {
		if _, err := fmt.Fprintln(w, "Applied changes:"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(w, "DRY RUN — no changes will be written. Pass --apply to execute."); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}

	sandboxNames := make([]string, 0, len(plan.BySandbox))
	for name := range plan.BySandbox {
		sandboxNames = append(sandboxNames, name)
	}
	sort.Strings(sandboxNames)

	for _, name := range sandboxNames {
		ops := plan.BySandbox[name]
		if _, err := fmt.Fprintf(w, "sandbox=%s\n", name); err != nil {
			return err
		}
		for _, op := range ops {
			mark := opMark(op.Kind)
			extra := ""
			if op.SourceLabel != "" && op.Kind != OpDelete {
				extra = fmt.Sprintf("  (%s)", op.SourceLabel)
			}
			sizeStr := ""
			if op.Kind != OpDelete {
				sizeStr = fmt.Sprintf("  [%s]", formatBytes(op.Bytes))
			}
			// A `~` line otherwise reads as a file being rewritten, while the
			// apply removes a host directory and everything under it.
			if op.ReplacesHostDir {
				extra += "  ← replaces a host directory: its contents are deleted"
			}
			if _, err := fmt.Fprintf(w, "  %s %s%s%s\n", mark, op.HostPath, sizeStr, extra); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}

	c, o, d, bytes := plan.Totals()
	if _, err := fmt.Fprintf(w, "Summary: %d create, %d overwrite, %d delete, %s total.\n", c, o, d, formatBytes(bytes)); err != nil {
		return err
	}
	if n := plan.DirReplacements(); n > 0 {
		verb := "deletes"
		if n > 1 {
			verb = "delete"
		}
		if _, err := fmt.Fprintf(w,
			"Warning: %d of those %s a host directory recursively to put a file or symlink in its place.\n",
			n, verb); err != nil {
			return err
		}
	}
	if !applied {
		if _, err := fmt.Fprintln(w, "Re-run with --apply to execute."); err != nil {
			return err
		}
	}
	return nil
}

func opMark(k OpKind) string {
	switch k {
	case OpCreate:
		return "+"
	case OpOverwrite:
		return "~"
	case OpDelete:
		return "-"
	}
	return "?"
}

func formatBytes(n int64) string {
	const (
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/gb)
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/mb)
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/kb)
	}
	return fmt.Sprintf("%d B", n)
}
