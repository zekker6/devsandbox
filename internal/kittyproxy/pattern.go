package kittyproxy

import (
	"path/filepath"
	"regexp"
	"strings"
)

// CommandPattern restricts which commands a tool may pass to `kitty @ launch`.
// A pattern accepts an argv if Program matches argv[0] (basename or absolute path)
// and ArgsMatcher returns true for argv[1:].
type CommandPattern struct {
	Program     string
	ArgsMatcher func(args []string) bool
}

// MatchesArgv reports whether p accepts the given argv.
func (p CommandPattern) MatchesArgv(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	if !programMatches(p.Program, argv[0]) {
		return false
	}
	if p.ArgsMatcher == nil {
		return true
	}
	return p.ArgsMatcher(argv[1:])
}

// programMatches accepts either an exact basename or an absolute path that
// resolves to the same basename.
func programMatches(want, got string) bool {
	if want == got {
		return true
	}
	return filepath.Base(got) == want
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
func MatchShellExecSentinel(inner CommandPattern) func([]string) bool {
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

		// Tail must be exactly one single-quoted path.
		sentinel, ok := unwrapSingleQuoted(tail)
		if !ok {
			return false
		}
		if !sentinelPathRe.MatchString(sentinel) {
			return false
		}
		// Reject paths that aren't already canonicalized: `..`, `.`, `//`,
		// trailing `/` — filepath.Clean normalizes all of these.
		return filepath.Clean(sentinel) == sentinel
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
