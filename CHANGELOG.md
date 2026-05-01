# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased](https://github.com/zekker6/devsandbox/compare/v0.17.0...HEAD)

## [v0.17.0](https://github.com/zekker6/devsandbox/releases/tag/v0.17.0) - 2026-04-30

### Added

- **Audit-grade structured logging.** Per-session fields (`session_id`, `sandbox_name`, `sandbox_path`, `project_dir`, `isolator`, `pid`, `devsandbox_version`) on every dispatched entry, plus synthesized `session.start` / `session.end` lifecycle events and security events (`proxy.filter.decision`, `proxy.redaction.applied`, `proxy.credential.injected`, `proxy.mitm.bypass`, `mount.decision`, `notice.overflow`). See [Audit Logging](docs/configuration.md#audit-logging).
- **OTLP `header_sources`.** Resolve receiver headers from `value` / `env` / `file` at runtime so secrets stay on the host. See [Authenticating to an Auth-Enforced Endpoint](docs/configuration.md#authenticating-to-an-auth-enforced-endpoint).
- `NODE_USE_ENV_PROXY=1` is now set automatically in proxy mode so Node.js ≥24's built-in `fetch` (undici) honors `HTTP(S)_PROXY` - fixes `ENETUNREACH` from npx-based tools like `mcp-remote`.

## [v0.16.0](https://github.com/zekker6/devsandbox/releases/tag/v0.16.0) - 2026-04-29

### Added

- **Proxy `log_skip` rules.** Drop matching requests from the proxy log (local + remote dispatchers); the request itself still passes through. See [Skipping Log Entries](docs/proxy.md#skipping-log-entries).

## [v0.15.0](https://github.com/zekker6/devsandbox/releases/tag/v0.15.0) - 2026-04-29

### Added

- `devsandbox sandboxes prune --orphaned` flag to restrict pruning to orphaned sandboxes (those whose original project directory no longer exists). The flag intersects with other selectors: `--orphaned --older-than 30d` removes orphans last used over 30 days ago; `--orphaned --keep N` prunes orphans outside the N most-recently-used set; `--orphaned --all` (or `--orphaned` alone) removes every orphan. Without the flag, the existing default (orphans-only when no other selector is set) is unchanged.
- **Generic credential injector for proxy.** Define credential injection by `host` + `header` + `value_format` + `[...source]` + `overwrite` in TOML - no Go code change required to add a new service. Built-in `github` preset preserves existing config compatibility (`[proxy.credentials.github] enabled = true` works unchanged, including `GITHUB_TOKEN` → `GH_TOKEN` fallback). Specificity-based ordering when multiple injectors could match the same request (exact host > longer literal > shorter glob, tie-break by name). `BuildCredentialInjectors` now returns an error for invalid configs (unknown preset, missing `host`/`header`, invalid glob).

## [v0.14.1](https://github.com/zekker6/devsandbox/releases/tag/v0.14.1) - 2026-04-28

### Changed

- **`zellij` tool is now disabled by default.** Unlike `kitty`, the zellij socket has no capability filtering - exposing it lets sandboxed code drive the host multiplexer (run commands in any pane, read pane contents, etc.). Auto-detection of an active `ZELLIJ` session no longer mounts the socket or forwards `ZELLIJ*` env vars on its own. Set `[tools.zellij] enabled = true` to opt back in. `devsandbox tools check zellij` reports the opt-in requirement.

## [v0.14.0](https://github.com/zekker6/devsandbox/releases/tag/v0.14.0) - 2026-04-27

### Added

- `codex`, `opencode`, and `pi` tools now honor their respective custom config-location env vars on the host and forward them into the sandbox so the CLIs resolve the same paths inside:
  - `codex`: `CODEX_HOME` overrides `~/.codex`. When set, the host value is passed through and the directory is mounted at the same path.
  - `opencode`: `OPENCODE_CONFIG_DIR` is mounted in addition to (not in place of) `~/.config/opencode`, matching opencode's load semantics; the env var is forwarded.
  - `pi`: `PI_CODING_AGENT_DIR` overrides `~/.pi/agent`. The agent dir is still tmpoverlayed (settings/credentials are write-discarded) and the `sessions/` subdirectory is still persisted; the env var is forwarded.

## [v0.13.3](https://github.com/zekker6/devsandbox/releases/tag/v0.13.3) - 2026-04-20

### Fixed

- `kitty` proxy revdiff launch pattern now accepts the unquoted `/usr/bin/env` prefix the launcher actually emits (only `ENV_PREFIX` assignments and the inner argv are single-quoted). The literal absolute path is required - bare `env` (PATH-relative) still rejects, so `$PATH` shadowing can't be used to bypass the inner-program check.

## [v0.13.2](https://github.com/zekker6/devsandbox/releases/tag/v0.13.2) - 2026-04-20

### Fixed

- `kitty` proxy revdiff launch pattern: added `MatchShellExecEnvSentinel`, accepting `sh -c "'/usr/bin/env' 'KEY=VAL' ... '<prog>' '<arg>'...; touch '<sentinel>'"`. The revdiff launcher injects an `env` wrapper so the kitty-spawned overlay inherits `EDITOR`/`VISUAL` from the caller's login shell; the previous pattern matched only the no-env form. Env-var names are restricted to `^[A-Z_][A-Z0-9_]*$`, the inner argv is still validated against the existing revdiff pattern, and the sentinel-tail rules (no shell metacharacters, canonical path) are unchanged.

## [v0.13.1](https://github.com/zekker6/devsandbox/releases/tag/v0.13.1) - 2026-04-18

### Fixed

- `revdiff` tool no longer wipes its shared IPC directory on `Start`/`Stop`. Because the dir is exported as `$TMPDIR` for every sandboxed process, long-lived tenants (Claude Code's per-session task cache under `$TMPDIR/claude-<uid>/…/tasks/`, Node's compile cache, Go's build cache) populate subtrees that must survive sandbox restarts for the same project - and parallel sandboxes on the same project share the directory, so wiping it from one tore state out from under the others. The old `RemoveAll` on `Start` could yank state out from under a running caller; Node's non-recursive `fs.mkdirSync` then failed with `ENOENT`, breaking every subsequent Claude Code Bash tool call. `Start` now only ensures the dir exists (0700); `Stop` is a no-op. Stale revdiff sentinels are harmless - the launcher uses `mktemp` with fresh names.

## [v0.13.0](https://github.com/zekker6/devsandbox/releases/tag/v0.13.0) - 2026-04-17

### Added

- `[sandbox.environment.<NAME>]` config block: declare sandbox environment variables using the same source model as proxy credentials (`value` / `env` / `file`, priority `value > env > file`). `env = "X"` with `X` unset on the host silently skips the variable; an unreadable `file = "..."` is a startup error. Declaring the same variable in both `env_passthrough` and `environment` fails at startup with a message naming the variable - each variable belongs in exactly one place.
- `pi` tool: integrates [Pi Coding Agent](https://github.com/badlogic/pi-mono). `~/.pi/agent` is mounted with credential protection; `~/.pi/agent/sessions` persists across runs.
- `[proxy.credentials.github] overwrite = true`: force-replace any existing `Authorization` header on outgoing `api.github.com` requests. Intended for the pattern where a sandboxed CLI (e.g. `gh`) refuses to start without a token in its environment - pass a placeholder through `env_passthrough` / `sandbox.environment` while the real token stays on the host and is swapped in by the proxy. Default remains `false` (existing tool-set headers are preserved).
- `revdiff` tool now provides a shared IPC directory (`~/.cache/devsandbox/revdiff-ipc/<session>/`) bind-mounted at the same path on both sides and exported as `TMPDIR`. The kitty-spawned overlay shell runs on the host and receives sentinel/output paths as literal strings, so host and sandbox must agree on the string - argv-shipped paths need `Source == Dest` equality, not just a shared inode.

### Changed

- **Kitty tool now runs a capability-filtering proxy instead of bind-mounting the host socket.** The host kitty remote-control socket is no longer exposed inside the sandbox; a local proxy at `$HOME/.kitty.sock` is exposed instead, and `KITTY_LISTEN_ON` is rewritten to point at it. Sandboxed processes can only issue kitty commands declared as capabilities by an enabled tool (`launch_overlay`, `launch_window`, `launch_tab`, `launch_os_window`, `close_owned`, `wait_owned`, `focus_owned`, `send_text_owned`, `get_text_owned`, `set_title_owned`, `list_owned`), and `*_owned` commands are scoped by ownership tracking to windows the sandbox itself opened. Shell metacharacters in `sh -c` payloads for `launch_*` are rejected outright. `remote_control_password` is unsupported - use `allow_remote_control = socket-only`. New `[tools.kitty]` fields: `mode` (`auto` default / `disabled` / `enforce`) and `extra_capabilities` (additive; `launch_*` entries rejected). Under `auto`, the proxy only starts when at least one enabled tool declares a capability - zero attack surface when no tool needs kitty. `revdiff` is the built-in consumer.

### Fixed

- macOS: shortened test directory names to stay under the platform's unix socket path length limit (affected `kittyproxy` and `kitty` tool tests).

## [v0.12.0](https://github.com/zekker6/devsandbox/releases/tag/v0.12.0) - 2026-04-16

### Changed

- Wrapper diagnostic output (port-forward notices, session warnings, proxy setup info, container progress) no longer writes directly to stderr while a child process owns the terminal. Messages are written to `$XDG_STATE_HOME/devsandbox/wrapper.log` (or `~/.local/state/devsandbox/wrapper.log`) and a one-line banner is shown on exit if anything was suppressed. This prevents wrapper output from corrupting TUI applications (Claude Code, aider, etc.) running inside the sandbox. Pass `--verbose` or set `DEVSANDBOX_DEBUG=1` to restore the old behavior of writing every message to stderr.

### Added

- `--worktree` and `--worktree-base` flags: opt-in git-worktree mode. Bare `--worktree` auto-generates `devsandbox/<session-or-timestamp>` off HEAD; `--worktree=<branch>` reuses or creates a named branch. The sandbox CWD is the worktree; the main checkout is untouched. With `--git-mode=readwrite`, commits land on the worktree branch only. `--rm` removes the worktree on exit via `git worktree remove --force` + `prune`. `--worktree` + `--git-mode=disabled` is rejected at flag-parse time. Worktrees live at `~/.local/share/devsandbox/<project-slug>/worktrees/<branch>/` and the slug is derived from the main repo root so sibling worktrees share sandbox state. `devsandbox sandboxes prune` and the `doctor` command are worktree-aware.

### Fixed

- `zellij` and `kitty` tool socket bindings are now explicit bind mounts (`Type: MountBind`) instead of inheriting the default tmpoverlay from `CategoryRuntime`. Overlayfs cannot expose a unix socket from its lower layer, so under the previous policy the host socket was invisible inside the sandbox and `zellij list-sessions` / `kitten @` silently failed.
- Auto port-forwarding no longer tries (and fails) to forward when the sandbox shares the host network namespace. Without proxy mode the sandbox uses bwrap's `--share-net`, so a tool listener inside the sandbox is the same kernel socket as the "host" bind the forwarder would attempt - producing a spurious `bind: address already in use` error for every detected port. Auto-detect now inspects the sandbox netns inode and skips forwarding (with a one-line explanatory message) when it matches the host; the sandbox ports are already directly reachable on `127.0.0.1`. For the rare case where auto-forward runs in a properly isolated netns but the host happens to already have that port in use, the forwarder falls back to an ephemeral host port and logs the mapping instead of silently dropping the service.

## [v0.11.0](https://github.com/zekker6/devsandbox/releases/tag/v0.11.0) - 2026-04-14

### Added

- `zellij` tool forwards an active Zellij session into the sandbox by mounting the session socket directory and the `zellij` binary. Auto-detected when `ZELLIJ` is set and the binary is on `PATH`, so `zellij` commands run inside the sandbox attach to the host multiplexer.
- `zellij` tool now also mounts `$XDG_RUNTIME_DIR/zellij/`, which is where zellij 0.41+ stores its IPC socket (the legacy `/tmp/zellij-$UID/` holds only cache/log files on modern releases). The override env var is `ZELLIJ_SOCKET_DIR` (previously the tool checked the incorrect `ZELLIJ_SOCK_DIR`).

## [v0.10.0](https://github.com/zekker6/devsandbox/releases/tag/v0.10.0) - 2026-04-10

### Added

- `kitty` tool forwards the Kitty remote-control socket into the sandbox so `kitten @` commands inside the sandbox can drive the host terminal.

## [v0.9.3](https://github.com/zekker6/devsandbox/releases/tag/v0.9.3) - 2026-04-08

- `~/.local/bin` and `~/.local/share/claude` are now read-only bind mounts instead of persistent writable overlays. Under the split-mode default introduced in v0.8.0 these host-managed tool-install directories were being treated as `CategoryData`, which let in-sandbox tool self-updaters (e.g. Claude Code's own updater) write partial/empty files into the per-project overlay upper-dir. Those writes shadowed the real host binaries in every subsequent session, causing failures like `fish: '/home/$USER/.local/bin/claude' exists but is not an executable file` (exit 126).

## [v0.9.2](https://github.com/zekker6/devsandbox/releases/tag/v0.9.2) - 2026-04-08

### Fixed

- HTTP proxy no longer intercepts the body of HEAD requests. The previous behavior broke `Content-Length` handling and caused errors for some clients (e.g. Helm pulling OCI charts).

## [v0.9.1](https://github.com/zekker6/devsandbox/releases/tag/v0.9.1) - 2026-04-07

### Fixed

- Sandbox removal now `chmod`s files recursively before deletion. Go populates its build cache with `0500` files, which previously caused sandbox cleanup to fail.

## [v0.9.0](https://github.com/zekker6/devsandbox/releases/tag/v0.9.0) - 2026-04-07

### Added

- `devsandbox scratchpad [name] [command...]` subcommand for running sandboxes in managed, clean working directories under `~/.local/share/devsandbox-scratchpads/`. State persists between runs. Name defaults to `default`.
- `devsandbox scratchpad list` and `devsandbox scratchpad list --json` list scratchpads with size and state info.
- `devsandbox scratchpad rm <name>` (with `--all`, `--keep-state`, `--force`) removes scratchpads and their sandbox state.

### Fixed

- Git tool now strips sensitive fields from `.git/config` in place instead of replacing the file wholesale. The previous full replacement caused the git CLI to refuse to operate even for read-only commands inside the sandbox.

## [v0.8.2](https://github.com/zekker6/devsandbox/releases/tag/v0.8.2) - 2026-04-06

### Fixed

- Claude tool stores project knowledge under the `data` section so chat history persists between sandbox runs.

## [v0.8.1](https://github.com/zekker6/devsandbox/releases/tag/v0.8.1) - 2026-04-06

### Added

- macOS support for the devsandbox shim via a platform-specific copy-on-start overlay implementation, split from the Linux path.
- `jq` is now included in the default Docker image.

### Changed

- Debian base image bumped in the Docker image.
- mise-managed tool dependencies bumped.
- Docker and lint CI workflows limit concurrency to avoid redundant runs.

### Fixed

- Restored shim source files that were missing from the v0.8.0 release and added CI coverage so the shim is built and verified on every run.

## [v0.8.0](https://github.com/zekker6/devsandbox/releases/tag/v0.8.0) - 2026-04-05

### Breaking Changes

- **`[overlay] enabled` removed** - replaced by `[overlay] default` which accepts:
  `split` (default), `overlay`, `tmpoverlay`, `readonly`, `readwrite`.
- **`[tools.mise] writable` and `persistent` removed** - use `[tools.mise] mount_mode` instead.
  Mise no longer has tool-specific overlay configuration; use the unified `mount_mode` system.
- **Default mount behavior changed** - tool mounts now default to `split` overlay policy
  (configs → tmpoverlay, caches/data/state → persistent overlay) instead of read-only
  bind mounts. This prevents supply chain attacks from poisoning host tool configurations
  through sandboxed package managers.

### Migration Guide

| Before | After |
|--------|-------|
| `[overlay] enabled = true` | `[overlay] default = "split"` (or omit - it's the default) |
| `[overlay] enabled = false` | `[overlay] default = "readonly"` |
| `[tools.mise] writable = true, persistent = true` | `[tools.mise] mount_mode = "overlay"` |
| `[tools.mise] writable = true, persistent = false` | `[tools.mise] mount_mode = "tmpoverlay"` |
| (no equivalent) | `[tools.git] mount_mode = "readwrite"` |

### Added

- **Binding categories** - tools now classify each mount as `config`, `cache`, `data`,
  `state`, or `runtime`, enabling differentiated overlay policies.
- **`[overlay] default`** - global mount mode for all tool bindings with five modes:
  `split`, `overlay`, `tmpoverlay`, `readonly`, `readwrite`.
- **Per-tool `mount_mode`** - override the global default for specific tools
  (e.g., `[tools.git] mount_mode = "readwrite"`). Accepts `disabled` to prevent
  a tool's config from being mounted entirely.

### Changed

- Tool bindings no longer hardcode `ReadOnly` or `Type` - the builder resolves these
  based on the mount mode policy chain (per-tool > global > split).
- Claude, Copilot, Codex, and OpenCode tools previously mounted configs read-write;
  they now follow the global mount mode (default: tmpoverlay for configs).
