package tools

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func recordOrphanTmpOwner(t *testing.T, homeDir string) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", "")
	home := filepath.Join(t.TempDir(), "gone", "home")
	if err := recordSharedTmpOwner(homeDir, home); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestSharedTmpOwnerSurvivesBaseChange(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(homeDir, "state"))
	home := filepath.Join(homeDir, "old-base", "sandbox", "home")
	if err := prepareSharedTmp(homeDir, home, nil); err != nil {
		t.Fatal(err)
	}
	dir := SharedTmpPath(homeDir, home)
	writeFileAt(t, filepath.Join(dir, "scratch"), "work", time.Time{})
	ageTree(t, dir, time.Now().Add(-sharedTmpStaleAge-time.Hour))
	if n, err := SweepOrphanSharedTmp(homeDir, nil); err != nil || n != 0 {
		t.Fatalf("sweep with previous base absent from live set = (%d, %v)", n, err)
	}
	if n, err := SweepSharedTmpOwners(homeDir); err != nil || n != 0 {
		t.Fatalf("live owner sweep = (%d, %v)", n, err)
	}
	if err := os.RemoveAll(filepath.Dir(home)); err != nil {
		t.Fatal(err)
	}
	if n, err := SweepSharedTmpOwners(homeDir); err != nil || n != 0 {
		t.Fatalf("owner must survive until temp is removed = (%d, %v)", n, err)
	}
	if n, err := SweepOrphanSharedTmp(homeDir, nil); err != nil || n != 1 {
		t.Fatalf("sweep after old owner removed = (%d, %v)", n, err)
	}
	if n, err := SweepSharedTmpOwners(homeDir); err != nil || n != 1 {
		t.Fatalf("orphan owner sweep = (%d, %v)", n, err)
	}
}

func TestSharedTmpOwnerUncertaintyPreservesTemp(t *testing.T) {
	for _, kind := range []string{"unknown", "corrupt", "missing-base"} {
		t.Run(kind, func(t *testing.T) {
			homeDir := t.TempDir()
			t.Setenv("XDG_STATE_HOME", filepath.Join(homeDir, "state"))
			home := filepath.Join(homeDir, "absent-base", "sandbox", "home")
			if kind != "unknown" {
				if err := recordSharedTmpOwner(homeDir, home); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "corrupt" {
				if err := os.WriteFile(filepath.Join(SharedTmpOwnerRoot(homeDir), sharedTmpSessionID(home)), []byte("invalid"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			dir := SharedTmpPath(homeDir, home)
			writeFileAt(t, filepath.Join(dir, "scratch"), "work", time.Time{})
			ageTree(t, dir, time.Now().Add(-sharedTmpStaleAge-time.Hour))
			n, err := SweepOrphanSharedTmp(homeDir, nil)
			if n != 0 || (err != nil) != (kind != "unknown") {
				t.Fatalf("sweep = (%d, %v)", n, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "scratch")); err != nil {
				t.Fatalf("unproven orphan removed: %v", err)
			}
		})
	}
}

func TestSharedTmpOwnerBackfillsKnownHomes(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(homeDir, "state"))
	home := filepath.Join(homeDir, "base", "sandbox", "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := SharedTmpPath(homeDir, home)
	writeFileAt(t, filepath.Join(dir, "scratch"), "work", time.Time{})
	ageTree(t, dir, time.Now().Add(-sharedTmpStaleAge-time.Hour))
	if n, err := SweepOrphanSharedTmp(homeDir, []string{home}); err != nil || n != 0 {
		t.Fatalf("backfill = (%d, %v)", n, err)
	}
	data, err := os.ReadFile(filepath.Join(SharedTmpOwnerRoot(homeDir), sharedTmpSessionID(home)))
	if err != nil || string(data) != home {
		t.Fatalf("owner = %q, %v", data, err)
	}
	if n, err := SweepOrphanSharedTmp(homeDir, nil); err != nil || n != 0 {
		t.Fatalf("sweep after base change = (%d, %v)", n, err)
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		t.Fatal("backfilled owner did not protect temp")
	}
}
