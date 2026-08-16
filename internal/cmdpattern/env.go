package cmdpattern

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// envVarNameRe accepts POSIX-safe env var names: leading letter/underscore,
// then letters/digits/underscores. It is a shape test only — it decides whether
// a token is an assignment at all, so that `--output=/tmp/o` reads as the start
// of the argv rather than as a variable. hostExecEnv below decides which names
// are actually accepted.
var envVarNameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// envValueKind says what a variable's value is allowed to carry.
type envValueKind int

const (
	// envProgram names a program the host will execute. revdiff spawns
	// whatever EDITOR names when the user opens a file in the overlay, so the
	// value is the execution, not the key.
	envProgram envValueKind = iota

	// envInert is a scalar the receiving program parses itself and never
	// executes.
	envInert
)

// hostExecEnv is the closed set of assignments the host-exec validators accept
// ahead of the program, and what each value may be.
//
// It is a fixed set rather than a name pattern because the prefix exists for
// exactly one thing: the revdiff launcher forwarding the caller's editor to an
// overlay whose parent process predates the user's shell rc files, plus the one
// feature flag it passes the same way. Every other name a `KEY=VAL` prefix
// could carry is something the sandbox chose, so it is refused rather than
// weighed — BASH_ENV, PYTHONSTARTUP, PERL5OPT and the loader variables all
// exist, and enumerating the dangerous ones is the losing side of that list.
var hostExecEnv = map[string]envValueKind{
	"EDITOR": envProgram,
	"VISUAL": envProgram,
	// The launcher passes exit-code-on-annotations this way because an old
	// revdiff binary ignores an unknown env var but hard-fails on an unknown
	// flag. revdiff parses it as a bool.
	"REVDIFF_EXIT_CODE_ON_ANNOTATIONS": envInert,
}

// maxEnvValueLen bounds a value. Nothing legitimate here is long, and an
// unbounded value is a payload the host would carry into its own environment.
const maxEnvValueLen = 256

