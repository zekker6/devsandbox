// Package cmdpattern provides static validation of shell command lines that a
// sandboxed caller asks a host-side terminal to execute.
//
// It is protocol-agnostic: the kitty proxy uses it to vet `kitty @ launch`
// argv, and the herdr proxy uses it to vet `pane.send_input` payloads. The
// matchers deliberately model a tiny, fixed grammar rather than the shell —
// anything outside that grammar is rejected rather than interpreted.
package cmdpattern

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// CommandPattern restricts which commands a tool may ask the host to execute.
// A pattern accepts an argv if the program matches argv[0] and ArgsMatcher
// returns true for argv[1:].
//
// Program matching has two modes, and neither one ever matches on basename:
//
//   - ResolvedBin set (preferred): argv[0] must equal ResolvedBin exactly after
//     cleaning, pinning the pattern to one binary at one path.
//   - ResolvedBin empty: argv[0] must equal Program byte for byte, so only a
//     literal spelling the host itself resolves is accepted.
//
// Matching on basename is unsafe wherever the sandbox can create a file at a
// path the host sees. Most of the sandbox home is an overlay whose writes never
// reach the host, but a few directories are write-through bind mounts shared at
// an identical path on both sides — the shared temp directory and the revdiff
// IPC directory are two. A sandbox can drop an executable named `revdiff`, or
// `sh`, there and emit a command naming it by absolute path; basename matching
// accepted it and the host ran it as the host user. Setting ResolvedBin closes
// that, because the real resolved path lives on an overlay the sandbox cannot
// alter from the host's point of view, and exact Program matching closes it for
// the wrapping shell, which has no single resolved path worth pinning.
//
// Both modes additionally require the spelling to be canonical, or a path
// routed through those same write-through directories cleans to the pinned one
// while resolving somewhere else. See IsCanonicalPath.
type CommandPattern struct {
	Program     string
	ArgsMatcher func(args []string) bool

	// ResolvedBin, when non-empty, is the absolute host path of the only
	// binary this pattern accepts. Populate it via ResolveProgram.
	ResolvedBin string
}

// MatchesArgv reports whether p accepts the given argv.
func (p CommandPattern) MatchesArgv(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	if !p.programMatches(argv[0]) {
		return false
	}
	if p.ArgsMatcher == nil {
		return true
	}
	return p.ArgsMatcher(argv[1:])
}

// programMatches reports whether got is an acceptable argv[0] for p.
func (p CommandPattern) programMatches(got string) bool {
	if !IsCanonicalPath(got) {
		return false
	}

	if p.ResolvedBin != "" {
		return got == filepath.Clean(p.ResolvedBin)
	}

	return p.Program != "" && p.Program == got
}

// IsCanonicalPath reports whether p is already spelled the way filepath.Clean
// spells it, so that comparing it lexically against a host-derived path means
// what the kernel will mean when it resolves it.
//
// This is the guard that makes every "pin the program to its resolved absolute
// path" rule in this package hold. filepath.Clean collapses `..` *lexically*;
// execve and the shell resolve it *physically*, expanding each symlink before
// applying the next `..`. A few directories are write-through binds shared with
// the host at an identical path - the shared temp directory is one - so a
// sandbox that plants a symlink there and sends
// `<shared-tmp>/link/../../../usr/bin/nano` hands the validator a path that
// cleans to `/usr/bin/nano` and hands the host one that resolves to a binary
// the sandbox wrote. Refusing the non-canonical spelling outright is the only
// comparison that cannot diverge; nothing legitimate emits one, because every
// caller here forwards a path the host itself resolved.
func IsCanonicalPath(p string) bool {
	return p != "" && filepath.Clean(p) == p
}

