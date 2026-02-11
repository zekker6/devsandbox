package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"devsandbox/internal/config"
	"devsandbox/internal/sandbox/mounts"
	"devsandbox/internal/sandbox/tools"
)

const (
	// initialArgsCapacity is the initial capacity for bwrap arguments slice.
	initialArgsCapacity = 128
	// initialMountsCapacity is the initial capacity for mount tracking slice.
	initialMountsCapacity = 64
)

// getCaller returns the name of the calling function (skipping n frames).
func getCaller(skip int) string {
	pc, _, _, ok := runtime.Caller(skip)
	if !ok {
		return "unknown"
	}
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return "unknown"
	}
	// Extract just the function name
	name := fn.Name()
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	return name
}

// mountInfo tracks information about a mount for conflict detection.
type mountInfo struct {
	dest     string
	source   string
	readOnly bool
	caller   string // function that added this mount for error messages
}

type Builder struct {
	cfg            *Config
	args           []string
	overlaySrcSeen bool // tracks if OverlaySrc was called before overlay mount
	mounts         []mountInfo
	err            error // captures errors from build steps (e.g., critical tool setup failures)
}

func NewBuilder(cfg *Config) *Builder {
	return &Builder{
		cfg:    cfg,
		args:   make([]string, 0, initialArgsCapacity),
		mounts: make([]mountInfo, 0, initialMountsCapacity),
	}
}

// logWarnf logs a warning via the configured logger, if any.
func (b *Builder) logWarnf(format string, args ...any) {
	if b.cfg.Logger != nil {
		b.cfg.Logger.Warnf(format, args...)
	}
}

// trackMount records a mount and checks for conflicts.
// Panics if:
// - The exact destination was already mounted (ambiguous)
// - This mount would shadow an existing child mount (parent after child)
//
// Note: We use filepath.Clean on destination paths, not symlink resolution,
// because destinations are paths in the sandbox namespace (not host paths).
// Symlink resolution would incorrectly resolve host symlinks like /etc/localtime.
func (b *Builder) trackMount(dest, source string, readOnly bool, caller string) {
	dest = filepath.Clean(dest)

	for _, existing := range b.mounts {
		existingDest := filepath.Clean(existing.dest)

		// Check for exact same destination
		if dest == existingDest {
			panic(fmt.Sprintf(
				"builder: ambiguous mount - %s already mounted by %s, cannot mount again by %s\n"+
					"  existing: %s -> %s (ro=%v)\n"+
					"  new:      %s -> %s (ro=%v)",
				dest, existing.caller, caller,
				existing.source, existingDest, existing.readOnly,
				source, dest, readOnly,
			))
		}

		// Check if new mount is a parent of existing mount (would shadow it)
		// A parent mount after a child mount shadows the child
		if isParentPath(dest, existingDest) {
			panic(fmt.Sprintf(
				"builder: mount ordering error - mounting parent %s after child %s would shadow it\n"+
					"  child mounted by:  %s (%s -> %s, ro=%v)\n"+
					"  parent mounted by: %s (%s -> %s, ro=%v)\n"+
					"  Fix: mount parent paths before child paths",
				dest, existingDest,
				existing.caller, existing.source, existingDest, existing.readOnly,
				caller, source, dest, readOnly,
			))
		}
	}

	b.mounts = append(b.mounts, mountInfo{
		dest:     dest,
		source:   source,
		readOnly: readOnly,
		caller:   caller,
	})
}

// isParentPath checks if parent is a parent directory of child.
// Both paths should already be cleaned before calling.
func isParentPath(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)

	if parent == child {
		return false
	}

	// Ensure parent ends with separator for proper prefix check
	if !strings.HasSuffix(parent, string(filepath.Separator)) {
		parent += string(filepath.Separator)
	}

	return strings.HasPrefix(child, parent)
}

func (b *Builder) Build() []string {
	return b.args
}

// Err returns any error that occurred during building.
// This should be checked after all Add* methods are called.
func (b *Builder) Err() error {
	return b.err
}

func (b *Builder) add(args ...string) {
	b.args = append(b.args, args...)
}

