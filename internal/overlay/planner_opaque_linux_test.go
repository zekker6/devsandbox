//go:build linux

package overlay

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

// setOpaque marks dir with the opaque xattr an unprivileged overlayfs mount
// writes (bwrap mounts its overlays inside a user namespace, which forces
// userxattr). The trusted-namespace spelling cannot be set without
// CAP_SYS_ADMIN, so it is not exercised here.
func setOpaque(t *testing.T, dir string) {
	t.Helper()
	if err := syscall.Setxattr(dir, "user.overlay.opaque", []byte("y"), 0); err != nil {
		t.Skipf("cannot set user.overlay.opaque on %s: %v", dir, err)
	}
}

func TestBuildPlan_OpaqueDirHidesEarlierDescendants(t *testing.T) {
	tmp := t.TempDir()
	upperA := filepath.Join(tmp, "upperA")
	upperB := filepath.Join(tmp, "upperB")
	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(upperA, "d", "old.txt"), "A")
	writeFile(t, filepath.Join(upperA, "d", "sub", "deep.txt"), "A")
	writeFile(t, filepath.Join(upperB, "d", "new.txt"), "B")
	setOpaque(t, filepath.Join(upperB, "d"))

	sources := []UpperSource{
		{Kind: UpperPrimary, Path: upperA, SandboxID: "s1"},
		{Kind: UpperSession, Path: upperB, SandboxID: "s1", SessionID: "abc"},
	}
	plan, err := BuildPlan(sources, host)
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(plan.Operations)
	for _, hidden := range []string{
		filepath.Join("d", "old.txt"),
		filepath.Join("d", "sub"),
		filepath.Join("d", "sub", "deep.txt"),
	} {
		if slices.Contains(got, hidden) {
			t.Errorf("entry hidden by opaque directory was planned: %s; ops=%v", hidden, got)
		}
	}
	if !slices.Contains(got, filepath.Join("d", "new.txt")) {
		t.Errorf("entry from the opaque directory missing; ops=%v", got)
	}
	if !slices.Contains(got, "d") {
		t.Errorf("opaque directory itself missing; ops=%v", got)
	}
}

func TestBuildPlan_NonOpaqueDirMergesWithEarlier(t *testing.T) {
	tmp := t.TempDir()
	upperA := filepath.Join(tmp, "upperA")
	upperB := filepath.Join(tmp, "upperB")
	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(upperA, "d", "old.txt"), "A")
	writeFile(t, filepath.Join(upperB, "d", "new.txt"), "B")

	sources := []UpperSource{
		{Kind: UpperPrimary, Path: upperA, SandboxID: "s1"},
		{Kind: UpperSession, Path: upperB, SandboxID: "s1", SessionID: "abc"},
	}
	plan, err := BuildPlan(sources, host)
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(plan.Operations)
	for _, want := range []string{filepath.Join("d", "old.txt"), filepath.Join("d", "new.txt")} {
		if !slices.Contains(got, want) {
			t.Errorf("plain directory dropped %s; ops=%v", want, got)
		}
	}
}
