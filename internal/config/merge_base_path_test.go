package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMergeProjectConfig_IgnoresLocalBasePath pins the sandbox base as a
// host-level setting. `sandboxes list` and `sandboxes prune` read the host
// config only - reading the project file would let one project decide which
// sandboxes the whole host is judged against - so a sandbox created under a
// project-local base is invisible to both. Invisible is not merely
// incomplete: prune finds an orphaned shared temp directory by elimination
// against the sandboxes it can see, and those directories live under the home
// rather than under the base, so such a sandbox could have its live $TMPDIR
// reclaimed as an orphan.
func TestMergeProjectConfig_IgnoresLocalBasePath(t *testing.T) {
	base := &Config{Sandbox: SandboxConfig{BasePath: "/host/sandboxes"}}
	local := &Config{Sandbox: SandboxConfig{BasePath: "/project/sandboxes", Isolation: "docker"}}

	merged := mergeProjectConfig(base, local)
	if merged.Sandbox.BasePath != "/host/sandboxes" {
		t.Errorf("base_path = %q, want the host value %q", merged.Sandbox.BasePath, "/host/sandboxes")
	}
	if merged.Sandbox.Isolation != "docker" {
		t.Errorf("isolation = %q, want the project override to still apply", merged.Sandbox.Isolation)
	}
}

// An unset host value stays unset rather than picking up the project's: the
// default base is resolved from the home directory, not from this file.
func TestMergeProjectConfig_LocalBasePathCannotSetAnUnsetHostValue(t *testing.T) {
	merged := mergeProjectConfig(&Config{}, &Config{Sandbox: SandboxConfig{BasePath: "/project/sandboxes"}})
	if merged.Sandbox.BasePath != "" {
		t.Errorf("base_path = %q, want it left unset", merged.Sandbox.BasePath)
	}
}

// TestApplyIncludes_IgnoresIncludeBasePath pins the same rule against an
// `[[include]]`. An include is selected by the working directory, so a base
// set there is a per-directory value - and `sandboxes list` and `sandboxes
// prune` are host-level commands that are not run from the project directory.
// They would judge the host against whichever base their own cwd selects, and
// the orphaned-shared-temp sweep eliminates against exactly that base.
func TestApplyIncludes_IgnoresIncludeBasePath(t *testing.T) {
	dir := t.TempDir()
	includePath := filepath.Join(dir, "include.toml")
	if err := os.WriteFile(includePath, []byte("[sandbox]\nbase_path = \"/include/sandboxes\"\nisolation = \"docker\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		Sandbox: SandboxConfig{BasePath: "/host/sandboxes"},
		Include: []Include{{If: "dir:" + dir, Path: includePath}},
	}

	merged, err := applyIncludes(cfg, dir)
	if err != nil {
		t.Fatalf("applyIncludes: %v", err)
	}
	if merged.Sandbox.BasePath != "/host/sandboxes" {
		t.Errorf("base_path = %q, want the global value %q", merged.Sandbox.BasePath, "/host/sandboxes")
	}
	if merged.Sandbox.Isolation != "docker" {
		t.Errorf("isolation = %q, want the include's other settings to still apply", merged.Sandbox.Isolation)
	}
}

// An unset global value stays unset rather than picking up an include's, so
// the base is resolved from the home directory as it is with no config at all.
func TestApplyIncludes_IncludeBasePathCannotSetAnUnsetGlobalValue(t *testing.T) {
	dir := t.TempDir()
	includePath := filepath.Join(dir, "include.toml")
	if err := os.WriteFile(includePath, []byte("[sandbox]\nbase_path = \"/include/sandboxes\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	merged, err := applyIncludes(&Config{Include: []Include{{If: "dir:" + dir, Path: includePath}}}, dir)
	if err != nil {
		t.Fatalf("applyIncludes: %v", err)
	}
	if merged.Sandbox.BasePath != "" {
		t.Errorf("base_path = %q, want it left unset", merged.Sandbox.BasePath)
	}
}
