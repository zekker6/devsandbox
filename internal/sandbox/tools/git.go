package tools

import (
	"bufio"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"unicode"

	"devsandbox/internal/cmdpattern"
	"devsandbox/internal/fsutil"
	"devsandbox/internal/notice"
)

func init() {
	Register(&Git{})
}

// GitMode defines the level of git access in the sandbox.
type GitMode string

const (
	// GitModeReadOnly mounts .git as read-only to prevent commits.
	// Provides a safe gitconfig carrying only the identity (user.name,
	// user.email) and the global ignore/attributes files, repointed at
	// read-only copies inside the sandbox. No credentials, signing keys,
	// or other sensitive data.
	GitModeReadOnly GitMode = "readonly"

	// GitModeReadWrite provides full git access including credentials,
	// SSH keys, and GPG keys for signing commits. .git is writable.
	GitModeReadWrite GitMode = "readwrite"

	// GitModeDisabled completely disables git configuration.
	// Git commands will work but without any user configuration.
	// .git remains writable (controlled by project bindings).
	GitModeDisabled GitMode = "disabled"
)

// ValidGitMode returns true if the given string is a valid git mode value.
func ValidGitMode(mode string) bool {
	switch strings.ToLower(mode) {
	case "readonly", "readwrite", "disabled":
		return true
	default:
		return false
	}
}

// Git provides configurable git configuration.
// Supports three modes: readonly (default), readwrite, and disabled.
type Git struct {
	mode        GitMode
	projectDir  string
	gitRepoRoot string // main repo root when projectDir is a worktree; empty otherwise

	// mountMode is the mount mode this tool's bindings resolve under - the
	// [tools.git] mount_mode when set, the global [overlay] default otherwise,
	// which is the same chain ResolveBindingType applies. Empty when neither is
	// configured.
	//
	// Two places need it, both because an explicit mode changes a fact the tool
	// would otherwise assume: readWriteStaticBindings must not pin a writable
	// bind over a configured readonly, and auxUntrustedRoots must know whether
	// the main repo's .git is a tree the sandbox can write.
	mountMode string

	// refBindings carries the files the host's global git config *references*
	// into a readwrite sandbox. Setup resolves the config and fills it in;
	// Bindings only appends it, because Bindings must not run a resolver
	// subprocess of its own. Nil until Setup runs - `tools info` calls
	// Bindings without Setup, and must still get exactly the static set.
	refBindings []Binding
}

func (g *Git) Name() string {
	return "git"
}

func (g *Git) Description() string {
	switch g.mode {
	case GitModeReadWrite:
		return "Git configuration (full access with credentials)"
	case GitModeDisabled:
		return "Git configuration (disabled)"
	default:
		return "Git configuration (read-only, no commits)"
	}
}

func (g *Git) Available(homeDir string) bool {
	// Git tool is always "available" - it handles all modes including disabled
	// Check if git binary exists
	_, err := exec.LookPath("git")
	return err == nil
}

// gitConfig is the [tools.git] section.
type gitConfig struct {
	MountModeConfig
	Mode string `toml:"mode"`
}

// ConfigType implements ToolWithConfigType.
func (g *Git) ConfigType() reflect.Type { return reflect.TypeFor[gitConfig]() }

// Configure implements ToolWithConfig.
func (g *Git) Configure(globalCfg GlobalConfig, toolCfg map[string]any) {
	g.mode = GitModeReadOnly // default
	g.projectDir = globalCfg.ProjectDir
	g.gitRepoRoot = globalCfg.GitRepoRoot

	var cfg gitConfig
	decodeConfig(g.Name(), toolCfg, &cfg)

	// Same precedence ResolveBindingType applies, and deliberately no case
	// folding: that function switches on the raw string, so folding here would
	// have the tool and the resolver disagree about what a mode is.
	g.mountMode = globalCfg.DefaultMountMode
	if cfg.MountMode != "" {
		g.mountMode = cfg.MountMode
	}

	switch strings.ToLower(cfg.Mode) {
	case "readwrite", "read-write", "rw":
		g.mode = GitModeReadWrite
	case "disabled", "none", "off":
		g.mode = GitModeDisabled
	}
}

// mountModeBinds reports whether mode makes ResolveBindingType produce a plain
// bind mount rather than an overlay. Only "readwrite" and "readonly" do; every
// other mode, the empty default included, resolves a CategoryConfig directory
// to an overlay.
//
// It is what tells an overlay a binding has to escape from an explicit choice
// the user made. Keep the value set in step with ResolveBindingType.
func mountModeBinds(mode string) bool {
	return mode == "readwrite" || mode == "readonly"
}

func (g *Git) Bindings(homeDir, sandboxHome string) []Binding {
	switch g.mode {
	case GitModeDisabled:
		// Disabled mode suppresses git *configuration*, not git itself, and
		// a worktree's metadata lives outside the project mount - so the
		// shared git directory is still bound, writable, exactly as an
		// ordinary checkout's .git is by the project binding.
		if b, ok := g.worktreeGitDirBinding(); ok {
			return []Binding{b}
		}
		return nil

	case GitModeReadWrite:
		return g.readWriteBindings(homeDir, sandboxHome)

	default: // GitModeReadOnly
		return g.readOnlyBindings(homeDir, sandboxHome)
	}
}

// isWorktreeProject reports whether projectDir is a git worktree whose real
// metadata lives in a separate main repository. One spelling of the test, used
// everywhere, so the bindings that mount that tree and the deny list that has
// to cover it cannot disagree about when it exists.
func (g *Git) isWorktreeProject() bool {
	return g.gitRepoRoot != "" && g.gitRepoRoot != g.projectDir
}

// gitDirSource returns the directory holding the real .git metadata.
// In worktree mode that is the main repo; otherwise it is the project dir.
func (g *Git) gitDirSource() string {
	if g.isWorktreeProject() {
		return g.gitRepoRoot
	}
	return g.projectDir
}

// worktreeMainGitDir returns the main repository's .git directory in worktree
// mode, and "" otherwise. It is the single expression the worktree bindings and
// sandboxWritableRoots derive from.
func (g *Git) worktreeMainGitDir() string {
	if !g.isWorktreeProject() {
		return ""
	}
	return filepath.Join(g.gitRepoRoot, ".git")
}

// worktreeGitDirBinding returns the base binding for the shared git directory
// backing a linked worktree. Dest is pinned to the host path so the Docker and
// krun backends do not remap it under /home/sandboxuser: the worktree's .git
// file carries an absolute gitdir pointer that has to resolve unchanged.
func (g *Git) worktreeGitDirBinding() (Binding, bool) {
	gitDir := g.worktreeMainGitDir()
	if gitDir == "" {
		return Binding{}, false
	}
	if info, err := os.Stat(gitDir); err != nil || !info.IsDir() {
		return Binding{}, false
	}
	return Binding{Source: gitDir, Dest: gitDir, Category: CategoryConfig}, true
}

// bindingWritesHost reports whether b lands on the host filesystem as a mount
// the sandbox can write, under the given mount mode.
//
// Only a writable bind does. Every overlay flavour keeps the sandbox's writes
// in an upper layer, and the resolver runs on the host reading host paths - so
// an overlaid file is not one the sandbox can put words into. An unset Type is
// whatever ResolveBindingType will make of it, which is a writable bind for
// "readwrite" and an overlay or a read-only bind for everything else.
func bindingWritesHost(b Binding, mountMode string) bool {
	if b.Type != "" {
		return b.Type == MountBind && !b.ReadOnly
	}
	return mountMode == "readwrite"
}

// sandboxWritableRoots returns the host paths this tool itself mounts writable,
// which auxUntrustedRoots must deny for the same reason it denies the project
// dir: the sandbox writes them, the writes survive into the next launch, and
// the resolver reads them back as though they were the host's word.
//
// It is derived by walking the bindings rather than by listing paths, and that
// is the point. Three separate reviews found three separate roots missing from
// a hand-written list - the worktree main .git, the global config chain, and
// the ~/.ssh and ~/.gnupg directory binds - each time because the list was
// written from the instance in hand rather than from the rule. A walk cannot
// fall behind a binding the tool grows later.
//
// Only readwrite mode contributes: readonly mode pins every path it mounts to a
// read-only bind or points it at a generated copy under the sandbox home, which
// is denied already. The mount mode matters just as much as the git mode, since
// mount_mode = "readonly" turns the whole set read-only.
func (g *Git) sandboxWritableRoots(homeDir string) []string {
	if g.mode != GitModeReadWrite {
		return nil
	}

	// The static set only. The resolved refBindings are built by the callers of
	// auxUntrustedRoots, so reading them here would be circular; and the two
	// classes they hold need nothing - the config chain is pinned read-only,
	// and an ignore or attributes file is a leaf devsandbox never reads a
	// setting out of.
	var roots []string
	for _, b := range g.readWriteStaticBindings(homeDir) {
		if bindingWritesHost(b, g.mountMode) {
			roots = append(roots, b.Source)
		}
	}
	return roots
}

// readOnlyBindings returns bindings for readonly mode (safe gitconfig + read-only .git).
func (g *Git) readOnlyBindings(homeDir, sandboxHome string) []Binding {
	safeGitconfig := filepath.Join(sandboxHome, ".gitconfig.safe")

	bindings := []Binding{
		{
			Source: safeGitconfig,
			Dest:   filepath.Join(homeDir, ".gitconfig"),
			// The destination is inside the sandbox home, which only bwrap
			// binds at the host home path - see HomeRelativeDest.
			HomeRelativeDest: true,
			Category:         CategoryConfig,
			Optional:         true, // Safe config might not exist if Setup failed
		},
	}

	// The global ignore and attributes files, as sanitized copies Setup wrote.
	// Emitted unconditionally and Optional, exactly like the sibling above:
	// only Setup knows whether a copy happened, and Bindings must not run a
	// resolver subprocess of its own. Type and ReadOnly are explicit because
	// these are pure inputs - nothing in the sandbox should appear to modify
	// them - whereas an unset Type resolves to an overlay under the split
	// policy.
	for _, f := range gitAuxFiles {
		bindings = append(bindings, Binding{
			Source:           filepath.Join(sandboxHome, f.safeName),
			Dest:             filepath.Join(homeDir, f.safeName),
			HomeRelativeDest: true,
			Type:             MountBind,
			ReadOnly:         true,
			Optional:         true,
			Category:         CategoryConfig,
		})
	}

	// Mount .git as read-only to prevent commits. In worktree mode the
	// worktree's .git is a regular file pointing at the main repo's
	// .git/worktrees/<name>; we mount the main repo's .git so the
	// absolute gitdir: pointer resolves correctly inside the sandbox.
	gitDirHost := g.gitDirSource()
	isWorktree := g.isWorktreeProject()
	if gitDirHost != "" {
		gitDir := filepath.Join(gitDirHost, ".git")
		if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
			b := Binding{
				Source:   gitDir,
				Type:     MountBind, // Explicit: must be ro bind, not overlay
				ReadOnly: true,      // Security constraint of readonly mode
				Category: CategoryConfig,
			}
			// In worktree mode, pin Dest to the host path so the Docker
			// backend does not remap it under /home/sandboxuser. The
			// worktree's .git file contains an absolute gitdir: pointer
			// that must resolve inside the container.
			if isWorktree {
				b.Dest = gitDir
			}
			bindings = append(bindings, b)

			// Overlay .git/config with a sanitized copy. Embedded credentials in
			// remote URLs (e.g., https://ghp_xxxx@github.com/user/repo.git) and
			// any [credential] sections are stripped, but the rest of the config
			// is preserved verbatim so git itself can still read the repo —
			// otherwise even `git log` and pre-commit hooks fail with "unable to
			// access '.git/config': Permission denied".
			gitConfig := filepath.Join(gitDir, "config")
			if info, err := os.Stat(gitConfig); err == nil && info.Mode().IsRegular() {
				bindings = append(bindings, Binding{
					Source:   filepath.Join(sandboxHome, ".git-config.safe"),
					Dest:     gitConfig,
					Type:     MountBind,
					ReadOnly: true,
					Optional: true, // Setup may have skipped if source unreadable
					Category: CategoryConfig,
				})
			}
		}
	}

	return bindings
}