func (b *Builder) ClearEnv() *Builder {
	b.add("--clearenv")
	return b
}

func (b *Builder) UnshareUser() *Builder {
	b.add("--unshare-user")
	return b
}

func (b *Builder) UnsharePID() *Builder {
	b.add("--unshare-pid")
	return b
}

func (b *Builder) UnshareIPC() *Builder {
	b.add("--unshare-ipc")
	return b
}

func (b *Builder) UnshareUTS() *Builder {
	b.add("--unshare-uts")
	return b
}

func (b *Builder) DieWithParent() *Builder {
	b.add("--die-with-parent")
	return b
}

func (b *Builder) Proc(dest string) *Builder {
	b.add("--proc", dest)
	return b
}

func (b *Builder) Dev(dest string) *Builder {
	b.add("--dev", dest)
	return b
}

func (b *Builder) Tmpfs(dest string) *Builder {
	b.add("--tmpfs", dest)
	return b
}

func (b *Builder) ROBind(src, dest string) *Builder {
	b.trackMount(dest, src, true, getCaller(2))
	b.add("--ro-bind", src, dest)
	return b
}

func (b *Builder) ROBindIfExists(src, dest string) *Builder {
	if pathExists(src) {
		b.trackMount(dest, src, true, getCaller(2))
		b.add("--ro-bind", src, dest)
	}
	return b
}

func (b *Builder) Bind(src, dest string) *Builder {
	b.trackMount(dest, src, false, getCaller(2))
	b.add("--bind", src, dest)
	return b
}

func (b *Builder) BindIfExists(src, dest string) *Builder {
	if pathExists(src) {
		b.trackMount(dest, src, false, getCaller(2))
		b.add("--bind", src, dest)
	}
	return b
}

func (b *Builder) Symlink(target, linkPath string) *Builder {
	b.add("--symlink", target, linkPath)
	return b
}

// OverlaySrc adds a source directory for the following overlay mount.
// Multiple sources are stacked (first call is bottom layer).
// Must be called before TmpOverlay, Overlay, or ROOverlay.
func (b *Builder) OverlaySrc(src string) *Builder {
	b.add("--overlay-src", src)
	b.overlaySrcSeen = true
	return b
}

// OverlaySrcIfExists adds a source directory if it exists.
func (b *Builder) OverlaySrcIfExists(src string) *Builder {
	if pathExists(src) {
		b.OverlaySrc(src)
	}
	return b
}

// requireOverlaySrc panics if OverlaySrc was not called before an overlay mount.
func (b *Builder) requireOverlaySrc(method string) {
	if !b.overlaySrcSeen {
		panic(fmt.Sprintf("builder: %s called without preceding OverlaySrc", method))
	}
}

// TmpOverlay mounts overlayfs with writes to an invisible tmpfs.
// Changes are lost when the sandbox exits.
// Panics if OverlaySrc was not called first.
func (b *Builder) TmpOverlay(dest string) *Builder {
	b.requireOverlaySrc("TmpOverlay")
	b.trackMount(dest, "overlay:tmpfs", false, getCaller(2))
	b.add("--tmp-overlay", dest)
	b.overlaySrcSeen = false // reset for next overlay
	return b
}

// Overlay mounts overlayfs with persistent writes.
// rwSrc: host directory for writes (upper layer)
// workDir: work directory (must be on same filesystem as rwSrc)
// dest: mount point inside sandbox
// Panics if OverlaySrc was not called first.
func (b *Builder) Overlay(rwSrc, workDir, dest string) *Builder {
	b.requireOverlaySrc("Overlay")
	b.trackMount(dest, "overlay:"+rwSrc, false, getCaller(2))
	b.add("--overlay", rwSrc, workDir, dest)
	b.overlaySrcSeen = false // reset for next overlay
	return b
}

// ROOverlay mounts overlayfs read-only.
// Panics if OverlaySrc was not called first.
func (b *Builder) ROOverlay(dest string) *Builder {
	b.requireOverlaySrc("ROOverlay")
	b.trackMount(dest, "overlay:ro", true, getCaller(2))
	b.add("--ro-overlay", dest)
	b.overlaySrcSeen = false // reset for next overlay
	return b
}

