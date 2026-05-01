# Quick start

Five minutes from install to first sandboxed AI run.

## 1. Install

```bash
mise use -g github:zekker6/devsandbox
```

(See [Installation](install.md) for prerequisites and alternatives.)

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

## What just happened

- `~/projects/my-app` (your CWD) became the project root with full read/write access.
- `~/.ssh`, `~/.aws`, `~/.azure`, `~/.gcloud` → not mounted, invisible.
- `.env` and `.env.*` files → masked with `/dev/null`.
- `.git/` → mounted read-only (no commits, no credentials).
- mise-managed tools, your shell config, editor setup → mounted read-only so they work inside.
- Network → full access by default; add `--proxy` to log every HTTP request.

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