// readWriteBindings returns bindings for readwrite mode (full git access):
// the fixed set below, plus whatever Setup resolved the host config to
// reference.
//
// g.refBindings is nil whenever Setup did not run - `tools info` builds a tool
// registry and calls Bindings straight out - and appending a nil slice yields
// exactly the static set, which is the behavior every caller had before.
func (g *Git) readWriteBindings(homeDir, _ string) []Binding {
	return append(g.readWriteStaticBindings(homeDir), g.refBindings...)
}

// readWriteStaticBindings returns the readwrite bindings that do not depend on
// the resolved host config. Split out so setupReadWriteRefs can dedup the
// resolved bindings against them without re-entering Bindings.
func (g *Git) readWriteStaticBindings(homeDir string) []Binding {
	bindings := []Binding{
		{
			Source: filepath.Join(homeDir, ".gitconfig"),
			// Pinned read-only for the reason configChainBinding is: this is
			// the root of the config devsandbox resolves to decide which host
			// files to mount, so a sandbox that can write it can choose them.
			// The other three entries below are credentials this mode
			// deliberately shares and devsandbox never parses, so they keep
			// following the mount mode.
			Type:     MountBind,
			ReadOnly: true,
			Category: CategoryConfig,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".git-credentials"),
			Category: CategoryConfig,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".ssh"),
			Category: CategoryConfig,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".gnupg"),
			Category: CategoryConfig,
			Optional: true,
		},
	}

	// In worktree mode the project mount only contains the worktree
	// directory. The worktree's .git is a file whose gitdir: pointer
	// references the main repo's .git — which must also be mounted
	// (writable, so commits can land). Pin Dest to the host path so
	// the Docker backend does not remap it under /home/sandboxuser.
	if b, ok := g.worktreeGitDirBinding(); ok {
		// Escape the overlay, but only where there is one to escape.
		// ResolveBindingType returns on any non-empty Type, so a pin
		// applied unconditionally beats not just the split policy this
		// needs to override - under which a directory source resolves to
		// MountTmpOverlay, sending every commit to tmpfs on bwrap and to a
		// container-local copy on Docker/krun - but also an explicit
		// mount_mode the user set. Overriding "readonly" that way removed a
		// boundary the documented setting promises, silently: the launch
		// succeeded and the sandbox got host-write access to the main
		// repository's objects, refs, index and hooks, which the host then
		// executes on its next commit. Leaving Type unset for the two modes
		// that already resolve to a bind honors both of them.
		if !mountModeBinds(g.mountMode) {
			b.Type = MountBind
		}
		bindings = append(bindings, b)
	}

	return bindings
}

func (g *Git) Environment(homeDir, sandboxHome string) []EnvVar {
	if g.mode == GitModeDisabled {
		return nil
	}

	// Pass through SSH auth socket for ssh-agent
	if g.mode == GitModeReadWrite {
		return []EnvVar{
			{Name: "SSH_AUTH_SOCK", FromHost: true},
			{Name: "GPG_TTY", FromHost: true},
		}
	}

	return nil
}

func (g *Git) ShellInit(shell string) string {
	return ""
}

// Setup implements ToolWithSetup. In readonly mode it generates the safe
// gitconfig and the sanitized per-repo .git/config; in readwrite mode it
// resolves the host config and works out which of the files that config
// references have to be bound in.
func (g *Git) Setup(homeDir, sandboxHome string) error {
	if g.mode == GitModeDisabled {
		return nil
	}

	// Both paths are required before anything is read or written. An empty
	// sandboxHome makes every destination *relative* - filepath.Join("",
	// ".gitignore.safe") is ".gitignore.safe" - so the copies would land in
	// whatever directory the process happens to be in. An empty homeDir is the
	// mirror: HOME is overridden to "" for the resolver, but $XDG_CONFIG_HOME
	// is not, so git's default ignore path resolves to the *invoking user's*
	// real one and its contents would be what gets copied out. Neither can
	// happen on a real launch; the guard is what keeps it that way.
	if homeDir == "" || sandboxHome == "" {
		return nil
	}

	if g.mode == GitModeReadWrite {
		return g.setupReadWriteRefs(homeDir, sandboxHome)
	}

	if err := g.setupUserGitconfig(homeDir, sandboxHome); err != nil {
		return err
	}

	return g.setupRepoGitconfig(sandboxHome)
}

// lostIncludeAlertSuffix is the half of the degraded-resolver alert that is the
// same whichever way the resolver failed: what the sandbox still gets, and what
// it does not. Only the sentence in front of it differs, because an old git is
// not something to name at the user.
const lostIncludeAlertSuffix = "so only the top-level [include] targets of the global config files are " +
	"carried into the sandbox; a config file included from inside another include, or by a conditional " +
	"includeIf, is not there, and git ignores a missing include with no warning"

// setupReadWriteRefs resolves the host's global git config and records the
// bindings that carry the files it references, for readWriteBindings to append.
//
// readwrite mounts ~/.gitconfig verbatim, so every path-valued setting arrives
// inside the sandbox spelled exactly as the host wrote it - naming host files
// nothing mounts. Git ignores a missing core.excludesFile, attributesFile or
// [include] target with exit 0 and no warning, so those settings are lost with
// nothing on screen to say so. Binding the files the config already names is
// what makes them resolve, and it is why nothing here rewrites a value.
//
// Two sets go out: the files a file-valued key names, and the files the global
// scope is assembled from - every [include]/[includeIf] target that actually
// contributed, plus the roots that declared them. The second set is what
// carries a $XDG/git/config-only host its identity, since builder.go repoints
// XDG_CONFIG_HOME into the sandbox. A third takes over when the resolver
// cannot run at all, carrying what each config file's own top-level sections
// still say.
//
// It never returns an error. Every condition it can hit is one the user should
// merely be told about, and the two backends disagree about what an error
// means: builder.go aborts the launch on a Setup error while docker.go only
// warns, so returning one would make an unreadable host config fatal on bwrap
// and cosmetic on Docker. Diagnostics go out as notice.Alert - Setup runs in
// PhaseRunning, where a notice.Warn is diverted to the log file.
func (g *Git) setupReadWriteRefs(homeDir, sandboxHome string) error {
	sources := existingGlobalConfigs(homeDir)
	entries, retriedOutsideRepo, err := g.resolveGlobalConfig(homeDir)
	values, origins := globalConfigMap(entries)

	var degraded []Binding
	switch {
	case err != nil:
		// Nothing an include supplied is visible any more, so the entry list
		// goes with it and what is left is each config file's own top-level
		// sections. The file-valued keys are re-read from there rather than
		// left unset: an unset core.excludesFile is not "no value", it makes
		// auxFileBindings carry git's XDG default - a file the host does not
		// use, in place of the one it does.
		entries = nil

		var (
			carried    []gitConfigEntry
			sawInclude bool
		)
		degraded, carried, sawInclude = g.fallbackRefBindings(sources, homeDir, sandboxHome)
		// Read from every config file the fallback carries, not just the two
		// roots. Unlike readonly - which generates a config and mounts no
		// include at all - readwrite mounts the include targets, so a
		// core.excludesFile declared inside one *applies* in the sandbox. Read
		// only the roots and that value is invisible here, so nothing binds the
		// file it names and git drops the host's ignore rules with exit 0 and
		// no warning: the silent loss this whole path exists to close, reopened
		// on the degraded branch alone.
		values, origins = fallbackValues(carried)
		if sawInclude {
			// Silent when the host declares no include: the fallback then
			// carries everything the resolver would have, and a notice raised
			// on every launch of a correct host is one the user learns to
			// dismiss unread.
			if errors.Is(err, errShowScopeUnsupported) {
				// A host git older than 2.26 has no --show-scope. That is not a
				// setting the user got wrong, so the version is not named - but
				// the includes it cannot expand are a real gap and are.
				notice.Alert("git: the global config could not be fully resolved, " + lostIncludeAlertSuffix)
			} else {
				notice.Alert("git: could not read the resolved global config (%v); "+lostIncludeAlertSuffix, err)
			}
		}

	case retriedOutsideRepo != nil && hasConditionalIncludes(values):
		// The retry kept every plain [include], so this is silent unless the
		// host actually has an includeIf - which is the identity-per-directory
		// setup this resolver exists for, and the one thing reading outside the
		// repository costs.
		notice.Alert("git: the global config could not be resolved from the project directory (%v), "+
			"so it was read outside the repository and no includeIf condition was evaluated; the config "+
			"file a matching conditional include names is not carried into the sandbox, so a commit "+
			"there lands with the identity the outer config sets", retriedOutsideRepo)
	}

	refs := g.auxFileBindings(values, origins, homeDir, sandboxHome)
	refs = append(refs, g.includeOriginBindings(entries, homeDir, sandboxHome)...)
	refs = append(refs, degraded...)

	// Assigned, never appended to. tools.Register hands out singletons and
	// docker.go calls getToolBindings twice per launch, so Setup runs more than
	// once against this same struct; appending would emit every binding twice,
	// which is a trackMount panic on bwrap and a "Duplicate mount point" error
	// on Docker.
	g.refBindings = dedupBindingDests(g.readWriteStaticBindings(homeDir), refs)
	return nil
}

