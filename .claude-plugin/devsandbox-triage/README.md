# devsandbox-triage

A Bash tool hook that recognizes a failing command as one of devsandbox's documented restrictions and hands the model the setting or document that explains it.

Install it separately from the `devsandbox-config` plugin, which carries the configuration skill:

```
/plugin marketplace add zekker6/devsandbox
/plugin install devsandbox-triage@devsandbox
```

## What it does

The hook matches the command's output against a fixed table of signatures taken from devsandbox's own sources. On a hit it adds one short note to the model's context naming the cause, the fix, and the document to read. On anything it does not recognize it emits nothing, so an ordinary failure costs nothing.

Two tiers, separated by how much the signature proves on its own:

| Tier | Fires when | Examples |
|---|---|---|
| Named | always - the message names devsandbox, so the sandbox is already established | `Request blocked by devsandbox:`, `devsandbox: egress lockdown:`, `docker proxy: … write operations not allowed`, `unknown config key`, `invalid configuration:` |
| Ambient | only when `$DEVSANDBOX` is set or the command invoked `devsandbox` | `Read-only file system`, `Permission denied (publickey)`, cloud SDK credential errors |

An ambient signature is an ordinary kernel or tool error that means nothing on its own, which is why it needs the context gate. A named one identifies devsandbox by itself.

## Events

Registered on both `PostToolUse` and `PostToolUseFailure`, because a signature lands on either. A non-zero exit makes Claude Code treat the tool call as failed, so `docker run` against the proxied socket arrives on `PostToolUseFailure` with the output in `error`; a command that does not check the status - `curl` without `-f` prints the proxy's 403 body and still exits 0 - arrives on `PostToolUse` with the output in `tool_response`. Registering only the first would miss every blocked request that a tool reports in its body rather than its exit code.

The note always ends by telling the model not to work around the boundary, and to report the restriction to the user instead. Whether to loosen a sandbox restriction is the user's decision, not the agent's.

## False positives

Two are guarded, because both happen while working on devsandbox itself:

- Output lines shaped like `path:123:` are dropped before matching, so grepping the sources for a signature is not read as hitting it.
- A signature that also appears in the command is ignored, for the same reason.

The note still says it is a signature match rather than a diagnosis. Test output that quotes these strings can reach the model; nothing suppresses that.

## Requirements

`jq` on `PATH`. Without it the hook reports itself disabled once, through `systemMessage`, and then stays quiet.

## Scope

Bash only. A `.env` file read inside a sandbox returns empty rather than failing - it is overlaid with `/dev/null` - so nothing in a tool result marks it as blocked, and catching it would mean matching on `Read` of a path pattern instead of on an error. That is deliberately not covered.
