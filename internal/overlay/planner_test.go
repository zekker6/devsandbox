package overlay

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile is a small helper — the test uses it often.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildPlan_SingleUpper_Create(t *testing.T) {
	tmp := t.TempDir()
	upper := filepath.Join(tmp, "upper")
	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(upper, "a.jsonl"), "new")

	sources := []UpperSource{{Kind: UpperPrimary, Path: upper, SandboxID: "s1"}}
	plan, err := BuildPlan(sources, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 1 {
		t.Fatalf("want 1 op, got %d: %+v", len(plan.Operations), plan.Operations)
	}
	op := plan.Operations[0]
	if op.Kind != OpCreate || op.RelPath != "a.jsonl" || op.Bytes != 3 {
		t.Errorf("unexpected op: %+v", op)
	}
}

func TestBuildPlan_Overwrite_WhenHostHasFile(t *testing.T) {
	tmp := t.TempDir()
	upper := filepath.Join(tmp, "upper")
	host := filepath.Join(tmp, "host")
	writeFile(t, filepath.Join(host, "a.jsonl"), "old")
	writeFile(t, filepath.Join(upper, "a.jsonl"), "new")

	sources := []UpperSource{{Kind: UpperPrimary, Path: upper, SandboxID: "s1"}}
	plan, err := BuildPlan(sources, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 1 || plan.Operations[0].Kind != OpOverwrite {
		t.Fatalf("want OpOverwrite, got %+v", plan.Operations)
	}
}

func TestBuildPlan_StackedUppers_LastWins(t *testing.T) {
	tmp := t.TempDir()
	upperA := filepath.Join(tmp, "upperA")
	upperB := filepath.Join(tmp, "upperB")
	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(upperA, "m.md"), "A-version")
	writeFile(t, filepath.Join(upperB, "m.md"), "B-version")

	sources := []UpperSource{
		{Kind: UpperPrimary, Path: upperA, SandboxID: "s1", SourceLabel: "s1:primary"},
		{Kind: UpperSession, Path: upperB, SandboxID: "s1", SessionID: "abc", SourceLabel: "s1:session/abc"},
	}
	plan, err := BuildPlan(sources, host)
	if err != nil {
		t.Fatal(err)
	}

	// Find the m.md operation (the plan may also include a parent dir op).
	var found *Operation
	for i := range plan.Operations {
		if plan.Operations[i].RelPath == "m.md" {
			found = &plan.Operations[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no op for m.md; ops=%+v", plan.Operations)
	}
	if found.Source != filepath.Join(upperB, "m.md") {
		t.Errorf("later upper should win, got source=%s", found.Source)
	}
}

func TestBuildPlan_MissingHost_PlansCreate(t *testing.T) {
	tmp := t.TempDir()
	upper := filepath.Join(tmp, "upper")
	host := filepath.Join(tmp, "host-not-yet") // doesn't exist
	writeFile(t, filepath.Join(upper, "m.md"), "x")

	sources := []UpperSource{{Kind: UpperPrimary, Path: upper, SandboxID: "s1"}}
	plan, err := BuildPlan(sources, host)
	if err != nil {
		t.Fatal(err)
	}
	var mOp *Operation
	for i := range plan.Operations {
		if plan.Operations[i].RelPath == "m.md" {
			mOp = &plan.Operations[i]
			break
		}
	}
	if mOp == nil || mOp.Kind != OpCreate {
		t.Fatalf("missing host should plan Create for m.md, got %+v", plan.Operations)
	}
}

func TestBuildPlan_Symlink_PreservedAsSymlink(t *testing.T) {
	tmp := t.TempDir()
	upper := filepath.Join(tmp, "upper")
	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(upper, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target-file", filepath.Join(upper, "link")); err != nil {
		t.Fatal(err)
	}

	sources := []UpperSource{{Kind: UpperPrimary, Path: upper, SandboxID: "s1"}}
	plan, err := BuildPlan(sources, host)
	if err != nil {
		t.Fatal(err)
	}
	var linkOp *Operation
	for i := range plan.Operations {
		if plan.Operations[i].RelPath == "link" {
			linkOp = &plan.Operations[i]
			break
		}
	}
	if linkOp == nil {
		t.Fatalf("no op for link; ops=%+v", plan.Operations)
	}
	if !linkOp.IsSymlink || linkOp.LinkTarget != "target-file" {
		t.Errorf("expected symlink with target-file, got %+v", *linkOp)
	}
}

func TestBuildPlan_GroupedBySandbox(t *testing.T) {
	tmp := t.TempDir()
	upperS1 := filepath.Join(tmp, "s1-upper")
	upperS2 := filepath.Join(tmp, "s2-upper")
	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(upperS1, "s1.jsonl"), "x")
	writeFile(t, filepath.Join(upperS2, "s2.jsonl"), "y")

	sources := []UpperSource{
		{Kind: UpperPrimary, Path: upperS1, SandboxID: "s1"},
		{Kind: UpperPrimary, Path: upperS2, SandboxID: "s2"},
	}
	plan, err := BuildPlan(sources, host)
	if err != nil {
		t.Fatal(err)
	}
	// Count only file ops per sandbox (parent-dir ops may be present).
	var s1Files, s2Files int
	for _, op := range plan.BySandbox["s1"] {
		if op.RelPath == "s1.jsonl" {
			s1Files++
		}
	}
	for _, op := range plan.BySandbox["s2"] {
		if op.RelPath == "s2.jsonl" {
			s2Files++
		}
	}
	if s1Files != 1 || s2Files != 1 {
		t.Fatalf("grouping wrong: s1Files=%d s2Files=%d BySandbox=%+v", s1Files, s2Files, plan.BySandbox)
	}
}