// dedupBindingDests returns the subset of candidates whose effective
// destination collides neither with an existing binding nor with an earlier
// candidate.
//
// Two bindings landing on one path are not a preference, they are a crash: a
// duplicate mount is a trackMount panic on bwrap and a "Duplicate mount point"
// error on Docker. The existing set wins, because a resolved reference is an
// addition to the fixed set and never a replacement for it.
func dedupBindingDests(existing, candidates []Binding) []Binding {
	seen := make(map[string]struct{}, len(existing)+len(candidates))
	for _, b := range existing {
		seen[bindingDest(b)] = struct{}{}
	}

	var out []Binding
	for _, b := range candidates {
		dest := bindingDest(b)
		if _, dup := seen[dest]; dup {
			continue
		}
		seen[dest] = struct{}{}
		out = append(out, b)
	}
	return out
}

// bindingDest returns the path a binding actually lands on, which is Source
// whenever Dest is empty.
//
// Comparing the Dest fields alone finds no collision at all against the static
// readwrite bindings: every one of them leaves Dest empty and is mounted at its
// Source by both backends (builder.go's applyBinding, docker.go's
// remapToContainerHome). A resolved ~/.gitconfig reference would then be
// emitted a second time.
func bindingDest(b Binding) string {
	if b.Dest != "" {
		return b.Dest
	}
	return b.Source
}

// setupUserGitconfig generates the sanitized ~/.gitconfig overlay.
//
// It regenerates on every launch. The mtime comparison this used to do only
// ever looked at ~/.gitconfig, so an edit to an *included* file left a stale
// safe config behind indefinitely - and a correct multi-origin check has to run
// after the resolver subprocess, which is where the cost actually is.
func (g *Git) setupUserGitconfig(homeDir, sandboxHome string) error {
	sources := existingGlobalConfigs(homeDir)

	entries, retriedOutsideRepo, err := g.resolveGlobalConfig(homeDir)
	values, origins := globalConfigMap(entries)
	switch {
	case err != nil:
		// An unsupported --show-scope means the host git predates 2.26; that is
		// not a setting the user got wrong, so it must not warn on every launch.
		if !errors.Is(err, errShowScopeUnsupported) {
			notice.Alert("git: could not read the resolved global config (%v); "+
				"falling back to the top-level sections of the global config files, so a value "+
				"defined only inside an include is missing", err)
		}
		values, origins = fallbackValues(fallbackFileEntries(sources))

	case retriedOutsideRepo != nil && hasConditionalIncludes(values):
		// The retry kept every plain [include], so it is silent unless the host
		// actually has an includeIf - that is the only thing reading outside the
		// repository costs, and it is exactly the identity-per-directory setup
		// this resolver exists for.
		notice.Alert("git: the global config could not be resolved from the project directory (%v), "+
			"so it was read outside the repository and no includeIf condition was evaluated; a value "+
			"defined only inside a matching conditional include is missing from the sandbox gitconfig",
			retriedOutsideRepo)
	}

	// Deliberately not gated on len(sources): git reads ~/.config/git/ignore
	// whether or not any global config *file* exists, so a host with global
	// ignore rules and no gitconfig still has rules to carry. Runs on the
	// fallback map too - an unset key resolves to the host XDG default, which
	// the fallback path can carry just as well as the resolver.
	g.copyAuxFiles(values, origins, homeDir, sandboxHome)

	safePath := filepath.Join(sandboxHome, ".gitconfig.safe")
	if !hasSafeConfigValues(values) {
		// Nothing survives the allowlist. Drop any copy an earlier launch left
		// behind rather than keeping a config the host no longer has: the
		// binding is emitted unconditionally and only tests for existence.
		//
		// Reported rather than returned, for the same reason copyAuxFiles
		// reports its identical cleanup: builder.go aborts the launch on a
		// Setup error while docker.go only warns, so returning here would make
		// one stale file fatal on bwrap and cosmetic on Docker.
		if err := removeStale(safePath); err != nil {
			notice.Alert("git: could not remove the safe gitconfig left by a previous launch (%v); "+
				"the sandbox keeps reading it until it is deleted", err)
		}
		return nil
	}

	return generateSafeGitconfig(values, safePath)
}

// hasSafeConfigValues reports whether any allowlisted key has something to
// emit, which is what decides between writing the safe config and removing it.
func hasSafeConfigValues(values map[string]string) bool {
	keys := make([]string, 0, 2+len(gitAuxFiles))
	keys = append(keys, "user.name", "user.email")
	for _, f := range gitAuxFiles {
		keys = append(keys, f.key)
	}
	for _, k := range keys {
		if strings.TrimSpace(values[k]) != "" {
			return true
		}
	}
	return false
}

// removeStale deletes a generated file that this launch is not writing, so a
// copy from an earlier launch cannot outlive the host setting that produced it.
// A file that was never there is not an error.
func removeStale(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// globalConfigSources lists the host files git reads as global-scope
// configuration, in git's own read order so that a later file overrides an
// earlier one. ~/.gitconfig is not the only one: a user whose identity lives
// solely in the XDG location has no ~/.gitconfig at all.
//
// The XDG entry is exactly one path, never both spellings: when
// XDG_CONFIG_HOME is set git reads $XDG_CONFIG_HOME/git/config and ignores
// ~/.config/git/config entirely. Listing both would put a file git never reads
// *after* the one it does, so fallbackIdentity's last-wins walk would let a
// stale leftover override the live identity, and `tools check` would report a
// config source that is not in play. hostGitXDGDir already encodes git's
// either/or rule.
func globalConfigSources(homeDir string) []string {
	return []string{
		filepath.Join(hostGitXDGDir(homeDir), "config"),
		filepath.Join(homeDir, ".gitconfig"),
	}
}

// existingGlobalConfigs returns the subset of globalConfigSources that exists
// as a readable regular file, preserving git's read order.
func existingGlobalConfigs(homeDir string) []string {
	var out []string
	for _, p := range globalConfigSources(homeDir) {
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			out = append(out, p)
		}
	}
	return out
}

// fallbackValues collapses an ordered fallback entry stream to the last value
// per key, alongside the origin of each one, the way globalConfigMap does for
// the resolver's own stream. The include directives that mark where an
// expansion belongs are dropped: they name a file, not a setting.
//
// The file-valued keys are carried here too, not just the identity: leaving
// them out does not make copyAuxFiles skip them, it makes it treat a configured
// core.excludesFile as unset and carry git's XDG default instead - a file the
// host does not use, substituted for the one it does, with nothing on screen to
// say so. Origins are returned because copyAuxFile refuses a value whose origin
// it cannot resolve to a host file.
func fallbackValues(entries []gitConfigEntry) (values, origins map[string]string) {
	values = make(map[string]string, 2+len(gitAuxFiles))
	origins = make(map[string]string, 2+len(gitAuxFiles))
	for _, e := range entries {
		if e.key == gitIncludeKey {
			continue
		}
		values[e.key] = e.value
		origins[e.key] = e.origin
	}
	return values, origins
}

// fallbackFileEntries reads the global config files' own top-level sections in
// git's read order, expanding no include. That is what readonly mode is left
// with when the resolver fails: it generates a config rather than mounting the
// host's, so an include target is not carried into the sandbox at all and a
// value defined only inside one has nothing to apply to.
//
// A read error is not reported here. Both fallback callers already raise one -
// readonly unconditionally, readwrite through fallbackRefBindings' sawInclude -
// and whatever the parse did manage to read is still better than nothing.
func fallbackFileEntries(sources []string) []gitConfigEntry {
	var entries []gitConfigEntry
	for _, path := range sources {
		fileEntries, _, _ := parseGitconfig(path)
		entries = append(entries, fileEntries...)
	}
	return entries
}

// fallbackRefBindings carries what is still visible when the resolver cannot
// run: the global config files git reads, and the top-level [include] targets
// those files name.
//
// sawInclude reports whether the config declared any include directive at all -
// or whether a config file could not be read, which leaves that unknowable -
// and is what decides whether the degradation cost the user anything. A host
// with no includes reaches exactly what a working resolver would have given it,
// so alerting there would fire on every launch of a host with nothing wrong.
//
// Targets are resolved by the same spelling rule as the resolver path and
// through the same helpers: the value's spelling decides where the file is read
// inside the sandbox, and a relative one resolves against the declaring file's
// directory on both sides. Only unconditional [include] targets are carried -
// an includeIf condition cannot be evaluated here, and binding its target
// anyway would put a work identity into a personal project's sandbox, which is
// what the resolver path refuses for free.
//
// The config files themselves are bound too, not only their targets.
// ~/.gitconfig is already in the fixed set and dedups away, but $XDG/git/config
// is not, and builder.go repoints XDG_CONFIG_HOME into the sandbox - so a host
// whose global config lives only there would lose the whole file, identity
// included, in the one mode where commits land.
//
// carried is the key stream of every config file those bindings make readable
// inside the sandbox, in the order git applies them - each declaring file's own
// keys with the keys of each carried target spliced in where the [include]
// directive stands. That is not the same as parsing the roots alone: unlike
// readonly, this mode mounts the include target, so a core.excludesFile
// declared inside one *applies* in the sandbox, and reading only the roots
// leaves nothing bound for it to name.
func (g *Git) fallbackRefBindings(sources []string, homeDir, sandboxHome string) (bindings []Binding, carried []gitConfigEntry, sawInclude bool) {
	untrusted := g.auxUntrustedRoots(homeDir, sandboxHome)
	refs := rootConfigRefs(homeDir)

	for _, path := range sources {
		// sources are existingGlobalConfigs' output, so each is one of git's
		// own two roots and exists as a regular file.
		parent, known := refs[filepath.Clean(path)]
		if !known || pathDenied(parent.src, untrusted) {
			continue
		}
		bindings = append(bindings, configChainBinding(parent.src, parent.dest, parent.homeRelative))

		entries, conditional, err := parseGitconfig(path)
		// A file that could not be read to the end tells us nothing about what
		// it declares, and an unknown is not an absence: reporting "no
		// includes" here would carry the parent config into the sandbox naming
		// targets nothing mounted, with nothing on screen. Treat it as a loss
		// so the caller's alert fires.
		sawInclude = sawInclude || err != nil || conditional

		for _, e := range entries {
			if e.key != gitIncludeKey {
				carried = append(carried, e)
				continue
			}
			sawInclude = true

			target, ok := includeTarget(e, parent, homeDir)
			if !ok || pathDenied(target.src, untrusted) {
				continue
			}
			// A file the host does not have is not a loss: git ignores a
			// missing include target on the host too.
			if info, err := os.Stat(target.src); err != nil || !info.Mode().IsRegular() {
				continue
			}
			bindings = append(bindings, configChainBinding(target.src, target.dest, target.homeRelative))
			// Spliced in at the directive, which is where git reads them, so a
			// key the declaring file sets *after* its [include] still wins and
			// one set before it does not. Only the target's own top-level
			// sections: a nested include is not carried, so acting on a value
			// it supplies would name a file the sandbox cannot read.
			included, _, _ := parseGitconfig(target.src)
			carried = append(carried, included...)
		}
	}
	return bindings, carried, sawInclude
}

