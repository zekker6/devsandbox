package isolator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"devsandbox/internal/cgroups"
	"devsandbox/internal/cmdpattern"
	"devsandbox/internal/config"
	"devsandbox/internal/notice"
	"devsandbox/internal/proxyenv"
	"devsandbox/internal/sandbox"
	"devsandbox/internal/sandbox/tools"
	"devsandbox/internal/seatbelt"
)

// SeatbeltConfig holds the settings whose enforcement must be checked before launch.
type SeatbeltConfig struct {
	Limits cgroups.Limits
}

// SeatbeltIsolator runs native macOS programs under a deny-by-default SBPL policy.
type SeatbeltIsolator struct {
	config SeatbeltConfig
}

func NewSeatbeltIsolator(cfg SeatbeltConfig) *SeatbeltIsolator {
	return &SeatbeltIsolator{config: cfg}
}

func (s *SeatbeltIsolator) Name() Backend { return BackendSeatbelt }
func (s *SeatbeltIsolator) IsolationType() sandbox.IsolationType {
	return sandbox.IsolationSeatbelt
}
func (s *SeatbeltIsolator) Available() error { return seatbelt.Available() }
func (s *SeatbeltIsolator) Cleanup() error   { return nil }

func (s *SeatbeltIsolator) Preflight(ctx context.Context, _ string) error {
	if !s.config.Limits.IsZero() {
		return errors.New("seatbelt cannot enforce sandbox.resources; remove the limits or use a container backend")
	}
	profile, err := (seatbelt.Policy{}).Profile()
	if err != nil {
		return err
	}
	return seatbelt.Check(ctx, profile)
}

func (s *SeatbeltIsolator) PrepareNetwork(context.Context, string) (*NetworkInfo, error) {
	return &NetworkInfo{BindAddress: "127.0.0.1"}, nil
}

// ValidateConfig runs before tools or the proxy start. A setting that needs a
// different filesystem view or network namespace must not become a no-op.
func (s *SeatbeltIsolator) ValidateConfig(cfg *sandbox.Config, app *config.Config) error {
	if !s.config.Limits.IsZero() {
		return errors.New("seatbelt cannot enforce sandbox.resources; use a container backend for resource limits")
	}
	for _, project := range cmdpattern.ResolvedSpellings(cfg.ProjectDir) {
		for _, protected := range append(cmdpattern.ResolvedSpellings(cfg.HomeDir), cmdpattern.ResolvedSpellings(cfg.SandboxRoot)...) {
			if nativeWithin(project, protected) {
				return errors.New("seatbelt project must not contain the host home or sandbox state directory")
			}
		}
	}
	if mode := nativeGitMode(cfg); mode != "readonly" && mode != "disabled" {
		return errors.New("seatbelt supports git mode readonly or disabled; readwrite credential sharing is not supported")
	}
	if cfg.MountsConfig != nil && len(cfg.MountsConfig.Rules()) != 0 {
		return errors.New("seatbelt does not support sandbox.mounts; it restricts paths but cannot remap them")
	}
	if cfg.DefaultMountMode == "readwrite" {
		return errors.New("seatbelt does not support overlay.default=readwrite; use split, overlay, tmpoverlay, or readonly")
	}
	if app != nil && (app.PortForwarding.IsEnabled() || app.PortForwarding.IsAutoDetectEnabled() || len(app.PortForwarding.Rules) != 0) {
		return errors.New("seatbelt does not support port_forwarding or auto_detect; it has no private network namespace")
	}
	for _, tool := range tools.All() {
		mode := config.ToolMountMode(cfg.ToolsConfig, tool.Name())
		if mode == "disabled" {
			continue
		}
		if mode == "readwrite" {
			return fmt.Errorf("seatbelt: tools.%s.mount_mode=readwrite is not supported; use private copies instead", tool.Name())
		}
		_, active := tool.(tools.ActiveTool)
		_, shared := tool.(tools.ToolWithSharedTmp)
		if (active || shared) && nativeIntegrationRequested(config.ToolSection(cfg.ToolsConfig, tool.Name())) {
			return fmt.Errorf("seatbelt does not support host integration %s; disable it or use another backend", tool.Name())
		}
	}
	for _, name := range cfg.EnvPassthrough {
		if nativeReservedEnv(name) {
			return fmt.Errorf("seatbelt reserves environment variable %s", name)
		}
	}
	for _, name := range append(slices.Clone(cfg.ProxyExtraEnv), cfg.ProxyExtraCAEnv...) {
		if nativeReservedEnv(name) {
			return fmt.Errorf("seatbelt reserves proxy environment variable %s", name)
		}
	}
	for name := range cfg.EnvVars {
		if nativeReservedEnv(name) {
			return fmt.Errorf("seatbelt reserves environment variable %s", name)
		}
	}
	return nil
}

