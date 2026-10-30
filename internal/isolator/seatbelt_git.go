package isolator

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"devsandbox/internal/cmdpattern"
	"devsandbox/internal/sandbox"
	"devsandbox/internal/sandbox/tools"
	"devsandbox/internal/shellwrap"
)

type nativeGitRepo struct {
	checkout    string
	marker      string
	common      string
	private     string
	snapshot    string
	spellings   []string
	commonInfo  fs.FileInfo
	privateInfo fs.FileInfo
}

func (p *nativePlan) prepareGit(cfg *sandbox.Config) (retErr error) {
	repos, err := discoverNativeGit(cfg)
	if err != nil {
		return err
	}
	if nativeGitMode(cfg) == "disabled" {
		for _, repo := range repos {
			if repo.private != "" {
				return errors.New("seatbelt Git disabled mode cannot expose linked worktree metadata; use readonly mode")
			}
		}
		return nil
	}
	if len(repos) == 0 {
		return nil
	}
	program, err := cmdpattern.ResolveProgram("git")
	if err != nil {
		return err
	}
	for _, root := range []string{cfg.ProjectDir, cfg.SandboxHome, p.setup} {
		if nativeWithin(root, program) {
			return errors.New("seatbelt Git executable must be outside sandbox-writable paths")
		}
	}
	stage, err := os.OpenRoot(p.setup)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, stage.Close()) }()
	for i := range repos {
		repo := &repos[i]
		name := fmt.Sprintf("git/%d", i)
		if err := snapshotNativeGit(stage, *repo, name); err != nil {
			return fmt.Errorf("seatbelt Git snapshot %s: %w", repo.checkout, err)
		}
		repo.snapshot = filepath.Join(p.setup, name)
		for _, original := range append([]string{repo.marker, repo.common, repo.private}, repo.spellings...) {
			if original != "" {
				p.policy.DenyRead = append(p.policy.DenyRead, cmdpattern.ResolvedSpellings(original)...)
			}
		}
	}
	if err := stage.MkdirAll("bin", 0o700); err != nil {
		return err
	}
	helperDir, err := nativeGitHelpers(stage, program)
	if err != nil {
		return err
	}
	p.policy.ReadOnly = append(p.policy.ReadOnly, helperDir)
	bin := filepath.Join(p.setup, "bin")
	if err := nativeGitWrite(stage, "bin/git", []byte(nativeGitDispatcher(program, bin, repos)), 0o700); err != nil {
		return err
	}
	for _, path := range []string{filepath.Join(p.setup, "git"), bin, program} {
		p.policy.ReadOnly = append(p.policy.ReadOnly, path)
		p.policy.DenyWrite = append(p.policy.DenyWrite, path)
	}
	cfg.CommandPathPrefix = bin
	p.env["PATH"] = bin + string(os.PathListSeparator) + p.env["PATH"]
	p.env["GIT_OPTIONAL_LOCKS"] = "0"
	p.env["GIT_CONFIG_NOSYSTEM"] = "1"
	return nil
}

// Discovery does not follow directory symlinks and never descends into .git.
func discoverNativeGit(cfg *sandbox.Config) ([]nativeGitRepo, error) {
	var repos []nativeGitRepo
	err := filepath.WalkDir(cfg.ProjectDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() != ".git" {
			return nil
		}
		repo, err := resolveNativeGit(path, cfg)
		if err != nil {
			return fmt.Errorf("seatbelt Git metadata %s: %w", path, err)
		}
		repos = append(repos, repo)
		if entry.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
	return repos, err
}

func resolveNativeGit(marker string, cfg *sandbox.Config) (nativeGitRepo, error) {
	repo := nativeGitRepo{checkout: filepath.Dir(marker), marker: marker}
	info, err := os.Lstat(marker)
	if err != nil {
		return repo, err
	}
	if info.IsDir() {
		repo.common, repo.commonInfo = marker, info
		return repo, nil
	}
	data, err := nativeGitRead(marker)
	if err != nil {
		return repo, err
	}
	line := strings.TrimSuffix(string(data), "\n")
	pointer, ok := strings.CutPrefix(line, "gitdir: ")
	if !ok {
		return repo, errors.New("expected a registered linked worktree gitdir file")
	}
	private, err := nativeGitPath(repo.checkout, pointer)
	if err != nil {
		return repo, err
	}
	data, err = nativeGitRead(filepath.Join(private, "commondir"))
	if err != nil {
		return repo, fmt.Errorf("linked worktree commondir: %w", err)
	}
	common, err := nativeGitPath(private, strings.TrimSuffix(string(data), "\n"))
	if err != nil {
		return repo, err
	}
	repo.spellings = []string{private, common}
	// Only the standard registered layout is supported, not arbitrary gitdir
	// indirection. Resolve ancestors of the main checkout (e.g. /var on macOS),
	// but reject links at .git or between it and the worktree metadata.
	parent, err := filepath.EvalSymlinks(filepath.Dir(common))
	if err != nil {
		return repo, err
	}
	common = filepath.Join(parent, filepath.Base(common))
	if filepath.Base(common) != ".git" {
		return repo, errors.New("linked worktree common directory must be a main checkout's .git")
	}
	resolved, err := filepath.EvalSymlinks(common)
	if err != nil || resolved != common {
		return repo, errors.New("linked worktree common metadata is unresolved or symlinked")
	}
	privateParent, err := filepath.EvalSymlinks(filepath.Dir(private))
	if err != nil {
		return repo, err
	}
	private = filepath.Join(privateParent, filepath.Base(private))
	if filepath.Dir(private) != filepath.Join(common, "worktrees") {
		return repo, errors.New("gitdir is not registered under the common worktrees directory")
	}
	for _, path := range []string{common, filepath.Join(common, "worktrees"), private} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			return repo, errors.New("linked worktree metadata must be regular directories, not symlinks")
		}
		if path == common {
			repo.commonInfo = info
		}
		if path == private {
			repo.privateInfo = info
		}
	}
	if repo.checkout == cfg.ProjectDir && cfg.GitRepoRoot != "" {
		main, err := filepath.EvalSymlinks(cfg.GitRepoRoot)
		if err != nil || common != filepath.Join(main, ".git") {
			return repo, errors.New("linked worktree common metadata differs from the host-discovered repository")
		}
	}
	data, err = nativeGitRead(filepath.Join(private, "gitdir"))
	if err != nil {
		return repo, fmt.Errorf("linked worktree registration backlink: %w", err)
	}
	backlink, err := nativeGitPath(private, strings.TrimSuffix(string(data), "\n"))
	if err != nil {
		return repo, err
	}
	backlink, err = filepath.EvalSymlinks(backlink)
	if err != nil || backlink != marker {
		return repo, errors.New("linked worktree registration backlink does not match checkout .git")
	}
	repo.common, repo.private = common, private
	return repo, nil
}

