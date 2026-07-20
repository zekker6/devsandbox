# devsandbox

## Coding practices

After completing the task always run:

- `task test` - to run tests
- `task lint` - to run lint

Always prefer reasonable defaults if that it possible. Reduce amount of work for the user to do when this is possible.
All defaults must be secure by default, and not cause any security issues if used without modification.
Never bind to all interfaces by default, and do not expose any ports by default.

Errors must always be handled and reported when present. Silent failures are not acceptable.

## Terminal socket proxies

Proxies that let sandboxed code drive a host terminal (kitty, herdr) share the same building blocks. Reuse them rather than reimplementing:

- `internal/socketproxy` - UDS listener lifecycle. Handlers for streaming protocols must not assume one request per connection.
- `internal/cmdpattern` - validation of commands the host will execute: `CommandPattern` for a single argv, `ScriptPattern` for a generated script body.

Two invariants matter, both because they have already been violated:

- **Pin the program to its resolved absolute path** (`cmdpattern.ResolveProgram`), never match on basename. A few directories are write-through binds shared with the host at an *identical* path - the revdiff IPC directory is one - so basename matching lets the sandbox supply the binary. Most of the sandbox home is an overlay whose writes never reach the host, which is exactly why the resolved path is safe and the basename is not. If the binary cannot be resolved, deny everything rather than falling back.
- **Never validate a file the sandbox can still write.** Read it once, validate those bytes, and copy them somewhere the sandbox cannot reach before handing the path to the host. Validating in place leaves a swap window.

Scope mutations to resources the sandbox created, taking their ids from the server's response and never from the client. Deny by default: a method with no explicit validator is refused, not passed through.

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