func (b *Builder) Dir(path string) *Builder {
	b.add("--dir", path)
	return b
}

func (b *Builder) ShareNet() *Builder {
	b.add("--share-net")
	return b
}

func (b *Builder) Chdir(path string) *Builder {
	b.add("--chdir", path)
	return b
}

func (b *Builder) SetEnv(name, value string) *Builder {
	b.add("--setenv", name, value)
	return b
}

func (b *Builder) SetEnvIfSet(name string) *Builder {
	if value := os.Getenv(name); value != "" {
		b.SetEnv(name, value)
	}
	return b
}

func (b *Builder) AddBaseArgs() *Builder {
	b.ClearEnv().
		UnshareUser().
		UnsharePID().
		UnshareIPC().
		UnshareUTS().
		DieWithParent().
		Proc("/proc").
		Dev("/dev").
		Tmpfs("/tmp")

	// Map current user inside the sandbox (prevents running as root)
	uid := os.Getuid()
	gid := os.Getgid()
	b.add("--uid", fmt.Sprintf("%d", uid))
	b.add("--gid", fmt.Sprintf("%d", gid))

	return b
}

func (b *Builder) AddSystemBindings() *Builder {
	b.ROBind("/usr", "/usr")
	b.addLibBinding("/lib", "usr/lib")
	b.addLibBinding("/lib64", "usr/lib64")
	b.addLibBinding("/bin", "usr/bin")
	b.addLibBinding("/sbin", "usr/sbin")

	return b
}

func (b *Builder) addLibBinding(path, symlinkTarget string) {
	info, err := os.Lstat(path)
	if err != nil {
		return
	}

	if info.Mode()&os.ModeSymlink != 0 {
		b.Symlink(symlinkTarget, path)
	} else if info.IsDir() {
		b.ROBind(path, path)
	}
}

func (b *Builder) AddNetworkBindings() *Builder {
	networkFiles := []string{
		"/etc/resolv.conf",
		"/etc/hosts",
		"/etc/ssl",
		"/etc/passwd",
		"/etc/group",
		"/etc/nsswitch.conf",
	}

	for _, f := range networkFiles {
		b.ROBindIfExists(f, f)
	}

	return b
}

func (b *Builder) AddLocaleBindings() *Builder {
	localeFiles := []string{
		"/etc/locale.gen",
		"/etc/localtime",
	}

	for _, f := range localeFiles {
		b.ROBindIfExists(f, f)
	}

	b.ROBindIfExists("/usr/share/zoneinfo", "/usr/share/zoneinfo")

	return b
}

func (b *Builder) AddCABindings() *Builder {
	caPaths := []string{
		"/etc/ca-certificates",
		"/etc/pki/tls/certs",
		"/etc/ssl/certs",
	}

	for _, p := range caPaths {
		b.ROBindIfExists(p, p)
	}

	return b
}

func (b *Builder) AddSandboxHome() *Builder {
	home := b.cfg.HomeDir

	// Use shared network unless proxy mode is enabled.
	// Proxy mode uses pasta which creates an isolated network namespace
	// where all traffic goes through the gateway to our proxy.
	if !b.cfg.ProxyEnabled {
		b.ShareNet()
	}
	b.Bind(b.cfg.SandboxHome, home)

	homeDirs := []string{
		filepath.Join(home, ".config"),
		filepath.Join(home, ".cache"),
		filepath.Join(home, ".local", "share"),
		filepath.Join(home, ".local", "state"),
		filepath.Join(home, ".local", "bin"),
	}

	for _, d := range homeDirs {
		b.Dir(d)
	}

	return b
}