// setupRepoGitconfig generates the sanitized per-repo .git/config overlay.
// Skips silently if there's no project, no .git/config, or the source is not
// a regular file (e.g. /dev/null overlay from a nested sandbox).
func (g *Git) setupRepoGitconfig(sandboxHome string) error {
	src := g.gitDirSource()
	if src == "" {
		return nil
	}

	repoConfigPath := filepath.Join(src, ".git", "config")
	safeRepoConfigPath := filepath.Join(sandboxHome, ".git-config.safe")

	srcInfo, err := os.Stat(repoConfigPath)
	if err != nil || !srcInfo.Mode().IsRegular() {
		return nil
	}

	// Regenerated on every launch, and never stat'd at the destination first.
	// sandboxHome is bind-mounted read-write into the sandbox at homeDir, so the
	// destination is a path the sandbox itself can replace with a symlink; an
	// os.Stat there follows it, and so did the os.WriteFile that used to run
	// when the mtime comparison came out the other way - handing the sandbox an
	// arbitrary host file to have truncated and overwritten, or (when the
	// comparison short-circuited) mounted into the sandbox at .git/config.
	// generateSafeRepoConfig writes through a temp file and a rename, which
	// replaces such a link rather than following it.
	return generateSafeRepoConfig(repoConfigPath, safeRepoConfigPath)
}

// generateSafeRepoConfig writes a sanitized copy of a repo's .git/config to dst.
//
// It strips embedded credentials from remote URLs and drops any [credential]
// sections, but otherwise preserves the file verbatim — including [core],
// [branch], [remote] (minus credentials), and any custom sections — so that
// git operations like `git log`, `git status`, and pre-commit hooks continue
// to function inside the sandbox.
func generateSafeRepoConfig(src, dst string) error {
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	var out strings.Builder
	scanner := bufio.NewScanner(file)

	inRemote := false
	inCredential := false

	for scanner.Scan() {
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)

		if strings.HasPrefix(trimmed, "[") {
			lower := strings.ToLower(trimmed)
			inRemote = strings.HasPrefix(lower, "[remote")
			inCredential = strings.HasPrefix(lower, "[credential")
			if inCredential {
				continue // drop the section header itself
			}
			out.WriteString(raw)
			out.WriteByte('\n')
			continue
		}

		if inCredential {
			continue // drop section body
		}

		if inRemote {
			if key, value, ok := splitConfigKV(trimmed); ok {
				lk := strings.ToLower(key)
				if lk == "url" || lk == "pushurl" {
					indent := raw[:len(raw)-len(strings.TrimLeft(raw, " \t"))]
					out.WriteString(indent)
					out.WriteString(key)
					out.WriteString(" = ")
					out.WriteString(stripURLCredentials(value))
					out.WriteByte('\n')
					continue
				}
			}
		}

		out.WriteString(raw)
		out.WriteByte('\n')
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return fsutil.WriteFileAtomic(dst, []byte(out.String()), 0o644)
}

// splitConfigKV splits a "key = value" line. Returns ok=false if there's no '='.
func splitConfigKV(line string) (key, value string, ok bool) {
	k, v, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	return strings.TrimSpace(k), strings.TrimSpace(v), true
}

// stripURLCredentials removes embedded credentials (userinfo) from http/https/ftp
// URLs. SSH/git URLs are returned unchanged because the user component there is
// the SSH login, not a secret. Non-URL strings (scp-style git refs, local paths)
// are also passed through.
func stripURLCredentials(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.User == nil {
		return rawURL
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ftp", "ftps":
		u.User = nil
		return u.String()
	}
	return rawURL
}

// gitConfigEntry is one record of `git config --list --show-scope --show-origin -z`.
//
// origin is retained because a global-scope value is not automatically
// host-derived: an [include] whose target lives in the project tree is reported
// as global, and that tree is bind-mounted read-write, so the sandbox writes it.
// Any key whose value devsandbox acts on has to be checked against where it came
// from - see copyAuxFile.
type gitConfigEntry struct {
	scope  string
	origin string
	key    string
	value  string
}

// parseGitConfigList decodes the NUL-separated stream emitted by
// `git config --list --show-scope --show-origin -z`.
//
// The payload is a flat sequence of NUL-terminated fields, three per record:
//
//	scope \0 origin \0 key [ \n value ] \0
//
// A third field carrying no newline is a valueless boolean key (`core.bare`),
// which is returned with an empty value rather than discarded. Values may
// themselves contain newlines, so the key/value split is on the first one only.
// A trailing group of fewer than three fields is malformed and is discarded.
func parseGitConfigList(data []byte) []gitConfigEntry {
	if len(data) == 0 {
		return nil
	}

	fields := strings.Split(string(data), "\x00")
	// The final NUL terminates the last field rather than starting a new one.
	if len(fields) > 0 && fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}

	var entries []gitConfigEntry
	for i := 0; i+2 < len(fields); i += 3 {
		key, value, _ := strings.Cut(fields[i+2], "\n")
		entries = append(entries, gitConfigEntry{
			scope:  fields[i],
			origin: fields[i+1],
			key:    key,
			value:  value,
		})
	}

	return entries
}

// resolveGlobalConfig returns the fully resolved global-scope git configuration.
//
// Unlike `git config --global <key>`, this expands [include] and [includeIf]
// chains. --global suppresses that expansion entirely and reports both
// `include.path` and `includeif.<cond>.path` literally; running outside a
// repository still expands a plain [include] but has no gitdir to test a
// conditional one against, so it reports `includeif.<cond>.path` and none of
// the values behind it. The command therefore omits --global and runs from the
// project directory - that is what makes `includeIf "gitdir:"` conditions
// evaluate against the repository the sandbox is being built for. Do not
// "simplify" it back to --global.
//
// HOME is overridden to the homeDir the caller was handed rather than the
// process's own: a no-op in production, and what keeps tests off the
// developer's real ~/.gitconfig. GIT_CONFIG_GLOBAL is not equivalent — it
// suppresses discovery of ~/.config/git/config.
//
// Running from the project directory has a cost the --global form does not:
// the command now performs repository discovery and reads the local config, so
// any local-scope failure - an unreadable .git/config, dubious ownership, a
// malformed file - aborts it with exit 128 and takes the whole global scope
// down with it. Such a repository is not a reason to lose the user's identity,
// so the command is retried once from a directory that is not a repository.
// Only includeIf conditions are lost that way; a plain [include] still expands
// outside a repository. retriedOutsideRepo carries the first attempt's error
// when that happened, and is nil otherwise.
//
// Errors are returned for the caller to report; no notices are emitted here.
//
// The entries are returned rather than the collapsed value/origin maps because
// an [include] chain is only legible in the entry list: git reports each
// directive as an ordinary key whose origin is the *declaring* file and whose
// value is the literal unexpanded spelling, so two files including different
// targets both spell the key `include.path` and the map keeps one of them.
// Callers that only need the resolved values run globalConfigMap over the
// result.
func (g *Git) resolveGlobalConfig(homeDir string) (entries []gitConfigEntry, retriedOutsideRepo, err error) {
	entries, err = runGitConfigList(g.projectDir, homeDir)
	if err == nil || errors.Is(err, errShowScopeUnsupported) {
		return entries, nil, err
	}

	// os.TempDir() is not guaranteed to sit outside a repository, but a retry
	// that lands in one only fails the same way the first attempt did, and the
	// original error is what gets reported either way.
	retryEntries, retryErr := runGitConfigList(os.TempDir(), homeDir)
	if retryErr != nil {
		return nil, nil, err
	}
	return retryEntries, err, nil
}

// runGitConfigList executes the resolver with dir as its working directory and
// reduces the output to the global scope. An empty dir inherits the process's
// own working directory.
func runGitConfigList(dir, homeDir string) ([]gitConfigEntry, error) {
	cmd := exec.Command("git", "config", "--list", "--show-scope", "--show-origin", "-z")
	cmd.Dir = dir
	cmd.Env = gitCommandEnv(os.Environ(), homeDir)

	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && isUnsupportedShowScope(exitErr.Stderr) {
			return nil, errors.Join(errShowScopeUnsupported, err)
		}
		return nil, err
	}

	return globalScopeEntries(parseGitConfigList(out)), nil
}

