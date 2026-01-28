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

	b.ShareNet()
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

	miseDirs := []string{
		filepath.Join(home, ".config", "mise"),
		filepath.Join(home, ".local", "share", "mise"),
		filepath.Join(home, ".cache", "mise"),
		filepath.Join(home, ".local", "state", "mise"),
	}
	for _, d := range miseDirs {
		b.BindIfExists(d, d)
	}

	b.ROBindIfExists(filepath.Join(home, ".config", "fish"), filepath.Join(home, ".config", "fish"))
	b.ROBindIfExists(
		filepath.Join(home, ".local", "share", "fish", "vendor_completions.d"),
		filepath.Join(home, ".local", "share", "fish", "vendor_completions.d"),
	)

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

	envFiles := []string{".env", ".env.local", ".env.development", ".env.production", ".env.staging", ".env.test"}
	for _, envFile := range envFiles {
		envPath := filepath.Join(b.cfg.ProjectDir, envFile)
		if pathExists(envPath) {
			b.ROBind("/dev/null", envPath)
		}
	}

	subdirs := []string{"src", "app", "server", "client", "packages"}
	for _, subdir := range subdirs {
		subdirPath := filepath.Join(b.cfg.ProjectDir, subdir)
		if pathExists(subdirPath) {
			for _, envFile := range []string{".env", ".env.local", ".env.development", ".env.production"} {
				envPath := filepath.Join(subdirPath, envFile)
				if pathExists(envPath) {
					b.ROBind("/dev/null", envPath)
				}
			}
		}
	}

	b.Tmpfs(b.cfg.XDGRuntime)

	return b
}

func (b *Builder) AddAIToolBindings() *Builder {
	home := b.cfg.HomeDir

	claudeDirs := []string{
		filepath.Join(home, ".claude"),
		filepath.Join(home, ".config", "Claude"),
		filepath.Join(home, ".cache", "claude-cli-nodejs"),
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

func (b *Builder) AddEnvironment() *Builder {
	home := b.cfg.HomeDir

	b.SetEnv("HOME", home)
	b.SetEnv("USER", os.Getenv("USER"))
	b.SetEnv("LOGNAME", os.Getenv("LOGNAME"))
	b.SetEnv("SHELL", "/usr/bin/fish")
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

	b.SetEnv("EDITOR", "nvim")
	b.SetEnv("VISUAL", "nvim")

	b.SetEnvIfSet("COLORTERM")
	b.SetEnvIfSet("COLUMNS")
	b.SetEnvIfSet("LINES")

	b.SetEnv("MISE_SHELL", "fish")

	b.SetEnv("SANDBOX", "1")
	b.SetEnv("SANDBOX_PROJECT", b.cfg.ProjectName)

	return b
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
