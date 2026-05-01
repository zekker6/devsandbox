# devsandbox

Your real dev environment, sandboxed per project. Run Claude Code, Copilot, aider, and other AI coding agents safely - without giving up your shell, your `mise`-managed tools, or your editor configs.

## The DX gap

Docker and VMs isolate by *replacing* your dev environment. Fresh shell with no aliases. No `mise`, no editor, no prompt. Reinstall every tool inside the container, fight file watchers across the VM boundary, and wait 10-30 seconds for cold starts. So most people skip isolation entirely and let agents run on the host - with full access to `~/.ssh`, cloud credentials, `.env` secrets, and every other project on disk.

devsandbox closes that gap. It wraps any command in a sandbox scoped to your current working directory and brings the rest of your real environment with it:

- **Your shell, aliases, history.** Detected from `$SHELL` and bound read-only.
- **`mise`-managed tools.** Go, Node, Python, kubectl, whatever - already there, no `mise install` twice.
- **Editor + LSP, prompt, multiplexer.** nvim, helix, starship, tmux, fish, zsh - all preserved.
- **Sub-second startup.** bubblewrap on Linux shares the host kernel; native file watching works.

The isolation boundary is still real. Inside the sandbox, the agent sees the project directory and your tools - and nothing else. SSH keys, cloud credentials (`~/.aws`, `~/.azure`, `~/.gcloud`), `.env` files, sibling projects, and parent directories are invisible. `.git` is read-only by default. An optional MITM proxy logs every HTTP/HTTPS request for inspection.

## Prerequisites

