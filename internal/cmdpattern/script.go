package cmdpattern

import (
	"path/filepath"
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
//	[/usr/bin/env 'KEY=VAL'...] [KEY=VAL...] '<prog>' '<arg>'...; rc=$?; printf "%s" "$rc" > '<sentinel>'.tmp && mv -f '<sentinel>'.tmp '<sentinel>'
//
// That is the completion-sentinel form emitted by the revdiff launcher. The
// leading command must satisfy Statement; the trailing clause must match
// byte-for-byte apart from the sentinel path, which must be a canonical
// absolute path of safe characters and identical in all three positions.
//
// Anything else — a second statement, a heredoc, command substitution, a
// pipeline, an unexpected redirect — is rejected. The pattern does not try to
// model the shell; it recognizes one sentence.
type ScriptPattern struct {
	// Shebangs is the allowlist of acceptable first lines. A body whose first
	// line starts with "#!" must match one of these exactly. Empty means no
	// shebang is permitted.
	Shebangs []string

	// Statement matches the leading command's argv.
	Statement CommandPattern

	// MaxBytes caps the body size. Zero means defaultScriptMaxBytes.
	MaxBytes int
}

const defaultScriptMaxBytes = 64 << 10 // 64 KiB — launcher scripts are one line

// sentinelTailRe matches the fixed completion clause the launcher appends. The
// three sentinel occurrences are captured separately so the caller can require
// they are identical; a regexp backreference is unavailable in RE2.
var sentinelTailRe = regexp.MustCompile(
	`^; rc=\$\?; printf "%s" "\$rc" > '([^']*)'\.tmp && mv -f '([^']*)'\.tmp '([^']*)'$`,
)

// MatchesBody reports whether body is a script this pattern accepts.
func (s ScriptPattern) MatchesBody(body []byte) bool {
	maxBytes := s.MaxBytes
	if maxBytes == 0 {
		maxBytes = defaultScriptMaxBytes
	}
	if len(body) == 0 || len(body) > maxBytes {
		return false
	}
	if strings.ContainsRune(string(body), '\x00') {
		return false
	}

	lines := splitScriptLines(string(body))
	if len(lines) == 0 {
		return false
	}

	// A leading "#!" line must be explicitly allowlisted.
	if strings.HasPrefix(lines[0], "#!") {
		if !slices.Contains(s.Shebangs, lines[0]) {
			return false
		}
		lines = lines[1:]
	}

	// Exactly one statement. More than one would let an accepted command sit
	// beside an unaccepted one.
	if len(lines) != 1 {
		return false
	}
	return s.matchesStatement(lines[0])
}

// matchesStatement validates the single command line: leading command plus the
// fixed sentinel tail.
func (s ScriptPattern) matchesStatement(line string) bool {
	// Split at the first "; rc=" so the tail is matched as a literal shape and
	// the head is never scanned for metacharacters it is allowed to contain.
	idx := strings.Index(line, "; rc=")
	if idx < 0 {
		return false
	}
	head, tail := line[:idx], line[idx:]

	m := sentinelTailRe.FindStringSubmatch(tail)
	if m == nil {
		return false
	}
	sentinel := m[1]
	if m[2] != sentinel || m[3] != sentinel {
		return false
	}
	if !sentinelPathRe.MatchString(sentinel) || filepath.Clean(sentinel) != sentinel {
		return false
	}

	return s.matchesHead(head)
}

// matchesHead validates the leading command: an optional `/usr/bin/env` prefix,
// then any number of KEY=VAL assignments, then the program and its arguments.
func (s ScriptPattern) matchesHead(head string) bool {
	head = strings.TrimSpace(head)

	// The launcher emits `/usr/bin/env` unquoted. Only this exact absolute path
	// is accepted, so a PATH-relative `env` cannot be substituted.
	if rest, ok := strings.CutPrefix(head, "/usr/bin/env "); ok {
		head = strings.TrimLeft(rest, " ")
	}

	toks, ok := tokenizeScriptHead(head)
	if !ok {
		return false
	}

	// Consume leading KEY=VAL assignments, quoted or bare.
	i := 0
	for i < len(toks) {
		eq := strings.IndexByte(toks[i].value, '=')
		if eq <= 0 || !envVarNameRe.MatchString(toks[i].value[:eq]) {
			break
		}
		i++
	}

	argvToks := toks[i:]
	if len(argvToks) == 0 {
		return false
	}
	// Everything from the program onward must have been single-quoted, which is
	// what the launcher's shell quoter emits. An unquoted token here would mean
	// the shell could still expand it.
	argv := make([]string, 0, len(argvToks))
	for _, t := range argvToks {
		if !t.quoted {
			return false
		}
		argv = append(argv, t.value)
	}
	return s.Statement.MatchesArgv(argv)
}

// scriptToken is one whitespace-separated token of a command head.
type scriptToken struct {
	value  string
	quoted bool
}

// tokenizeScriptHead splits a command head into tokens, honoring single quotes.
// Bare tokens may not contain any character the shell would act on, so the only
// place shell-significant bytes can appear is inside single quotes — where the
// shell itself will not interpret them.
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

		if s[i] == '\'' {
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, false
			}
			tok := s[i+1 : i+1+end]
			if strings.ContainsAny(tok, "\\\n\r\x00") {
				return nil, false
			}
			out = append(out, scriptToken{value: tok, quoted: true})
			i += 1 + end + 1
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
func splitScriptLines(body string) []string {
	if strings.ContainsRune(body, '\r') {
		return nil
	}
	var out []string
	for line := range strings.SplitSeq(body, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, strings.TrimRight(line, " \t"))
	}
	return out
}
