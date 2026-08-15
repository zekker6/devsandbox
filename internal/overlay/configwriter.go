package overlay

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// ErrNotRegularFile reports a config path that is not a plain file - a symlink,
// a directory, a device. SetToolMode refuses to read or rewrite one.
var ErrNotRegularFile = errors.New("not a regular file")

// readRegularFile reads path, refusing to follow a symlink sitting there and
// refusing to block on anything that is not a plain file.
//
// `overlay migrate --set-mode` runs on the host and rewrites a path inside the
// project directory, which is bind-mounted read-write into the sandbox. A
// project with no .devsandbox.toml at launch time does not get the protective
// /dev/null bind, so the sandbox can create that name as a symlink to any host
// file - and a later --set-mode would then read that file, rewrite it as TOML
// and hand it back, skipping the confirmation prompt entirely when only one
// config is affected. Same construct copyFile closes on the migration path.
//
// O_NONBLOCK is what makes the non-regular check below reachable. O_NOFOLLOW
// covers symlinks only, and a FIFO at the same name blocks the open until a
// writer appears - so the sandbox could create one and hang the command with no
// output rather than get the refusal this function exists to give.
func readRegularFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%q: %w", path, ErrNotRegularFile)
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%q: %w", path, ErrNotRegularFile)
	}
	return io.ReadAll(f)
}

// writeRegularFile replaces path atomically via a temporary file in the same
// directory, so the write lands on the name itself rather than through whatever
// a symlink at that name points to, and a failure part-way leaves the original
// intact rather than truncated.
//
// An existing file keeps the permissions it had: rewriting one setting must not
// be how a config the user deliberately kept at 0600 becomes world-readable.
// Only a file this call creates gets 0644.
func writeRegularFile(path string, content []byte) (retErr error) {
	perm := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
		perm = fi.Mode().Perm()
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".devsandbox-config-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

var validToolModes = map[string]bool{
	"split":      true,
	"overlay":    true,
	"tmpoverlay": true,
	"readonly":   true,
	"readwrite":  true,
	"disabled":   true,
}

// SetToolMode updates the [tools.<tool>] section of configPath so that
// `mount_mode = "<mode>"`. Creates the file if missing, the section if
// missing, the field if missing. Preserves comments and other sections.
func SetToolMode(configPath, tool, mode string) error {
	if !validToolModes[mode] {
		return fmt.Errorf("invalid mount_mode %q (want one of split/overlay/tmpoverlay/readonly/readwrite/disabled)", mode)
	}

	raw, err := readRegularFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	header := fmt.Sprintf("[tools.%s]", tool)
	mountLine := fmt.Sprintf(`mount_mode = %q`, mode)

	if len(raw) == 0 {
		content := header + "\n" + mountLine + "\n"
		return writeRegularFile(configPath, []byte(content))
	}

	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return err
	}

	sectionRE := regexp.MustCompile(`^\s*\[[^\]]+\]\s*$`)
	mountRE := regexp.MustCompile(`^\s*mount_mode\s*=`)

	inSection := false
	sectionStart := -1
	sectionEnd := -1 // exclusive
	for i, line := range lines {
		if sectionRE.MatchString(line) {
			trimmed := strings.TrimSpace(line)
			if trimmed == header {
				inSection = true
				sectionStart = i + 1
				continue
			}
			if inSection {
				sectionEnd = i
				break
			}
		}
	}
	if inSection && sectionEnd == -1 {
		sectionEnd = len(lines)
	}

	if !inSection {
		// Append new section
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, header, mountLine)
		return writeRegularFile(configPath, []byte(strings.Join(lines, "\n")+"\n"))
	}

	// Section exists — look for existing mount_mode line
	mountIdx := -1
	for i := sectionStart; i < sectionEnd; i++ {
		if mountRE.MatchString(lines[i]) {
			mountIdx = i
			break
		}
	}
	if mountIdx >= 0 {
		lines[mountIdx] = mountLine
	} else {
		// Insert right after header
		newLines := append([]string{}, lines[:sectionStart]...)
		newLines = append(newLines, mountLine)
		newLines = append(newLines, lines[sectionStart:]...)
		lines = newLines
	}
	return writeRegularFile(configPath, []byte(strings.Join(lines, "\n")+"\n"))
}