// AddTools applies bindings from all available tools in the registry.
// Tools are discovered automatically based on what's installed on the host.
func (b *Builder) AddTools() *Builder {
	home := b.cfg.HomeDir
	sandboxHome := b.cfg.SandboxHome

	// Configure tools that support it
	for _, tool := range tools.Available(home) {
		if configurable, ok := tool.(tools.ToolWithConfig); ok {
			b.configureTool(configurable, tool.Name())
		}
	}

	// Run setup for tools that need it (e.g., generate safe gitconfig, starship config)
	for _, tool := range tools.Available(home) {
		if setup, ok := tool.(tools.ToolWithSetup); ok {
			if err := setup.Setup(home, sandboxHome); err != nil {
				b.err = fmt.Errorf("tool setup failed for %s: %w", tool.Name(), err)
				return b
			}
		}
	}

	// Apply bindings from all available tools
	for _, tool := range tools.Available(home) {
		for _, binding := range tool.Bindings(home, sandboxHome) {
			b.applyBinding(binding, sandboxHome)
		}
	}

	return b
}

// configureTool applies configuration to a tool based on sandbox config.
func (b *Builder) configureTool(tool tools.ToolWithConfig, toolName string) {
	// Build global config
	globalCfg := tools.GlobalConfig{
		OverlayEnabled: b.cfg.OverlayEnabled,
		ProjectDir:     b.cfg.ProjectDir,
		HomeDir:        b.cfg.HomeDir,
	}

	// Get tool's config section from ToolsConfig
	var toolCfg map[string]any
	if b.cfg.ToolsConfig != nil {
		if section, ok := b.cfg.ToolsConfig[toolName]; ok {
			toolCfg, _ = section.(map[string]any)
		}
	}

	tool.Configure(globalCfg, toolCfg)
}

// applyBinding applies a single binding based on its type.
func (b *Builder) applyBinding(binding tools.Binding, sandboxHome string) {
	dest := binding.Dest
	if dest == "" {
		dest = binding.Source
	}

	switch binding.Type {
	case tools.MountTmpOverlay:
		b.applyTmpOverlay(binding, dest)

	case tools.MountOverlay:
		b.applyPersistentOverlay(binding, dest, sandboxHome)

	default: // MountBind or empty
		b.applyBindMount(binding, dest)
	}
}

// applyBindMount applies a regular bind mount.
func (b *Builder) applyBindMount(binding tools.Binding, dest string) {
	if binding.Optional {
		if binding.ReadOnly {
			b.ROBindIfExists(binding.Source, dest)
		} else {
			b.BindIfExists(binding.Source, dest)
		}
	} else {
		if binding.ReadOnly {
			b.ROBind(binding.Source, dest)
		} else {
			b.Bind(binding.Source, dest)
		}
	}
}

// applyTmpOverlay applies an overlay with writes to tmpfs (discarded on exit).
func (b *Builder) applyTmpOverlay(binding tools.Binding, dest string) {
	// Check if source exists when optional
	if binding.Optional && !pathExists(binding.Source) {
		return
	}

	// Add additional overlay sources (lower layers)
	for _, src := range binding.OverlaySources {
		if binding.Optional {
			b.OverlaySrcIfExists(src)
		} else {
			b.OverlaySrc(src)
		}
	}

	// Add primary source and mount
	b.OverlaySrc(binding.Source)
	b.TmpOverlay(dest)
}

// applyPersistentOverlay applies an overlay with persistent writes.
func (b *Builder) applyPersistentOverlay(binding tools.Binding, dest string, sandboxHome string) {
	// Check if source exists when optional
	if binding.Optional && !pathExists(binding.Source) {
		return
	}

	upperDir, workDir, err := createOverlayDirs(sandboxHome, dest, "")
	if err != nil {
		b.logWarnf("overlay dirs: %v", err)
		return
	}

	// Add additional overlay sources (lower layers)
	for _, src := range binding.OverlaySources {
		if binding.Optional {
			b.OverlaySrcIfExists(src)
		} else {
			b.OverlaySrc(src)
		}
	}

	// Add primary source and mount with persistent upper layer
	b.OverlaySrc(binding.Source)
	b.Overlay(upperDir, workDir, dest)
}

