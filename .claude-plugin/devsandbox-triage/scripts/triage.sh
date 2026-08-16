#!/usr/bin/env bash
#
# PostToolUse triage. Recognizes a failing Bash command as one of devsandbox's
# documented restrictions and tells the model which setting or document
# explains it. Matches a fixed signature table and stays silent on anything it
# does not recognize, so an ordinary failure costs nothing.

set -uo pipefail

payload=$(cat)

if ! command -v jq >/dev/null 2>&1; then
	# A hook that cannot parse its input must say so once rather than fail
	# silently, but it runs on every Bash call, so the notice is stamped.
	marker="${CLAUDE_PLUGIN_DATA:-}/jq-missing-reported"
	if [ -z "${CLAUDE_PLUGIN_DATA:-}" ] || [ ! -e "$marker" ]; then
		[ -n "${CLAUDE_PLUGIN_DATA:-}" ] && mkdir -p "${CLAUDE_PLUGIN_DATA}" 2>/dev/null && : >"$marker"
		printf '%s\n' '{"systemMessage":"devsandbox-triage: jq is not on PATH, so sandbox error triage is disabled. Install jq to enable it."}'
	fi
	exit 0
fi

tool=$(jq -r '.tool_name // ""' <<<"$payload")
[ "$tool" = "Bash" ] || exit 0

cmd=$(jq -r '.tool_input.command // ""' <<<"$payload")
out=$(jq -r '.tool_output | if type == "object" then (.text // tostring) else (. // "" | tostring) end' <<<"$payload")

[ -n "$out" ] || exit 0

# Drop "path:123:" lines so searching the devsandbox sources for one of these
# strings is not mistaken for hitting it. Same reason a signature found in the
# command itself is ignored below.
scan=$(printf '%s\n' "$out" | grep -vE '^[^[:space:]]+:[0-9]+:' || true)

# hit <literal> - the signature appears in the output but not in the command.
hit() {
	case "$scan" in
	*"$1"*) ;;
	*) return 1 ;;
	esac
	case "$cmd" in
	*"$1"*) return 1 ;;
	esac
	return 0
}

# in_sandbox - the failure plausibly happened under devsandbox. Required only
# for the ambient signatures (a bare kernel or ssh error), never for the ones
# that name devsandbox themselves.
in_sandbox() {
	[ -n "${DEVSANDBOX:-}" ] && return 0
	case "$cmd" in
	devsandbox\ * | *[\ \|\&\;\(]devsandbox\ *) return 0 ;;
	esac
	return 1
}

# emit <cause> <fix> <docs> [inspect]
emit() {
	local body
	body="devsandbox-triage: this failure matches a known devsandbox restriction.

Cause: $1
Fix:   $2
Docs:  $3"
	[ -n "${4:-}" ] && body="$body
Check: $4"
	body="$body

This is a signature match, not a diagnosis - confirm it fits before acting. Do not work around the sandbox boundary; report the restriction and the fix to the user and let them decide. For the exact configuration key, use the devsandbox-config skill if it is installed."
	jq -n --arg t "$body" '{additionalContext: $t}'
	exit 0
}

# --- Signatures that name devsandbox, so they need no context gate -----------

if hit "secret pattern detected in outgoing request"; then
	emit "the proxy's content redaction blocked the request: a configured secret pattern matched the outgoing body." \
		"loosen or retarget [proxy.redaction] rules, or remove the secret from the request." \
		'docs/proxy.md, section "Content Redaction"' \
		"devsandbox logs proxy --last 20"
fi

if hit "Request blocked by devsandbox:"; then
	emit "proxy mode's HTTP filter refused the request with 403. The text after the colon is the rule's reason." \
		"add an allow rule under [proxy.filter], or raise default_action, in ~/.config/devsandbox/config.toml." \
		'docs/proxy.md, section "HTTP Filtering"' \
		"devsandbox logs proxy --last 20; devsandbox filter show"
fi

if hit "docker proxy:" && hit "write operations not allowed"; then
	emit "the Docker socket is proxied read-only, so container lifecycle calls are refused." \
		"there is no setting that permits writes; run the container outside the sandbox if it is really needed." \
		'docs/tools.md, section "Allowed Operations"'
fi

if hit "devsandbox: egress lockdown:"; then
	emit "the egress lockdown failed to apply, so the launch aborted with exit 78 before the workload ran. Proxy mode fails closed." \
		"usually a missing nf_tables/nf_conntrack module or an undiscoverable route device. There is no opt-out; run without --proxy if the proxy is not needed." \
		'docs/proxy.md, section "egress lockdown aborts the launch"' \
		"devsandbox doctor"
fi

if hit "proxy mode needs an enforceable egress lockdown"; then
	emit "the preflight refused the launch: the host cannot enforce the egress rules proxy mode depends on." \
		"install iproute2 and nft or iptables, and load nf_tables and nf_conntrack. The message names the missing piece." \
		'docs/proxy.md, section "Requirements (bwrap backend)"' \
		"devsandbox doctor"
fi

if hit "proxy mode requires pasta"; then
	emit "the embedded pasta binary failed to extract and no system package is installed." \
		"install the passt/pasta package, or check why extraction failed." \
		'docs/proxy.md, section "proxy mode requires pasta"' \
		"devsandbox doctor"
fi

if hit "unknown config key"; then
	emit "a configuration key was not recognized, so it was ignored and its setting stayed at the default. The full dotted path is in the message." \
		"correct the spelling or remove the key. A typo here silently disables what it was meant to configure." \
		'docs/configuration.md, section "Unrecognized Keys"' \
		"devsandbox config show"
fi

if hit "invalid configuration:"; then
	emit "a recognized key holds a value of the wrong type, which fails the load rather than falling back to the default." \
		"fix the value or drop the key - a quoted boolean is the common case. The whole section is rejected, not just that key." \
		'docs/configuration.md, section "Values of the Wrong Type"' \
		"devsandbox config show"
fi

# --- Ambient signatures: only meaningful if this ran under devsandbox --------

in_sandbox || exit 0

if hit "Read-only file system"; then
	case "$cmd$scan" in
	*.git* | git\ *)
		emit "git runs in readonly mode by default, which mounts .git read-only so commits are blocked." \
			'set [tools.git] mode = "readwrite" in the config, which also exposes SSH and GPG credentials.' \
			'docs/tools.md, section "Modes"'
		;;
	esac
	emit "the path is on a read-only mount. Only the project directory is writable by default." \
		"add a [[sandbox.mounts.rules]] entry with mode = \"readwrite\" for the path, if exposing it is acceptable." \
		'docs/sandboxing.md, section "Custom Mounts"'
fi

if hit "Permission denied (publickey)" || hit "Host key verification failed" || hit "Could not read from remote repository"; then
	emit "~/.ssh is not mounted in the default git mode, so key-based SSH authentication cannot succeed." \
		'use an HTTPS remote, or set [tools.git] mode = "readwrite" to expose SSH and agent forwarding.' \
		'docs/sandboxing.md, section "What'"'"'s Not Available (by default)"'
fi

if hit "Unable to locate credentials" || hit "NoCredentialProviders" || hit "could not find default credentials" ||
	hit "az login" || hit "gcloud auth login"; then
	emit "~/.aws, ~/.azure, ~/.gcloud and ~/.config/gcloud are not mounted, so no cloud SDK can authenticate." \
		"there is no config key for these; run the cloud command outside the sandbox, or pass a token in via [sandbox] env_passthrough." \
		'docs/sandboxing.md, section "What'"'"'s Not Available (by default)"'
fi

exit 0