var (
	// envProgramValueRe accepts a bare program name or an absolute path built
	// from safe filename characters. No whitespace, so a value cannot smuggle
	// arguments; no shell-significant byte, since revdiff may hand the value to
	// a shell of its own; no `~`, which the shell would expand.
	envProgramValueRe = regexp.MustCompile(`^/?[A-Za-z0-9._+-]+(?:/[A-Za-z0-9._+-]+)*$`)

	// envProgramFlagRe is the *shape* pre-filter for an option word that may
	// follow the program in an EDITOR/VISUAL value. Passing it is necessary and
	// not sufficient: editorOptionWords decides which words are actually
	// accepted, per editor.
	envProgramFlagRe = regexp.MustCompile(`^--?[A-Za-z0-9][A-Za-z0-9-]*$`)

	// envInertValueRe accepts a short scalar with no path separator, so an
	// inert variable can never grow into one that names a file.
	envInertValueRe = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,32}$`)
)

// maxEnvProgramFlags bounds how many option words may trail the program.
const maxEnvProgramFlags = 4

// consumeEnvAssignments walks the leading assignments of a command head and
// returns the index of the first token that is not one.
//
// A token that does not look like an assignment ends the prefix and starts the
// argv, which is how `'--output=/tmp/o'` stays an argument. A token that does
// look like one but is not accepted fails the whole head: falling through to
// the argv matcher instead would decide the same case for the wrong reason.
//
// envConsumed says whether an `env` program word was stripped ahead of these
// tokens, and it decides what a *quoted* token means. Only `env` parses its own
// arguments: with it, `'EDITOR=nvim'` is an assignment it applies to the child.
// Without it the shell is parsing, and POSIX recognizes an assignment prefix
// only while the name and `=` are unquoted - so a quoted token is the command
// word, and the shell does a PATH lookup (or a pathname exec, once it contains
// a `/`) for a file literally named `EDITOR=nvim`. Vetting it as a variable
// would leave the program it actually names unreviewed, at a path the sandbox
// can write; the argv matcher never sees the token at all.
func consumeEnvAssignments(toks []scriptToken, bounds LaunchBounds, envConsumed bool) (int, bool) {
	i := 0
	for i < len(toks) {
		name, value, ok := splitEnvAssignment(toks[i].value)
		if !ok {
			break
		}
		if !envConsumed && toks[i].quoted {
			return 0, false
		}
		if !envAssignmentAllowed(name, value, toks[i].quoted, bounds) {
			return 0, false
		}
		i++
	}
	return i, true
}

// splitEnvAssignment reports whether tok has the shape of an environment
// assignment and splits it. It does not decide whether the name is acceptable.
func splitEnvAssignment(tok string) (name, value string, ok bool) {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return "", "", false
	}
	name = tok[:eq]
	if !envVarNameRe.MatchString(name) {
		return "", "", false
	}
	return name, tok[eq+1:], true
}

// envAssignmentAllowed reports whether one assignment may reach the host.
//
// quoted is load-bearing for the variables that name a program: the launcher
// runs those through its shell quoter, so an unquoted EDITOR would be a token
// no quoter produced. The inert flag is deliberately exempt — a leading shell
// assignment is only recognized as an assignment while it is unquoted, so
// requiring quotes there would reject the one form that works.
func envAssignmentAllowed(name, value string, quoted bool, bounds LaunchBounds) bool {
	kind, ok := hostExecEnv[name]
	if !ok {
		return false
	}
	// A set-but-empty variable is what the launcher emits when the caller
	// exported the name with no value. It cannot name anything.
	if value == "" {
		return true
	}
	if len(value) > maxEnvValueLen {
		return false
	}
	switch kind {
	case envProgram:
		return quoted && envProgramValueAllowed(value, bounds)
	case envInert:
		return envInertValueRe.MatchString(value)
	}
	return false
}

// knownEditors is the set of program names accepted as EDITOR/VISUAL when the
// host's own setting does not name them.
//
// The list exists because the host runs `$EDITOR <file>` on a file inside the
// project tree, which is bind-mounted read-write: `EDITOR=sh` (or python, perl,
// awk, node) makes that file the *program*, so the sandbox writes what the host
// executes. Resolving the name against the host's PATH does not bound that — it
// only decides which host binary opens the sandbox's file. Enumerating the
// interpreters instead of the editors is the losing side of the list, the same
// argument hostExecEnv makes for names.
var knownEditors = map[string]struct{}{
	"amp": {}, "code": {}, "code-insiders": {}, "codium": {}, "cursor": {},
	"emacs": {}, "emacsclient": {}, "geany": {}, "gedit": {}, "helix": {},
	"hx": {}, "jed": {}, "joe": {}, "kak": {}, "kakoune": {}, "kate": {},
	"kwrite": {}, "lapce": {}, "mate": {}, "mg": {}, "micro": {},
	"mousepad": {}, "ne": {}, "nano": {}, "nvim": {}, "pico": {}, "pluma": {},
	"subl": {}, "sublime_text": {}, "vi": {}, "view": {}, "vim": {},
	"vimdiff": {}, "vscodium": {}, "windsurf": {}, "xed": {}, "zed": {},
	"zeditor": {},
}

// editorNameAllowed reports whether name may be the program EDITOR or VISUAL
// resolves to. The host's own setting wins over the built-in list: it is
// host-derived, which is the anchor a validator is supposed to have, and it is
// what lets an editor the list does not carry keep working.
func editorNameAllowed(name string) bool {
	for _, host := range []string{os.Getenv("EDITOR"), os.Getenv("VISUAL")} {
		if host != "" && filepath.Base(host) == name {
			return true
		}
	}
	_, ok := knownEditors[name]
	return ok
}

// Option-word sets shared by editors that take the same flags. Each set holds
// only words that editor treats as a self-contained switch — never one that
// consumes the following operand, and never one that names something to
// evaluate.
var (
	// VS Code and its forks: all of these are `code --help` switches.
	vscodeEditorFlags = []string{"-w", "--wait", "-n", "--new-window", "-r", "--reuse-window"}
	// Sublime Text.
	sublEditorFlags = []string{"-w", "--wait", "-n", "--new-window", "-a", "--add", "-b", "--background"}
	// Zed and Lapce.
	zedEditorFlags = []string{"-w", "--wait", "-n", "--new", "-a", "--add"}
	// The vi family. `-R` is read-only, `-p`/`-o`/`-O` are tabs and splits,
	// `--clean`/`-f` skip config and forking. `-u`, `-S`, `-s`, `-c`, `--cmd`,
	// `-w` and `-i` are deliberately absent: each takes the next word, and the
	// first four make that word a script the editor executes.
	vimEditorFlags = []string{"-R", "-p", "-o", "-O", "--clean", "-f", "--nofork"}
	// GTK single-instance editors.
	gtkEditorFlags = []string{"-w", "--wait", "-s", "--standalone"}
)

// editorOptionWords maps an accepted editor name to the option words that may
// follow it in an EDITOR/VISUAL value. An editor with no entry accepts no
// flags, which is the right default: a terminal editor already blocks.
//
// This is a positive per-editor list rather than a shape test because a shape
// test decides nothing here. Every editor below has at least one flag that
// reinterprets its trailing operand as *code* — `nvim -u <file>` sources the
// file as the init vimrc, `vim -S` as a Vim script, `emacs -l` as Elisp,
// `hx -c` as a config, `kak -e` as a command to run — and the operand is the
// file revdiff opens in the project tree, which is bind-mounted read-write. A
// value matching `--?[A-Za-z0-9-]+` therefore handed the sandbox exactly the
// host execution the knownEditors list exists to deny: write the payload into
// the project, then name the flag that makes the host interpret it. Enumerating
// the dangerous flags instead is the losing side of that list, the same
// argument hostExecEnv makes for variable names.
//
// The same flag word means different things to different programs — `-c` is
// `--create-frame` to emacsclient, a config path to helix and an ex command to
// vim — so the list is keyed on the editor rather than shared, and a word is
// listed only where that editor is known to treat it as a switch.
var editorOptionWords = map[string][]string{
	"code":          vscodeEditorFlags,
	"code-insiders": vscodeEditorFlags,
	"codium":        vscodeEditorFlags,
	"vscodium":      vscodeEditorFlags,
	"cursor":        vscodeEditorFlags,
	"windsurf":      vscodeEditorFlags,
	"subl":          sublEditorFlags,
	"sublime_text":  sublEditorFlags,
	"zed":           zedEditorFlags,
	"zeditor":       zedEditorFlags,
	"lapce":         {"-w", "--wait", "-n", "--new"},
	"vi":            vimEditorFlags,
	"vim":           vimEditorFlags,
	"view":          vimEditorFlags,
	"vimdiff":       vimEditorFlags,
	"nvim":          vimEditorFlags,
	"emacs":         {"-nw", "--no-window-system", "-Q", "--quick"},
	"emacsclient":   {"-t", "--tty", "-nw", "-c", "--create-frame", "-n", "--no-wait", "-r", "--reuse-frame"},
	"hx":            {"--vsplit", "--hsplit"},
	"helix":         {"--vsplit", "--hsplit"},
	"kak":           {"-n"},
	"kakoune":       {"-n"},
	"kate":          {"-b", "--block", "-n", "--new"},
	"kwrite":        {"-b", "--block"},
	"gedit":         gtkEditorFlags,
	"pluma":         gtkEditorFlags,
	"xed":           gtkEditorFlags,
	"mousepad":      {"--disable-server"},
	"geany":         {"-i", "--new-instance"},
	"mate":          {"-w", "--wait"},
	"micro":         {"-readonly"},
	"nano":          {"-v", "--view"},
}

// editorFlagsAllowed reports whether every option word trailing the named
// editor is one that editor is known to treat as a self-contained switch.
func editorFlagsAllowed(name string, flags []string) bool {
	if len(flags) == 0 {
		return true
	}
	allowed, ok := editorOptionWords[name]
	if !ok {
		return false
	}
	for _, flag := range flags {
		if !envProgramFlagRe.MatchString(flag) || !slices.Contains(allowed, flag) {
			return false
		}
	}
	return true
}

// hostEditorValueMatches reports whether value is byte-identical to the host's
// own EDITOR or VISUAL.
//
// Such a value is host-derived, which is the anchor a validator is supposed to
// have, and it is what lets the flags and the editors the built-in table does
// not carry keep working. The comparison is on the entire value rather than the
// program name, because a basename match would accept option words the host
// never set.
//
// It is not sufficient on its own: see hostEditorValueAllowed.
func hostEditorValueMatches(value string) bool {
	for _, host := range []string{os.Getenv("EDITOR"), os.Getenv("VISUAL")} {
		if host != "" && host == value {
			return true
		}
	}
	return false
}

// hostEditorValueAllowed reports whether value may be accepted whole - flags and
// all - on the strength of being the host's own EDITOR or VISUAL.
//
// "The setting the host would have run anyway" is the argument for accepting it,
// and that argument is only as good as what the setting names. `EDITOR=nvim`
// says nothing about which file runs: the host resolves it against a PATH that
// routinely reaches a virtualenv's bin, node_modules/.bin or a `bin` direnv adds
// - all inside the project tree, which is bind-mounted read-write - so the
// sandbox plants the binary and the host's own setting names it. The program
// word therefore goes through the same untrusted-root test as a value the
// sandbox chose freely; only the flags ride on the host match.
func hostEditorValueAllowed(value string, bounds LaunchBounds) bool {
	if !hostEditorValueMatches(value) {
		return false
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return false
	}
	return !programWordUntrusted(fields[0], bounds)
}

// programWordUntrusted reports whether prog - the word an EDITOR/VISUAL value
// names its program with - is a file the sandbox can supply.
//
// A bare name is decided by the host's own lookup, since that is the lookup the
// host will perform. A relative path is untrusted outright: it resolves against
// the overlay's working directory, which is the project tree. An absolute path
// is judged where it points, symlinks included, so a link sitting outside every
// root but aimed inside one is still refused.
func programWordUntrusted(prog string, bounds LaunchBounds) bool {
	roots := bounds.UntrustedRoots()
	if len(roots) == 0 {
		return false
	}
	if !strings.ContainsRune(prog, '/') {
		return resolvesUnderUntrustedRoot(prog, bounds)
	}
	if !filepath.IsAbs(prog) {
		return true
	}
	if PathUnder(prog, roots) {
		return true
	}
	real, err := filepath.EvalSymlinks(prog)
	return err == nil && PathUnder(real, roots)
}

// envProgramValueAllowed reports whether value may name the program the host
// executes for EDITOR or VISUAL, optionally followed by option words.
//
// A bare name is accepted because the host resolves it against its own PATH;
// the sandbox supplies the name, not the location - unless that PATH reaches a
// directory the sandbox writes, which is what bounds.UntrustedRoots() rules
// out. A value carrying a path is
// accepted only when it is absolute, already canonical, and is *lexically* one
// of the paths the host's own lookup of that basename yields, which is what
// keeps a planted `<shared-tmp>/nvim` out: the shared temp directory is
// bind-mounted read-write at an identical path on both sides, so a path the
// sandbox writes there is a path the host would execute. A relative path is
// refused outright — it would resolve against the overlay's working directory,
// which is the project directory the sandbox can write.
//
// The comparison is lexical because resolving the sandbox's own path first
// leaves a swap window: `<shared-tmp>/nvim` pointed at the real nvim passes an
// EvalSymlinks check, and the sandbox repoints the symlink before the host
// execs it. Both candidates are derived on the host instead — the PATH lookup
// and that result with its own symlinks resolved — so nothing the sandbox
// controls is followed. That is also why a non-canonical spelling is refused
// rather than cleaned: see IsCanonicalPath.
func envProgramValueAllowed(value string, bounds LaunchBounds) bool {
	// A value the host itself sets is accepted whole; a value whose program word
	// the sandbox can supply falls through to the checks below, which refuse it
	// for the same reason they would refuse any other spelling of it.
	if hostEditorValueAllowed(value, bounds) {
		return true
	}
	fields := strings.Fields(value)
	if len(fields) == 0 || len(fields) > 1+maxEnvProgramFlags {
		return false
	}
	prog := fields[0]
	if !envProgramValueRe.MatchString(prog) {
		return false
	}
	name := filepath.Base(prog)
	if !editorNameAllowed(name) {
		return false
	}
	// Option words are checked against what *this* editor treats as a switch,
	// so a flag that would turn the file operand into a script is refused even
	// though the program naming it is allowed.
	if !editorFlagsAllowed(name, fields[1:]) {
		return false
	}
	// Both forms rest on the host's own lookup - the bare name because the host
	// performs it later, the path because it must be what that lookup yields -
	// and that lookup stops being a host anchor when PATH reaches a directory
	// the sandbox writes. A virtualenv's bin, node_modules/.bin or a `bin`
	// direnv adds all sit in the project tree, which is bind-mounted read-write
	// at the path the host resolves, so a name resolving there is a program the
	// sandbox planted. Refuse it in either form.
	if resolvesUnderUntrustedRoot(name, bounds) {
		return false
	}
	if !strings.ContainsRune(prog, '/') {
		return true
	}
	if !filepath.IsAbs(prog) || !IsCanonicalPath(prog) {
		return false
	}
	return slices.Contains(HostProgramPaths(name), prog)
}

// resolvesUnderUntrustedRoot reports whether the host's own lookup of name
// yields a path inside a directory the sandbox can write.
//
// It is deliberately a lookup on this side rather than a check of the value:
// what matters is where the *host* will find the program, and the value carries
// only a name. The check binds the host's PATH as devsandbox sees it, which is
// the same environment the terminal that will run the program was started from.
func resolvesUnderUntrustedRoot(name string, bounds LaunchBounds) bool {
	roots := bounds.UntrustedRoots()
	if len(roots) == 0 {
		return false
	}
	for _, p := range HostProgramPaths(name) {
		if PathUnder(p, roots) {
			return true
		}
	}
	return false
}

// HostProgramPaths returns the absolute paths the host itself accepts as name:
// what its PATH lookup yields, and that path with symlinks resolved. Empty when
// the host cannot resolve the name at all, which denies rather than widens.
//
// Both are needed wherever the command the host runs spells the program by bare
// name, because the lookup happens on the host at execution time and either
// candidate is the file that ends up open: a `$PROJECT/bin/sh` symlink aimed at
// `/bin/sh` resolves out of every untrusted root while the file the host
// actually opens is the link, which the sandbox can repoint or replace.
func HostProgramPaths(name string) []string {
	found, err := exec.LookPath(name)
	if err != nil {
		return nil
	}
	abs, err := filepath.Abs(found)
	if err != nil {
		return nil
	}
	paths := []string{abs}
	if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
		paths = append(paths, real)
	}
	return paths
}

// envBins returns the absolute paths accepted as the `env` program, resolved
// once per process.
//
// `/usr/bin/env` is the path the launcher hardcodes and the one location POSIX
// fixes; the host's own resolution covers a distribution that puts it
// elsewhere. Matching on basename instead is what this replaces: a few
// directories are write-through binds shared with the host at an identical
// path, so any path ending in `/env` can be supplied by the sandbox.
var envBins = sync.OnceValue(func() []string {
	bins := []string{"/usr/bin/env"}
	if resolved, err := ResolveProgram("env"); err == nil && !slices.Contains(bins, resolved) {
		bins = append(bins, resolved)
	}
	return bins
})

// isEnvBin reports whether got names the `env` program at a path the host
// itself resolves. A non-canonical spelling is refused rather than cleaned, for
// the reason IsCanonicalPath gives.
func isEnvBin(got string, bounds LaunchBounds) bool {
	if !IsCanonicalPath(got) {
		return false
	}
	// envBins holds what the host's own lookup yielded, which stops meaning
	// "the host's env" the moment that lookup reaches a directory the sandbox
	// writes. Refuse such a path even when it is exactly what PATH produced.
	if PathUnder(got, bounds.UntrustedRoots()) {
		return false
	}
	return slices.Contains(envBins(), got)
}
