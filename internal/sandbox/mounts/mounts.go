// Package mounts provides custom mount configuration for sandboxes.
// It allows mounting paths with different modes (readonly, readwrite, overlay, etc.)
package mounts

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"devsandbox/internal/config"
)

// Logger is used by the mounts engine to report warnings.
type Logger interface {
	Warnf(format string, args ...any)
}

// Mode defines how a path should be mounted.
type Mode string

const (
	// ModeHidden overlays the path with /dev/null, making it inaccessible.
	ModeHidden Mode = "hidden"
	// ModeReadOnly mounts the path as read-only.
	ModeReadOnly Mode = "readonly"
	// ModeReadWrite mounts the path as read-write.
	ModeReadWrite Mode = "readwrite"
	// ModeOverlay mounts with persistent overlayfs (writes saved to sandbox).
	ModeOverlay Mode = "overlay"
	// ModeTmpOverlay mounts with tmpfs overlay (writes discarded on exit).
	ModeTmpOverlay Mode = "tmpoverlay"
)

// Rule represents a compiled mount rule.
type Rule struct {
	Pattern  string // Original pattern (may contain ~)
	Expanded string // Pattern with ~ expanded
	Mode     Mode
}

// Engine evaluates paths against mount rules.
type Engine struct {
	rules   []Rule
	homeDir string
	logger  Logger
}

// SetLogger sets the logger for the mounts engine.
func (e *Engine) SetLogger(l Logger) {
	e.logger = l
}

// logWarnf logs a warning via the configured logger, if any.
func (e *Engine) logWarnf(format string, args ...any) {
	if e.logger != nil {
		e.logger.Warnf(format, args...)
	}
}

// NewEngine creates a mount engine from configuration.
func NewEngine(cfg config.MountsConfig, homeDir string) *Engine {
	var rules []Rule

	for _, r := range cfg.Rules {
		rules = append(rules, Rule{
			Pattern:  r.Pattern,
			Expanded: expandHome(r.Pattern, homeDir),
			Mode:     parseMode(r.Mode),
		})
	}

	return &Engine{
		rules:   rules,
		homeDir: homeDir,
	}
}

// parseMode converts a string mode to Mode, defaulting to ModeReadOnly.
func parseMode(s string) Mode {
	switch s {
	case "hidden":
		return ModeHidden
	case "readwrite":
		return ModeReadWrite
	case "overlay":
		return ModeOverlay
	case "tmpoverlay":
		return ModeTmpOverlay
	default:
		return ModeReadOnly
	}
}

// ExpandPattern expands a glob pattern to matching file paths.
func (e *Engine) ExpandPattern(pattern string) ([]string, error) {
	expanded := expandHome(pattern, e.homeDir)
	return doublestar.FilepathGlob(expanded)
}

// Rules returns all configured rules.
func (e *Engine) Rules() []Rule {
	return e.rules
}

// ExpandedPaths returns all paths that match mount rules, grouped by mode.
// This expands glob patterns and returns concrete paths with their modes.
func (e *Engine) ExpandedPaths() map[string]Rule {
	paths := make(map[string]Rule)

	for _, rule := range e.rules {
		// Check if pattern ends with /** (entire directory) and the prefix is a literal path.
		// e.g., "/path/to/dir/**" is a directory pattern, but "**/vendor/**" is not.
		if dirPath, isDir := strings.CutSuffix(rule.Expanded, "/**"); isDir && !containsGlobChars(dirPath) {
			// Verify directory exists
			info, err := os.Stat(dirPath)
			if err == nil && info.IsDir() {
				if _, exists := paths[dirPath]; !exists {
					paths[dirPath] = rule
				}
			}
			continue
		}

		// Expand glob pattern to files
		matches, err := e.ExpandPattern(rule.Pattern)
		if err != nil {
			e.logWarnf("failed to expand pattern %q: %v", rule.Pattern, err)
			continue
		}

		for _, path := range matches {
			if _, err := os.Stat(path); err != nil {
				continue
			}

			// First match wins
			if _, exists := paths[path]; !exists {
				paths[path] = rule
			}
		}
	}

	return paths
}

// ExpandedPathsInDir returns paths matching relative patterns within a specific directory.
// Absolute patterns (starting with / or ~) are ignored - use ExpandedPaths() for those.
// This is useful for expanding patterns like "**/secrets/**" within a project directory.
func (e *Engine) ExpandedPathsInDir(baseDir string) map[string]Rule {
	paths := make(map[string]Rule)

	for _, rule := range e.rules {
		// Skip absolute patterns - they should be handled by ExpandedPaths()
		if filepath.IsAbs(rule.Expanded) || strings.HasPrefix(rule.Pattern, "~") {
			continue
		}

		// Check if pattern ends with /** (entire directory) and the prefix is a literal path
		// (no glob characters). e.g., "vendor/**" is a directory pattern, but "**/secrets/**" is not.
		if dirSuffix, isDir := strings.CutSuffix(rule.Expanded, "/**"); isDir && !containsGlobChars(dirSuffix) {
			// For relative dir patterns, join with base
			dirPath := filepath.Join(baseDir, dirSuffix)
			info, err := os.Stat(dirPath)
			if err == nil && info.IsDir() {
				if _, exists := paths[dirPath]; !exists {
					paths[dirPath] = rule
				}
			}
			continue
		}

		// Use doublestar.Glob with fs.FS to search relative to baseDir
		fsys := os.DirFS(baseDir)
		matches, err := doublestar.Glob(fsys, rule.Expanded)
		if err != nil {
			e.logWarnf("failed to expand pattern %q in %s: %v", rule.Pattern, baseDir, err)
			continue
		}

		for _, match := range matches {
			// Convert relative match to absolute path
			absPath := filepath.Join(baseDir, match)
			if _, err := os.Stat(absPath); err != nil {
				continue
			}

			// First match wins
			if _, exists := paths[absPath]; !exists {
				paths[absPath] = rule
			}
		}
	}

	return paths
}

// containsGlobChars returns true if the string contains glob special characters.
func containsGlobChars(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

// expandHome expands ~ to the user's home directory.
func expandHome(path, homeDir string) string {
	if len(path) == 0 || path[0] != '~' {
		return path
	}

	if len(path) == 1 {
		return homeDir
	}

	if path[1] == '/' {
		return filepath.Join(homeDir, path[2:])
	}

	return path
}
