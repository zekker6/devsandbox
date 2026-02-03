package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Include represents a conditional config include.
type Include struct {
	If   string `toml:"if"`   // e.g., "dir:~/work/**"
	Path string `toml:"path"` // e.g., "~/.config/devsandbox/work.toml"
}

// parseIncludeCondition parses an include condition string.
// Returns the condition type and value.
// Currently only "dir:" type is supported.
func parseIncludeCondition(condition string) (condType, value string, err error) {
	if !strings.Contains(condition, ":") {
		return "", "", fmt.Errorf("invalid include condition %q: missing type prefix (expected 'dir:')", condition)
	}

	parts := strings.SplitN(condition, ":", 2)
	condType = parts[0]
	value = parts[1]

	if condType != "dir" {
		return "", "", fmt.Errorf("unknown include condition type %q (only 'dir' is supported)", condType)
	}

	if value == "" {
		return "", "", fmt.Errorf("empty value in include condition %q", condition)
	}

	return condType, value, nil
}

// matchDirPattern checks if projectDir matches the dir: pattern.
// Pattern must start with "dir:" prefix.
// Supports glob patterns with ** for recursive matching.
func matchDirPattern(pattern, projectDir string) (bool, error) {
	condType, dirPattern, err := parseIncludeCondition(pattern)
	if err != nil {
		return false, err
	}
	if condType != "dir" {
		panic(fmt.Sprintf("matchDirPattern called with non-dir condition type: %q", condType))
	}

	dirPattern = expandHome(dirPattern)
	dirPattern = filepath.Clean(dirPattern)
	projectDir = filepath.Clean(projectDir)

	matched, err := doublestar.Match(dirPattern, projectDir)
	if err != nil {
		return false, fmt.Errorf("invalid glob pattern %q: %w", dirPattern, err)
	}

	return matched, nil
}

// getMatchingIncludes returns all includes that match the given project directory.
// It validates all conditions and returns an error if any condition is invalid.
func getMatchingIncludes(includes []Include, projectDir string) ([]Include, error) {
	var matched []Include

	for _, inc := range includes {
		match, err := matchDirPattern(inc.If, projectDir)
		if err != nil {
			return nil, err
		}
		if match {
			matched = append(matched, inc)
		}
	}

	return matched, nil
}
