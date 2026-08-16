#!/usr/bin/env bash
#
# Behavioral tests for triage.sh. The hook is a signature table matched against
# real devsandbox output, so an edit to a message in the Go sources can silently
# stop it matching - these cases are what notices.
#
# Every case runs twice, once per event the hook is registered on, because the
# two carry the command output in different fields: PostToolUseFailure in
# .error, PostToolUse in .tool_response. A payload shape invented for the test
# rather than taken from Claude Code is how this suite once passed against a
# hook that could never fire in production.

set -uo pipefail

HOOK="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/triage.sh"
pass=0
fail=0

if ! command -v jq >/dev/null 2>&1; then
	echo "SKIP: jq not on PATH, which is also what disables the hook itself"
	exit 0
fi

# payload <event> <tool> <command> <output> [stream]
payload() {
	local ev=$1 tool=$2 cmd=$3 out=$4 stream=${5:-stdout}
	if [ "$ev" = PostToolUseFailure ]; then
		# nnt() renders a ShellError as "Exit code N", stderr, stdout joined
		# by newlines; the Bash tool throws with the whole output in stderr.
		jq -n --arg t "$tool" --arg c "$cmd" --arg o "$out" \
			'{hook_event_name:"PostToolUseFailure", tool_name:$t,
			  tool_input:{command:$c}, tool_use_id:"toolu_test",
			  error:(if $o == "" then "" else "Exit code 1\n" + $o end),
			  is_interrupt:false}'
	else
		jq -n --arg t "$tool" --arg c "$cmd" --arg o "$out" --arg s "$stream" \
			'{hook_event_name:"PostToolUse", tool_name:$t,
			  tool_input:{command:$c}, tool_use_id:"toolu_test",
			  tool_response:({stdout:"", stderr:"", interrupted:false} + {($s): $o})}'
	fi
}

# check <name> <EMIT|SILENT> <expected substring or -> <in-sandbox 0|1> <event> <payload>
check() {
	local name=$1 expect=$2 want=$3 sb=$4 ev=$5 body=$6 got rc ok=1 err
	err=$(mktemp)
	if [ "$sb" = 1 ]; then
		got=$(DEVSANDBOX=1 bash "$HOOK" <<<"$body" 2>"$err")
	else
		got=$(env -u DEVSANDBOX bash "$HOOK" <<<"$body" 2>"$err")
	fi
	rc=$?
	[ $rc -eq 0 ] || {
		ok=0
		echo "      non-zero exit: $rc"
	}
	# A hook that talks on stderr is not silent: it lands in the debug log on
	# every Bash call, which is the cost this hook promises not to charge.
	[ -s "$err" ] && {
		ok=0
		echo "      stderr: $(head -c 200 "$err")"
	}
	rm -f "$err"
	if [ "$expect" = SILENT ]; then
		[ -z "$got" ] || ok=0
	else
		if [ -z "$got" ]; then
			ok=0
		else
			jq -e . >/dev/null 2>&1 <<<"$got" || {
				ok=0
				echo "      not valid JSON"
			}
			[ "$(jq -r '.hookSpecificOutput.hookEventName // ""' <<<"$got" 2>/dev/null)" = "$ev" ] || {
				ok=0
				echo "      hookEventName is not $ev"
			}
			if [ "$want" != "-" ]; then
				jq -r '.hookSpecificOutput.additionalContext // ""' <<<"$got" | grep -qF "$want" || {
					ok=0
					echo "      missing substring: $want"
				}
			fi
		fi
	fi
	if [ $ok = 1 ]; then
		pass=$((pass + 1))
		printf 'PASS  %s\n' "$name"
	else
		fail=$((fail + 1))
		printf 'FAIL  %s\n      got: %s\n' "$name" "${got:-<empty>}"
	fi
}

# run <name> <EMIT|SILENT> <expected substring or -> <in-sandbox 0|1> <tool> <command> <output>
run() {
	local ev
	for ev in PostToolUse PostToolUseFailure; do
		check "$1 [$ev]" "$2" "$3" "$4" "$ev" "$(payload "$ev" "$5" "$6" "$7")"
	done
}

