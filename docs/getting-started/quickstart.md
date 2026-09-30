# Quick start

Five minutes from install to first sandboxed AI run.

## 1. Install

The recommended way is [mise](https://mise.jdx.dev/):

```bash
mise use -g github:zekker6/devsandbox
```

mise is optional. Without it, download a release binary instead. [Installation](install.md) covers both, plus the platform requirements.

## 2. Sandbox an AI agent

```bash
# cd into your project - this directory becomes the sandbox root
cd ~/projects/my-app

# Run Claude Code inside the sandbox
devsandbox claude --dangerously-skip-permissions
```

`devsandbox` wraps any command. Everything after the binary name is passed through to the sandboxed program. The flag `--dangerously-skip-permissions` is a Claude Code flag that disables its permission prompts - safe to enable here because devsandbox provides the actual security boundary.

## 3. Verify what's protected

```bash
devsandbox --info
```

This prints the sandbox configuration: which directories are mounted read-only, which are blocked, what network mode is active.

## 4. Sandbox agents by default (optional)

Typing `devsandbox` first is easy to forget. Shell wrappers make `claude` run as `devsandbox claude` in every new shell. They cover `claude`, `pi`, `codex`, `opencode` and `copilot`, whichever are installed. Add the line for your shell to its startup file:

```bash
# fish: ~/.config/fish/config.fish
if test -z "$DEVSANDBOX"; devsandbox shell-wrappers activate fish | source; end

# bash: ~/.bashrc
if [ -z "${DEVSANDBOX:-}" ]; then eval "$(devsandbox shell-wrappers activate bash)"; fi

# zsh: ~/.zshrc
if [ -z "${DEVSANDBOX:-}" ]; then eval "$(devsandbox shell-wrappers activate zsh)"; fi
```

Open a new shell, `cd` into a project and run `claude` as usual. `claude-no-ds` or `command claude` runs the real binary unsandboxed. `--agents claude,codex` wraps only the agents you name, and `[shell_wrappers]` in the config wraps other commands such as `npm`. See [Shell wrappers](../tools.md#shell-wrappers-run-agents-sandboxed-by-default).

## What just happened

- `~/projects/my-app` (your CWD) became the project root with full read/write access.
- `~/.ssh`, `~/.aws`, `~/.azure`, `~/.gcloud` → not mounted, invisible.
- `.env` and `.env.*` files → masked with `/dev/null`, scanned up to 3 directory levels below the project root (`node_modules`, `.git`, `vendor`, `.venv` are skipped).
- `.git/` → mounted read-only (no commits, no credentials).
- mise-managed tools, your shell config, editor setup → mounted read-only so they work inside.
- Network → full access by default; add `--proxy` to log HTTP requests (enforcement strength varies by backend - see [per-backend behavior](../proxy.md#backend-specific-behavior)).

## Other tools, same pattern

devsandbox wraps anything with a CLI:

```bash
devsandbox aider
devsandbox cursor
devsandbox npm install
devsandbox go test ./...
```

## Next step

Continue to [First run](first-run.md) for a guided walkthrough that shows what the sandbox actually does.
