package cmdpattern

import (
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

	// envInertValueRe accepts a short scalar with no path separator, so an
	// inert variable can never grow into one that names a file.
	envInertValueRe = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,32}$`)
)

// consumeEnvAssignments walks the leading assignments of a command head and
// returns the index of the first token that is not one.
//
// A token that does not look like an assignment ends the prefix and starts the
// argv, which is how `'--output=/tmp/o'` stays an argument. A token that does
// look like one but is not accepted fails the whole head: falling through to
// the argv matcher instead would decide the same case for the wrong reason.
func consumeEnvAssignments(toks []scriptToken) (int, bool) {
	i := 0
	for i < len(toks) {
		name, value, ok := splitEnvAssignment(toks[i].value)
		if !ok {
			break
		}
		if !envAssignmentAllowed(name, value, toks[i].quoted) {
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
func envAssignmentAllowed(name, value string, quoted bool) bool {
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
		return quoted && envProgramValueAllowed(value)
	case envInert:
		return envInertValueRe.MatchString(value)
	}
	return false
}

// envProgramValueAllowed reports whether value may name the program the host
// executes for EDITOR or VISUAL.
//
// A bare name is accepted because the host resolves it against its own PATH;
// the sandbox supplies the name, not the location. A value carrying a path is
// accepted only when it is absolute and resolves to the very file the host's
// own lookup of that basename yields, which is what keeps a planted
// `<shared-tmp>/nvim` out: the shared temp directory is bind-mounted
// read-write at an identical path on both sides, so a path the sandbox writes
// there is a path the host would execute. A relative path is refused outright —
// it would resolve against the overlay's working directory, which is the
// project directory the sandbox can write.
func envProgramValueAllowed(value string) bool {
	if !envProgramValueRe.MatchString(value) {
		return false
	}
	if !strings.ContainsRune(value, '/') {
		return true
	}
	if !filepath.IsAbs(value) {
		return false
	}
	wantBin, err := ResolveProgram(filepath.Base(value))
	if err != nil {
		return false
	}
	gotBin, err := filepath.EvalSymlinks(value)
	if err != nil {
		return false
	}
	return gotBin == wantBin
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
// itself resolves.
func isEnvBin(got string) bool {
	return slices.Contains(envBins(), filepath.Clean(got))
}
