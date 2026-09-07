package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devsandbox/internal/herdrproxy"
)

func TestCollectCacheMounts(t *testing.T) {
	// CollectCacheMounts depends on host tool availability (exec.LookPath),
	// so we only assert structural invariants rather than specific counts.
	mounts := CollectCacheMounts()

	for _, m := range mounts {
		if m.Name == "" {
			t.Error("CacheMount has empty Name")
		}
		if m.EnvVar == "" {
			t.Error("CacheMount has empty EnvVar")
		}
		if m.FullPath() == "" {
			t.Error("CacheMount has empty FullPath()")
		}
	}
}

func TestAllReturnsRegisteredTools(t *testing.T) {
	tools := All()
	if len(tools) == 0 {
		t.Fatal("All() returned no tools, expected at least one registered tool")
	}

	// Verify sorted order
	for i := 1; i < len(tools); i++ {
		if tools[i].Name() < tools[i-1].Name() {
			t.Errorf("All() not sorted: %s comes after %s", tools[i].Name(), tools[i-1].Name())
		}
	}
}

// TestRegistry_NoWritableBindUnderHostHome pins that no tool hands the sandbox
// a writable bind of anything under the real home directory.
//
// A host config file the host itself reads back is the dangerous case:
// ~/.claude.json was bound read-write, so a sandboxed agent could plant an
// mcpServers entry the host's Claude Code then launched. Files like that are
// seeded into the sandbox home instead (Claude.Setup is the worked example),
// and an explicit `Type: MountBind` without ReadOnly is the only way a tool can
// force a writable host bind past the mount-mode policy - which is what this
// test scans for.
//
// The fixture mirrors production: the sandbox home is nested inside the host
// home, so a binding whose source is sandbox state is under homeDir too, and
// the project lives outside it, so git's project-tree binds stay out of the
// set. The exceptions below are the deliberate identical-path binds the host
// does not read as config; each is exercised, not merely declared, so the
// allowlist cannot drift from the bindings it excuses.
func TestRegistry_NoWritableBindUnderHostHome(t *testing.T) {
	tmp := t.TempDir()
	homeDir := filepath.Join(tmp, "home")
	sandboxHome := filepath.Join(homeDir, ".local", "share", "devsandbox", "project-0123abcd", "home")
	projectDir := filepath.Join(tmp, "project")
	zellijSocketDir := filepath.Join(homeDir, ".run", "zellij")
	for _, d := range []string{sandboxHome, projectDir, zellijSocketDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ZELLIJ", "1")
	t.Setenv("ZELLIJ_SOCKET_DIR", zellijSocketDir)
	t.Setenv("HERDR_ENV", "1")

	exceptions := map[string]string{
		// The whole point of the directory is that the sandbox's writes reach
		// the host, at an identical path; nothing on the host reads it as
		// configuration (sharedtmp.go).
		SharedTmpPath(homeDir, sandboxHome): "shared temp directory",
		// Unix sockets cannot be reached through an overlay, and the bind is
		// opt-in via [tools.zellij] enabled and only inside a zellij session.
		zellijSocketDir: "zellij socket directory",
		// devsandbox's own filtered proxy socket under the sandbox home, which
		// connect(2) needs write permission on.
		filepath.Join(runDir(sandboxHome), herdrProxySocketName): "herdr proxy socket",
	}
	// Per-tool settings that reach the widest writable set: zellij binds
	// nothing until enabled, and readwrite is git's most permissive mode.
	toolConfigs := map[string]map[string]any{
		"zellij": {"enabled": true},
		"git":    {"mode": "readwrite"},
	}
	global := GlobalConfig{HomeDir: homeDir, ProjectDir: projectDir, DefaultMountMode: "split"}

	seen := map[string]bool{}
	for _, tool := range All() {
		if configurable, ok := tool.(ToolWithConfig); ok {
			configurable.Configure(global, toolConfigs[tool.Name()])
			t.Cleanup(func() { configurable.Configure(GlobalConfig{}, nil) })
		}
		if h, ok := tool.(*Herdr); ok {
			// The socket binding is emitted only once Start has attached a
			// proxy; Bindings checks the pointer for nil and nothing else.
			saved := h.proxy
			h.proxy = &herdrproxy.Proxy{}
			t.Cleanup(func() { h.proxy = saved })
		}

		bindings := tool.Bindings(homeDir, sandboxHome)
		if _, ok := tool.(ToolWithSharedTmp); ok {
			bindings = append(bindings, SharedTmpBinding(homeDir, sandboxHome)...)
		}
		for _, b := range bindings {
			if b.Type != MountBind || b.ReadOnly {
				continue
			}
			if !underDir(homeDir, b.Source) {
				continue
			}
			if what, ok := exceptions[b.Source]; ok {
				seen[what] = true
				continue
			}
			t.Errorf("tool %s emits a writable bind of %s (dest %q) under the host home; seed a copy into the sandbox home instead", tool.Name(), b.Source, b.Dest)
		}
	}

	for _, what := range exceptions {
		if !seen[what] {
			t.Errorf("the %s exception was never exercised: the fixture no longer reaches the binding it excuses", what)
		}
	}
}

// underDir reports whether path is dir or lexically inside it.
func underDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