// globalScopeEntries drops every entry outside the global scope, preserving the
// order git reported the survivors in.
//
// Order is what makes the entry list usable: git emits an include directive
// before the keys it pulls in, so a nested chain reads as a walk from the root
// config outwards.
func globalScopeEntries(entries []gitConfigEntry) []gitConfigEntry {
	var out []gitConfigEntry
	for _, e := range entries {
		if e.scope != "global" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// gitIncludeIfPrefix is how git reports an includeIf directive as a config key:
// `includeif.<condition>.path`.
const gitIncludeIfPrefix = "includeif."

// hasConditionalIncludes reports whether the resolved config declares any
// includeIf directive. Git reports the directive itself as an ordinary global
// key whether or not the condition matched, so this says "the host has
// conditional includes", which is what decides whether reading the config
// outside the repository actually lost anything.
func hasConditionalIncludes(values map[string]string) bool {
	for k := range values {
		if strings.HasPrefix(k, gitIncludeIfPrefix) {
			return true
		}
	}
	return false
}

// errShowScopeUnsupported marks a host git older than 2.26, which has no
// --show-scope. The caller falls back silently on it: an unsupported git is not
// a setting the user configured wrongly, and warning would fire every launch.
var errShowScopeUnsupported = errors.New("git config --show-scope is not supported by this git")

// isUnsupportedShowScope recognises the usage error an older git emits for an
// option it does not know. The version is not queried separately - the command
// that must work is the one that reports whether it works.
func isUnsupportedShowScope(stderr []byte) bool {
	s := string(stderr)
	if !strings.Contains(s, "show-scope") {
		return false
	}
	return strings.Contains(s, "unknown option") || strings.Contains(s, "usage:")
}

// gitCommandEnv returns env with HOME replaced by homeDir, GIT_CONFIG_GLOBAL
// dropped, and the locale pinned to C.
//
// The locale is not cosmetic: git translates its diagnostics, and
// isUnsupportedShowScope decides whether to fall back silently or alert on every
// launch by matching "unknown option" and "usage:" in stderr. On a localized
// host those spellings differ, so the sentinel that exists to keep an old git
// quiet would stop recognising it. LANGUAGE overrides LC_ALL for messages in
// GNU gettext, so it is dropped rather than overridden.
//
// GIT_CONFIG_GLOBAL is dropped because the resolver has to see the same global
// config the sandbox will. Set on the host, it *replaces* the global scope:
// git reads that one file and reports neither ~/.gitconfig nor the XDG config.
// The sandbox does not follow - builder.go clears the environment and re-adds
// an explicit list that leaves it out, so in-sandbox git reads the statically
// bound ~/.gitconfig. Inheriting it made every value this tool acts on describe
// a file the sandbox never reads: harmless for includes, which are gated on
// rootConfigRefs and denied anyway, but a core.excludesFile named there was
// bind-mounted in on the strength of a config that does not apply.
//
// The rest of git's config environment needs no handling. GIT_CONFIG_SYSTEM,
// GIT_CONFIG_NOSYSTEM and GIT_CONFIG_COUNT/KEY/VALUE all report their entries
// outside the global scope, which globalScopeEntries already drops.
// XDG_CONFIG_HOME is deliberately left in place: hostGitXDGDir reads the same
// variable, so the resolver and globalConfigSources agree on where the XDG
// config lives, and that path is handled explicitly where it is bound.
func gitCommandEnv(env []string, homeDir string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "HOME="),
			strings.HasPrefix(kv, "GIT_CONFIG_GLOBAL="),
			strings.HasPrefix(kv, "LC_ALL="),
			strings.HasPrefix(kv, "LANGUAGE="):
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HOME="+homeDir, "LC_ALL=C")
}

// globalConfigMap collapses entries to a last-wins value map and returns the
// origin of each surviving value alongside it.
//
// It does not filter by scope: runGitConfigList hands every caller
// globalScopeEntries' output already, and a second copy of that rule here would
// be one more place to keep in step. Values pulled in through an include are
// labelled global by git and reported with the included file as their origin,
// so they survive that filter; last-wins is what lets such an include override
// the outer file, matching git's own precedence. The origins map is keyed
// identically and holds git's raw origin token ("file:/path/to/config"), which
// copyAuxFile needs in order to tell a host-owned config file from one the
// sandbox can write.
func globalConfigMap(entries []gitConfigEntry) (values, origins map[string]string) {
	values = make(map[string]string, len(entries))
	origins = make(map[string]string, len(entries))
	for _, e := range entries {
		values[e.key] = e.value
		origins[e.key] = e.origin
	}
	return values, origins
}

// gitAuxFile describes a file-valued global config key whose target is carried
// into the sandbox as a sanitized copy.
//
// XDG_CONFIG_HOME is repointed into the sandbox, so git's default location for
// both of these resolves to an empty in-sandbox path; an explicit value names a
// host path that is never mounted. Git ignores a missing excludesFile and
// attributesFile silently (exit 0, no warning), so without this the setting is
// lost with nothing to show for it.
type gitAuxFile struct {
	key      string // resolved config key, lowercased as git reports it
	emit     string // spelling written into the safe config
	xdgName  string // basename under the host's XDG git directory
	safeName string // name of the copy, identical in sandboxHome and homeDir
}

// gitAuxFiles is the file-valued half of the safe-config allowlist. Adding an
// entry here wires up the copy, the emitted key and the binding at once.
var gitAuxFiles = []gitAuxFile{
	{key: "core.excludesfile", emit: "excludesFile", xdgName: "ignore", safeName: ".gitignore.safe"},
	{key: "core.attributesfile", emit: "attributesFile", xdgName: "attributes", safeName: ".gitattributes.safe"},
}

// hostGitXDGDir returns the directory git reads its XDG-located files from on
// the host - the global config, the default ignore file and the default
// attributes file all live there.
func hostGitXDGDir(homeDir string) string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "git")
	}
	return filepath.Join(homeDir, ".config", "git")
}

// copyAuxFiles copies each file-valued key's target into sandboxHome and
// rewrites its value in values to the ~/-relative path the copy will have in
// the sandbox.
// A key whose file was not copied is deleted from values, so the caller can
// never emit a config value pointing at a path that will not resolve - git
// would ignore such a value in silence, downgrading a setting the user made
// with nothing on screen to say so.
func (g *Git) copyAuxFiles(values, origins map[string]string, homeDir, sandboxHome string) {
	for _, f := range gitAuxFiles {
		dest, ok := g.copyAuxFile(f, values, origins, homeDir, sandboxHome)
		if !ok {
			delete(values, f.key)
			// The binding for this copy is emitted unconditionally and only
			// tests whether the source exists, so a copy left by an earlier
			// launch would stay mounted inside the sandbox after the setting
			// that produced it is gone.
			if err := removeStale(filepath.Join(sandboxHome, f.safeName)); err != nil {
				notice.Alert("git: could not remove the stale %s copy from a previous launch (%v)", f.safeName, err)
			}
			continue
		}
		values[f.key] = dest
	}
}

// auxSource returns the host path a file-valued key names: its configured value
// when the key is set, and git's XDG default on the host when it is not.
//
// configured reports whether the key was set at all - that is what separates a
// setting reaching less than it says from an integration that is simply not
// present, and it decides whether the caller warns. An error means a configured
// value names nothing devsandbox can resolve. An empty src with a nil error is
// a key set to an empty value: it names no file deliberately, so it must not
// fall back to the default it was written over.
func auxSource(f gitAuxFile, values map[string]string, homeDir string) (src string, configured bool, err error) {
	raw, configured := values[f.key]
	if !configured {
		return filepath.Join(hostGitXDGDir(homeDir), f.xdgName), false, nil
	}

	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true, nil
	}

	expanded, err := expandGitPath(raw, homeDir)
	if err != nil {
		return "", true, err
	}
	return expanded, true, nil
}

// auxSourcePaths returns the host ignore and attributes files git resolves for
// this project, in gitAuxFiles order, skipping any that is not a readable
// regular file.
//
// It resolves the config itself because an explicit core.excludesFile may be
// set from an included file, which no top-level read of ~/.gitconfig sees. A
// resolver failure degrades to git's XDG defaults: this feeds `tools check`,
// which reports what exists and raises no notices of its own.
//
// The trust rules copyAuxFile applies are mirrored here rather than skipped:
// reporting a file the launch will refuse to carry would be a check that
// contradicts the thing it is checking. auxFileBindings applies the same
// refusals through the same helpers, so this reports what either mode carries.
func (g *Git) auxSourcePaths(homeDir string) []string {
	entries, _, err := g.resolveGlobalConfig(homeDir)
	if err != nil {
		entries = nil
	}
	values, origins := globalConfigMap(entries)
	// Check has no sandbox home, so neither the shared temp root nor the
	// sandbox home itself can be derived here and this bounds the project
	// directory only. It is the narrower of the two deny lists, which is the
	// right way round for something that only reports.
	untrusted := cmdpattern.ResolveRoots([]string{g.checkProjectDir()})

	var out []string
	for _, f := range gitAuxFiles {
		src, configured, err := auxSource(f, values, homeDir)
		if err != nil || src == "" {
			continue
		}
		// Report only what Setup would actually carry, or `tools check` names
		// files the launch refuses. Both refusals are mirrored here.
		if configured && !originTrusted(origins[f.key], untrusted) {
			continue
		}
		if pathDenied(src, untrusted) {
			continue
		}
		if info, err := os.Stat(src); err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, src)
	}
	return out
}

