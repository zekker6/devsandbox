package overlay

import (
	"bytes"
	"strings"
	"testing"
)

func TestFormatPreview_BasicCounts(t *testing.T) {
	plan := Plan{
		HostPath: "/home/zekker/.claude/projects",
		Operations: []Operation{
			{Kind: OpCreate, RelPath: "a.jsonl", HostPath: "/home/zekker/.claude/projects/a.jsonl", Bytes: 100, SourceLabel: "s1:primary"},
			{Kind: OpOverwrite, RelPath: "m.md", HostPath: "/home/zekker/.claude/projects/m.md", Bytes: 50, SourceLabel: "s1:session/abc"},
			{Kind: OpDelete, RelPath: "gone.txt", HostPath: "/home/zekker/.claude/projects/gone.txt", SourceLabel: "s1:primary"},
		},
		BySandbox: map[string][]Operation{
			"s1": {
				{Kind: OpCreate, RelPath: "a.jsonl", HostPath: "/home/zekker/.claude/projects/a.jsonl", SourceLabel: "s1:primary", Bytes: 100},
				{Kind: OpOverwrite, RelPath: "m.md", HostPath: "/home/zekker/.claude/projects/m.md", SourceLabel: "s1:session/abc", Bytes: 50},
				{Kind: OpDelete, RelPath: "gone.txt", HostPath: "/home/zekker/.claude/projects/gone.txt", SourceLabel: "s1:primary"},
			},
		},
	}
	var buf bytes.Buffer
	if err := FormatPreview(&buf, plan, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "+ /home/zekker/.claude/projects/a.jsonl") {
		t.Errorf("missing create line:\n%s", out)
	}
	if !strings.Contains(out, "~ /home/zekker/.claude/projects/m.md") {
		t.Errorf("missing overwrite line:\n%s", out)
	}
	if !strings.Contains(out, "- /home/zekker/.claude/projects/gone.txt") {
		t.Errorf("missing delete line:\n%s", out)
	}
	if !strings.Contains(out, "1 create, 1 overwrite, 1 delete") {
		t.Errorf("missing summary:\n%s", out)
	}
	if !strings.Contains(out, "DRY RUN") {
		t.Errorf("missing dry-run label:\n%s", out)
	}
}

func TestFormatPreview_AppliedLabel(t *testing.T) {
	var buf bytes.Buffer
	if err := FormatPreview(&buf, Plan{BySandbox: map[string][]Operation{}}, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Applied changes:") {
		t.Errorf("expected 'Applied changes:' label, got:\n%s", buf.String())
	}
}
