#!/usr/bin/env bash
#
# Behavioral tests for triage.sh. The hook is a signature table matched against
# real devsandbox output, so an edit to a message in the Go sources can silently
# stop it matching - these cases are what notices.

set -uo pipefail

HOOK="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/triage.sh"
pass=0
fail=0

if ! command -v jq >/dev/null 2>&1; then
	echo "SKIP: jq not on PATH, which is also what disables the hook itself"
	exit 0
fi

# run <name> <EMIT|SILENT> <expected substring or -> <in-sandbox 0|1> <tool> <command> <output>
run() {
	local name=$1 expect=$2 want=$3 sb=$4 tool=$5 cmd=$6 out=$7 got rc ok=1
	local payload
	payload=$(jq -n --arg t "$tool" --arg c "$cmd" --arg o "$out" \
		'{tool_name:$t, tool_input:{command:$c}, tool_output:{type:"text", text:$o}}')
	if [ "$sb" = 1 ]; then
		got=$(DEVSANDBOX=1 bash "$HOOK" <<<"$payload")
	else
		got=$(env -u DEVSANDBOX bash "$HOOK" <<<"$payload")
	fi
	rc=$?
	[ $rc -eq 0 ] || {
		ok=0
		echo "      non-zero exit: $rc"
	}
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
			if [ "$want" != "-" ]; then
				jq -r '.additionalContext // ""' <<<"$got" | grep -qF "$want" || {
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

echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ]