// checkProjectDir returns the directory auxSourcePaths treats as the
// sandbox-writable project tree.
//
// projectDir is only ever set by Configure, and `tools check` and `tools info`
// call Check on the registry singleton without calling it - the same shape that
// once left the aux-file branch above dead. A deny list built from an empty
// projectDir bounds nothing, so the check would report an ignore or attributes
// file the launch then refuses, which is the contradiction auxSourcePaths
// mirrors those rules to avoid. The resolver already runs in the process's
// working directory when projectDir is unset (runGitConfigList passes it as
// cmd.Dir), and a launch started from there binds that same directory
// read-write, so it is the bound that matches what is being reported. A working
// directory that cannot be determined bounds nothing, as before - this path only
// reports and raises no notices.
func (g *Git) checkProjectDir() string {
	if g.projectDir != "" {
		return g.projectDir
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// auxUntrustedRoots returns the host directories a file-valued git key may
// neither be set from nor point into.
//
// It is the launch bounds' deny list plus the sandbox home. The sandbox home is
// not one of those bounds - it is not a path the host's own PATH lookup reaches,
// which is what LaunchBounds exists to bound - but every backend mounts it
// read-write as the sandbox's $HOME (builder.go's AddSandboxHome, docker.go's
// home mount), so its whole contents are written from inside the sandbox and
// survive into the next launch. A value read from a config file there, or
// naming a file there, is therefore the sandbox's word rather than the host's;
// and os.Stat/os.ReadFile follow symlinks, so a link planted under it would
// have the host file it aims at copied into ~/.gitignore.safe, which the
// sandbox reads. Widening a deny list this way is the safe direction - see
// cmdpattern.ResolveRoots.
//
// Everything this tool itself mounts writable joins them, which sandboxWritableRoots
// derives by walking the bindings rather than naming paths. Those roots meet the same
// description and sit outside every bound LaunchBounds knows: the worktree main .git
// is not projectDir, which is the whole point of worktree mode, and ~/.ssh and
// ~/.gnupg are not either. Deriving them from the bindings is what keeps the mounts
// and the deny list from drifting apart as the tool grows more of them.
func (g *Git) auxUntrustedRoots(homeDir, sandboxHome string) []string {
	bounds := launchBoundsFor(homeDir, sandboxHome, g.projectDir)
	roots := append([]string{sandboxHome}, bounds.UntrustedRoots()...)
	roots = append(roots, g.sandboxWritableRoots(homeDir)...)
	return cmdpattern.ResolveRoots(roots)
}

// copyAuxFile handles one file-valued key. It returns the ~/-relative
// in-sandbox path to emit for the key, and false when nothing should be
// emitted.
//
// A configured value that cannot be used raises notice.Alert naming it: the
// user set it and is getting less than it says. An unset key whose host XDG
// default is simply absent is silent - most hosts have no ~/.config/git/ignore,
// and nothing was lost.
func (g *Git) copyAuxFile(f gitAuxFile, values, origins map[string]string, homeDir, sandboxHome string) (string, bool) {
	untrusted := g.auxUntrustedRoots(homeDir, sandboxHome)

	src, configured, err := auxSource(f, values, homeDir)
	if err != nil {
		notice.Alert("git: core.%s = %q is not a path devsandbox can resolve (%v); "+
			"the sandbox will not see that file", f.emit, strings.TrimSpace(values[f.key]), err)
		return "", false
	}

	// Global scope does not mean host-derived. Git labels a value pulled in
	// through an [include] as global and reports the included file as its
	// origin, so a global config that includes a file from the project tree -
	// bind-mounted read-write - lets the sandbox choose which host file gets
	// read and copied in. Anchor on where the value came from, not on the scope
	// git stamped it with.
	if configured && !originTrusted(origins[f.key], untrusted) {
		notice.Alert("git: core.%s is set from %s, which the sandbox can write; "+
			"devsandbox does not act on it, so the setting is not carried into the sandbox",
			f.emit, originDisplay(origins[f.key]))
		return "", false
	}
	if src == "" {
		// An explicitly empty value names no file. Nothing to carry, and no
		// reason to substitute the default the user wrote over.
		return "", false
	}

	info, err := os.Stat(src)
	if err != nil || !info.Mode().IsRegular() {
		if configured {
			notice.Alert("git: core.%s points at %s, which is not a readable regular file; "+
				"the sandbox will not see it", f.emit, src)
		}
		return "", false
	}

	// A source inside the project directory is already mounted there. Copying
	// it would freeze a snapshot taken at launch over the live file. The shared
	// temp directory is denied for the same reason plus a stronger one: it is
	// bound read-write at an identical path, so its contents are the sandbox's.
	if pathDenied(src, untrusted) {
		if configured {
			notice.Alert("git: core.%s points at %s, which is inside a directory the sandbox writes; "+
				"devsandbox does not copy it, so the setting is not carried into the sandbox", f.emit, src)
		}
		return "", false
	}

	data, err := os.ReadFile(src)
	if err != nil {
		if configured {
			notice.Alert("git: core.%s points at %s, which could not be read (%v); "+
				"the sandbox will not see it", f.emit, src, err)
		}
		return "", false
	}

	if err := fsutil.WriteFileAtomic(filepath.Join(sandboxHome, f.safeName), data, 0o644); err != nil {
		// devsandbox's own failure, not a mistake in the user's config, so it
		// is reported whether the key was configured or defaulted.
		notice.Alert("git: could not copy %s into the sandbox (%v); the sandbox will not see it", src, err)
		return "", false
	}

	// Emitted as a ~/-relative path rather than an absolute one: git expands a
	// leading ~/ in a path-valued key against $HOME, and the sandbox home is
	// not at the same absolute path on every backend - bwrap binds it at the
	// host home, Docker and krun at /home/sandboxuser. An absolute host path
	// here would resolve on bwrap and name nothing on the other two, which git
	// ignores in silence.
	return "~/" + f.safeName, true
}

// auxFileBindings returns the bindings that carry the host's global ignore and
// attributes files into a readwrite sandbox.
//
// readwrite mounts the host ~/.gitconfig verbatim, so the values of
// core.excludesFile and core.attributesFile arrive inside the sandbox exactly
// as the host wrote them - naming host paths nothing mounts. Git ignores a
// missing excludesFile and attributesFile with exit 0 and no warning, so the
// setting is lost with nothing on screen to say so. Binding the file the value
// already names is what makes it resolve, and it is why nothing here rewrites
// the value the way readonly's generated config does.
//
// The trust rules are copyAuxFile's, applied through the same helpers rather
// than restated: a value set from a file the sandbox can write, or naming a
// file inside a directory the sandbox writes, is the sandbox's word about which
// host file devsandbox should mount. Those refusals are silent, because the
// alternative to acting on them is mounting nothing, which is what git already
// does with the value.
func (g *Git) auxFileBindings(values, origins map[string]string, homeDir, sandboxHome string) []Binding {
	untrusted := g.auxUntrustedRoots(homeDir, sandboxHome)

	var bindings []Binding
	for _, f := range gitAuxFiles {
		src, configured, err := auxSource(f, values, homeDir)
		if err != nil {
			// Only a configured value can fail to expand, and the user set it:
			// they are getting less than the config says.
			notice.Alert("git: core.%s = %q is not a path devsandbox can resolve (%v); "+
				"the sandbox will not see that file", f.emit, strings.TrimSpace(values[f.key]), err)
			continue
		}
		if src == "" {
			continue
		}
		if configured && !originTrusted(origins[f.key], untrusted) {
			continue
		}
		if pathDenied(src, untrusted) {
			continue
		}
		// A file the host does not have is not a loss: git ignores it on the
		// host too, so there is nothing to carry and nothing to report.
		if info, err := os.Stat(src); err != nil || !info.Mode().IsRegular() {
			continue
		}

		dest, homeRelative := auxDest(f, values[f.key], src, configured, homeDir)
		bindings = append(bindings, configRefBinding(src, dest, homeRelative))
	}
	return bindings
}

// configRefBinding builds the binding that carries one host file the global git
// config *names* into a readwrite sandbox - the ignore and attributes files, and
// nothing else.
//
// Type and ReadOnly are deliberately unset, unlike the readonly aux bindings in
// readOnlyBindings: those point at generated copies in sandboxHome, where an
// overlay would be meaningless. These point at the host's own files, so they
// take whatever ResolveBindingType gives them and follow the user wherever they
// move the mount mode. That is safe only because devsandbox never parses them:
// nothing in an ignore file decides what the next launch mounts. Anything
// devsandbox reads keys out of goes through configChainBinding instead.
//
// Under the default split policy this resolves to MountTmpOverlay, which for a
// *file* source is not an overlay at all: builder.go's applyBinding and
// docker.go both downgrade a non-directory overlay to a read-only bind, because
// overlayfs needs a directory. So under every policy except an explicit
// readwrite these arrive read-only anyway.
//
// Dest is always passed in explicitly, never left empty for the verbatim case:
// docker.go's remapping switch tests an empty Dest first and rewrites it under
// the container home, so leaving it empty both silently disables
// HomeRelativeDest and rewrites a path that has to stay verbatim.
func configRefBinding(src, dest string, homeRelative bool) Binding {
	return Binding{
		Source:           src,
		Dest:             dest,
		HomeRelativeDest: homeRelative,
		Optional:         true,
		Category:         CategoryConfig,
	}
}

// configChainBinding carries one file devsandbox itself reads keys out of: a
// global config root, or an [include] target whose settings it resolves.
//
// It is pinned read-only whatever the mount mode says, and that is a security
// boundary rather than a policy default. auxFileBindings and
// includeOriginBindings decide which host files to mount by reading these, and
// they anchor that decision on the origin path - so a config file the sandbox
// can write is a config file the sandbox can use to choose. Left following the
// mount mode, an explicit mount_mode = "readwrite" made ~/.gitconfig a writable
// host bind: a sandbox could append core.excludesFile = ~/.aws/credentials to
// the real file on one launch and have devsandbox bind that file in - writable -
// on the next, with originTrusted and pathDenied both satisfied. The deny list
// cannot close that one, because the origin is the user's own ~/.gitconfig and
// denying it would refuse the whole feature.
//
// Only an explicit readwrite mount mode changes behavior here. split, overlay
// and tmpoverlay all downgrade a file source to a read-only bind already, and
// readonly asks for one outright.
func configChainBinding(src, dest string, homeRelative bool) Binding {
	b := configRefBinding(src, dest, homeRelative)
	b.Type = MountBind
	b.ReadOnly = true
	return b
}

// auxDest returns the in-sandbox path a file-valued key resolves to, and
// whether that path is inside the sandbox home.
//
// The value's *spelling* decides this, not where the file sits on the host. A
// ~/-spelled value is re-expanded by git against the sandbox's own $HOME, which
// is the host home only on bwrap; Docker and krun mount it at
// /home/sandboxuser, so the binding has to follow with HomeRelativeDest. An
// absolute value names the same path on every backend and is bound verbatim.
//
// An unset key is the case that is easy to get wrong: its destination comes
// from homeDir, never from hostGitXDGDir. builder.go pins XDG_CONFIG_HOME to
// $HOME/.config inside the sandbox unconditionally, so in-sandbox git reads
// $HOME/.config/git/<name> however the host's own XDG_CONFIG_HOME is set - and
// on a host that points it outside $HOME, hostGitXDGDir is the right Source and
// the wrong Dest.
//
// Dest is always set explicitly, never left empty for the verbatim case:
// docker.go's remapping switch tests an empty Dest first and rewrites it under
// the container home, so leaving it empty both silently disables
// HomeRelativeDest and rewrites a path that has to stay verbatim.
func auxDest(f gitAuxFile, raw, src string, configured bool, homeDir string) (dest string, homeRelative bool) {
	if !configured {
		return filepath.Join(homeDir, ".config", "git", f.xdgName), true
	}
	// auxSource expanded this same value without error, so only the flag it
	// discards is left to recover.
	_, homeRelative, _ = expandGitPathRel(strings.TrimSpace(raw), homeDir)
	return src, homeRelative
}

// gitIncludeKey is how git reports an unconditional [include] directive.
const gitIncludeKey = "include.path"

// gitIncludePathSuffix is the trailing component of every include directive git
// honours: `include.path` and `includeif.<condition>.path`.
const gitIncludePathSuffix = ".path"

// isIncludePathKey reports whether key is an include directive.
//
// The includeIf form is matched as a prefix plus a suffix rather than by
// splitting on `.`, because the condition is a subsection carrying `.` and `/`
// of its own - `includeif.gitdir:~/work/.path` is one key with three dots in
// it. The condition must be non-empty, which is what keeps a bare
// `includeif.path` - a key git does not act on - out.
func isIncludePathKey(key string) bool {
	if key == gitIncludeKey {
		return true
	}
	cond, ok := strings.CutPrefix(key, gitIncludeIfPrefix)
	if !ok {
		return false
	}
	cond, ok = strings.CutSuffix(cond, gitIncludePathSuffix)
	return ok && cond != ""
}

// gitOriginRef is one global-scope config file: where it lives on the host, and
// where the sandbox has to see it.
//
// The two are not the same string in general. A config read from an
// XDG_CONFIG_HOME the host points outside $HOME lives at /opt/cfg/git/config
// and is read inside the sandbox at $HOME/.config/git/config, because
// builder.go pins XDG_CONFIG_HOME to $HOME/.config there unconditionally. An
// absolutely-spelled include names the same path on every backend and is bound
// verbatim.
type gitOriginRef struct {
	src          string
	dest         string
	homeRelative bool
}

// rootConfigRefs returns the two files git reads the global scope from, keyed
// by their cleaned host path.
//
// Both are home-relative destinations. ~/.gitconfig is trivially so; the XDG
// config is so because in-sandbox git reads $HOME/.config/git/config whatever
// the host's own XDG_CONFIG_HOME says - which makes hostGitXDGDir the right
// Source and the wrong Dest, exactly as in auxDest.
func rootConfigRefs(homeDir string) map[string]gitOriginRef {
	xdg := filepath.Join(hostGitXDGDir(homeDir), "config")
	gitconfig := filepath.Join(homeDir, ".gitconfig")
	return map[string]gitOriginRef{
		filepath.Clean(xdg): {
			src:          xdg,
			dest:         filepath.Join(homeDir, ".config", "git", "config"),
			homeRelative: true,
		},
		filepath.Clean(gitconfig): {
			src:          gitconfig,
			dest:         gitconfig,
			homeRelative: true,
		},
	}
}

// originFilePath extracts the host path from git's origin token, cleaned.
//
// Cleaning is load-bearing rather than tidy: git does not normalize the origin
// it reports, so an include spelled `~/sub/../sub/inc.gitconfig` is reported as
// origin `file:/home/u/sub/../sub/inc.gitconfig` while filepath.Join collapses
// the same spelling to `/home/u/sub/inc.gitconfig`. Comparing the two raw finds
// no match, and the origin is then either dropped - losing the include in
// silence - or bound at a destination nothing derived.
//
// A non-file origin is refused rather than guessed at; the other forms
// ("command line:", "blob:", "standard input:") cannot occur for the global
// scope of a plain `git config --list`, so deny-by-default costs nothing and an
// alert for it would be noise.
func originFilePath(origin string) (string, bool) {
	path, ok := strings.CutPrefix(origin, gitOriginFilePrefix)
	if !ok || path == "" {
		return "", false
	}
	return filepath.Clean(path), true
}

// includeTarget resolves one include directive to the file it names.
//
// The value's spelling decides the destination, and a *relative* spelling is
// the case that only works if the parent is carried along: git resolves it
// against the directory of the file that declared it, so both halves of the ref
// are derived from the parent's - the source from where the parent sits on the
// host, the destination from where the sandbox reads it - and the
// home-relative flag is inherited rather than recomputed. Those three can
// disagree: a relative include from an out-of-$HOME XDG config has a source
// under /opt and a home-relative destination under $HOME/.config/git.
func includeTarget(e gitConfigEntry, parent gitOriginRef, homeDir string) (gitOriginRef, bool) {
	if !isIncludePathKey(e.key) {
		return gitOriginRef{}, false
	}
	value := strings.TrimSpace(e.value)
	if value == "" {
		// [include] with no path names no file. Git ignores it; so does this.
		return gitOriginRef{}, false
	}

	if path, homeRelative, err := expandGitPathRel(value, homeDir); err == nil {
		return gitOriginRef{src: path, dest: path, homeRelative: homeRelative}, true
	}

	if strings.HasPrefix(value, "~") {
		// The ~user/ form, which git resolves from the password database.
		// The user set it and is getting less than the config says.
		notice.Alert("git: %s = %q is not a path devsandbox can resolve; "+
			"the sandbox will not see that included config", e.key, value)
		return gitOriginRef{}, false
	}

	dest := filepath.Join(filepath.Dir(parent.dest), value)
	return gitOriginRef{
		src:          filepath.Join(filepath.Dir(parent.src), value),
		dest:         dest,
		homeRelative: parent.homeRelative && pathUnderDir(dest, homeDir),
	}, true
}

// includeOriginBindings returns the bindings that carry every config file the
// host's global git configuration is actually assembled from into a readwrite
// sandbox.
//
// readwrite mounts ~/.gitconfig verbatim, so an [include] or [includeIf]
// directive arrives inside the sandbox spelled exactly as the host wrote it,
// naming a file nothing mounts. Git ignores a missing include target with exit
// 0 and no warning, so an identity-per-directory setup silently loses its
// identity in the one mode where commits land.
//
// The binding set is the distinct file: origins of the global scope, which is
// the right filter for two reasons that fall out rather than being imposed: a
// file contributing nothing is never reported as an origin and needs no binding
// (git ignores a missing include silently), while an intermediate file in a
// nested chain still appears, because declaring include.path *is* a contributed
// key with that file as its origin. A non-matching includeIf target contributes
// no origin and so is never bound - which keeps a work-identity file out of a
// personal project's sandbox for free.
//
// entries must be in the order git reported them: git emits an include
// directive before the keys it pulls in, so a single forward pass sees every
// declaring file before the file it declares.
//
// The trust rules are copyAuxFile's, applied through the same helpers. An
// origin under a root the sandbox writes is refused, and so is everything it
// declares: the refusal is about who chose the path, and a config file the
// sandbox can rewrite chooses its own include targets. An origin that was never
// declared by a trusted file and is not one of git's own two roots is refused
// on the same deny-by-default footing. All of it is silent, because the
// alternative to acting on such a value is mounting nothing, which is what git
// already does with it.
func (g *Git) includeOriginBindings(entries []gitConfigEntry, homeDir, sandboxHome string) []Binding {
	untrusted := g.auxUntrustedRoots(homeDir, sandboxHome)
	refs := rootConfigRefs(homeDir)
	denied := make(map[string]bool)
	bound := make(map[string]bool)

	var bindings []Binding
	for _, e := range entries {
		origin, ok := originFilePath(e.origin)
		if !ok || denied[origin] {
			continue
		}

		ref, known := refs[origin]
		if !known {
			denied[origin] = true
			continue
		}

		if !bound[origin] {
			if pathDenied(ref.src, untrusted) {
				denied[origin] = true
				continue
			}
			bound[origin] = true
			bindings = append(bindings, configChainBinding(ref.src, ref.dest, ref.homeRelative))
		}

		target, ok := includeTarget(e, ref, homeDir)
		if !ok {
			continue
		}
		// First declaration wins, matching git's own read order. A target that
		// is already a root - an [include] naming ~/.gitconfig - keeps the root
		// destination it was going to be bound at anyway.
		//
		// One destination per source, deliberately. A host that includes the
		// same file twice under two spellings - `~/inc.gitconfig` and
		// `/home/u/inc.gitconfig` - reads it at two guest paths on Docker and
		// krun, so carrying both inclusions would take two mounts; but those
		// two destinations are the *same string* on bwrap, where the sandbox
		// home is bound at the host home path, and a second mount on one
		// destination is a trackMount panic - a dead launch, in place of a
		// second application of a file whose contents are identical. The
		// residual cost is documented with the includeIf caveat in
		// docs/tools.md.
		if key := filepath.Clean(target.src); !denied[key] {
			if _, exists := refs[key]; !exists {
				refs[key] = target
			}
		}
	}

	return bindings
}

// expandGitPath turns a git path value into an absolute host path.
//
// A leading ~/ is replaced with homeDir and an absolute path passes through.
// Anything else - a bare relative path, or the ~user/ form git resolves from
// the password database - is rejected rather than guessed at. The expansion is
// done here rather than by git itself because `git config --get --type=path`
// resolves across all scopes (a repo-local value would win) and
// `git config --list --type=path` expands every value, corrupting non-path ones.
func expandGitPath(value, homeDir string) (string, error) {
	path, _, err := expandGitPathRel(value, homeDir)
	return path, err
}

// expandGitPathRel is expandGitPath plus the distinction expandGitPath drops:
// whether the value was written ~/-relative.
//
// That distinction decides where a binding for the file has to land. The
// sandbox home is bound at the host home path on bwrap and at
// /home/sandboxuser on Docker and krun, so a ~/-spelled value resolves to a
// different absolute path per backend and its binding must be marked
// HomeRelativeDest; an absolute value names the same path everywhere and must
// be bound verbatim. Expanding both against homeDir and forgetting which was
// which mounts the file where Docker and krun never look, which git ignores in
// silence.
//
// A ~/ spelling that climbs back out of $HOME - `~/../shared/team.gitconfig` -
// is reported as *not* home-relative, because the flag means "a path inside the
// sandbox home" and this is not one. git expands the value by concatenation and
// leaves the .. for the kernel, so the origin it reports still carries the
// segment; filepath.Join cleans it away, and a Dest with the host home prefix
// gone is one docker.go's remapHomePrefix leaves untouched - the flag would
// read as applied while changing nothing, which is the silent no-op it exists
// to remove. Such a value is carried verbatim, which resolves on bwrap and on a
// Docker host whose home shares a parent with the container home; where it does
// not, git ignores the missing file exactly as it would without the mount. See
// docs/tools.md for the same backend-dependent caveat on includeIf.
func expandGitPathRel(value, homeDir string) (path string, homeRelative bool, err error) {
	switch {
	case strings.HasPrefix(value, "~/"):
		expanded := filepath.Join(homeDir, value[2:])
		return expanded, pathUnderDir(expanded, homeDir), nil
	case filepath.IsAbs(value):
		return filepath.Clean(value), false, nil
	default:
		return "", false, errors.New("not an absolute or ~/ path")
	}
}

// pathUnderDir reports whether path names something strictly inside dir. The
// directory itself does not count: a binding whose Dest is the sandbox home
// would mount over the home mount, and HomeRelativeDest describes a path
// *within* it.
func pathUnderDir(path, dir string) bool {
	return strings.HasPrefix(filepath.Clean(path), filepath.Clean(dir)+string(filepath.Separator))
}

// pathDenied reports whether path lands in one of the sandbox-writable roots.
//
// Both spellings of the path are tested, because roots already carries both
// spellings of each root and neither side alone is enough: projectDir comes
// from os.Getwd(), which returns $PWD verbatim when it names the same
// directory, so a shell that cd'd through a symlink hands devsandbox the link's
// name while the bind mount uses the target's inode. A config value may then be
// written as either one. Widening a *deny* list this way is the only direction
// that is safe - see cmdpattern.ResolveRoots.
func pathDenied(path string, roots []string) bool {
	if path == "" || len(roots) == 0 {
		return false
	}
	for _, spelling := range cmdpattern.ResolvedSpellings(path) {
		if cmdpattern.PathUnder(spelling, roots) {
			return true
		}
	}
	return false
}

// gitOriginFilePrefix is the origin token git emits for a value read from a
// config file. The other forms ("command line:", "blob:", "standard input:")
// cannot occur for the global scope of a plain `git config --list`.
const gitOriginFilePrefix = "file:"

// originTrusted reports whether a config value read from origin may be acted
// on. Deny by default: an origin devsandbox cannot resolve to a host file is
// refused rather than assumed benign.
func originTrusted(origin string, untrustedRoots []string) bool {
	path, ok := strings.CutPrefix(origin, gitOriginFilePrefix)
	if !ok || path == "" {
		return false
	}
	return !pathDenied(path, untrustedRoots)
}

// originDisplay renders an origin token for a notice, falling back to the raw
// token when it is not the file form.
func originDisplay(origin string) string {
	if path, ok := strings.CutPrefix(origin, gitOriginFilePrefix); ok && path != "" {
		return path
	}
	if origin == "" {
		return "an unknown source"
	}
	return origin
}

// generateSafeGitconfig writes a sanitized gitconfig built from the resolved
// global-scope configuration.
//
// values is the fully resolved map, so an identity supplied by an [include] or
// [includeIf] block is already in it. Only the allowlisted keys are emitted;
// everything else - credential.helper, alias.*, url.*.insteadOf,
// http.extraHeader, sendemail.smtpPass, user.signingkey and the
// includeif.<cond>.path directives themselves - is dropped.
//
// The write is atomic: the generator now runs on every launch and concurrent
// sessions share sandboxHome, so a truncate-in-place would be visible to a
// running session through its read-only bind mount, whereas a rename leaves
// that session on the inode it already pinned.
func generateSafeGitconfig(values map[string]string, dst string) error {
	content := "[user]\n"
	if name, ok := safeConfigValue("user.name", values["user.name"]); ok {
		content += "\tname = " + name + "\n"
	}
	if email, ok := safeConfigValue("user.email", values["user.email"]); ok {
		content += "\temail = " + email + "\n"
	}

	// The file-valued keys already point at the in-sandbox copies: copyAuxFiles
	// rewrote the ones it copied and deleted the rest, so whatever is left here
	// resolves inside the sandbox.
	var core strings.Builder
	for _, f := range gitAuxFiles {
		if v, ok := safeConfigValue("core."+f.emit, values[f.key]); ok {
			core.WriteString("\t" + f.emit + " = " + v + "\n")
		}
	}
	if core.String() != "" {
		content += "[core]\n" + core.String()
	}

	return fsutil.WriteFileAtomic(dst, []byte(content), 0o644)
}

// safeConfigValue renders one allowlisted value for the generated config,
// reporting false when there is nothing to emit.
//
// The rendering is not cosmetic. A value is arbitrary text from the host's
// config, and writing it raw makes the allowlist decorative: git keeps the
// newlines in a `name = "Ada\n[core]\n\tsshCommand = …"` value, so pasting it
// after `name = ` opens a section this function never agreed to emit, and every
// key after it lands inside that section. Quieter corruption comes from the
// comment introducers - an unquoted `Jane #1 Dev` reads back as `Jane`. Values
// are therefore emitted git-quoted, and a value carrying a control character -
// which no quoting can represent on one line - is dropped with a notice rather
// than written in a form that would mean something else.
func safeConfigValue(key, raw string) (string, bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", false
	}
	if hasControlRune(v) {
		notice.Alert("git: %s contains a control character and cannot be written to the sandbox "+
			"gitconfig safely; the key is dropped", key)
		return "", false
	}
	return quoteGitConfigValue(v), true
}

// hasControlRune reports whether s carries a Unicode control character. The
// scan is over runes, not bytes: the C1 controls arrive UTF-8-encoded as bytes
// >= 0xC2 and would sail straight through a byte-wise `< 0x20` test.
func hasControlRune(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// quoteGitConfigValue renders v in git's double-quoted config form, which makes
// `#`, `;` and surrounding whitespace literal. Only `"` and `\` need escaping
// inside it; callers reject control characters before reaching here.
//
// The scan is byte-wise, unlike hasControlRune's. A config value is a byte
// string, not necessarily UTF-8, and ranging over a string decodes an invalid
// byte as utf8.RuneError - which WriteRune would then emit as U+FFFD, silently
// rewriting a Latin-1 name instead of copying it. A check may decode; a writer
// must not.
func quoteGitConfigValue(v string) string {
	var b strings.Builder
	b.Grow(len(v) + 2)
	b.WriteByte('"')
	for i := 0; i < len(v); i++ {
		if v[i] == '"' || v[i] == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(v[i])
	}
	b.WriteByte('"')
	return b.String()
}

func (g *Git) Check(homeDir string) CheckResult {
	result := CheckBinary("git", "Install via system package manager (apt install git, pacman -S git)")
	if !result.Available {
		return result
	}

	// Add mode info
	switch g.mode {
	case GitModeReadWrite:
		result.AddIssue("mode: readwrite (full access)")
	case GitModeDisabled:
		result.AddIssue("mode: disabled")
	default:
		result.AddIssue("mode: readonly (safe, default)")
	}

	// Every file git reads as global-scope configuration, not just
	// ~/.gitconfig: a user whose identity lives solely in the XDG location has
	// no ~/.gitconfig at all, and the safe config generator reads that file too.
	result.AddConfigPaths(globalConfigSources(homeDir)...)
	if len(result.ConfigPaths) == 0 {
		result.AddIssue("no global git config found (~/.gitconfig or ~/.config/git/config) (will use defaults)")
	}

	// The global ignore and attributes files carried into the sandbox. Their
	// location follows the resolved config, so a core.excludesFile set from an
	// included file is reported at the path it actually names. Both readonly
	// and readwrite carry them - readonly as generated copies the safe config
	// points at, readwrite as bind mounts at the paths the host config already
	// names - so both report them. Only disabled carries nothing, and resolving
	// costs a git subprocess, so it neither reports nor pays for it.
	//
	// Tested the same way the mode line above is - by what the mode is *not*.
	// `tools check` and `tools info` call Check on the registry singleton
	// without ever calling Configure, so g.mode is still the zero value there;
	// an `== GitModeReadOnly` test made this branch dead on the only path that
	// reaches it, while the switch above kept printing "readonly (safe,
	// default)" from its default arm.
	if g.mode != GitModeDisabled {
		result.AddConfigPaths(g.auxSourcePaths(homeDir)...)
	}

	// Check for SSH and GPG in readwrite mode
	if g.mode == GitModeReadWrite {
		result.AddConfigPaths(
			filepath.Join(homeDir, ".ssh"),
			filepath.Join(homeDir, ".gnupg"),
		)
	}

	return result
}

// parseGitconfig extracts the allowlisted keys from a gitconfig file's own
// top-level sections, keyed the way git reports them ("user.name"), with the
// include directives that file declares left in place among them.
//
// The entries come back **in file order**, and the include directives are
// entries of their own rather than a separate list, because git expands an
// include where it stands: a key set above `[include]` is overridden by the
// included file, and the same key set below it wins. A caller that expanded a
// separate include list either before or after the declaring file's own keys
// would be guessing at that, and the guess decides which host file gets bound
// for core.excludesFile - so the wrong one is mounted and the right one is not,
// which git reports as exit 0 and no ignore rules.
//
// err is non-nil when the file could not be opened or could not be read to the
// end. It is not cosmetic: the degraded path decides whether the launch says an
// include was lost from what this returns, so a truncated read that came back
// as "no includes" would suppress the very report the fallback exists to make.
//
// Values come back in git's semantic form - unquoted, with an inline comment
// stripped - because that is what the caller re-quotes. Returning the raw text
// after the '=' is what made a host `name = "Jane Doe"` reach the sandbox as
// `"Jane Doe"` with the quote characters part of the name, once emitted values
// started being git-quoted.
//
// An include entry carries the literal unexpanded spelling as its value,
// because that spelling is what decides where the target has to be mounted.
// conditional reports only that an [includeIf] section was present, never what
// it names: its condition cannot be evaluated here, and binding the target
// anyway would carry a work identity into a personal project's sandbox. It
// exists so the caller can say the include was lost rather than lose it in
// silence.
func parseGitconfig(path string) (entries []gitConfigEntry, conditional bool, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = file.Close() }()

	origin := gitOriginFilePrefix + path
	scanner := bufio.NewScanner(file)
	section := ""

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		if strings.HasPrefix(line, "[") {
			section = parseSectionName(line)
			conditional = conditional || isIncludeIfHeader(line)
			// Git accepts a key on the same line as the header that opens its
			// section - `[include] path = ~/inc` and `[user] name = Ada` both
			// take effect - so what follows the ']' is a key/value line for the
			// section just opened, not decoration. Dropping it made a one-line
			// [include] read as "no includes at all", which on the degraded
			// path silently loses the target *and* the report that says so.
			rest := sectionRemainder(line)
			if rest == "" {
				continue
			}
			line = rest
		}
		if section == "" {
			continue
		}

		rawKey, rawValue, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := section + "." + strings.ToLower(strings.TrimSpace(rawKey))
		value := parseConfigValue(strings.TrimLeft(rawValue, " \t"))
		if key == gitIncludeKey {
			// [include] with no path names no file; git ignores it, and
			// reporting it would have the caller alert about nothing.
			if value == "" {
				continue
			}
		} else if !fallbackConfigKeys[key] {
			continue
		}
		// scope is left unset: these entries never reach globalScopeEntries,
		// which is the only reader, and every file parsed here is one git
		// reads as global by construction.
		entries = append(entries, gitConfigEntry{origin: origin, key: key, value: value})
	}

	if err := scanner.Err(); err != nil {
		return entries, conditional, err
	}
	return entries, conditional, nil
}

