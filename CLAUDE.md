# devsandbox

## Coding practices

After completing the task always run:

- `task test` - to run tests
- `task lint` - to run lint

Always prefer reasonable defaults if that it possible. Reduce amount of work for the user to do when this is possible.
All defaults must be secure by default, and not cause any security issues if used without modification.
Never bind to all interfaces by default, and do not expose any ports by default.

Errors must always be handled and reported when present. Silent failures are not acceptable.

A limit the user configured must never silently fail to apply. This is stricter than error handling, because the dangerous case is not an error at all - it is a success path that quietly enforces less than promised. systemd, for example, accepts `CPUQuota=` in a user scope and ignores it when the `cpu` controller is not delegated, warning only to the journal. Verify enforceability before launch and abort naming what is missing, rather than degrading to an unlimited run the user believes is capped. Where a value can be accepted but round to nothing (`cpus = "0.004"` becoming `CPUQuota=0%`), reject it at translation time.

## Terminal socket proxies

Proxies that let sandboxed code drive a host terminal (kitty, herdr) share the same building blocks. Reuse them rather than reimplementing:

- `internal/socketproxy` - UDS listener lifecycle. Handlers for streaming protocols must not assume one request per connection.
- `internal/cmdpattern` - validation of commands the host will execute: `CommandPattern` for a single argv, `ScriptPattern` for a generated script body.

Two invariants matter, both because they have already been violated:

- **Pin the program to its resolved absolute path** (`cmdpattern.ResolveProgram`), never match on basename. A few directories are write-through binds shared with the host at an *identical* path - the revdiff IPC directory is one - so basename matching lets the sandbox supply the binary. Most of the sandbox home is an overlay whose writes never reach the host, which is exactly why the resolved path is safe and the basename is not. If the binary cannot be resolved, deny everything rather than falling back.
- **Never validate a file the sandbox can still write.** Read it once, validate those bytes, and copy them somewhere the sandbox cannot reach before handing the path to the host. Validating in place leaves a swap window.

Scope mutations to resources the sandbox created, taking their ids from the server's response and never from the client. Deny by default: a method with no explicit validator is refused, not passed through.

Two more invariants come from the herdr agent-reporting work:

- **Anchor every validator to something derived on the host.** A request is checked against what devsandbox already knows - the pane id herdr gave this process, the agent devsandbox was asked to launch, the session directory that tool's own bindings produce - never against a value the request supplies. Where a bound is a filesystem path, take it from the same function that produces the bind mount so the two cannot drift apart.
- **Charset-restrict any sandbox-supplied string the host will hand to a shell.** herdr shell-quotes a reported session id and types it into a host pane shell, so the filter caps it at 128 bytes of `[A-Za-z0-9._-]` rather than trusting an unversioned third-party quoter. Length checks are not enough. Confinement of a path that names a location *inside* the sandbox overlay must be lexical - `pathWithin` resolves symlinks against the host filesystem, which proves nothing about a path the host cannot see.
- **Scan runes, not bytes, when rejecting what a terminal will act on.** A byte-wise `< 0x20` test sees only C0. The C1 controls (U+0080-U+009F, U+009B being CSI) arrive UTF-8-encoded as bytes ≥ 0xC2 and sail straight through it, which defeats the check in the case it exists for. `hasControlRune` in `internal/herdrproxy/filter.go` refuses Cc and Cf; ordinary non-ASCII text stays allowed, because the check bounds behavior, not charset.

**Adding a supported agent touches four places, and missing one fails silently.** `internal/agentid`'s `agents` table (the name, plus the resume shape herdr compiles in - `claude`/`pi` use a flag, `codex` a `resume` subcommand); the tool registered under that same name; `ToolWithAgentSessionDir` on that tool, derived from the same helper its bindings use; and a `CategoryData` binding for that directory so sessions survive the sandbox. The last two fail in opposite directions - a missing binding discards the session, a bound that drifts from the binding denies every real report. A binding alone is not enough either: `Optional` bindings whose host source is missing are skipped, so a directory the agent only ever creates in-sandbox needs a `ToolWithSetup` that creates it on the host (`Codex.Setup` is the worked example).