// AddCustomMounts applies custom mount rules from configuration.
// This handles mounting additional paths, hiding files, and setting up overlays.
// Note: Paths inside the project directory are handled by AddProjectBindings() instead.
func (b *Builder) AddCustomMounts() *Builder {
	if b.cfg.MountsConfig == nil {
		return b
	}

	engine := b.cfg.MountsConfig
	if len(engine.Rules()) == 0 {
		return b
	}

	// Get all expanded paths with their rules and sort for deterministic ordering
	expandedPaths := engine.ExpandedPaths()
	paths := make([]string, 0, len(expandedPaths))
	for path := range expandedPaths {
		// Skip paths inside project directory - they're handled in AddProjectBindings()
		if b.isInsideProject(path) {
			continue
		}
		paths = append(paths, path)
	}
	sortedPaths := sortPaths(paths)

	// Apply mounts in sorted order
	for _, path := range sortedPaths {
		rule := expandedPaths[path]
		b.applyMountRule(path, rule)
	}

	return b
}

// isInsideProject checks if a path is inside the project directory.
func (b *Builder) isInsideProject(path string) bool {
	projectDir := filepath.Clean(b.cfg.ProjectDir)
	cleanPath := filepath.Clean(path)

	// Check if path is the project dir itself or inside it
	if cleanPath == projectDir {
		return true
	}

	// Check if path is a child of project dir
	return strings.HasPrefix(cleanPath, projectDir+string(filepath.Separator))
}

// applyMountRule applies a single custom mount rule to a path.
func (b *Builder) applyMountRule(path string, rule mounts.Rule) {
	info, err := os.Stat(path)
	if err != nil {
		return // Path doesn't exist
	}

	switch rule.Mode {
	case mounts.ModeHidden:
		if info.IsDir() {
			// Hiding directories is not supported - log and skip
			b.logWarnf("mounts: cannot hide directory %q - use 'readonly', 'overlay', or 'tmpoverlay' mode instead (pattern: %s)", path, rule.Pattern)
			return
		}
		// For files within mounted paths, overlay with /dev/null
		b.ROBind("/dev/null", path)

	case mounts.ModeReadOnly:
		b.ROBind(path, path)

	case mounts.ModeReadWrite:
		b.Bind(path, path)

	case mounts.ModeOverlay:
		b.applyCustomOverlay(path, rule, false)

	case mounts.ModeTmpOverlay:
		b.applyCustomOverlay(path, rule, true)
	}
}

// sortPaths sorts paths so that parent directories come before children.
// This ensures deterministic mount ordering.
func sortPaths(paths []string) []string {
	sorted := make([]string, len(paths))
	copy(sorted, paths)

	// Sort by path length first (shorter paths = parents), then lexicographically
	slices.SortFunc(sorted, func(a, b string) int {
		if len(a) != len(b) {
			return len(a) - len(b)
		}
		return strings.Compare(a, b)
	})
	return sorted
}

// applyCustomOverlay applies an overlay mount for a custom mount rule.
func (b *Builder) applyCustomOverlay(path string, rule mounts.Rule, tmpfs bool) {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		b.logWarnf("mounts: overlay only supported for directories: %s (pattern: %s)", path, rule.Pattern)
		return
	}

	b.OverlaySrc(path)

	if tmpfs {
		b.TmpOverlay(path)
		return
	}

	upperDir, workDir, err := createOverlayDirs(b.cfg.SandboxHome, path, "custom")
	if err != nil {
		b.logWarnf("mounts: %v", err)
		return
	}

	b.Overlay(upperDir, workDir, path)
}

func (b *Builder) AddProjectBindings() *Builder {
	b.Bind(b.cfg.ProjectDir, b.cfg.ProjectDir)
	b.Chdir(b.cfg.ProjectDir)

	// Handle .devsandbox.toml visibility
	configPath := filepath.Join(b.cfg.ProjectDir, config.LocalConfigFile)
	if _, err := os.Stat(configPath); err == nil {
		switch b.cfg.ConfigVisibility {
		case string(config.ConfigVisibilityHidden), "":
			// Hide config file (default)
			b.ROBind("/dev/null", configPath)
		case string(config.ConfigVisibilityReadOnly):
			// Expose as read-only (re-bind as read-only over the read-write project bind)
			b.ROBind(configPath, configPath)
		case string(config.ConfigVisibilityReadWrite):
			// Already writable from project bind, nothing to do
		}
	}

	// Apply custom mount rules for paths inside the project directory
	// This must happen AFTER the project is bound
	b.applyProjectCustomMounts()

	// Hide .env files in project directory
	for _, path := range FindEnvFiles(b.cfg.ProjectDir, 3) {
		b.ROBind("/dev/null", path)
	}

	b.Tmpfs(b.cfg.XDGRuntime)

	return b
}