func nativeGitPath(base, value string) (string, error) {
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("empty or malformed Git metadata path")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	return filepath.Clean(value), nil
}

// Open through os.Root and refuse symlinks and special files before reading.
func nativeGitRead(path string) (data []byte, retErr error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { retErr = errors.Join(retErr, root.Close()) }()
	return nativeGitReadRoot(root, filepath.Base(path))
}

const nativeGitMaxTextSize = 1 << 20

func nativeGitReadRoot(root *os.Root, name string) (data []byte, retErr error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { retErr = errors.Join(retErr, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("git metadata %s is not a regular file", name)
	}
	data, err = io.ReadAll(io.LimitReader(f, nativeGitMaxTextSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > nativeGitMaxTextSize {
		return nil, fmt.Errorf("git metadata %s exceeds %d bytes", name, nativeGitMaxTextSize)
	}
	return data, nil
}

func nativeGitWrite(root *os.Root, name string, data []byte, mode fs.FileMode) (retErr error) {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, file.Close()) }()
	_, err = file.Write(data)
	return err
}

func snapshotNativeGit(stage *os.Root, repo nativeGitRepo, dest string) error {
	if err := stage.MkdirAll(dest, 0o700); err != nil {
		return err
	}
	if err := copyNativeGitTree(stage, repo.common, dest, repo.commonInfo, repo.private != "", false); err != nil {
		return err
	}
	if repo.private != "" {
		return copyNativeGitTree(stage, repo.private, dest, repo.privateInfo, false, true)
	}
	return nil
}

// Opening a path again must not turn a validated metadata directory into an
// unrelated tree. Check the descriptor identity before reading, and check the
// spelling on both sides of OpenRoot, which itself follows symlinks.
func openNativeGitRoot(source string, expected fs.FileInfo) (*os.Root, error) {
	if err := verifyNativeGitSource(source, expected); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err == nil && !os.SameFile(expected, info) {
		err = fmt.Errorf("git metadata directory changed since discovery: %s", source)
	}
	if err == nil {
		err = verifyNativeGitSource(source, expected)
	}
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	return root, nil
}

func verifyNativeGitSource(source string, expected fs.FileInfo) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if expected == nil || !info.IsDir() || !os.SameFile(expected, info) {
		return fmt.Errorf("git metadata directory changed since discovery: %s", source)
	}
	canonical, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	if canonical != source {
		return fmt.Errorf("git metadata directory was redirected through a symlink: %s", source)
	}
	return nil
}

func copyNativeGitTree(stage *os.Root, source, dest string, expected fs.FileInfo, shared, private bool) (retErr error) {
	root, err := openNativeGitRoot(source, expected)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, root.Close()) }()
	return fs.WalkDir(root.FS(), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if rel == "." {
			return nil
		}
		if nativeGitSkip(rel, shared, private) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink Git metadata is unsupported: %s", filepath.Join(source, rel))
		}
		name := filepath.Join(dest, rel)
		if rel == "config" || rel == "config.worktree" {
			data, err := nativeGitReadRoot(root, rel)
			if err != nil {
				return err
			}
			data, err = tools.SanitizeRepoConfig(bytes.NewReader(data), true)
			if err != nil {
				return err
			}
			return nativeGitWrite(stage, name, data, 0o600)
		}
		if entry.IsDir() {
			return stage.MkdirAll(name, 0o700)
		}
		return nativeCopyEntry(stage, root, rel, name, nil)
	})
}

