package sandbox

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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
			_ = setup.Setup(home, sandboxHome) // Ignore errors, bindings are optional
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

	// Validate dest path to prevent path traversal
	cleanDest := filepath.Clean(dest)
	if !filepath.IsAbs(cleanDest) {
		log.Printf("warning: overlay dest must be absolute path, got: %s", dest)
		return
	}
	if strings.Contains(cleanDest, "..") {
		log.Printf("warning: overlay dest contains invalid path traversal: %s", dest)
		return
	}

	// Create upper/work directories for persistent storage
	// Use a path based on dest to avoid collisions
	safePath := strings.ReplaceAll(strings.TrimPrefix(cleanDest, "/"), "/", "_")
	overlayDir := filepath.Join(sandboxHome, "overlay", safePath)
	upperDir := filepath.Join(overlayDir, "upper")
	workDir := filepath.Join(overlayDir, "work")

	// Ensure directories exist with proper error handling
	if err := os.MkdirAll(upperDir, 0o755); err != nil {
		log.Printf("warning: failed to create overlay upper dir %s: %v", upperDir, err)
		return
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		log.Printf("warning: failed to create overlay work dir %s: %v", workDir, err)
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

func (b *Builder) AddProjectBindings() *Builder {
	b.Bind(b.cfg.ProjectDir, b.cfg.ProjectDir)
	b.Chdir(b.cfg.ProjectDir)

	// Recursively find and hide all .env files
	_ = filepath.WalkDir(b.cfg.ProjectDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip inaccessible paths
		}

		// Skip directories
		if d.IsDir() {
			// Skip common large directories that won't have env files
			name := d.Name()
			if name == "node_modules" || name == ".git" || name == "vendor" || name == ".venv" {
				return filepath.SkipDir
			}
			return nil
		}

		// Check if file is an env file (.env or .env.*)
		name := d.Name()
		if name == ".env" || (len(name) > 5 && name[:5] == ".env.") {
			b.ROBind("/dev/null", path)
		}

		return nil
	})

	b.Tmpfs(b.cfg.XDGRuntime)

	return b
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
