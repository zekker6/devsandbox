package cmdpattern

import (
	"regexp"
	"slices"
	"strings"
)

// ScriptPattern validates a small shell *script file* rather than a single
// argv.
//
// It exists because some launchers hand a terminal a generated script path
// (`sh /tmp/launch-abc`) instead of an inline command. CommandPattern cannot
// vet that: it models one argv, and shellMeta deliberately rejects the `;`,
// `$`, `>` and `&` that any real script contains. Rather than loosening
// shellMeta — which would weaken every inline pattern too — ScriptPattern
// accepts one exact, fixed script shape and nothing else:
//
//	#!/bin/sh
//	[/usr/bin/env 'KEY=VAL'...] [KEY=VAL...] '<prog>' '<arg>'... [2>'<stderr>']; rc=$?; printf "%s" "$rc" > '<sentinel>'.tmp && mv -f '<sentinel>'.tmp '<sentinel>'
//
// That is the completion-sentinel form emitted by the revdiff launcher. The
// leading command must satisfy Statement; the trailing clause must match
// byte-for-byte apart from the sentinel path, which must be a canonical
// absolute path of safe characters, identical in all three positions, and
// inside Bounds.SentinelRoot().
//
// The stderr redirect is the launcher's own (v0.8.23+): the overlay closes the
// moment a fast-failing revdiff exits, taking the error text with it, so the
// launcher captures fd 2 into a file it reads back afterwards. It is accepted
// only in exactly that spelling - `2>` immediately followed by one single-quoted
// path, as the final token of the head - and its target is bounded exactly like
// the sentinel, because it is the same thing: a path the host opens for writing
// on the sandbox's word. A body carrying one is reported through MatchBody, and
// the caller has work to do before running it; see ScriptMatch.StderrFile.
//
// Anything else - a second statement, a heredoc, command substitution, a
// pipeline, a stdout redirect, an unquoted or non-final `2>` - is rejected. The
// pattern does not try to model the shell; it recognizes one sentence.
type ScriptPattern struct {
	// Shebangs is the allowlist of acceptable first lines. A body whose first
	// line starts with "#!" must match one of these exactly. Empty means no
	// shebang is permitted.
	Shebangs []string

	// Statement matches the leading command's argv.
	Statement CommandPattern

	// Bounds carries the host-derived directories this pattern anchors to: the
	// one the completion sentinel must live under, and the roots the sandbox
	// can write at a path the host resolves. An empty SharedTmp rejects every
	// body - the trailing clause has the host write and rename a file, so a
	// pattern that cannot bound where must deny rather than accept any absolute
	// path. See sentinelAllowed and LaunchBounds.
	Bounds LaunchBounds

	// MaxBytes caps the body size. Zero means defaultScriptMaxBytes.
	MaxBytes int
}

const defaultScriptMaxBytes = 64 << 10 // 64 KiB — launcher scripts are one line

// ScriptMatch reports what an accepted body carries beyond the argv Statement
// matched.
type ScriptMatch struct {
	// StderrFile is the path the body redirects the command's stderr into,
	// empty when it carries no redirect. It is canonical, absolute and inside
	// Bounds.SentinelRoot().
	//
	// A caller running HardenBody's output must unlink this path first, and the
	// two halves of that are inseparable. `set -C` refuses a redirect onto an
	// existing path, and the launcher creates this one with mktemp - so
	// hardening an unprepared body means the host shell fails the redirect,
	// never runs revdiff, and reports a shell error in its place. Removing the
	// file without hardening is worse: the redirect then follows whatever the
	// sandbox planted at that path in the meantime. Together they open the path
	// with O_CREAT|O_EXCL, which creates the file when nothing is there and
	// fails when something is. See herdrproxy.Relocator.
	StderrFile string
}

// sentinelTailRe matches the fixed completion clause the launcher appends. The
// three sentinel occurrences are captured separately so the caller can require
// they are identical; a regexp backreference is unavailable in RE2.
var sentinelTailRe = regexp.MustCompile(
	`^; rc=\$\?; printf "%s" "\$rc" > '([^']*)'\.tmp && mv -f '([^']*)'\.tmp '([^']*)'$`,
)

// noClobberPrologue is the line HardenBody inserts. `set -C` makes every shell
// that can run this body open a `>` target with O_CREAT|O_EXCL, so a redirect
// onto an existing path - a symlink included - fails instead of writing through
// it.
const noClobberPrologue = "set -C"