func nativeGitSkip(rel string, shared, private bool) bool {
	top, _, _ := strings.Cut(rel, string(filepath.Separator))
	if top == "worktrees" || top == "modules" || top == "commondir" || top == "gitdir" || top == "locked" {
		return true
	}
	if shared {
		if !slices.Contains([]string{"branches", "config", "hooks", "info", "logs", "objects", "packed-refs", "refs", "remotes", "rr-cache", "shallow"}, top) {
			return true
		}
		for _, local := range []string{"logs/HEAD", "refs/bisect", "refs/worktree", "refs/rewritten", "logs/refs/bisect", "logs/refs/worktree", "logs/refs/rewritten"} {
			if nativeWithin(filepath.FromSlash(local), rel) {
				return true
			}
		}
	}
	// A worktree does not own config or the object database. Its config is
	// config.worktree; allowing a second config would overwrite the shared one.
	return private && slices.Contains([]string{"config", "objects", "packed-refs", "hooks", "info", "shallow"}, top)
}

// Git prepends its exec path before executing shell aliases. Mirror its helper
// entries beside the dispatcher so a child `git` still reselects its checkout,
// while external subcommands and scripts such as git-sh-setup remain available.
func nativeGitHelpers(stage *os.Root, program string) (string, error) {
	cmd := exec.Command(program, "--exec-path")
	cmd.Dir = stage.Name()
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	data, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("seatbelt Git helper directory: %w", err)
	}
	path := strings.TrimSpace(string(data))
	if !filepath.IsAbs(path) {
		return "", errors.New("git helper directory is not absolute")
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.Name() == "git" {
			continue
		}
		if err := stage.Symlink(filepath.Join(path, entry.Name()), filepath.Join("bin", entry.Name())); err != nil {
			return "", err
		}
	}
	return path, nil
}

func nativeGitDispatcher(program, bin string, repos []nativeGitRepo) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("git_program=" + shellwrap.PosixQuote(program) + "\n")
	b.WriteString("GIT_EXEC_PATH=" + shellwrap.PosixQuote(bin) + "\nexport GIT_EXEC_PATH\n")
	b.WriteString(`
# Shell aliases inherit our route, but may cd into a different repository.
if [ "${DEVSANDBOX_GIT_DIR-}" = "${GIT_DIR-}" ] && [ -n "${DEVSANDBOX_GIT_DIR-}" ]; then
    unset GIT_DIR
    if [ "${DEVSANDBOX_GIT_WORK_TREE-}" = "${GIT_WORK_TREE-}" ]; then unset GIT_WORK_TREE; fi
fi
unset DEVSANDBOX_GIT_DIR DEVSANDBOX_GIT_WORK_TREE
if [ "${GIT_DIR+x}" = x ]; then exec "$git_program" "$@"; fi
select_repo() {
    current=$(pwd -P) || return
    operand=
    for arg do
        if [ -n "$operand" ]; then
            if [ "$operand" = C ] && [ -n "$arg" ]; then
                case "$arg" in /*) next=$arg ;; *) next=$current/$arg ;; esac
                current=$(CDPATH= cd -P "$next" && pwd -P) || return
            fi
            operand=
            continue
        fi
        case "$arg" in
            --git-dir|--git-dir=*|--work-tree|--work-tree=*|--bare) return ;;
            -C) operand=C ;;
            -C?*) next=${arg#-C}; case "$next" in /*) ;; *) next=$current/$next ;; esac
                current=$(CDPATH= cd -P "$next" && pwd -P) || return ;;
            -c|--config-env|--namespace|--super-prefix|--attr-source) operand=skip ;;
            -*) ;;
            init|clone) return ;;
            *) break ;;
        esac
    done
    [ -z "$operand" ] || return
    while :; do
        case "$current" in
`)
	for _, repo := range repos {
		fmt.Fprintf(&b, "            %s) route=%s; checkout=%s; return ;;\n", shellwrap.PosixQuote(repo.checkout), shellwrap.PosixQuote(repo.snapshot), shellwrap.PosixQuote(repo.checkout))
	}
	b.WriteString(`        esac
        # A new nested repository takes precedence over a parent's snapshot.
        if [ -e "$current/.git" ] || [ -L "$current/.git" ]; then return; fi
        [ "$current" != / ] || return
        current=${current%/*}
        [ -n "$current" ] || current=/
    done
}
route=
select_repo "$@"
if [ -n "$route" ]; then
    GIT_DIR=$route GIT_WORK_TREE=$checkout
    DEVSANDBOX_GIT_DIR=$route DEVSANDBOX_GIT_WORK_TREE=$checkout
    export GIT_DIR GIT_WORK_TREE DEVSANDBOX_GIT_DIR DEVSANDBOX_GIT_WORK_TREE
fi
exec "$git_program" "$@"
`)
	return b.String()
}
