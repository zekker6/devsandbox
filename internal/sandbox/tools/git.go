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

	switch strings.ToLower(cfg.Mode) {
	case "readwrite", "read-write", "rw":
		g.mode = GitModeReadWrite
	case "disabled", "none", "off":
		g.mode = GitModeDisabled
	}
}

func (g *Git) Bindings(homeDir, sandboxHome string) []Binding {
	switch g.mode {
	case GitModeDisabled:
		return nil

	case GitModeReadWrite:
		return g.readWriteBindings(homeDir, sandboxHome)

	default: // GitModeReadOnly
		return g.readOnlyBindings(homeDir, sandboxHome)
	}
}

// gitDirSource returns the directory holding the real .git metadata.
// In worktree mode that is the main repo; otherwise it is the project dir.
func (g *Git) gitDirSource() string {
	if g.gitRepoRoot != "" && g.gitRepoRoot != g.projectDir {
		return g.gitRepoRoot
	}
	return g.projectDir
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
	isWorktree := g.gitRepoRoot != "" && g.gitRepoRoot != g.projectDir
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

// readWriteBindings returns bindings for readwrite mode (full git access).
func (g *Git) readWriteBindings(homeDir, _ string) []Binding {
	bindings := []Binding{
		{
			Source:   filepath.Join(homeDir, ".gitconfig"),
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
	if g.gitRepoRoot != "" && g.gitRepoRoot != g.projectDir {
		gitDir := filepath.Join(g.gitRepoRoot, ".git")
		if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
			bindings = append(bindings, Binding{
				Source:   gitDir,
				Dest:     gitDir,
				Category: CategoryConfig,
			})
		}
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

// Setup implements ToolWithSetup to generate the safe gitconfig and the
// sanitized per-repo .git/config used by readonly mode.
func (g *Git) Setup(homeDir, sandboxHome string) error {
	// Only generate safe configs for readonly mode
	if g.mode != GitModeReadOnly {
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

	if err := g.setupUserGitconfig(homeDir, sandboxHome); err != nil {
		return err
	}

	return g.setupRepoGitconfig(sandboxHome)
}

// setupUserGitconfig generates the sanitized ~/.gitconfig overlay.
//
// It regenerates on every launch. The mtime comparison this used to do only
// ever looked at ~/.gitconfig, so an edit to an *included* file left a stale
// safe config behind indefinitely - and a correct multi-origin check has to run
// after the resolver subprocess, which is where the cost actually is.
func (g *Git) setupUserGitconfig(homeDir, sandboxHome string) error {
	sources := existingGlobalConfigs(homeDir)

	values, origins, retriedOutsideRepo, err := g.resolveGlobalConfig(homeDir)
	switch {
	case err != nil:
		// An unsupported --show-scope means the host git predates 2.26; that is
		// not a setting the user got wrong, so it must not warn on every launch.
		if !errors.Is(err, errShowScopeUnsupported) {
			notice.Alert("git: could not read the resolved global config (%v); "+
				"falling back to the top-level sections of the global config files, so a value "+
				"defined only inside an include is missing", err)
		}
		values, origins = fallbackValues(sources)

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

// fallbackValues reads the allowlisted keys straight out of the global config
// files when the resolver is unavailable, alongside the origin of each one.
// This sees only each file's own top-level sections - a value supplied by an
// include is invisible to it, which is why it is a fallback and not the primary
// source. Files are consumed in git's read order, so a later one overrides an
// earlier one.
//
// The file-valued keys are read here too, not just the identity: leaving them
// out does not make copyAuxFiles skip them, it makes it treat a configured
// core.excludesFile as unset and carry git's XDG default instead - a file the
// host does not use, substituted for the one it does, with nothing on screen to
// say so. Origins are returned because copyAuxFile refuses a value whose origin
// it cannot resolve to a host file.
func fallbackValues(sources []string) (values, origins map[string]string) {
	values = make(map[string]string, 2+len(gitAuxFiles))
	origins = make(map[string]string, 2+len(gitAuxFiles))
	for _, path := range sources {
		for key, value := range parseGitconfig(path) {
			values[key] = value
			origins[key] = gitOriginFilePrefix + path
		}
	}
	return values, origins
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
// The second return value maps each key to the origin git reported for it.
func (g *Git) resolveGlobalConfig(homeDir string) (values, origins map[string]string, retriedOutsideRepo, err error) {
	values, origins, err = runGitConfigList(g.projectDir, homeDir)
	if err == nil || errors.Is(err, errShowScopeUnsupported) {
		return values, origins, nil, err
	}

	// os.TempDir() is not guaranteed to sit outside a repository, but a retry
	// that lands in one only fails the same way the first attempt did, and the
	// original error is what gets reported either way.
	retryValues, retryOrigins, retryErr := runGitConfigList(os.TempDir(), homeDir)
	if retryErr != nil {
		return nil, nil, nil, err
	}
	return retryValues, retryOrigins, err, nil
}

// runGitConfigList executes the resolver with dir as its working directory and
// reduces the output to the global scope. An empty dir inherits the process's
// own working directory.
func runGitConfigList(dir, homeDir string) (values, origins map[string]string, err error) {
	cmd := exec.Command("git", "config", "--list", "--show-scope", "--show-origin", "-z")
	cmd.Dir = dir
	cmd.Env = gitCommandEnv(os.Environ(), homeDir)

	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && isUnsupportedShowScope(exitErr.Stderr) {
			return nil, nil, errors.Join(errShowScopeUnsupported, err)
		}
		return nil, nil, err
	}

	values, origins = globalConfigMap(parseGitConfigList(out))
	return values, origins, nil
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

// gitCommandEnv returns env with HOME replaced by homeDir and the locale pinned
// to C.
//
// The locale is not cosmetic: git translates its diagnostics, and
// isUnsupportedShowScope decides whether to fall back silently or alert on every
// launch by matching "unknown option" and "usage:" in stderr. On a localized
// host those spellings differ, so the sentinel that exists to keep an old git
// quiet would stop recognising it. LANGUAGE overrides LC_ALL for messages in
// GNU gettext, so it is dropped rather than overridden.
func gitCommandEnv(env []string, homeDir string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "HOME="),
			strings.HasPrefix(kv, "LC_ALL="),
			strings.HasPrefix(kv, "LANGUAGE="):
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HOME="+homeDir, "LC_ALL=C")
}

// globalConfigMap reduces parsed entries to the global scope, last-wins, and
// returns the origin of each surviving value alongside it.
//
// Values pulled in through an include are labelled global by git and reported
// with the included file as their origin, so they survive the scope filter;
// last-wins is what lets such an include override the outer file, matching
// git's own precedence. The origins map is keyed identically and holds git's
// raw origin token ("file:/path/to/config"), which copyAuxFile needs in order
// to tell a host-owned config file from one the sandbox can write.
func globalConfigMap(entries []gitConfigEntry) (values, origins map[string]string) {
	values = make(map[string]string, len(entries))
	origins = make(map[string]string, len(entries))
	for _, e := range entries {
		if e.scope != "global" {
			continue
		}
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
// contradicts the thing it is checking.
func (g *Git) auxSourcePaths(homeDir string) []string {
	values, origins, _, err := g.resolveGlobalConfig(homeDir)
	if err != nil {
		values, origins = nil, nil
	}
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
func auxUntrustedRoots(homeDir, sandboxHome, projectDir string) []string {
	bounds := launchBoundsFor(homeDir, sandboxHome, projectDir)
	return cmdpattern.ResolveRoots(append([]string{sandboxHome}, bounds.UntrustedRoots()...))
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
	untrusted := auxUntrustedRoots(homeDir, sandboxHome, g.projectDir)

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

// expandGitPath turns a git path value into an absolute host path.
//
// A leading ~/ is replaced with homeDir and an absolute path passes through.
// Anything else - a bare relative path, or the ~user/ form git resolves from
// the password database - is rejected rather than guessed at. The expansion is
// done here rather than by git itself because `git config --get --type=path`
// resolves across all scopes (a repo-local value would win) and
// `git config --list --type=path` expands every value, corrupting non-path ones.
func expandGitPath(value, homeDir string) (string, error) {
	switch {
	case strings.HasPrefix(value, "~/"):
		return filepath.Join(homeDir, value[2:]), nil
	case filepath.IsAbs(value):
		return filepath.Clean(value), nil
	default:
		return "", errors.New("not an absolute or ~/ path")
	}
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

	// The global ignore and attributes files readonly mode carries into the
	// sandbox. Their location follows the resolved config, so a core.excludesFile
	// set from an included file is reported at the path it actually names.
	// Only readonly mode carries them, and resolving costs a git subprocess, so
	// the other two modes neither report nor pay for it.
	//
	// Tested the same way the mode line above is - by what the mode is *not*.
	// `tools check` and `tools info` call Check on the registry singleton
	// without ever calling Configure, so g.mode is still the zero value there;
	// an `== GitModeReadOnly` test made this branch dead on the only path that
	// reaches it, while the switch above kept printing "readonly (safe,
	// default)" from its default arm.
	if g.mode != GitModeReadWrite && g.mode != GitModeDisabled {
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
// top-level sections, keyed the way git reports them ("user.name"). A file that
// cannot be opened yields no keys.
//
// Values come back in git's semantic form - unquoted, with an inline comment
// stripped - because that is what the caller re-quotes. Returning the raw text
// after the '=' is what made a host `name = "Jane Doe"` reach the sandbox as
// `"Jane Doe"` with the quote characters part of the name, once emitted values
// started being git-quoted.
func parseGitconfig(path string) map[string]string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()

	values := make(map[string]string, 2+len(gitAuxFiles))
	scanner := bufio.NewScanner(file)
	section := ""

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		if strings.HasPrefix(line, "[") {
			section = parseSectionName(line)
			continue
		}
		if section == "" {
			continue
		}

		rawKey, rawValue, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := section + "." + strings.ToLower(strings.TrimSpace(rawKey))
		if !fallbackConfigKeys[key] {
			continue
		}
		values[key] = parseConfigValue(strings.TrimLeft(rawValue, " \t"))
	}

	return values
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