devsandbox requires [mise](https://mise.jdx.dev/) for tool version management. Install it before proceeding.

**Linux:**

```bash
curl https://mise.jdx.dev/install.sh | sh
```

After installing, activate mise in your shell ([setup guide](https://mise.jdx.dev/getting-started.html)):

```bash
# bash
echo 'eval "$(~/.local/bin/mise activate bash)"' >> ~/.bashrc

# zsh
echo 'eval "$(~/.local/bin/mise activate zsh)"' >> ~/.zshrc

# fish
echo '~/.local/bin/mise activate fish | source' >> ~/.config/fish/config.fish
```

Additionally, your kernel must support unprivileged user namespaces. Verify with:

```bash
unshare --user true
# Should succeed silently. If it fails, see Limitations.
```

**macOS:**

```bash
brew install mise
```

A Docker runtime is also required (ensure it is running before using devsandbox):
- [OrbStack](https://orbstack.dev/) - recommended for Apple Silicon (fastest startup, lowest resource usage)
- [Docker Desktop](https://docs.docker.com/desktop/install/mac-install/) - most widely tested
- [Colima](https://github.com/abiosoft/colima) - free and open-source

## Quickstart

**Install:**

```bash
mise use -g github:zekker6/devsandbox
```

> Homebrew is not currently available. For direct binary download, see [Installation Details](#installation-details).

**Sandbox your AI agent:**

```bash
# 1. cd into your project
cd ~/projects/my-app

# 2. Run Claude Code in the sandbox (scoped to ~/projects/my-app)
devsandbox claude --dangerously-skip-permissions

# 3. Verify what's protected
devsandbox --info
```

devsandbox sandboxes the current working directory - `cd` into your project first, then run `devsandbox`. Everything after `devsandbox` is passed to the sandboxed command. `--dangerously-skip-permissions` is a Claude Code flag that skips permission prompts - safe inside the sandbox because devsandbox provides the security boundary.

**Works with:** Claude Code, GitHub Copilot, aider, Cursor, Continue, Cline, OpenCode, and any CLI-based development tool.

That's it. No config files needed. On Linux, devsandbox includes embedded binaries - zero dependencies. On macOS, a Docker runtime is required (see [Installation Details](#installation-details)).

Run `devsandbox doctor` to verify your setup.

> **macOS:** devsandbox runs your code inside a lightweight Linux container (Debian slim) via Docker. Your project files are mounted into the container, so edits sync bidirectionally. Ensure your Docker runtime is running before using devsandbox. The first start downloads a base Docker image (~200MB); subsequent starts reuse Docker layer caching and complete in 1-2 seconds.

## Scratchpads

Sometimes you want to run a sandboxed tool in an empty working directory - try a library, give an AI agent a clean slate, or quickly build something unrelated to your current project. `devsandbox scratchpad` gives you a managed, persistent scratch workspace without having to mkdir under `/tmp` by hand.

```bash
# Interactive shell in the default scratchpad
devsandbox scratchpad

# Named scratchpad "experiments", interactive shell
devsandbox scratchpad experiments

# Named scratchpad with a command
devsandbox scratchpad experiments claude --dangerously-skip-permissions

# Command in the default scratchpad (name it explicitly)
devsandbox scratchpad default bun init

# Ephemeral run - wipe state on exit
devsandbox scratchpad --rm experiments bun init
```

Scratchpad working directories live at `~/.local/share/devsandbox-scratchpads/scratchpad-<name>/` and persist between runs. The sandbox state (home overlay, tool caches) lives in the usual `~/.local/share/devsandbox/` tree alongside your project sandboxes.

Local `.devsandbox.toml` files are never loaded inside a scratchpad - the baseline is always global config only, so scratchpads stay uncontaminated by project-specific overrides.

### Managing scratchpads

```bash
# List all scratchpads
devsandbox scratchpad list
devsandbox scratchpad list --json

# Remove one (cleans working dir + sandbox state)
devsandbox scratchpad rm experiments

# Wipe the working dir but keep the sandbox home overlay
devsandbox scratchpad rm experiments --keep-state

# Remove everything
devsandbox scratchpad rm --all --force
```

## Security baseline

DX is the headline; isolation is the floor. The defaults are tuned so an agent inside a fresh sandbox can do its job and nothing more - no flags required.

**CAN:** Read/write your project files, use your `mise`-managed tools, inherit your shell and editor configs, run build commands, install dependencies, make API calls (logged in proxy mode).

**CANNOT:** Read SSH keys, access cloud credentials (AWS/Azure/GCloud), read `.env` secrets, see other projects, push to git (by default), or modify your system.

### Resource access defaults

| Resource | Default Access |
|---|---|
| Project directory | Read/Write |
| `.env` / `.env.*` files | Hidden (masked with `/dev/null`) |
| `~/.ssh` | Not mounted |
| `~/.aws`, `~/.azure`, `~/.gcloud` | Not mounted |
| `~/.gitconfig` | Sanitized (user.name/email only) |
| `.git` directory | Read-only (no commits, no credentials) |
| mise-managed tools | Read-only |
| Network (default) | Full access |
| Network (proxy mode) | Isolated and logged |
| Outgoing secrets (proxy + redaction) | Blocked or redacted |

Everything is configurable. See [Configuration](docs/configuration.md) for details.

## Features

- **Your real dev env, inside the sandbox** - mise-managed tools, shell configs, editor setups (nvim, starship, tmux) auto-detected and bound, no Dockerfile required
- **Sub-second startup** - [bubblewrap](https://github.com/containers/bubblewrap) namespaces on Linux share the host kernel; native file watching works. Docker layer caching keeps macOS restarts at 1-2s
- **Per-project isolation** - each project gets its own sandbox home, caches, and logs
- **Zero-config security baseline** - SSH keys, cloud credentials, `.env` files, and git credentials blocked by default
- **MITM proxy** - optional traffic inspection with log viewing, filtering, and export
- **HTTP filtering** - whitelist/blacklist domains, or interactively approve requests one at a time
- **Content redaction** - scan outgoing requests for secrets, block or replace them before they leave your machine
- **Git modes** - readonly (default), readwrite (with SSH/GPG), or disabled
- **Desktop notifications** - sandboxed apps can send notifications to the host via XDG Desktop Portal (Linux)

## How It Works

**Linux:** Uses [bubblewrap](https://github.com/containers/bubblewrap) to create namespace-based isolation. No root privileges, no Docker, no system packages required - bwrap and pasta binaries are embedded. Startup is sub-second.

**macOS:** Uses Docker containers with volume mounts that mirror the bwrap behavior. Named volumes provide near-native filesystem performance. Containers are cached for 1-2 second restarts.

Both backends automatically detect your shell, tools, and editor configs and make them available read-only inside the sandbox.

## Usage Examples

```bash
# Interactive sandbox shell
devsandbox

# Run any command in the sandbox
devsandbox npm install
devsandbox go test ./...
devsandbox cargo build

# AI assistant with traffic monitoring
devsandbox --proxy claude --dangerously-skip-permissions

# View what the AI accessed
devsandbox logs proxy --last 50

# Follow traffic in real-time (in a second terminal)
devsandbox logs proxy -f

# Whitelist-only network access
devsandbox --proxy --filter-default=block \
  --allow-domain="*.github.com" \
  --allow-domain="api.anthropic.com"

# Choose isolation backend explicitly
devsandbox --isolation=docker npm install

# Ephemeral sandbox (removed after exit)
devsandbox --rm
```

## Git Integration

By default, `.git` is mounted read-only - you can view history, diff, and status, but commits are blocked and no credentials are exposed.

| Mode | `.git` | Commits | Credentials |
|---|---|---|---|
| `readonly` | read-only | blocked | none **(default)** |
| `readwrite` | read-write | allowed | SSH, GPG, credentials |
| `disabled` | read-write | allowed | none |

```toml
# ~/.config/devsandbox/config.toml
[tools.git]
mode = "readwrite"  # for trusted projects that need push/sign
```

### Worktree-aware mode

`devsandbox --worktree` creates (or reuses) a git worktree and enters the sandbox rooted there. Agent edits land on a dedicated branch without touching your main checkout.

| Flag | Behavior |
|---|---|
| `--worktree` | Auto-generate `devsandbox/<session-or-timestamp>` off `HEAD`. |
| `--worktree=<branch>` | Reuse the branch if it exists; otherwise create it off `--worktree-base`. |
| `--worktree-base=<ref>` | Base ref when creating a new branch. Defaults to `HEAD`. Ignored if the branch already exists. |

Interaction with `--git-mode`:

| Combination | Effect |
|---|---|
| `--worktree` + `--git-mode=readonly` (default) | `git status`/`log`/`diff` work; commits fail. |
| `--worktree` + `--git-mode=readwrite` | Commits land on the worktree's branch only. Main checkout untouched. |
| `--worktree` + `--git-mode=disabled` | Rejected at flag parse time. |
| `--worktree` + `--rm` | Worktree removed on exit (`git worktree remove --force` + `prune`). |
| `--worktree` outside a git repo | Rejected before sandbox spin-up. |

Worktrees live under the per-project sandbox state dir:

```
~/.local/share/devsandbox/<project-slug>/worktrees/<sanitized-branch>/
```

The slug is derived from the main repo root so worktrees of the same repo share sandbox state (overlays, logs). Branch names with slashes are stored as dashes in the filesystem leaf; the git branch name is preserved verbatim.

Known limitations:

- Submodule init inside a readonly sandbox fails - not worked around.
- If git already has the canonical path registered to a different branch, invocation fails with git's own error plus a hint; run `git worktree list` to investigate.
- A stale directory at the canonical path (git has no record) causes `devsandbox` to refuse to clobber - remove it manually.

## Proxy Mode - Monitor Your AI Agent's Network Activity

Route all HTTP/HTTPS traffic through a local MITM proxy. See every API call your AI agent makes in real-time, block suspicious domains, or interactively approve each request.

```bash
# Enable proxy
devsandbox --proxy

# View logs
devsandbox logs proxy --stats        # Summary statistics
devsandbox logs proxy --errors       # Failed requests only
devsandbox logs proxy --json         # JSON export for scripting

# Interactive request approval
devsandbox --proxy --filter-default=ask
# Then in another terminal:
devsandbox proxy monitor
```

On Linux, proxy mode uses [pasta](https://passt.top/) for network namespace isolation (embedded, no install needed). On macOS, it uses per-session Docker networks.

See [Proxy Mode docs](docs/proxy.md) for filtering rules, log formats, and remote logging setup. Audit-grade structured logging - per-session metadata, lifecycle events, and security events forwarded to syslog/OTLP - is documented under [Audit Logging](docs/configuration.md#audit-logging).

## Installation Details

**Linux:**

Requirements:
- Linux kernel with unprivileged user namespaces enabled (verify: `unshare --user true` should succeed silently)
- No system packages required (bwrap and pasta binaries are embedded)

```bash
# Option 1: mise
mise use -g github:zekker6/devsandbox

# Option 2: Download binary
curl -L https://github.com/zekker6/devsandbox/releases/latest/download/devsandbox_Linux_x86_64.tar.gz | tar xz
sudo mv devsandbox /usr/local/bin/
```

To use system-installed binaries instead of embedded ones, set `use_embedded = false` in [configuration](docs/configuration.md).

Optional system packages (fallback if embedded extraction fails). Note: the `passt` package provides the `pasta` binary used for network namespace isolation.

```bash
# Arch Linux
sudo pacman -S bubblewrap passt

# Debian/Ubuntu
sudo apt install bubblewrap passt

# Fedora
sudo dnf install bubblewrap passt
```

**macOS:** Requires a Docker runtime - see [Prerequisites](#prerequisites) for options.

**Build from source:**

```bash
# Requires: Go 1.22+ and Task (https://taskfile.dev/)
# Or use mise to install dependencies: mise install
task build
```

### Documentation site (development)

The project ships a small documentation site (Zensical + a hand-written landing page) that deploys to GitHub Pages from `main`. To work on it locally:

```bash
# Live-reload dev servers - landing on :8001, docs on :8000 (loopback only)
task site:dev

# Production-style assembly into ./public/
task site:build

# Clean build artifacts
task site:clean
```

`zensical.toml` at the repo root configures the docs site; landing source lives under `site/landing/`. The CI workflow `.github/workflows/pages.yml` runs the same `task site:build` and uploads `./public/` to GitHub Pages.

One-time setup (must be done in the GitHub UI, cannot be done from the workflow): **Repository → Settings → Pages → Source = "GitHub Actions"**. After the first successful workflow run, the site is reachable at `https://zekker6.github.io/devsandbox/`.

## Quick Reference

```bash
devsandbox                          # Interactive sandbox shell
devsandbox <command>                # Run command in sandbox
devsandbox --proxy                  # Enable proxy mode
devsandbox --rm                     # Ephemeral sandbox
devsandbox --info                   # Show sandbox configuration
devsandbox doctor                   # Check installation
devsandbox config init              # Generate config file
devsandbox sandboxes list           # List all sandboxes
devsandbox sandboxes prune          # Remove orphaned sandboxes
devsandbox logs proxy               # View proxy logs
devsandbox logs proxy -f            # Follow logs in real-time
devsandbox tools list               # List available tools
devsandbox tools check              # Verify tool setup
devsandbox image build              # Build Docker image (macOS)
```

## Documentation

| Page | Contents |
|---|---|
| [Sandboxing](docs/sandboxing.md) | Isolation backends, security model, filesystem layout, overlay mounts, custom mounts, Docker backend details |
| [Proxy Mode](docs/proxy.md) | Traffic inspection, log viewing/filtering/export, HTTP filtering, ask mode, content redaction, credential injection, remote logging |
| [Tools](docs/tools.md) | mise integration, shell/editor/prompt setup, AI assistant configs, Git modes, Docker socket proxy |
| [Configuration](docs/configuration.md) | Config file reference, per-project configs, conditional includes, port forwarding, overlay settings |
| [Use Cases](docs/use-cases.md) | Shell aliases, autocompletion, development workflows, security monitoring scripts |

## Limitations

**Linux (bwrap):**
- Requires unprivileged user namespaces (see [Troubleshooting](docs/sandboxing.md#troubleshooting) for distro-specific guidance)
- SELinux or AppArmor may restrict namespace operations (see [Security Modules](docs/sandboxing.md#security-modules))
- MITM proxy may break tools with certificate pinning
- GUI applications are not supported (no display server forwarding), but desktop notifications work via XDG Portal

**macOS (Docker):**
- Requires a running Docker daemon
- Project directory access goes through macOS virtualization (VirtioFS/gRPC-FUSE), which may be slower for I/O-heavy operations. Sandbox-internal operations (npm install, Go builds) use named Docker volumes with near-native speed.
- File watching (hot reload) may require polling mode. See [File Watching Limitations](docs/sandboxing.md#file-watching-limitations) for workarounds.
- Network isolation uses HTTP_PROXY instead of pasta

**Both:**
- Docker socket access is read-only (no container creation/deletion) - see [Tools docs](docs/tools.md#docker)
- No nested Docker (cannot run Docker inside the sandbox)

## License

MIT
