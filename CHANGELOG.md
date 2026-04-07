# Changelog

All notable changes to this project will be documented in this file.

## Unreleased

### Added

- `devsandbox scratchpad [name] [command...]` subcommand for running sandboxes in managed, clean working directories under `~/.local/share/devsandbox-scratchpads/`. State persists between runs. Name defaults to `default`.
- `devsandbox scratchpad list` and `devsandbox scratchpad list --json` list scratchpads with size and state info.
- `devsandbox scratchpad rm <name>` (with `--all`, `--keep-state`, `--force`) removes scratchpads and their sandbox state.

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
