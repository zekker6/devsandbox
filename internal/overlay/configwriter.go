package overlay

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"
)

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

	raw, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	header := fmt.Sprintf("[tools.%s]", tool)
	mountLine := fmt.Sprintf(`mount_mode = %q`, mode)

	if len(raw) == 0 {
		content := header + "\n" + mountLine + "\n"
		return os.WriteFile(configPath, []byte(content), 0o644)
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
		return os.WriteFile(configPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
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
	return os.WriteFile(configPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
