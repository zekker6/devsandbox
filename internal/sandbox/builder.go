package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
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
	return b.ClearEnv().
		UnsharePID().
		DieWithParent().
		Proc("/proc").
		Dev("/dev").
		Tmpfs("/tmp")
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

func (b *Builder) AddToolBindings() *Builder {
	home := b.cfg.HomeDir

	b.ROBindIfExists(filepath.Join(home, ".local", "bin"), filepath.Join(home, ".local", "bin"))

	// Mise directories - read-only to prevent sandbox from modifying host's tool state
	miseDirs := []string{
		filepath.Join(home, ".config", "mise"),
		filepath.Join(home, ".local", "share", "mise"),
		filepath.Join(home, ".cache", "mise"),
		filepath.Join(home, ".local", "state", "mise"),
	}
	for _, d := range miseDirs {
		b.ROBindIfExists(d, d)
	}

	// Shell-specific config bindings
	b.AddShellConfigBindings()

	nvimDirs := []struct {
		path string
		ro   bool
	}{
		{filepath.Join(home, ".config", "nvim"), true},
		{filepath.Join(home, ".local", "share", "nvim"), true},
		{filepath.Join(home, ".local", "state", "nvim"), true},
		{filepath.Join(home, ".cache", "nvim"), true},
	}
	for _, d := range nvimDirs {
		if d.ro {
			b.ROBindIfExists(d.path, d.path)
		} else {
			b.BindIfExists(d.path, d.path)
		}
	}

	// Pywal (wal) cache for terminal colors
	b.ROBindIfExists(filepath.Join(home, ".cache", "wal"), filepath.Join(home, ".cache", "wal"))

	return b
}

func (b *Builder) AddGitConfig() *Builder {
	home := b.cfg.HomeDir
	gitconfigPath := filepath.Join(home, ".gitconfig")

	if !pathExists(gitconfigPath) {
		return b
	}

	safeGitconfig := filepath.Join(b.cfg.SandboxHome, ".gitconfig.safe")

	if !pathExists(safeGitconfig) {
		if err := generateSafeGitconfig(safeGitconfig); err != nil {
			return b
		}
	}

	b.ROBind(safeGitconfig, filepath.Join(home, ".gitconfig"))

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

func (b *Builder) AddAIToolBindings() *Builder {
	home := b.cfg.HomeDir

	claudeDirs := []string{
		filepath.Join(home, ".claude"),
		filepath.Join(home, ".config", "Claude"),
		filepath.Join(home, ".cache", "claude-cli-nodejs"),
		filepath.Join(home, ".local", "share", "claude"), // Claude Code installation
	}
	for _, d := range claudeDirs {
		b.BindIfExists(d, d)
	}

	claudeFiles := []string{
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".claude.json.backup"),
	}
	for _, f := range claudeFiles {
		b.BindIfExists(f, f)
	}

	copilotDirs := []string{
		filepath.Join(home, ".config", "github-copilot"),
		filepath.Join(home, ".cache", "github-copilot"),
	}
	for _, d := range copilotDirs {
		b.BindIfExists(d, d)
	}

	opencodeDirs := []string{
		filepath.Join(home, ".config", "opencode"),
		filepath.Join(home, ".local", "share", "opencode"),
		filepath.Join(home, ".cache", "opencode"),
		filepath.Join(home, ".cache", "oh-my-opencode"),
	}
	for _, d := range opencodeDirs {
		b.BindIfExists(d, d)
	}

	return b
}

