package main

import (
	"os"
	"testing"
)

func TestPrepareOverlayDirs(t *testing.T) {
	base := t.TempDir()
	target := "/home/sandboxuser/.config/fish"

	upper, work, err := prepareOverlayDirs(base, target)
	if err != nil {
		t.Fatalf("prepareOverlayDirs: %v", err)
	}

	// Verify dirs exist
	if _, err := os.Stat(upper); err != nil {
		t.Errorf("upper dir %s does not exist: %v", upper, err)
	}
	if _, err := os.Stat(work); err != nil {
		t.Errorf("work dir %s does not exist: %v", work, err)
	}

	// Verify deterministic — same target produces same dirs
	upper2, work2, err := prepareOverlayDirs(base, target)
	if err != nil {
		t.Fatal(err)
	}
	if upper != upper2 || work != work2 {
		t.Error("prepareOverlayDirs should be deterministic for same target")
	}
}

func TestPrepareOverlayDirs_DifferentTargets(t *testing.T) {
	base := t.TempDir()

	upper1, _, _ := prepareOverlayDirs(base, "/home/sandboxuser/.config/fish")
	upper2, _, _ := prepareOverlayDirs(base, "/home/sandboxuser/.config/mise")

	if upper1 == upper2 {
		t.Error("different targets should produce different dirs")
	}
}

func TestBuildOverlayMountOptions(t *testing.T) {
	opts := buildOverlayMountOptions("/lower", "/upper", "/work")
	expected := "lowerdir=/lower,upperdir=/upper,workdir=/work"
	if opts != expected {
		t.Errorf("got %q, want %q", opts, expected)
	}
}

func TestSplitOverlaySpec(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			"normal",
			"/home/sandboxuser/.config/fish:/tmp/.devsandbox-overlay-layers/abc/upper:/tmp/.devsandbox-overlay-layers/abc/work",
			[]string{
				"/home/sandboxuser/.config/fish",
				"/tmp/.devsandbox-overlay-layers/abc/upper",
				"/tmp/.devsandbox-overlay-layers/abc/work",
			},
		},
		{
			"empty",
			"",
			nil,
		},
		{
			"one colon",
			"a:b",
			nil,
		},
		{
			"exactly three parts",
			"a:b:c",
			[]string{"a", "b", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitOverlaySpec(tt.input)
			if tt.want == nil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