func nativeReservedEnv(name string) bool {
	return name == "HOME" || name == "PATH" || name == "TMPDIR" || name == "SHELL" ||
		strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "DEVSANDBOX") ||
		strings.HasPrefix(name, "DYLD_") || strings.HasPrefix(name, "GIT_")
}

func (s *SeatbeltIsolator) Run(ctx context.Context, cfg *RunConfig) error {
	if err := s.ValidateConfig(cfg.SandboxCfg, cfg.AppCfg); err != nil {
		return err
	}
	if err := s.Available(); err != nil {
		return err
	}
	plan, err := prepareNative(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(plan.setup); err != nil {
			notice.Alert("seatbelt: remove setup files: %v", err)
		}
	}()
	profile, err := plan.policy.Profile()
	if err != nil {
		return fmt.Errorf("seatbelt policy: %w", err)
	}
	if err := seatbelt.Check(ctx, profile); err != nil {
		return err
	}
	cmd := nativeCommand(ctx, cfg, profile, plan.env)
	// Native processes have no namespace PID. Do not invoke the Linux
	// OnSandboxStart callback, which installs procfs-based port forwarding.
	return runNativeCommand(cmd, cfg.OnNativeStart)
}

func nativeCommand(ctx context.Context, cfg *RunConfig, profile string, env map[string]string) *exec.Cmd {
	args := append([]string{"-p", profile}, sandbox.BuildShellCommand(cfg.SandboxCfg, cfg.Command)...)
	cmd := exec.CommandContext(ctx, seatbelt.Executable, args...)
	cmd.Dir = cfg.SandboxCfg.ProjectDir
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		cmd.Env = append(cmd.Env, key+"="+env[key])
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd
}

type nativePlan struct {
	setup        string
	policy       seatbelt.Policy
	env          map[string]string
	seeds        map[string]bool
	installSeeds map[string]bool
}