# --- signatures that name devsandbox ----------------------------------------

run "proxy filter block" EMIT "HTTP filter refused" 0 Bash \
	"curl -sS https://example.com" \
	"curl: (22) The requested URL returned error: 403
Request blocked by devsandbox: domain not in allow list"

run "redaction takes precedence over filter" EMIT "content redaction" 0 Bash \
	"curl -sS -d @payload https://api.example.com" \
	"Request blocked by devsandbox: request blocked: secret pattern detected in outgoing request"

run "docker socket write" EMIT "read-only" 0 Bash \
	"docker run -it alpine sh" \
	"Error response from daemon: docker proxy: POST /v1.43/containers/create blocked (write operations not allowed)"

run "egress lockdown abort" EMIT "exit 78" 0 Bash \
	"devsandbox --proxy claude" \
	"devsandbox: egress lockdown: nft: rule refused"

run "egress preflight refusal" EMIT "iproute2" 0 Bash \
	"devsandbox --proxy" \
	"Error: proxy mode needs an enforceable egress lockdown: nf_conntrack not loaded"

run "pasta missing" EMIT "passt/pasta" 0 Bash \
	"devsandbox --proxy" \
	"Error: proxy mode requires pasta: no such file
Run 'devsandbox doctor' for installation instructions"

run "unknown config key" EMIT "ignored" 0 Bash \
	"devsandbox --info" \
	"[warn] unknown config key in /home/u/.config/devsandbox/config.toml, ignored: sandbox.docker.keep_containers"

run "value of the wrong type" EMIT "wrong type" 0 Bash \
	"devsandbox --info" \
	"failed to load config: invalid configuration: [tools.mise]: ignore_global_config: expected a boolean, got a string"

# --- both output streams of a succeeding command ----------------------------

# A proxy refusal reaches a command that exits 0 whenever it does not check the
# status - curl without -f prints the 403 body and succeeds - so the signature
# arrives on PostToolUse, on either stream.
check "proxy filter block on stdout [PostToolUse]" EMIT "HTTP filter refused" 0 PostToolUse \
	"$(payload PostToolUse Bash "curl -sS https://example.com" \
		"Request blocked by devsandbox: domain not in allow list" stdout)"

check "proxy filter block on stderr [PostToolUse]" EMIT "HTTP filter refused" 0 PostToolUse \
	"$(payload PostToolUse Bash "curl -sS https://example.com" \
		"Request blocked by devsandbox: domain not in allow list" stderr)"

# --- false-positive guards --------------------------------------------------

run "grep hit is not a failure" SILENT - 0 Bash \
	"grep -rn 'blocked' internal/" \
	"internal/proxy/filter.go:227:	body := fmt.Sprintf(\"Request blocked by devsandbox: %s\\n\", reason)"

run "signature echoed by the command" SILENT - 0 Bash \
	"echo 'Request blocked by devsandbox: test'" \
	"Request blocked by devsandbox: test"

# --- ambient signatures need the context gate -------------------------------

run "read-only fs outside a sandbox" SILENT - 0 Bash \
	"git commit -m x" \
	"error: could not lock config file .git/config: Read-only file system"

run "read-only fs inside a sandbox" EMIT "readonly mode" 1 Bash \
	"git commit -m x" \
	"error: could not lock config file .git/config: Read-only file system"

run "read-only fs via a devsandbox command" EMIT "readonly mode" 0 Bash \
	"devsandbox git commit -m x" \
	"error: could not lock config file .git/config: Read-only file system"

run "read-only fs on a non-git path" EMIT "read-only mount" 1 Bash \
	"touch /usr/local/x" \
	"touch: cannot touch '/usr/local/x': Read-only file system"

run "ssh key auth outside a sandbox" SILENT - 0 Bash \
	"git push" \
	"git@github.com: Permission denied (publickey).
fatal: Could not read from remote repository."

run "ssh key auth inside a sandbox" EMIT "~/.ssh is not mounted" 1 Bash \
	"git push" \
	"git@github.com: Permission denied (publickey).