**State the host trusts must live where the sandbox cannot write it.** `$XDG_STATE_HOME` is repointed at the synthetic home inside the sandbox, which is what makes `~/.local/state/devsandbox/` safe for host-owned records such as `internal/herdrstate`. Anything under the project dir or a bound tool directory is sandbox-writable. Hash any opaque third-party id into the filename rather than letting it choose a path.

## Shell snippets installed on the host

`internal/shellwrap` generates snippets that devsandbox installs into the user's shell config. Two things are load-bearing there:

- **Guard the whole snippet on `DEVSANDBOX`.** `internal/sandbox/tools/shell.go` binds fish's `conf.d` and bash/zsh rc files *into* the sandbox, so anything installed there is also read in there - an unguarded `claude` function would re-invoke `devsandbox claude` recursively. Use non-empty semantics (`test -z "$DEVSANDBOX"` in fish, `-n "${DEVSANDBOX:-}"` in bash/zsh) and match it in Go, so `DEVSANDBOX=""` behaves identically everywhere.
- **Existence-guard anything sourced from an rc file.** `~/.config/devsandbox` is bound by nothing, while `.bashrc`/`.zshrc` are bound in, so a bare `source` line errors on every in-sandbox shell start. Emit `[ -r <path> ] && . <path>`.

Emit devsandbox's resolved absolute path into generated snippets, not `command devsandbox`: a login or terminal-multiplexer pane shell may have a different `PATH` (mise shims are the common case).

## Platform-specific packages

`internal/bwrap` and `internal/isolator/bwrap.go` have **no build tags** - they compile on darwin even though bwrap is Linux-only. Anything they import must build on darwin too. A Linux-only package pulled into that import graph breaks the darwin release build.

Split such a package three ways: an untagged file holding the types and any pure logic, a `_linux.go` with the real implementation, and a `_other.go` (`//go:build !linux`) whose stub returns a clear "only supported on Linux" error rather than silently succeeding. `internal/cgroups` is the worked example.

`task test`, `task lint` and CI all build for the host only, so this class of break is invisible to them. Run `task cross-build` and `task cross-test` after touching a platform-split package or its importers.

## Pinned dependencies

`github.com/elazarl/goproxy` is held at **v1.8.4**. v1.8.5 wraps the client connection in a `bufio.Writer` that is only flushed after `resp.Write` returns, so on the MITM path response headers and small SSE events stay buffered until the whole body is consumed - streaming responses arrive all at once. `renovate.json` caps the version, but that does not stop a manual `go get -u`; if `internal/proxy`'s two streaming regression tests start failing, check whether goproxy moved. Lift the cap only once upstream ships a fix, and re-run those tests to confirm.

## Changelog

`CHANGELOG.md` follows the Keep a Changelog format with an `[Unreleased]` section at the top.

- Every user-facing change MUST be recorded under `[Unreleased]` in the same change that introduces it: new features, behavior/config/CLI/API changes, bug fixes, and breaking changes. Do not defer it to a later "docs" commit.
- Categorize each entry as `Added`, `Changed`, `Fixed`, or `Breaking Changes`. Describe what changed and why it matters to the user, not the implementation detail.
- Do not itemize routine dependency bumps or CI/workflow digest updates. Only note a dependency change when it is user-visible (e.g. an embedded binary such as `pasta` or `bwrap`).
- When cutting a release, rename `[Unreleased]` to `## [vX.Y.Z](https://github.com/zekker6/devsandbox/releases/tag/vX.Y.Z) - YYYY-MM-DD` and repoint the `[Unreleased]` compare link to the new tag, in the same commit that gets tagged. Never tag a release while its content is still under `[Unreleased]`.

## Tools to use

Tools management: mise
Create .mise.toml with all tools to be used and pin the dependencies.

Use `task` for setting up automation: create a taskfile which will cover build, lint, test, running the appliication.

CI: repository will be hosted at gitea. Update it to correlate with current project when needed.

Linters: golangci + go default linters.