// ResolveProgram returns the absolute, symlink-free host path of name, for use
// as CommandPattern.ResolvedBin.
//
// Callers should treat a failure as fatal to the pattern rather than falling
// back to basename matching: a pattern that cannot pin its binary should deny
// everything, not silently widen.
func ResolveProgram(name string) (string, error) {
	found, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("resolve program %q: %w", name, err)
	}
	abs, err := filepath.Abs(found)
	if err != nil {
		return "", fmt.Errorf("resolve program %q: absolute path: %w", name, err)
	}
	// EvalSymlinks so a pattern pins the real file, not a symlink that could
	// later be repointed.
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve program %q: eval symlinks: %w", name, err)
	}
	return real, nil
}

// LaunchBounds carries the host-derived directories a launch validator anchors
// to. It is built once per launch, from the same functions that produce the
// sandbox's mounts, so what a pattern bounds cannot drift from what the sandbox
// can actually reach.
type LaunchBounds struct {
	// SharedTmp is the directory bind-mounted read-write at an identical path
	// on both sides. A completion sentinel must live under it.
	SharedTmp string

	// ProjectDir is the project tree, bind-mounted read-write at the path the
	// host knows it by.
	ProjectDir string
}

// SentinelRoot returns the directory a completion sentinel must live under.
// Empty denies every sentinel; see sentinelAllowed.
func (b LaunchBounds) SentinelRoot() string { return b.SharedTmp }

// UntrustedRoots returns the host directories the sandbox can write at a path
// the host itself resolves.
//
// A program found in one of these is supplied by the sandbox no matter how
// host-derived the lookup that found it looks: the host's PATH routinely
// reaches into the project tree (a virtualenv's bin, node_modules/.bin, a bin
// directory direnv or mise adds), and every byte of that tree is writable from
// inside the sandbox.
//
// Every root contributes both of its spellings; see ResolvedSpellings for why
// one is not enough.
func (b LaunchBounds) UntrustedRoots() []string {
	return ResolveRoots([]string{b.SharedTmp, b.ProjectDir})
}

