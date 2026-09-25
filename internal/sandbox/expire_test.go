package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"devsandbox/internal/worktree"
)

const testMaxAge = 30 * 24 * time.Hour

// newAgedSandbox creates a sandbox under base whose metadata says it was
// created age ago.
func newAgedSandbox(t *testing.T, base, name string, age time.Duration, iso IsolationType) string {
	t.Helper()
	root := filepath.Join(base, name)
	if err := os.MkdirAll(filepath.Join(root, "home"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(Metadata{
		Name:       name,
		ProjectDir: "/nonexistent",
		CreatedAt:  time.Now().Add(-age),
		LastUsed:   time.Now(),
		Isolation:  iso,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, MetadataFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// Sets HOME (the shared temp directory is resolved from it), so it must not
// call t.Parallel().
func TestRemoveExpired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	expiredAge := testMaxAge + 24*time.Hour

	expired := newAgedSandbox(t, base, "expired-00000001", expiredAge, IsolationBwrap)
	fresh := newAgedSandbox(t, base, "fresh-00000002", testMaxAge-24*time.Hour, IsolationBwrap)
	container := newAgedSandbox(t, base, "container-00000003", expiredAge, IsolationDocker)
	withWorktree := newAgedSandbox(t, base, "wt-00000004", expiredAge, IsolationBwrap)
	if err := os.MkdirAll(filepath.Join(worktree.WorktreesDir(withWorktree), "branch"), 0o755); err != nil {
		t.Fatal(err)
	}
	busy := newAgedSandbox(t, base, "busy-00000005", expiredAge, IsolationBwrap)
	handle, err := AcquireSession(busy)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Release() }()
	// No metadata: the directory's mtime is not a creation time, so an old
	// one must not expire it.
	noMetadata := filepath.Join(base, "nometa-00000006")
	if err := os.MkdirAll(noMetadata, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-365 * 24 * time.Hour)
	if err := os.Chtimes(noMetadata, old, old); err != nil {
		t.Fatal(err)
	}

	res, err := RemoveExpired(base, testMaxAge, time.Now())
	if err != nil {
		t.Fatalf("RemoveExpired: %v", err)
	}

	if !slices.Equal(res.Removed, []string{"expired-00000001"}) {
		t.Errorf("Removed = %v, want only the idle expired sandbox", res.Removed)
	}
	slices.Sort(res.Kept)
	if !slices.Equal(res.Kept, []string{"container-00000003", "wt-00000004"}) {
		t.Errorf("Kept = %v, want the container-backed and worktree sandboxes", res.Kept)
	}
	if pathExists(expired) {
		t.Error("expired idle sandbox survived")
	}
	for name, root := range map[string]string{
		"fresh": fresh, "container-backed": container, "worktree": withWorktree, "busy": busy, "no metadata": noMetadata,
	} {
		if !pathExists(root) {
			t.Errorf("%s sandbox was removed", name)
		}
	}
}

// Expiry is judged again under the exclusive lock, so a sandbox recreated
// after the candidates were picked is not removed on the old one's age.
//
// Sets HOME, so it must not call t.Parallel().
func TestRemoveSandboxIfIdleWhen_DeclinedConditionKeepsSandbox(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := newAgedSandbox(t, t.TempDir(), "proj-a1b2c3d4", 0, IsolationBwrap)

	removed, err := RemoveSandboxIfIdleWhen(root, func() bool {
		return expiryOf(root, testMaxAge, time.Now()) == expiryRemove
	})
	if err != nil {
		t.Fatalf("RemoveSandboxIfIdleWhen: %v", err)
	}
	if removed || !pathExists(root) {
		t.Error("sandbox removed although the condition under the lock declined")
	}
	// A declined removal must release the lock, or the next launch of this
	// project would find its own sandbox busy.
	if IsSessionActive(root) {
		t.Error("lock still held after a declined removal")
	}
}