// HardenBody returns the bytes to run for a body MatchesBody accepted.
//
// The accepted tail is not a `touch`: it redirects into '<sentinel>'.tmp and
// renames that over '<sentinel>'. sentinelAllowed bounds the path the script
// *names* to the shared temp directory, but that directory is bind-mounted
// read-write at an identical path on both sides, so the sandbox can plant a
// symlink at '<sentinel>'.tmp and have the host's own shell follow it - the
// redirect truncates whatever it points at and writes the exit code there. No
// lexical check on the path can prevent that, because the resolution happens in
// the launcher's shell after validation.
//
// `set -C` is what closes it. Under noclobber both shells this body can run in
// refuse the redirect rather than following the link: dash opens `>` targets
// with O_CREAT|O_EXCL unconditionally, and bash refuses an existing regular
// file outright and falls back to O_EXCL when the path does not stat - which is
// the dangling-symlink case. The sentinel write then fails and the `&&` keeps
// the rename from running; nothing outside the shared directory is touched.
//
// The rename itself needs no guard: rename(2) acts on the link, not its target.
//
// The same prologue covers the accepted stderr redirect, which is the same kind
// of write to the same kind of path. That one needs one thing more, because the
// launcher creates its target with mktemp and noclobber refuses an existing
// path: the caller must unlink it first, or the redirect fails and revdiff never
// runs. See ScriptMatch.StderrFile.
//
// Callers must run this output rather than the validated bytes. It is only
// meaningful for a body MatchBody accepted - the prologue is inert on its own,
// but the argument for it is the accepted tail.
func (s ScriptPattern) HardenBody(body []byte) []byte {
	text := string(body)
	if strings.HasPrefix(text, "#!") {
		if nl := strings.IndexByte(text, '\n'); nl >= 0 {
			return []byte(text[:nl+1] + noClobberPrologue + "\n" + text[nl+1:])
		}
	}
	return []byte(noClobberPrologue + "\n" + text)
}

// MatchesBody reports whether body is a script this pattern accepts. Callers
// that go on to run the body want MatchBody instead: a body carrying a stderr
// redirect needs the target prepared before it runs.
func (s ScriptPattern) MatchesBody(body []byte) bool {
	_, ok := s.MatchBody(body)
	return ok
}

// MatchBody reports whether body is a script this pattern accepts, and what an
// accepted body carries that its caller has to act on.
func (s ScriptPattern) MatchBody(body []byte) (ScriptMatch, bool) {
	maxBytes := s.MaxBytes
	if maxBytes == 0 {
		maxBytes = defaultScriptMaxBytes
	}
	if len(body) == 0 || len(body) > maxBytes {
		return ScriptMatch{}, false
	}
	if strings.ContainsRune(string(body), '\x00') {
		return ScriptMatch{}, false
	}

	lines := splitScriptLines(string(body))
	if len(lines) == 0 {
		return ScriptMatch{}, false
	}

	// A leading "#!" line must be explicitly allowlisted.
	if strings.HasPrefix(lines[0], "#!") {
		if !slices.Contains(s.Shebangs, lines[0]) {
			return ScriptMatch{}, false
		}
		lines = lines[1:]
	}

	// Exactly one statement. More than one would let an accepted command sit
	// beside an unaccepted one.
	if len(lines) != 1 {
		return ScriptMatch{}, false
	}
	return s.matchStatement(lines[0])
}

// matchStatement validates the single command line: leading command plus the
// fixed sentinel tail.
func (s ScriptPattern) matchStatement(line string) (ScriptMatch, bool) {
	// Split at the first "; rc=" so the tail is matched as a literal shape and
	// the head is never scanned for metacharacters it is allowed to contain.
	idx := strings.Index(line, "; rc=")
	if idx < 0 {
		return ScriptMatch{}, false
	}
	head, tail := line[:idx], line[idx:]

	m := sentinelTailRe.FindStringSubmatch(tail)
	if m == nil {
		return ScriptMatch{}, false
	}
	sentinel := m[1]
	if m[2] != sentinel || m[3] != sentinel {
		return ScriptMatch{}, false
	}
	if !sentinelAllowed(sentinel, s.Bounds.SentinelRoot()) {
		return ScriptMatch{}, false
	}

	return s.matchHead(head)
}