func prepareNative(run *RunConfig) (_ *nativePlan, retErr error) {
	cfg := run.SandboxCfg
	// Resolve before granting access. /var and /tmp are aliases on macOS; an
	// allow must name the inode's path, not silently cover an unrelated alias.
	for _, field := range []*string{&cfg.ProjectDir, &cfg.HomeDir, &cfg.SandboxRoot, &cfg.SandboxHome} {
		resolved, err := filepath.EvalSymlinks(*field)
		if err != nil {
			return nil, fmt.Errorf("seatbelt resolve %s: %w", *field, err)
		}
		*field = resolved
	}
	if nativeWithin(cfg.ProjectDir, cfg.HomeDir) || nativeWithin(cfg.ProjectDir, cfg.SandboxRoot) {
		return nil, errors.New("seatbelt project must not contain the host home or sandbox state directory")
	}
	if cfg.SandboxHome != filepath.Join(cfg.SandboxRoot, "home") {
		return nil, errors.New("seatbelt private home must not be a symlink outside its sandbox root")
	}
	setup := filepath.Join(cfg.SandboxRoot, "native-setup")
	if err := os.RemoveAll(setup); err != nil {
		return nil, fmt.Errorf("seatbelt remove stale setup: %w", err)
	}
	if err := os.Mkdir(setup, 0o700); err != nil {
		return nil, err
	}
	defer func() {
		if retErr != nil {
			if err := os.RemoveAll(setup); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("seatbelt cleanup: %w", err))
			}
		}
	}()
	root, err := os.OpenRoot(cfg.SandboxHome)
	if err != nil {
		return nil, err
	}
	defer func() { retErr = errors.Join(retErr, root.Close()) }()
	p := &nativePlan{
		setup:        setup,
		seeds:        make(map[string]bool),
		installSeeds: make(map[string]bool),
		policy: seatbelt.Policy{
			Writable: []string{cfg.ProjectDir, cfg.SandboxHome},
			Network:  true, HideEnvFiles: cfg.HideEnvFiles,
			ProtectConfig: cfg.ConfigVisibility != "readwrite", ConfigHidden: cfg.ConfigVisibility == "hidden",
		},
		env: nativeBaseEnv(cfg),
	}
	if cfg.ProxyEnabled {
		if run.ProxyPort < 1 || run.ProxyPort > 65535 || run.ProxyAuthToken == "" {
			return nil, errors.New("seatbelt proxy mode requires a running authenticated proxy")
		}
		p.policy.ProxyPort = run.ProxyPort
		vars := proxyenv.Vars(proxyenv.URL("127.0.0.1", run.ProxyPort, run.ProxyAuthToken), cfg.ProxyExtraEnv)
		if run.ProxyCAPath != "" {
			p.policy.ReadOnly = append(p.policy.ReadOnly, run.ProxyCAPath)
			vars = append(vars, proxyenv.CAVars(run.ProxyCAPath, cfg.ProxyExtraCAEnv)...)
		}
		// Applied after tools and user environment, below.
		defer func() {
			for _, v := range vars {
				if _, exists := p.env[v.Name]; !v.Default || !exists {
					p.env[v.Name] = v.Value
				}
			}
		}()
	}
	bindings, err := p.collectTools(cfg)
	if err != nil {
		return nil, err
	}
	if err := p.applyBindings(root, cfg, bindings); err != nil {
		return nil, err
	}
	if err := p.prepareGit(cfg); err != nil {
		return nil, err
	}
	if err := p.protectProjectFiles(cfg.ProjectDir); err != nil {
		return nil, err
	}
	for _, dir := range []string{".tmp", ".config", ".cache", ".local/share", ".local/state", ".runtime", "go"} {
		if err := root.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	for _, dir := range []string{".tmp", ".runtime"} {
		path, err := filepath.EvalSymlinks(filepath.Join(cfg.SandboxHome, dir))
		if err != nil || !nativeWithin(cfg.SandboxHome, path) {
			return nil, fmt.Errorf("seatbelt private socket directory %s is not confined: %v", dir, err)
		}
		p.policy.UnixSocketRoots = append(p.policy.UnixSocketRoots, path)
	}
	for _, name := range cfg.EnvPassthrough {
		if value, ok := os.LookupEnv(name); ok {
			p.env[name] = value
		}
	}
	for name, value := range cfg.EnvVars {
		p.env[name] = value
	}
	return p, nil
}

func nativeBaseEnv(cfg *sandbox.Config) map[string]string {
	home := cfg.SandboxHome
	env := map[string]string{
		"HOME": home, "SHELL": cfg.ShellPath, "DEVSANDBOX": "1", "DEVSANDBOX_PROJECT": cfg.ProjectName,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_CACHE_HOME": filepath.Join(home, ".cache"),
		"XDG_DATA_HOME": filepath.Join(home, ".local/share"), "XDG_STATE_HOME": filepath.Join(home, ".local/state"),
		"XDG_RUNTIME_DIR": filepath.Join(home, ".runtime"), "TMPDIR": filepath.Join(home, ".tmp"),
		"GOCACHE": filepath.Join(home, ".cache/go-build"), "GOMODCACHE": filepath.Join(home, ".cache/go-mod"),
		"MISE_SHELL": string(cfg.Shell),
	}
	for _, key := range []string{"USER", "LOGNAME", "TERM", "LANG", "LC_ALL", "LC_CTYPE", "COLORTERM", "COLUMNS", "LINES"} {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	paths := []string{filepath.Join(home, ".local/share/mise/shims"), filepath.Join(home, ".local/bin")}
	for _, path := range filepath.SplitList(os.Getenv("PATH")) {
		if !filepath.IsAbs(path) {
			continue
		}
		if nativeWithin(cfg.HomeDir, path) && !nativeWithin(cfg.ProjectDir, path) {
			paths = append(paths, home+strings.TrimPrefix(path, cfg.HomeDir))
		}
		paths = append(paths, path)
	}
	paths = append(paths, "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin")
	env["PATH"] = strings.Join(paths, string(os.PathListSeparator))
	return env
}

func nativeWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && filepath.IsLocal(rel)
}