// sectionRemainder returns what a section header line carries after its
// closing ']', trimmed, or "" when the line is a bare header.
//
// It cuts at the first ']' exactly as parseSectionName does, so the two agree
// about where the header ends. A header carrying a subsection is quoted, and
// parseSectionName returns "" for those, so any remainder after one is dropped
// by the caller's empty-section guard rather than attributed to the wrong
// section.
func sectionRemainder(line string) string {
	_, rest, ok := strings.Cut(strings.TrimPrefix(line, "["), "]")
	if !ok {
		return ""
	}
	return strings.TrimSpace(rest)
}

// isIncludeIfHeader reports whether a section header opens a conditional
// include - `[includeIf "gitdir:~/work/"]`.
//
// It reads the raw header rather than going through parseSectionName, which
// deliberately returns nothing for a subsectioned header so that an include
// directive is never mistaken for a plain section.
func isIncludeIfHeader(line string) bool {
	name, _, ok := strings.Cut(strings.TrimPrefix(line, "["), "\"")
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(name), "includeif")
}

// fallbackConfigKeys is the set parseGitconfig looks for: the same allowlist
// generateSafeGitconfig emits, so the fallback path carries what the resolver
// would have rather than a subset of it.
var fallbackConfigKeys = func() map[string]bool {
	keys := map[string]bool{"user.name": true, "user.email": true}
	for _, f := range gitAuxFiles {
		keys[f.key] = true
	}
	return keys
}()