// applyProjectCustomMounts applies custom mount rules for paths inside the project directory.
// Called after the project is bound to avoid mount ordering conflicts.
func (b *Builder) applyProjectCustomMounts() {
	if b.cfg.MountsConfig == nil {
		return
	}

	engine := b.cfg.MountsConfig
	if len(engine.Rules()) == 0 {
		return
	}

	// Collect paths from two sources:
	// 1. Absolute paths from ExpandedPaths() that happen to be inside the project
	// 2. Relative patterns expanded within the project directory
	expandedPaths := make(map[string]mounts.Rule)

	// Source 1: Absolute paths inside the project
	globalPaths := engine.ExpandedPaths()
	for path, rule := range globalPaths {
		if b.isInsideProject(path) {
			expandedPaths[path] = rule
		}
	}

	// Source 2: Relative patterns expanded in project directory
	projectPaths := engine.ExpandedPathsInDir(b.cfg.ProjectDir)
	for path, rule := range projectPaths {
		if _, exists := expandedPaths[path]; !exists {
			expandedPaths[path] = rule
		}
	}

	if len(expandedPaths) == 0 {
		return
	}

	// Sort paths for deterministic ordering
	paths := make([]string, 0, len(expandedPaths))
	for path := range expandedPaths {
		paths = append(paths, path)
	}
	sortedPaths := sortPaths(paths)

	// Apply mounts
	for _, path := range sortedPaths {
		rule := expandedPaths[path]
		b.applyMountRule(path, rule)
	}
}

func (b *Builder) AddEnvironment() *Builder {
	home := b.cfg.HomeDir
	sandboxHome := b.cfg.SandboxHome

	// Core environment
	b.SetEnv("HOME", home)
	b.SetEnv("USER", os.Getenv("USER"))
	b.SetEnv("LOGNAME", os.Getenv("LOGNAME"))
	b.SetEnv("SHELL", b.cfg.ShellPath)
	b.SetEnv("TERM", os.Getenv("TERM"))
	b.SetEnv("LANG", os.Getenv("LANG"))

	path := fmt.Sprintf("%s/.local/share/mise/shims:%s/.local/bin:/usr/local/bin:/usr/bin:/bin",
		home, home)
	b.SetEnv("PATH", path)

	// XDG directories
	b.SetEnv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	b.SetEnv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	b.SetEnv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	b.SetEnv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	b.SetEnv("XDG_RUNTIME_DIR", b.cfg.XDGRuntime)

	// Terminal settings
	b.SetEnvIfSet("COLORTERM")
	b.SetEnvIfSet("COLUMNS")
	b.SetEnvIfSet("LINES")

	// Mise shell integration
	b.SetEnv("MISE_SHELL", string(b.cfg.Shell))

	// Sandbox markers
	b.SetEnv("DEVSANDBOX", "1")
	b.SetEnv("DEVSANDBOX_PROJECT", b.cfg.ProjectName)

	// Add environment from all available tools
	for _, tool := range tools.Available(home) {
		for _, env := range tool.Environment(home, sandboxHome) {
			if env.FromHost {
				b.SetEnvIfSet(env.Name)
			} else {
				b.SetEnv(env.Name, env.Value)
			}
		}
	}

	// Add proxy environment if enabled
	if b.cfg.ProxyEnabled {
		b.AddProxyEnvironment()
	}

	return b
}

