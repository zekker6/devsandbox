package sandbox

import (
	"fmt"
	"os"
	"path/filepath"

	"devsandbox/internal/sandbox/tools"
)

type Builder struct {
	cfg  *Config
	args []string
}

func NewBuilder(cfg *Config) *Builder {
	return &Builder{
		cfg:  cfg,
		args: make([]string, 0, 128),
	}
}

func (b *Builder) Build() []string {
	return b.args
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
	b.add("--ro-bind", src, dest)
	return b
}

func (b *Builder) ROBindIfExists(src, dest string) *Builder {
	if pathExists(src) {
		b.ROBind(src, dest)
	}
	return b
}

func (b *Builder) Bind(src, dest string) *Builder {
	b.add("--bind", src, dest)
	return b
}

func (b *Builder) BindIfExists(src, dest string) *Builder {
	if pathExists(src) {
		b.Bind(src, dest)
	}
	return b
}

func (b *Builder) Symlink(target, linkPath string) *Builder {
	b.add("--symlink", target, linkPath)
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

	if pathExists("/opt/claude-code") {
		b.ROBind("/opt/claude-code", "/opt/claude-code")
	}

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

	// Run setup for tools that need it (e.g., generate safe gitconfig, starship config)
	for _, tool := range tools.Available(home) {
		if setup, ok := tool.(tools.ToolWithSetup); ok {
			_ = setup.Setup(home, sandboxHome) // Ignore errors, bindings are optional
		}
	}

	// Apply bindings from all available tools
	for _, tool := range tools.Available(home) {
		for _, binding := range tool.Bindings(home, sandboxHome) {
			dest := binding.Dest
			if dest == "" {
				dest = binding.Source
			}

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
	}

	return b
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