// matchHead validates the leading command: an optional `/usr/bin/env` prefix,
// then the KEY=VAL assignments hostExecEnv accepts, then the program and its
// arguments, then an optional stderr redirect.
func (s ScriptPattern) matchHead(head string) (ScriptMatch, bool) {
	head = strings.TrimSpace(head)

	toks, ok := tokenizeScriptHead(head)
	if !ok {
		return ScriptMatch{}, false
	}

	// A redirect is accepted as the last token and nowhere else. Anywhere
	// earlier it would be routing a fd for something other than the argv
	// Statement vetted - `2>'x' '<prog>'` redirects the redirect's own position
	// - and the shell would still run the rest, so the position is part of the
	// shape rather than a formality.
	//
	// The target is bounded by sentinelAllowed for the reason its name gives:
	// this is a second path the host opens for writing because the sandbox
	// asked, and an unbounded one buys the same primitive the sentinel bound
	// exists to close. An empty root denies rather than widening, as there.
	var match ScriptMatch
	if last := len(toks) - 1; last >= 0 && toks[last].redirect {
		if !sentinelAllowed(toks[last].value, s.Bounds.SentinelRoot()) {
			return ScriptMatch{}, false
		}
		match.StderrFile = toks[last].value
		toks = toks[:last]
	}
	if slices.ContainsFunc(toks, func(t scriptToken) bool { return t.redirect }) {
		return ScriptMatch{}, false
	}

	// The launcher emits `env` unquoted as the command word. Only an absolute
	// path the host itself resolves is accepted, so neither a PATH-relative
	// `env` nor a path the sandbox planted ending in `/env` can stand in.
	envConsumed := false
	if len(toks) > 0 && !toks[0].quoted && isEnvBin(toks[0].value, s.Bounds) {
		toks = toks[1:]
		envConsumed = true
	}

	// Consume leading KEY=VAL assignments. Which spellings are assignments at
	// all depends on whether `env` was consumed, which is why that answer is
	// threaded through rather than inferred here: see consumeEnvAssignments.
	i, ok := consumeEnvAssignments(toks, s.Bounds, envConsumed)
	if !ok {
		return ScriptMatch{}, false
	}

	argvToks := toks[i:]
	if len(argvToks) == 0 {
		return ScriptMatch{}, false
	}
	// Everything from the program onward must have been single-quoted, which is
	// what the launcher's shell quoter emits. An unquoted token here would mean
	// the shell could still expand it.
	argv := make([]string, 0, len(argvToks))
	for _, t := range argvToks {
		if !t.quoted {
			return ScriptMatch{}, false
		}
		argv = append(argv, t.value)
	}
	if !s.Statement.MatchesArgv(argv) {
		return ScriptMatch{}, false
	}
	return match, true
}

// scriptToken is one whitespace-separated token of a command head.
type scriptToken struct {
	value string
	// quoted reports that the token arrived wrapped in single quotes. A
	// redirect token is never quoted: its value came from inside the quotes,
	// but the token itself carries the `2>` operator, so treating it as a
	// quoted word would let it stand in for the program or an argument.
	quoted   bool
	redirect bool
}

// stderrRedirect is the only redirect a command head may carry, and it must be
// followed immediately by a single-quoted path. See matchHead.
const stderrRedirect = "2>'"

// tokenizeScriptHead splits a command head into tokens, honoring single quotes.
// Bare tokens may not contain any character the shell would act on, so the only
// place shell-significant bytes can appear is inside single quotes — where the
// shell itself will not interpret them.
//
// The one exception is the `2>'<path>'` redirect, recognized here rather than by
// stripping a suffix off the raw head. The difference is not stylistic: a suffix
// match cannot tell a redirect from the same bytes sitting inside a quoted
// argument, so it would decide what the shell will act on by looking at text the
// shell will not. Scanning in quoting order removes the question.
func tokenizeScriptHead(s string) ([]scriptToken, bool) {
	var out []scriptToken
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}

		if s[i] == '\'' || strings.HasPrefix(s[i:], stderrRedirect) {
			redirect := s[i] != '\''
			open := i + 1
			if redirect {
				open = i + len(stderrRedirect)
			}
			end := strings.IndexByte(s[open:], '\'')
			if end < 0 {
				return nil, false
			}
			tok := s[open : open+end]
			if strings.ContainsAny(tok, "\\\n\r\x00") {
				return nil, false
			}
			out = append(out, scriptToken{value: tok, quoted: !redirect, redirect: redirect})
			i = open + end + 1
			// A quoted token must be followed by whitespace or end of input;
			// `'a'b` would otherwise concatenate into something unreviewed.
			if i < len(s) && s[i] != ' ' && s[i] != '\t' {
				return nil, false
			}
			continue
		}

		start := i
		for i < len(s) && s[i] != ' ' && s[i] != '\t' {
			i++
		}
		tok := s[start:i]
		if strings.ContainsAny(tok, shellMeta) || strings.ContainsRune(tok, '\'') {
			return nil, false
		}
		out = append(out, scriptToken{value: tok, quoted: false})
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// splitScriptLines returns the body's non-blank lines. A trailing newline is
// normal; blank lines carry no statements and are ignored.
//
// "Blank" is space and tab only, deliberately not unicode.IsSpace: the shell's
// default IFS is space, tab and newline, so a line holding U+000B, U+000C,
// U+0085 or U+00A0 is a command word to it and gets a PATH lookup. Treating
// such a line as blank would drop it before the one-statement check while
// leaving its bytes in the body HardenBody returns and the host runs - a second
// statement naming a file the sandbox can plant on PATH.
func splitScriptLines(body string) []string {
	if strings.ContainsRune(body, '\r') {
		return nil
	}
	var out []string
	for line := range strings.SplitSeq(body, "\n") {
		if strings.Trim(line, " \t") == "" {
			continue
		}
		out = append(out, strings.TrimRight(line, " \t"))
	}
	return out
}