fatal: Could not read from remote repository."

run "cloud credentials inside a sandbox" EMIT "~/.aws" 1 Bash \
	"aws s3 ls" \
	"Unable to locate credentials. You can configure credentials by running \"aws configure\"."

# --- silence ----------------------------------------------------------------

run "unrelated failure" SILENT - 1 Bash \
	"go test ./..." \
	"FAIL	devsandbox/internal/foo	0.01s
exit status 1"

run "tool other than Bash" SILENT - 1 Read \
	"" \
	"Request blocked by devsandbox: whatever"

run "empty output" SILENT - 1 Bash "true" ""

# An event the hook was not registered for must not produce a hookEventName
# that fails Claude Code's schema validation - it stays quiet instead.
check "unregistered event" SILENT - 1 PreToolUse \
	'{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"curl x"},"tool_response":{"stdout":"Request blocked by devsandbox: x"}}'

# --- malformed input --------------------------------------------------------

for bad in "not json at all" "" "[1,2,3]" '{"tool_name":"Bash","tool_input":"not-an-object","tool_response":"go: build failed"}'; do
	check "malformed input: ${bad:-<empty>}" SILENT - 1 PostToolUse "$bad"
done

# A tool_response that is not the {stdout, stderr} object still gets scanned
# rather than dropped, and a tool_input that is not an object does not abort
# the walk to it.
check "tool_response as a bare string" EMIT "HTTP filter refused" 0 PostToolUse \
	'{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":"not-an-object","tool_response":"Request blocked by devsandbox: x"}'

# --- jq missing -------------------------------------------------------------

# The disabled notice has to be reported once and then stay quiet, including
# when CLAUDE_PLUGIN_DATA is unset - the hook runs on every Bash call.
jq_missing_case() {
	local label=$1 with_data=$2 tmp bin got1 got2 err ok=1
	local -a extra=()
	tmp=$(mktemp -d)
	bin="$tmp/bin"
	mkdir -p "$bin"
	# Only the utilities the hook needs before it looks for jq, so that its
	# absence is what the test exercises rather than a broken PATH.
	local util
	for util in cat mkdir; do
		ln -s "$(command -v "$util")" "$bin/$util"
	done
	# The directory is deliberately not created: Claude Code makes it on first
	# reference, so the hook has to.
	[ "$with_data" = 1 ] && extra=("CLAUDE_PLUGIN_DATA=$tmp/data")
	err="$tmp/err"
	# Absolute path to bash: PATH holds only the stubbed utilities, so env
	# itself could not resolve the interpreter. ${extra[@]+...} rather than a
	# bare "${extra[@]}" because bash 3.2 - what macOS ships, and what CI runs
	# there - counts an empty array as unset under `set -u`.
	got1=$(env -i PATH="$bin" TMPDIR="$tmp" HOME="$tmp" ${extra[@]+"${extra[@]}"} "$BASH" "$HOOK" </dev/null 2>"$err")
	got2=$(env -i PATH="$bin" TMPDIR="$tmp" HOME="$tmp" ${extra[@]+"${extra[@]}"} "$BASH" "$HOOK" </dev/null 2>>"$err")
	jq -e '.systemMessage | test("jq is not on PATH")' >/dev/null 2>&1 <<<"$got1" || {
		ok=0
		echo "      first run did not report: ${got1:-<empty>}"
	}
	[ -z "$got2" ] || {
		ok=0
		echo "      second run repeated the notice: $got2"
	}
	[ -s "$err" ] && {
		ok=0
		echo "      stderr: $(head -c 200 "$err")"
	}
	rm -rf "$tmp"
	if [ $ok = 1 ]; then
		pass=$((pass + 1))
		printf 'PASS  %s\n' "$label"
	else
		fail=$((fail + 1))
		printf 'FAIL  %s\n' "$label"
	fi
}

jq_missing_case "jq missing, CLAUDE_PLUGIN_DATA unset: reported once" 0
jq_missing_case "jq missing, CLAUDE_PLUGIN_DATA set: reported once" 1

echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ]