// ResolveRoots expands a deny list of roots with each root's symlink-resolved
// spelling, dropping empties and duplicates.
//
// Only a deny list may be expanded this way. Widening an *allow* bound - the
// sentinel root, a cwd confinement - with a second spelling admits a path the
// caller never derived, which is the opposite of what those bounds are for.
func ResolveRoots(roots []string) []string {
	out := make([]string, 0, 2*len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		for _, s := range ResolvedSpellings(r) {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// ResolvedSpellings returns the host spellings of p that a lexical containment
// test has to consider: p as cleaned, plus p with symlinks resolved when that
// differs.
//
// A root spelled through a symlink bounds nothing on its own. ProjectDir comes
// from os.Getwd(), which returns $PWD verbatim whenever it names the same
// directory - so a shell that cd'd through a symlink hands devsandbox the
// link's spelling, while bwrap's `--bind <dir> <dir>` binds the *target's*
// inode read-write. Both spellings then name the same writable tree, and a
// lexical test against one of them misses every path written as the other:
// with a project at `~/proj-link` pointing into `~/.cache`, the relocation
// directory under `~/.cache/devsandbox` is not lexically inside `~/proj-link`,
// yet the sandbox reaches and replaces it there.
//
// Resolution stops at the deepest existing ancestor, because a root the caller
// derived need not exist yet - the shared temp directory is created after the
// bounds are built - and a path that cannot be resolved at all must still
// contribute its literal spelling rather than drop out of the list.
func ResolvedSpellings(p string) []string {
	clean := filepath.Clean(p)
	real := resolveExisting(clean)
	if real == clean {
		return []string{clean}
	}
	return []string{clean, real}
}

// resolveExisting resolves the symlinks in p as far as its existing prefix
// allows, re-appending the components that do not exist yet.
func resolveExisting(p string) string {
	rest := ""
	for cur := p; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			if rest == "" {
				return real
			}
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// PathUnder reports whether p is one of roots or lies beneath one, lexically.
// Both sides are cleaned; the caller is responsible for refusing non-canonical
// spellings where that matters (see IsCanonicalPath).
func PathUnder(p string, roots []string) bool {
	_, ok := PathUnderRoot(p, roots)
	return ok
}

// PathUnderRoot is PathUnder, returning the root that matched so a caller can
// name it in an error.
func PathUnderRoot(p string, roots []string) (string, bool) {
	clean := filepath.Clean(p)
	for _, root := range roots {
		r := filepath.Clean(root)
		if clean == r || strings.HasPrefix(clean, r+string(filepath.Separator)) {
			return r, true
		}
	}
	return "", false
}

// MatchAny returns a matcher that accepts any args.
func MatchAny() func([]string) bool {
	return func([]string) bool { return true }
}

// MatchPrefix returns a matcher that accepts args whose first len(prefix) elements
// match prefix exactly.
func MatchPrefix(prefix ...string) func([]string) bool {
	return func(args []string) bool {
		if len(args) < len(prefix) {
			return false
		}
		for i, p := range prefix {
			if args[i] != p {
				return false
			}
		}
		return true
	}
}

// shellMeta contains bytes that trigger shell interpretation beyond plain
// whitespace-separated argv. A script containing any of these cannot be
// statically validated by token-splitting alone, because the real shell will
// interpret them (command chaining, pipes, substitution, redirection, globbing,
// escape sequences). MatchShellExec rejects any script containing these bytes
// rather than trying to model the shell.
const shellMeta = ";&|`$()<>*?[]{}\\\n\r\t\""

// MatchShellExec returns a matcher that accepts the form ["-c", "<inner-cmd> ..."]
// or ["-c", "exec <inner-cmd> ..."], where <inner-cmd> matches inner.
//
// This is used to whitelist `sh -c '<allowed-program> ...'` invocations that
// tools wrap around their real command. Any script containing shell
// metacharacters (see shellMeta) is rejected outright — otherwise an attacker
// could pass `sh -c "<allowed> && <evil>"` and the inner matcher would accept
// it even though the shell runs both commands.
func MatchShellExec(inner CommandPattern) func([]string) bool {
	return func(args []string) bool {
		if len(args) != 2 || args[0] != "-c" {
			return false
		}
		script := strings.TrimSpace(args[1])
		// Strip optional leading "exec " (and any whitespace after).
		if rest, ok := strings.CutPrefix(script, "exec "); ok {
			script = strings.TrimLeft(rest, " ")
		}
		// Reject anything the shell would interpret as more than plain argv.
		if strings.ContainsAny(script, shellMeta) {
			return false
		}
		// First token of the script is the inner program name.
		fields := strings.Fields(script)
		if len(fields) == 0 {
			return false
		}
		return inner.MatchesArgv(fields)
	}
}

// sentinelPathRe accepts absolute paths composed of safe filename characters.
// Rejects ".." segments implicitly (no dots before slashes in a segment start)
// and anything containing shell metacharacters or whitespace. Used to validate
// the sentinel-file argument in MatchShellExecSentinel.
var sentinelPathRe = regexp.MustCompile(`^(/[a-zA-Z0-9._+@=,-]+)+$`)

// sentinelAllowed reports whether sentinel may be the completion-signal path the
// host writes, given the directory the caller derived on the host.
//
// The shape test alone decided nothing about *where*: it accepted any absolute
// path, and the clause the launcher appends is not a `touch` but
//
//	printf "%s" "$rc" > '<sentinel>'.tmp && mv -f '<sentinel>'.tmp '<sentinel>'
//
// so a sandbox naming `/home/u/.bashrc` had the host truncate the file, write an
// exit code into it and rename it into place. Anchoring the path to root - which
// callers take from the same function that produces the bind mount, so the two
// cannot drift - is what bounds it to a directory the sandbox already owns.
//
// An empty root denies every sentinel rather than falling back to the shape
// test: a caller that cannot derive the directory must not widen.
//
// Containment is lexical. The directory is bind-mounted read-write at an
// identical path on both sides, so resolving the path first would follow links
// the sandbox planted and leave a swap window besides; IsCanonicalPath refuses
// the `..` spellings that make a lexical prefix test disagree with the kernel.
//
// This bounds the path the script *names*, not the inode the host finally
// writes: a link planted at the named path resolves in the launcher's own
// shell, after every check here. What the two callers do with that differs.
// ScriptPattern's tail truncates its target, and ScriptPattern.HardenBody makes
// that write refuse a symlink rather than follow it. The `; touch '<sentinel>'`
// form below is an argv forwarded verbatim to the host terminal, so nothing can
// be inserted into it: a link planted at the sentinel path is followed, which
// creates or timestamps a file elsewhere but writes no content into it.
func sentinelAllowed(sentinel, root string) bool {
	if root == "" || !filepath.IsAbs(root) || !IsCanonicalPath(root) {
		return false
	}
	if !sentinelPathRe.MatchString(sentinel) || !IsCanonicalPath(sentinel) {
		return false
	}
	return strings.HasPrefix(sentinel, root+string(filepath.Separator))
}

// MatchShellExecSentinel accepts the form:
//
//	["-c", "'<prog>' '<arg1>' ...; touch '<sentinel-path>'"]
//
// Every token must be wrapped in exactly one pair of single quotes with NO
// embedded single quotes, backslashes, or other special characters. The inner
// program + args must satisfy `inner`. The sentinel path must match
// sentinelPathRe (absolute, safe filename chars only).
//
// This exists specifically for launcher scripts like revdiff's that need a
// sentinel-file completion signal and cannot use a wrapper script file. It is
// intentionally narrow: the only accepted "second statement" is literal
// `touch <path>`, and the only accepted quoting is the single-quote form
// produced by standard `printf "'%s'"` shell quoters.
//
// bounds carries the host-derived directory the sentinel must live under; see
// sentinelAllowed. An empty SharedTmp denies every launch.
func MatchShellExecSentinel(inner CommandPattern, bounds LaunchBounds) func([]string) bool {
	return func(args []string) bool {
		if len(args) != 2 || args[0] != "-c" {
			return false
		}
		script := strings.TrimSpace(args[1])
		if rest, ok := strings.CutPrefix(script, "exec "); ok {
			script = strings.TrimLeft(rest, " ")
		}
		// Split on exactly one "; touch " separator; the inner part is the
		// real command, the tail is the sentinel touch.
		head, tailRaw, ok := strings.Cut(script, "; touch ")
		if !ok {
			return false
		}
		tail := strings.TrimSpace(tailRaw)

		// Head must parse as single-quoted argv and satisfy inner.
		argv, ok := parseSingleQuotedArgv(head)
		if !ok {
			return false
		}
		if !inner.MatchesArgv(argv) {
			return false
		}

		// Tail must be exactly one single-quoted path, inside the directory the
		// caller derived on the host.
		sentinel, ok := unwrapSingleQuoted(tail)
		if !ok {
			return false
		}
		return sentinelAllowed(sentinel, bounds.SentinelRoot())
	}
}

// MatchShellExecEnvSentinel accepts the form:
//
//	["-c", "'/usr/bin/env' 'KEY=VAL' ... '<prog>' '<arg>'...; touch '<sentinel>'"]
//
// It is a narrow extension of MatchShellExecSentinel that tolerates an
// `/usr/bin/env KEY=VAL ...` prefix before the inner program. The revdiff
// launcher injects this wrapper (v0.8.0+) so the kitty-spawned overlay
// inherits EDITOR/VISUAL from the caller's login shell.
//
// Rules:
//   - argv[0] must be `env` at an absolute path the host itself resolves
//     (isEnvBin), never a path that merely ends in "env".
//   - Zero or more leading tokens shaped `KEY=VAL`, each of which must be an
//     assignment hostExecEnv accepts — both the name and the value, since the
//     value of EDITOR is a program the host will run.
//   - The remaining tokens (at least one) form the inner argv and must
//     satisfy inner.
//   - The sentinel tail rules (path shape, canonicalization, containment in
//     bounds.SentinelRoot(), no shell meta) are identical to
//     MatchShellExecSentinel.
func MatchShellExecEnvSentinel(inner CommandPattern, bounds LaunchBounds) func([]string) bool {
	return func(args []string) bool {
		if len(args) != 2 || args[0] != "-c" {
			return false
		}
		script := strings.TrimSpace(args[1])
		if rest, ok := strings.CutPrefix(script, "exec "); ok {
			script = strings.TrimLeft(rest, " ")
		}
		head, tailRaw, ok := strings.Cut(script, "; touch ")
		if !ok {
			return false
		}
		tail := strings.TrimSpace(tailRaw)

		// The upstream revdiff launcher emits the `env` program unquoted:
		//   /usr/bin/env 'EDITOR=nvim' '/path/to/revdiff' ...
		// Strip that prefix if present so the rest of the head parses as pure
		// single-quoted argv. Only an absolute path the host resolves as `env`
		// is accepted, so a PATH-relative `env` cannot be substituted - and the
		// candidate goes through isEnvBin rather than envBins directly, because
		// what the host's lookup produced stops meaning "the host's env" once it
		// lands in a directory the sandbox writes. The quoted form below applies
		// the same rule; matching the list here bypassed it.
		envConsumed := false
		for _, bin := range envBins() {
			if !isEnvBin(bin, bounds) {
				continue
			}
			if rest, ok := strings.CutPrefix(head, bin+" "); ok {
				head = rest
				envConsumed = true
				break
			}
		}

		argv, ok := parseSingleQuotedArgv(head)
		if !ok {
			return false
		}
		if !envConsumed {
			if len(argv) == 0 || !isEnvBin(argv[0], bounds) {
				return false
			}
			argv = argv[1:]
		}

		// Every token here came out of parseSingleQuotedArgv, so all of them
		// were single-quoted.
		assigns := make([]scriptToken, len(argv))
		for i, tok := range argv {
			assigns[i] = scriptToken{value: tok, quoted: true}
		}
		n, ok := consumeEnvAssignments(assigns, bounds)
		if !ok {
			return false
		}
		innerArgv := argv[n:]
		if len(innerArgv) == 0 {
			return false
		}
		if !inner.MatchesArgv(innerArgv) {
			return false
		}

		sentinel, ok := unwrapSingleQuoted(tail)
		if !ok {
			return false
		}
		return sentinelAllowed(sentinel, bounds.SentinelRoot())
	}
}

// parseSingleQuotedArgv parses a whitespace-separated sequence of single-quoted
// tokens. Each token must start and end with a single quote and contain no
// embedded single quotes, backslashes, or control characters. Returns the
// unquoted tokens, or ok=false if the input doesn't conform.
func parseSingleQuotedArgv(s string) ([]string, bool) {
	var out []string
	i := 0
	for i < len(s) {
		// Skip leading whitespace between tokens (space or tab only; newlines
		// are rejected by shellMeta upstream, but be defensive).
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] != '\'' {
			return nil, false
		}
		// Find the closing quote.
		end := strings.IndexByte(s[i+1:], '\'')
		if end < 0 {
			return nil, false
		}
		tok := s[i+1 : i+1+end]
		// Reject dangerous bytes inside the quoted segment.
		if strings.ContainsAny(tok, "\\\n\r\x00") {
			return nil, false
		}
		out = append(out, tok)
		i += 1 + end + 1
		// A quoted token must be followed by whitespace or end of input, the
		// same rule tokenizeScriptHead enforces: `'a''b'` would otherwise be
		// validated as two tokens while the shell builds the single word `ab`,
		// so the argv checked would not be the argv executed.
		if i < len(s) && s[i] != ' ' && s[i] != '\t' {
			return nil, false
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// unwrapSingleQuoted returns the content of a string that is exactly
// '<content>' with no extra bytes. Rejects anything else.
func unwrapSingleQuoted(s string) (string, bool) {
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return "", false
	}
	inner := s[1 : len(s)-1]
	if strings.ContainsAny(inner, "'\\\n\r\x00") {
		return "", false
	}
	return inner, true
}