func (b *Builder) AddShellConfigBindings() *Builder {
	home := b.cfg.HomeDir

	switch b.cfg.Shell {
	case ShellFish:
		b.ROBindIfExists(filepath.Join(home, ".config", "fish"), filepath.Join(home, ".config", "fish"))
		b.ROBindIfExists(
			filepath.Join(home, ".local", "share", "fish", "vendor_completions.d"),
			filepath.Join(home, ".local", "share", "fish", "vendor_completions.d"),
		)
	case ShellZsh:
		b.ROBindIfExists(filepath.Join(home, ".zshrc"), filepath.Join(home, ".zshrc"))
		b.ROBindIfExists(filepath.Join(home, ".zshenv"), filepath.Join(home, ".zshenv"))
		b.ROBindIfExists(filepath.Join(home, ".zprofile"), filepath.Join(home, ".zprofile"))
		b.ROBindIfExists(filepath.Join(home, ".config", "zsh"), filepath.Join(home, ".config", "zsh"))
		// Oh-my-zsh and other zsh frameworks
		b.ROBindIfExists(filepath.Join(home, ".oh-my-zsh"), filepath.Join(home, ".oh-my-zsh"))
		b.ROBindIfExists(filepath.Join(home, ".local", "share", "zsh"), filepath.Join(home, ".local", "share", "zsh"))
	case ShellBash:
		b.ROBindIfExists(filepath.Join(home, ".bashrc"), filepath.Join(home, ".bashrc"))
		b.ROBindIfExists(filepath.Join(home, ".bash_profile"), filepath.Join(home, ".bash_profile"))
		b.ROBindIfExists(filepath.Join(home, ".profile"), filepath.Join(home, ".profile"))
		b.ROBindIfExists(filepath.Join(home, ".config", "bash"), filepath.Join(home, ".config", "bash"))
	}

	// Starship prompt - create sandbox-aware config
	b.setupStarshipConfig()

	return b
}

func (b *Builder) setupStarshipConfig() {
	home := b.cfg.HomeDir
	starshipConfig := filepath.Join(home, ".config", "starship.toml")

	if !pathExists(starshipConfig) {
		return
	}

	sandboxStarshipConfig := filepath.Join(b.cfg.SandboxHome, ".config", "starship.toml")

	configDir := filepath.Join(b.cfg.SandboxHome, ".config")
	_ = os.MkdirAll(configDir, 0o755)

	original, err := os.ReadFile(starshipConfig)
	if err != nil {
		return
	}

	indicator := `

# Sandbox indicator (auto-generated by devsandbox)
[custom.devsandbox]
command = "echo [sandboxed 🔒]"
when = true
format = "[$output]($style) "
style = "bold red"
shell = ["sh"]
`
	modified := string(original) + indicator

	if err := os.WriteFile(sandboxStarshipConfig, []byte(modified), 0o644); err != nil {
		return
	}

	b.ROBind(sandboxStarshipConfig, starshipConfig)
}

func (b *Builder) AddEnvironment() *Builder {
	home := b.cfg.HomeDir

	b.SetEnv("HOME", home)
	b.SetEnv("USER", os.Getenv("USER"))
	b.SetEnv("LOGNAME", os.Getenv("LOGNAME"))
	b.SetEnv("SHELL", b.cfg.ShellPath)
	b.SetEnv("TERM", os.Getenv("TERM"))
	b.SetEnv("LANG", os.Getenv("LANG"))

	path := fmt.Sprintf("%s/.local/share/mise/shims:%s/.local/bin:/usr/local/bin:/usr/bin:/bin",
		home, home)
	b.SetEnv("PATH", path)

	b.SetEnv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	b.SetEnv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	b.SetEnv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	b.SetEnv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	b.SetEnv("XDG_RUNTIME_DIR", b.cfg.XDGRuntime)

	// Isolate Go environment to prevent version conflicts with host
	b.SetEnv("GOPATH", filepath.Join(home, "go"))
	b.SetEnv("GOCACHE", filepath.Join(home, ".cache", "go-build"))
	b.SetEnv("GOMODCACHE", filepath.Join(home, ".cache", "go-mod"))
	// Prevent Go from auto-downloading different toolchains which causes version mismatches
	b.SetEnv("GOTOOLCHAIN", "local")

	b.SetEnv("EDITOR", "nvim")
	b.SetEnv("VISUAL", "nvim")

	b.SetEnvIfSet("COLORTERM")
	b.SetEnvIfSet("COLUMNS")
	b.SetEnvIfSet("LINES")

	b.SetEnv("MISE_SHELL", string(b.cfg.Shell))

	b.SetEnv("DEVSANDBOX", "1")
	b.SetEnv("DEVSANDBOX_PROJECT", b.cfg.ProjectName)

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