func (p *nativePlan) collectTools(cfg *sandbox.Config) ([]tools.Binding, error) {
	global := tools.GlobalConfig{DefaultMountMode: cfg.DefaultMountMode, ProjectDir: cfg.ProjectDir, GitRepoRoot: cfg.GitRepoRoot, HomeDir: cfg.HomeDir, LaunchedAgent: cfg.LaunchedAgent}
	var bindings []tools.Binding
	for _, tool := range tools.Available(cfg.HomeDir) {
		mode := config.ToolMountMode(cfg.ToolsConfig, tool.Name())
		section := config.ToolSection(cfg.ToolsConfig, tool.Name())
		if mode == "disabled" {
			continue
		}
		_, active := tool.(tools.ActiveTool)
		_, shared := tool.(tools.ToolWithSharedTmp)
		if active || shared {
			notice.Info("seatbelt: optional host integration %s is not attached", tool.Name())
			continue
		}
		if configurable, ok := tool.(tools.ToolWithConfig); ok {
			configurable.Configure(global, section)
		}
		if setup, ok := tool.(tools.ToolWithSetup); ok {
			if err := setup.Setup(cfg.HomeDir, p.setup); err != nil {
				return nil, fmt.Errorf("seatbelt %s setup: %w", tool.Name(), err)
			}
		}
		for _, b := range tool.Bindings(cfg.HomeDir, p.setup) {
			// Git's repository bindings require a snapshot, not a path grant.
			if tool.Name() == "git" && !b.HomeRelativeDest {
				continue
			}
			sandbox.ResolveBindingType(&b, mode, cfg.DefaultMountMode)
			bindings = append(bindings, b)
		}
		effectiveMode := mode
		if effectiveMode == "" {
			effectiveMode = cfg.DefaultMountMode
		}
		if tool.Name() == "mise" && (effectiveMode == "" || effectiveMode == "split") {
			installs, err := nativeMiseInstallBindings(cfg.HomeDir)
			if err != nil {
				return nil, fmt.Errorf("seatbelt mise installs: %w", err)
			}
			for _, binding := range installs {
				p.installSeeds[binding.Source] = true
			}
			bindings = append(bindings, installs...)
		}
		if seeded, ok := tool.(tools.ToolWithHomeSeedFiles); ok {
			for _, name := range seeded.HomeSeedFiles() {
				if !filepath.IsLocal(name) || filepath.Base(name) != name {
					return nil, fmt.Errorf("seatbelt: invalid home seed name %q", name)
				}
				p.seeds[filepath.Join(p.setup, name)] = true
				bindings = append(bindings, tools.Binding{Source: filepath.Join(p.setup, name), Dest: filepath.Join(cfg.HomeDir, name), HomeRelativeDest: true, Type: tools.MountOverlay, Optional: true})
			}
		}
		for _, v := range tool.Environment(cfg.HomeDir, cfg.SandboxHome) {
			value := v.Value
			if v.FromHost {
				value = os.Getenv(v.Name)
			}
			if strings.HasPrefix(v.Name, "DYLD_") {
				return nil, fmt.Errorf("seatbelt: tool %s supplies reserved variable %s", tool.Name(), v.Name)
			}
			if value != "" {
				if !strings.Contains(value, cfg.SandboxHome) {
					value = remapEnvHome(value, cfg.HomeDir, cfg.ProjectDir, cfg.SandboxHome)
				}
				p.env[v.Name] = value
			}
		}
	}
	return bindings, nil
}

func nativeIntegrationRequested(section map[string]any) bool {
	// Explicitly disabled integrations are harmless. Auto-discovered optional
	// integrations are detached; an explicit request must get an error.
	if enabled, ok := section["enabled"].(bool); ok && !enabled {
		return false
	}
	if mode, _ := section["mode"].(string); mode == "disabled" || mode == "auto" {
		return false
	}
	return len(section) != 0
}