// parseSectionName returns the lowercased name of a plain `[section]` header.
//
// A header carrying a subsection (`[remote "origin"]`, `[includeIf "gitdir:…"]`)
// returns the empty string: the keys read here only ever live in a plain
// section, and an include directive in particular must not be mistaken for one.
func parseSectionName(line string) string {
	inner, _, ok := strings.Cut(strings.TrimPrefix(line, "["), "]")
	if !ok || strings.Contains(inner, "\"") {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(inner))
}

// parseConfigValue renders a raw gitconfig value the way git reads it: an
// unquoted '#' or ';' starts a comment that ends the value, unquoted trailing
// whitespace is dropped, and double quotes are removed with git's escapes
// (\", \\, \n, \t, \b) applied. Whitespace inside quotes is preserved, which is
// what a quoted value is for.
func parseConfigValue(raw string) string {
	var b []byte
	keep := 0
	inQuotes := false

	for i := 0; i < len(raw); i++ {
		c := raw[i]

		if c == '\\' && i+1 < len(raw) {
			i++
			switch raw[i] {
			case 'n':
				b = append(b, '\n')
			case 't':
				b = append(b, '\t')
			case 'b':
				b = append(b, '\b')
			default:
				// git rejects an unknown escape; keeping the character verbatim
				// is the reading that does not silently drop part of a value.
				b = append(b, raw[i])
			}
			keep = len(b)
			continue
		}

		if c == '"' {
			inQuotes = !inQuotes
			continue
		}
		if !inQuotes && (c == '#' || c == ';') {
			break
		}

		b = append(b, c)
		if inQuotes || (c != ' ' && c != '\t') {
			keep = len(b)
		}
	}

	return string(b[:keep])
}
