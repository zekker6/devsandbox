package kittyproxy

import "devsandbox/internal/cmdpattern"

// Command-pattern matching moved to internal/cmdpattern so the herdr proxy can
// share it. These aliases keep existing call sites (notably
// internal/sandbox/tools/revdiff.go) compiling against kittyproxy.
// New code should import cmdpattern directly.

type CommandPattern = cmdpattern.CommandPattern

var (
	MatchAny                  = cmdpattern.MatchAny
	MatchPrefix               = cmdpattern.MatchPrefix
	MatchShellExec            = cmdpattern.MatchShellExec
	MatchShellExecSentinel    = cmdpattern.MatchShellExecSentinel
	MatchShellExecEnvSentinel = cmdpattern.MatchShellExecEnvSentinel
)
