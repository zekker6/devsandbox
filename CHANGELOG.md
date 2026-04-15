# Changelog

All notable changes to this project will be documented in this file.

## Unreleased

### Changed

- Wrapper diagnostic output (port-forward notices, session warnings, proxy setup info, container progress) no longer writes directly to stderr while a child process owns the terminal. Messages are written to `$XDG_STATE_HOME/devsandbox/wrapper.log` (or `~/.local/state/devsandbox/wrapper.log`) and a one-line banner is shown on exit if anything was suppressed. This prevents wrapper output from corrupting TUI applications (Claude Code, aider, etc.) running inside the sandbox. Pass `--verbose` or set `DEVSANDBOX_DEBUG=1` to restore the old behavior of writing every message to stderr.

### Added

- `zellij` tool forwards an active Zellij session into the sandbox by mounting the session socket directory and the `zellij` binary. Auto-detected when `ZELLIJ` is set and the binary is on `PATH`, so `zellij` commands run inside the sandbox attach to the host multiplexer.
- `zellij` tool now also mounts `$XDG_RUNTIME_DIR/zellij/`, which is where zellij 0.41+ stores its IPC socket (the legacy `/tmp/zellij-$UID/` holds only cache/log files on modern releases). The override env var is `ZELLIJ_SOCKET_DIR` (previously the tool checked the incorrect `ZELLIJ_SOCK_DIR`).

### Fixed

- `zellij` and `kitty` tool socket bindings are now explicit bind mounts (`Type: MountBind`) instead of inheriting the default tmpoverlay from `CategoryRuntime`. Overlayfs cannot expose a unix socket from its lower layer, so under the previous policy the host socket was invisible inside the sandbox and `zellij list-sessions` / `kitten @` silently failed.
- Auto port-forwarding no longer tries (and fails) to forward when the sandbox shares the host network namespace. Without proxy mode the sandbox uses bwrap's `--share-net`, so a tool listener inside the sandbox is the same kernel socket as the "host" bind the forwarder would attempt — producing a spurious `bind: address already in use` error for every detected port. Auto-detect now inspects the sandbox netns inode and skips forwarding (with a one-line explanatory message) when it matches the host; the sandbox ports are already directly reachable on `127.0.0.1`. For the rare case where auto-forward runs in a properly isolated netns but the host happens to already have that port in use, the forwarder falls back to an ephemeral host port and logs the mapping instead of silently dropping the service.

## [v0.10.0] - 2026-04-10

### Added

- `kitty` tool forwards the Kitty remote-control socket into the sandbox so `kitten @` commands inside the sandbox can drive the host terminal.

## [v0.9.3] - 2026-04-08

- `~/.local/bin` and `~/.local/share/claude` are now read-only bind mounts instead of persistent writable overlays. Under the split-mode default introduced in v0.8.0 these host-managed tool-install directories were being treated as `CategoryData`, which let in-sandbox tool self-updaters (e.g. Claude Code's own updater) write partial/empty files into the per-project overlay upper-dir. Those writes shadowed the real host binaries in every subsequent session, causing failures like `fish: '/home/$USER/.local/bin/claude' exists but is not an executable file` (exit 126).

## [v0.9.2] - 2026-04-08

### Fixed

- HTTP proxy no longer intercepts the body of HEAD requests. The previous behavior broke `Content-Length` handling and caused errors for some clients (e.g. Helm pulling OCI charts).

## [v0.9.1] - 2026-04-07

### Fixed

- Sandbox removal now `chmod`s files recursively before deletion. Go populates its build cache with `0500` files, which previously caused sandbox cleanup to fail.

## [v0.9.0] - 2026-04-07

### Added

- `devsandbox scratchpad [name] [command...]` subcommand for running sandboxes in managed, clean working directories under `~/.local/share/devsandbox-scratchpads/`. State persists between runs. Name defaults to `default`.
- `devsandbox scratchpad list` and `devsandbox scratchpad list --json` list scratchpads with size and state info.
- `devsandbox scratchpad rm <name>` (with `--all`, `--keep-state`, `--force`) removes scratchpads and their sandbox state.

### Fixed

- Git tool now strips sensitive fields from `.git/config` in place instead of replacing the file wholesale. The previous full replacement caused the git CLI to refuse to operate even for read-only commands inside the sandbox.

## [v0.8.2] - 2026-04-06

### Fixed

- Claude tool stores project knowledge under the `data` section so chat history persists between sandbox runs.

## [v0.8.1] - 2026-04-06

### Added

- macOS support for the devsandbox shim via a platform-specific copy-on-start overlay implementation, split from the Linux path.
- `jq` is now included in the default Docker image.

### Changed

- Debian base image bumped in the Docker image.
- mise-managed tool dependencies bumped.
- Docker and lint CI workflows limit concurrency to avoid redundant runs.

### Fixed

- Restored shim source files that were missing from the v0.8.0 release and added CI coverage so the shim is built and verified on every run.

## [v0.8.0] - 2026-04-05

### Breaking Changes

- **`[overlay] enabled` removed** — replaced by `[overlay] default` which accepts:
  `split` (default), `overlay`, `tmpoverlay`, `readonly`, `readwrite`.
- **`[tools.mise] writable` and `persistent` removed** — use `[tools.mise] mount_mode` instead.
  Mise no longer has tool-specific overlay configuration; use the unified `mount_mode` system.
- **Default mount behavior changed** — tool mounts now default to `split` overlay policy
  (configs → tmpoverlay, caches/data/state → persistent overlay) instead of read-only
  bind mounts. This prevents supply chain attacks from poisoning host tool configurations
  through sandboxed package managers.

### Migration Guide

| Before | After |
|--------|-------|
| `[overlay] enabled = true` | `[overlay] default = "split"` (or omit — it's the default) |
| `[overlay] enabled = false` | `[overlay] default = "readonly"` |
| `[tools.mise] writable = true, persistent = true` | `[tools.mise] mount_mode = "overlay"` |
| `[tools.mise] writable = true, persistent = false` | `[tools.mise] mount_mode = "tmpoverlay"` |
| (no equivalent) | `[tools.git] mount_mode = "readwrite"` |

### Added

- **Binding categories** — tools now classify each mount as `config`, `cache`, `data`,
  `state`, or `runtime`, enabling differentiated overlay policies.
- **`[overlay] default`** — global mount mode for all tool bindings with five modes:
  `split`, `overlay`, `tmpoverlay`, `readonly`, `readwrite`.
- **Per-tool `mount_mode`** — override the global default for specific tools
  (e.g., `[tools.git] mount_mode = "readwrite"`). Accepts `disabled` to prevent
  a tool's config from being mounted entirely.

### Changed

- Tool bindings no longer hardcode `ReadOnly` or `Type` — the builder resolves these
  based on the mount mode policy chain (per-tool > global > split).
- Claude, Copilot, Codex, and OpenCode tools previously mounted configs read-write;
  they now follow the global mount mode (default: tmpoverlay for configs).
