package logrotate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devsandbox/internal/fsutil"
)

func writeLog(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestRotate_OverLimitMovesToBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wrapper.log")
	content := strings.Repeat("a", 128)
	writeLog(t, path, content)

	deleted, err := Rotate(path, Options{MaxSize: 64, MaxFiles: 3})
	if err != nil {
		t.Fatalf("Rotate failed: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted = %d, want 0 (no backups existed)", deleted)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("live log should be gone after rotation, stat err = %v", err)
	}
	if got := readLog(t, path+".1"); got != content {
		t.Fatalf("backup content = %q, want the rotated log", got)
	}

	// The caller opens its append handle after Rotate: the path must be free for
	// a fresh file that starts at zero bytes.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("reopen after rotation: %v", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("stat reopened log: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("reopened log size = %d, want 0", info.Size())
	}
}

func TestRotate_ShiftsBackupsAndDeletesOldest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wrapper.log")
	writeLog(t, path, strings.Repeat("l", 128))
	writeLog(t, path+".1", "first backup")
	writeLog(t, path+".2", "second backup")

	deleted, err := Rotate(path, Options{MaxSize: 64, MaxFiles: 3})
	if err != nil {
		t.Fatalf("Rotate failed: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	if got := readLog(t, path+".1"); got != strings.Repeat("l", 128) {
		t.Fatalf(".1 = %q, want the rotated live log", got)
	}
	if got := readLog(t, path+".2"); got != "first backup" {
		t.Fatalf(".2 = %q, want the shifted first backup", got)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf(".3 should not exist with MaxFiles=3, stat err = %v", err)
	}
}

func TestRotate_UnderLimitUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wrapper.log")
	content := strings.Repeat("s", 8)
	writeLog(t, path, content)

	deleted, err := Rotate(path, Options{MaxSize: 64, MaxFiles: 3})
	if err != nil {
		t.Fatalf("Rotate failed: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted = %d, want 0", deleted)
	}
	if got := readLog(t, path); got != content {
		t.Fatalf("log content = %q, want it untouched", got)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("no backup should be created, stat err = %v", err)
	}
}

func TestRotate_MissingLogIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrapper.log")

	deleted, err := Rotate(path, Options{MaxSize: 64, MaxFiles: 3})
	if err != nil {
		t.Fatalf("Rotate on a missing log failed: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted = %d, want 0", deleted)
	}
}

func TestRotate_RechecksSizeUnderLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wrapper.log")
	content := strings.Repeat("r", 128)
	writeLog(t, path, content)

	// Hold the rotation lock and perform the rotation this call is queued behind,
	// exactly as a competing process would. Rotate must see the fresh, small log
	// under the lock and do nothing rather than shift it again.
	lock, err := fsutil.AcquireFileLock(path + ".lock")
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}

	type result struct {
		deleted int
		err     error
	}
	done := make(chan result, 1)
	go func() {
		deleted, rerr := Rotate(path, Options{MaxSize: 64, MaxFiles: 3})
		done <- result{deleted, rerr}
	}()

	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatalf("simulate competing rotation: %v", err)
	}
	writeLog(t, path, "fresh")
	if err := lock.Release(); err != nil {
		t.Fatalf("release lock: %v", err)
	}

	got := <-done
	if got.err != nil {
		t.Fatalf("Rotate failed: %v", got.err)
	}
	if got.deleted != 0 {
		t.Fatalf("deleted = %d, want 0 for a no-op rotation", got.deleted)
	}
	if c := readLog(t, path); c != "fresh" {
		t.Fatalf("live log = %q, want the fresh file left in place", c)
	}
	if c := readLog(t, path+".1"); c != content {
		t.Fatalf(".1 = %q, want the content the first rotation moved there", c)
	}
	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Fatalf("second shift must not happen, stat err = %v", err)
	}
}
