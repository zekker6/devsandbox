package worktree

import (
	"strings"
	"testing"
	"time"

	"crypto/sha256"
	"encoding/hex"
)

// shortHash returns the 8-char hex hash suffix SanitizeLeaf appends.
func shortHash(branch string) string {
	h := sha256.Sum256([]byte(branch))
	return hex.EncodeToString(h[:4])
}

func TestSanitizeLeaf(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"feature", "feature-" + shortHash("feature")},
		{"feat/login", "feat-login-" + shortHash("feat/login")},
		{"feat/login/v2", "feat-login-v2-" + shortHash("feat/login/v2")},
		{"devsandbox/claude-edits", "devsandbox-claude-edits-" + shortHash("devsandbox/claude-edits")},
		{"a/b/c/d", "a-b-c-d-" + shortHash("a/b/c/d")},
	}
	for _, tc := range cases {
		if got := SanitizeLeaf(tc.in); got != tc.want {
			t.Errorf("SanitizeLeaf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeLeafCaseCollision(t *testing.T) {
	// Branches differing only in case must produce distinct leaves (macOS safety).
	a := SanitizeLeaf("feature/Foo")
	b := SanitizeLeaf("feature/foo")
	if a == b {
		t.Errorf("SanitizeLeaf(%q) == SanitizeLeaf(%q) = %q; want distinct", "feature/Foo", "feature/foo", a)
	}
}

func TestSanitizeLeafSlashDashCollision(t *testing.T) {
	// Branches "feat/login" and "feat-login" must produce distinct leaves.
	a := SanitizeLeaf("feat/login")
	b := SanitizeLeaf("feat-login")
	if a == b {
		t.Errorf("SanitizeLeaf(%q) == SanitizeLeaf(%q) = %q; want distinct", "feat/login", "feat-login", a)
	}
}

func TestAutoBranchName(t *testing.T) {
	if got := AutoBranchName("feat-x"); got != "devsandbox/feat-x" {
		t.Errorf("AutoBranchName(%q) = %q", "feat-x", got)
	}

	fixed := time.Date(2026, 4, 15, 13, 45, 30, 0, time.UTC)
	got := AutoBranchNameAt("", fixed)
	want := "devsandbox/20260415-134530"
	if got != want {
		t.Errorf("AutoBranchNameAt empty = %q, want %q", got, want)
	}

	// non-empty session overrides timestamp
	got = AutoBranchNameAt("my-session", fixed)
	if got != "devsandbox/my-session" {
		t.Errorf("AutoBranchNameAt non-empty = %q", got)
	}
}

func TestWorktreePath(t *testing.T) {
	got := WorktreePath("/home/alice/.local/share/devsandbox/myproj-abcd1234", "feat/login")
	want := "/home/alice/.local/share/devsandbox/myproj-abcd1234/worktrees/" + SanitizeLeaf("feat/login")
	if got != want {
		t.Errorf("WorktreePath = %q, want %q", got, want)
	}
}

func TestSanitizeLeafRejectsEmptyAfterStrip(t *testing.T) {
	// All-slash branch produces an empty leaf after stripping — sanitizer must replace with placeholder
	got := SanitizeLeaf("///")
	if got == "" || strings.Contains(got, "/") {
		t.Errorf("SanitizeLeaf(\"///\") = %q; want non-empty, no slashes", got)
	}
}