// SuppressSSHAgent prevents shell plugins (e.g. fish-ssh-agent) from producing
// warnings or starting unnecessary ssh-agent processes when SSH is not enabled.
//
// When SSH is not mounted by any tool:
//   - Creates an empty ~/.ssh/environment file so shell plugins don't error on missing file
//   - Places a no-op ssh-agent wrapper in ~/.local/bin/ which shadows /usr/bin/ssh-agent
//     (PATH includes ~/.local/bin before /usr/bin)
//
// When SSH IS mounted, any leftover wrapper from previous runs is cleaned up.
//
// Must be called after AddTools() so mount information is available.
func (b *Builder) SuppressSSHAgent() *Builder {
	sshDir := filepath.Join(b.cfg.HomeDir, ".ssh")

	// Check if .ssh is already mounted (SSH is enabled via a tool)
	sshEnabled := false
	for _, m := range b.mounts {
		if filepath.Clean(m.dest) == filepath.Clean(sshDir) {
			sshEnabled = true
			break
		}
	}

	wrapperPath := filepath.Join(b.cfg.SandboxHome, ".local", "bin", "ssh-agent")

	if sshEnabled {
		// Remove leftover wrapper from previous runs without SSH
		_ = os.Remove(wrapperPath)
		return b
	}

	// Create .ssh/environment in sandbox home to prevent "file not found" errors
	hostSSHDir := filepath.Join(b.cfg.SandboxHome, ".ssh")
	if err := os.MkdirAll(hostSSHDir, 0o700); err != nil {
		b.logWarnf("failed to create .ssh dir in sandbox home: %v", err)
		return b
	}

	envFile := filepath.Join(hostSSHDir, "environment")
	if err := os.WriteFile(envFile, nil, 0o600); err != nil {
		b.logWarnf("failed to create .ssh/environment: %v", err)
	}

	// Create no-op ssh-agent wrapper in ~/.local/bin/ to shadow the real binary.
	// PATH includes ~/.local/bin before /usr/bin, so this takes precedence.
	hostLocalBin := filepath.Join(b.cfg.SandboxHome, ".local", "bin")
	if err := os.MkdirAll(hostLocalBin, 0o755); err != nil {
		b.logWarnf("failed to create .local/bin in sandbox home: %v", err)
		return b
	}

	if err := os.WriteFile(wrapperPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		b.logWarnf("failed to create ssh-agent wrapper: %v", err)
	}

	return b
}

func (b *Builder) AddProxyEnvironment() *Builder {
	proxyURL := fmt.Sprintf("http://%s:%d", b.cfg.GatewayIP, b.cfg.ProxyPort)

	b.SetEnv("HTTP_PROXY", proxyURL)
	b.SetEnv("HTTPS_PROXY", proxyURL)
	b.SetEnv("http_proxy", proxyURL)
	b.SetEnv("https_proxy", proxyURL)
	b.SetEnv("NO_PROXY", "localhost,127.0.0.1")
	b.SetEnv("no_proxy", "localhost,127.0.0.1")

	// CA certificate path for various tools
	// We use /tmp because /etc/ssl is mounted read-only from host
	caCertPath := "/tmp/devsandbox-ca.crt"
	b.SetEnv("REQUESTS_CA_BUNDLE", caCertPath)
	b.SetEnv("NODE_EXTRA_CA_CERTS", caCertPath)
	b.SetEnv("CURL_CA_BUNDLE", caCertPath)
	b.SetEnv("GIT_SSL_CAINFO", caCertPath)
	b.SetEnv("SSL_CERT_FILE", caCertPath)

	b.SetEnv("DEVSANDBOX_PROXY", "1")

	return b
}

func (b *Builder) AddProxyCACertificate() *Builder {
	if !b.cfg.ProxyEnabled || b.cfg.ProxyCAPath == "" {
		return b
	}

	// Mount CA certificate to /tmp (which is a fresh tmpfs)
	// We can't mount to /etc/ssl/certs because it's bind-mounted read-only from host
	caCertDest := "/tmp/devsandbox-ca.crt"
	b.ROBindIfExists(b.cfg.ProxyCAPath, caCertDest)

	return b
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
