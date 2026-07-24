# Tools

How development tools are made available inside the sandbox.

devsandbox makes development tools available inside the sandbox while maintaining security boundaries.

## Inspecting Tools

Use the `tools` command to see which tools are available and how they're configured.

### List Available Tools

```bash
# List tools detected on your system
devsandbox tools list

# Include unavailable tools
devsandbox tools list --all

# JSON output for scripting
devsandbox tools list --json
```

Example output:

```
┌───────────────┬───────────┬───────────────────────────────────────────────┐
│     NAME      │  STATUS   │                  DESCRIPTION                  │
├───────────────┼───────────┼───────────────────────────────────────────────┤
│ claude        │ available │ Claude Code AI assistant                      │
│ copilot       │ available │ GitHub Copilot integration                    │
│ git           │ available │ Git configuration (safe mode, no credentials) │
│ go            │ available │ Go language environment isolation             │
│ mise          │ available │ Tool version manager (node, python, go, etc.) │
│ nvim          │ available │ Neovim editor configuration                   │
│ oh-my-posh    │ missing   │ Oh My Posh prompt with sandbox indicator      │
│ oh-my-zsh     │ missing   │ Oh My Zsh framework with sandbox indicator    │
│ opencode      │ available │ OpenCode AI assistant                         │
│ pi            │ available │ Pi coding agent AI assistant                  │
│ portal        │ available │ XDG Desktop Portal (notifications)            │
│ powerlevel10k │ missing   │ Powerlevel10k zsh theme                       │
│ rtk           │ available │ rtk CLI proxy (token-optimized output)         │
│ starship      │ available │ Starship prompt with sandbox indicator        │
│ tmux          │ missing   │ Tmux terminal multiplexer with sandbox indicator │
└───────────────┴───────────┴───────────────────────────────────────────────┘
```

Note: Tools show as "available" if their binary is found or config exists, "missing" otherwise.

### Tool Details

View bindings, environment variables, and shell init for a specific tool:

```bash
# Show details for a tool
devsandbox tools info mise

# Show details for all tools
devsandbox tools info --all

# JSON output
devsandbox tools info mise --json
```

Example output:

```
Tool: mise
Status: available
Description: Tool version manager (node, python, go, etc.)
Binary: /home/user/.local/bin/mise

Bindings:
  ~/.local/bin                        (read-only, optional)
  ~/.config/mise                      (read-only, optional)
  ~/.local/share/mise                 (read-only, optional)

Environment Variables: (none)

Shell Init:
  if command -v mise &>/dev/null; then eval "$(mise activate bash)"; fi
```

### Verify Tool Setup

Check tool availability and verify binding paths exist:

```bash
# Check all tools
devsandbox tools check

# Check specific tools
devsandbox tools check mise git claude

# JSON output
devsandbox tools check --json
```

Example output:

```
Checking tools...

✓ mise (/home/user/.local/bin/mise)
    ✓ ~/.local/bin
    ✓ ~/.config/mise
    ✓ ~/.local/share/mise
✓ git (/usr/bin/git)
    ○ ~/.local/share/devsandbox/<project>/home/.gitconfig.safe (optional, missing)
✓ claude (/home/user/.local/bin/claude)
    ✓ ~/.claude
    ✓ ~/.claude.json
✗ starship (not available)
    ! starship binary not found in PATH

Summary: 3/4 tools available
```

## Tool Management with mise

[mise](https://mise.jdx.dev/) is the recommended (but optional) tool manager. If installed, all mise-managed tools are
automatically available inside the sandbox.

### How It Works

By default, mise directories are bind-mounted read-only:

```
~/.config/mise      → Sandbox (read-only)
~/.local/share/mise → Sandbox (read-only)
~/.local/state/mise → Sandbox (read-only)
```

This means:

- All installed tool versions are available
- Tool configurations (`.mise.toml`) are respected
- New tools cannot be installed from inside the sandbox (by default)

On the container backends (`docker` and `krun`) the guest boots from a fresh image, but `MISE_DATA_DIR` points at the persistent sandbox home, so tools you install inside the sandbox (python, go, etc.) are installed once and reused on later runs rather than re-downloaded every launch. The image's pre-baked node is mirrored into that data dir on startup so it resolves immediately without a reinstall.

On Linux hosts the container backends also share the host's installed tools: `~/.local/share/mise/installs` is mounted read-only into the guest, and on startup each host-installed version is mirrored into the sandbox data dir as a real version directory of symlinks (and the mise shims regenerated), so your host toolchain resolves inside `docker`/`krun` sandboxes without reinstalling or network access. The guest also runs the **host's own `mise` binary** when `~/.local/bin/mise` is mounted and executes in the guest (falling back to the image's otherwise): mise versions can disagree about a tool's backend and on-disk layout, so binary parity is what keeps host-installed tools resolving exactly as they do on the host. A version you install inside the sandbox always takes precedence over the host copy. Three caveats:

- Host tools that were **compiled locally** against a newer glibc than the guest image ships may fail to run in the guest (upstream prebuilt tools - node, go, and most others - are unaffected). Fix: `mise uninstall <tool>@<version>` inside the sandbox (this only removes the seeded symlink, not the host install) and `mise install` to get a guest-local build.
- On macOS hosts nothing is shared: the guest is Linux, so host (darwin) binaries cannot run in it.
- Only the **installs directory itself** is shared, so a tool whose runtime lives outside it does not work in-guest. A `pipx:` tool backed by a uv-managed Python is the common case: the tool is seeded as a version dir, but its interpreter (under the uv data dir) is not mounted, so it fails to start. Reinstall such a tool inside the sandbox.

A host global config (`~/.config/mise/config.toml`) with `@latest` `npm:`/`go:`/`pipx:` tool specs is a hazard on an egress-locked sandbox: even when the tool itself is seeded from the host, mise refreshes the remote version list behind each `@latest` spec over the network, some backends' lookups (npm registry, python-build) never traverse the proxy and hang to their timeout, and mise re-resolves the toolset per listed row with no negative cache - a single `mise ls` can turn into hundreds of doomed lookups. devsandbox defends in three layers:

- The **krun + proxy guest runs mise offline** (`MISE_OFFLINE=1`): everything resolves instantly from installed/cached data - with host installs seeded, that is your full host toolchain. krun runs **no** online boot-time install of your project's tools: it is always ephemeral (`keep_container` does not apply to it), and the online `mise install` pass only runs on the persistent-container `docker` paths. Under krun the guest's startup work is purely local - seeding the baked and host installs into the data dir and regenerating shims, all with `MISE_OFFLINE=1`. A tool your `.mise.toml` pins that is neither seeded nor already in the sandbox home is therefore not fetched for you: install it inside the sandbox with an explicit `MISE_OFFLINE=0 mise install ...`.
- All **proxy-mode sandboxes bound remote lookups to 3s** (`MISE_FETCH_REMOTE_VERSIONS_TIMEOUT=3s`, was mise's 20s default; override via the sandbox env config).
- The **startup shim resolves nothing over the network** (`MISE_OFFLINE=1` for its own mise invocations).

To avoid the residual per-lookup warnings on non-krun proxy sandboxes entirely:

- Pin the versions in your global config, or prefer a per-project `.mise.toml` for the tools a sandboxed project actually needs.
- Set `ignore_global_config` for mise so the sandbox does not read your host global `config.toml` at all (your project `.mise.toml`, the baked node, and `~/.config/mise/settings.toml` still apply):

  ```toml
  [tools.mise]
  ignore_global_config = true
  ```

  This defaults to `false` (the host global config is respected). It applies to every backend (`bwrap`, `docker`, `krun`).

### Writable Mise (Overlay Mode)

Configure the overlay mount mode for mise to allow installing tools inside the sandbox. In `~/.config/devsandbox/config.toml`:

```toml
[tools.mise]
mount_mode = "overlay"    # Persist tool installations across sessions
# mount_mode = "tmpoverlay"  # Discard on exit (safer)
```

With overlay enabled:

- Sandbox can install new tool versions
- Host mise directories remain unchanged
- Changes are isolated to the sandbox

The global `[overlay] default` setting controls all tools. Per-tool `mount_mode` overrides the global default. See [Sandboxing: Overlay Filesystem](sandboxing.md#overlay-filesystem) for details on overlay modes and how layering works.

### Supported Tools

Any tool installable via mise works inside the sandbox:

| Category         | Examples                                |
|------------------|-----------------------------------------|
| Languages        | Node.js, Python, Go, Rust, Ruby, Java   |
| Package Managers | npm, pnpm, yarn, pip, cargo, uv, poetry |
| Build Tools      | make, cmake, ninja                      |
| Utilities        | jq, yq, gh, aws-cli                     |

### Installing Tools

Install tools on your host system (outside the sandbox):

```bash
# Install Node.js
mise install node@20

# Install Python
mise install python@3.12

# Install multiple tools
mise install node@20 python@3.12 go@1.22
```

Tools are then automatically available in all sandboxes.

## Shell Configuration

devsandbox detects your shell from `$SHELL` and loads appropriate configurations:

### Bash

```
~/.bashrc       → Sandbox (read-only)
~/.bash_profile → Sandbox (read-only)
~/.profile      → Sandbox (read-only)
```

### Zsh

```
~/.zshrc        → Sandbox (read-only)
~/.zshenv       → Sandbox (read-only)
~/.zprofile     → Sandbox (read-only)
```

### Fish

```
~/.config/fish/ → Sandbox (read-only)
```

**Note:** Shell configurations are read-only. Changes made inside the sandbox won't persist.

## Prompt Tools

devsandbox can display a sandbox indicator in your shell prompt. Several popular prompt tools are supported.

### Environment Variables

Inside the sandbox, these variables are always set:

- `DEVSANDBOX=1` - Indicates running inside a sandbox
- `DEVSANDBOX_PROJECT=<name>` - The project name

You can use these in custom prompt configurations.

### Starship

[Starship](https://starship.rs/) is automatically configured with a `[sandbox]` indicator:

```
~/.config/starship.toml → Modified with sandbox segment
```

### Powerlevel10k

[Powerlevel10k](https://github.com/romkatv/powerlevel10k) is configured with a custom `devsandbox` segment that shows
when `$DEVSANDBOX` is set.

### Oh My Zsh

[Oh My Zsh](https://ohmyz.sh/) gets a sandbox indicator prepended to the `PROMPT`.

### Oh My Posh

[Oh My Posh](https://ohmyposh.dev/) configurations are mounted. You can add a custom segment using the `$DEVSANDBOX`
environment variable.

### tmux

[tmux](https://github.com/tmux/tmux) is configured to show `[SANDBOX]` in the status bar with a red background when
running inside the sandbox.

## Editor Support

### Neovim

Neovim configuration is available inside the sandbox:

```
~/.config/nvim      → Sandbox (read-only)
~/.local/share/nvim → Sandbox (read-only)
~/.local/state/nvim → Sandbox (read-only)
```

This includes:

- Your init.lua/init.vim
- Installed plugins
- LSP configurations
- Color schemes

### Other Editors

For other editors, mount their config directories manually or use the sandbox home.

## AI Coding Assistants

AI coding assistants execute arbitrary code - installing packages, running builds, making network requests. Running them inside devsandbox ensures they can do their job without accessing your credentials, keys, or secrets.

### Shell wrappers - run agents sandboxed by default

Remembering to type `devsandbox` first is the weak point. `devsandbox agent-wrappers activate` prints shell functions that send supported agents through the sandbox automatically. Nothing is written to disk: you evaluate the output from your own startup file, the way `mise activate` works.

| Shell | Startup file | Line to add |
|-------|--------------|-------------|
| fish | `~/.config/fish/config.fish` | `if test -z "$DEVSANDBOX"; devsandbox agent-wrappers activate fish \| source; end` |
| bash | `~/.bashrc` | `if [ -z "${DEVSANDBOX:-}" ]; then eval "$(devsandbox agent-wrappers activate bash)"; fi` |
| zsh | `~/.zshrc` | `if [ -z "${DEVSANDBOX:-}" ]; then eval "$(devsandbox agent-wrappers activate zsh)"; fi` |

The `if ... $DEVSANDBOX ...` guard on each line is load-bearing, not boilerplate: these startup files are bind-mounted into the sandbox, where `devsandbox` need not exist, so an unguarded line would fail with command-not-found on every in-sandbox shell start. See **Two guards** below.

The shell argument defaults to the base name of `$SHELL`, so `devsandbox agent-wrappers activate` alone works for a quick look at what would be defined. Once the line is in place:

```bash
claude                # -> devsandbox claude, in the current directory
claude --resume ID    # arguments pass through untouched
claude-no-ds          # escape hatch: the real binary, unsandboxed
command claude        # escape hatch: the real binary, unsandboxed
```

The wrappable agents are `claude`, `pi`, `codex`, `opencode`, and `copilot` (the standalone GitHub Copilot CLI). Only the ones actually installed on your host are wrapped. Because the snippet is regenerated at every shell start, it cannot go stale: install a new agent, or upgrade devsandbox into a new directory, and the next shell picks it up with nothing to re-run. With none of them on the host the output is a comment saying so, which is still valid shell - your startup file keeps working.

**Two guards, and why the line carries one of its own.** The emitted snippet wraps every definition in a `DEVSANDBOX` test, so nothing is wrapped inside a sandbox and a wrapper cannot recurse. The line you paste repeats that test: startup files are mounted into the sandbox while devsandbox itself need not exist in there, so an unguarded line would fail with command-not-found on every in-sandbox shell start.

**Scope, stated exactly.** fish sources `config.fish` for non-interactive `fish -c` invocations too, so a fish script calling `claude` gets the wrapper. bash and zsh only source their rc file for interactive shells, so scripts there are unaffected.

Each wrapper calls `devsandbox run-agent <agent>`, which decides what to do: inside a sandbox it runs the real agent, and outside it re-enters devsandbox in the current directory.

**PATH resolution, exactly where it is safe.** The activation line resolves `devsandbox` through `PATH`, because it runs once per shell start and an absolute path there would break on every upgrade that moved the binary. The snippet that command emits bakes in the absolute path it resolved for itself, so no agent invocation goes through `PATH`: a project-local bin directory on `PATH` is writable by the sandbox, and resolving devsandbox through it would run that binary on the host. If the baked path disappears mid-session, the wrapper fails closed with `devsandbox: no executable at <path> - reinstall devsandbox, then start a new shell to refresh the wrappers` and exits 127 rather than falling back to a lookup.

The wrappers are a standalone feature, independent of herdr. herdr's native session restore builds on them, which means the line has to be in the startup file of the shell herdr opens panes with (`[terminal] default_shell`, falling back to `$SHELL`) - not only your login shell. See [Agent session capture and restore](#agent-session-capture-and-restore).

To undo everything: remove the line from your startup file.

### Claude Code

Claude Code is fully supported:

```bash
# Run Claude Code in sandbox
devsandbox claude

# With permissions disabled (recommended in sandbox)
devsandbox claude --dangerously-skip-permissions
```

Everything after `devsandbox` is passed to the sandboxed command. `--dangerously-skip-permissions` is a Claude Code flag that skips permission prompts - safe inside the sandbox because devsandbox provides the security boundary.

Configuration directories are mounted read-write to allow Claude to save settings:

```
~/.claude           → Sandbox (read-write)
~/.config/Claude    → Sandbox (read-write)
~/.claude.json      → Sandbox (read-write)
```

These directories are isolated to the sandbox home - not your real host directories. Claude's conversation state and settings persist across sandbox sessions for the same project but are not shared with your host.

Inside a herdr pane, a direct `devsandbox claude` launch lets Claude's herdr
integration report its native session to herdr through the filtered proxy, so
herdr can resume it (`claude --resume <id>`) after a restart. Reports are
confined to `~/.claude/projects` (honoring `CLAUDE_CONFIG_DIR`). See
[herdr Terminal Workspace](#herdr-terminal-workspace).

### aider

[aider](https://aider.chat/) works out of the box:

```bash
devsandbox aider

# With proxy monitoring
devsandbox --proxy aider
```

### OpenCode

[OpenCode](https://github.com/opencode-ai/opencode) is detected and supported:

```bash
devsandbox opencode
```

### Pi Coding Agent

[Pi](https://github.com/badlogic/pi-mono) is fully supported:

```bash
# Run pi in sandbox
devsandbox pi
```

Install via mise:

```bash
mise install npm:@mariozechner/pi-coding-agent
```

Configuration directories are mounted with credential protection:

```
~/.pi/agent           → Sandbox (protected: settings + auth credentials)
~/.pi/agent/sessions  → Sandbox (persistent: session history preserved)
```

The `~/.pi/agent` directory (containing `settings.json` and `auth.json`) is
mounted with tmpoverlay so API keys and settings are shielded from sandbox
code. Session history in `~/.pi/agent/sessions` uses a persistent overlay so
conversations survive across sandbox sessions for the same project.

Inside a herdr pane, a direct `devsandbox pi` launch lets Pi's herdr
integration report its agent state and native session to herdr through the
filtered proxy. Reports are confined to `~/.pi/agent/sessions` (honoring
`PI_CODING_AGENT_DIR`), so a report naming any other path - `auth.json`
included - is denied. See [herdr Terminal Workspace](#herdr-terminal-workspace).

### Codex CLI

[Codex](https://github.com/openai/codex) is fully supported:

```bash
devsandbox codex
```

Configuration directories are mounted with credential protection:

```
~/.codex           → Sandbox (protected: config.toml + auth credentials)
~/.codex/sessions  → Sandbox (persistent: recorded sessions preserved)
```

The Codex home is mounted with tmpoverlay so `auth.json` and `config.toml` are
shielded from sandbox code, while `~/.codex/sessions` uses a persistent overlay
so `codex resume <id>` can find a session recorded in an earlier sandbox run.
Both honor `CODEX_HOME`, whose host value is passed through so codex resolves
the same path inside.

Inside a herdr pane, a direct `devsandbox codex` launch lets Codex's herdr
integration report its native session to herdr through the filtered proxy, so
herdr can resume it (`codex resume <id>`) after a restart. See
[herdr Terminal Workspace](#herdr-terminal-workspace).

### GitHub Copilot

Two products share the name, and devsandbox mounts both:

- The **standalone Copilot CLI** (`npm install -g @github/copilot`, invoked as `copilot`) keeps its config, MCP servers, sessions and auth under `~/.copilot`. That directory is mounted read-write and **persistent**, so a sandboxed `copilot` runs authenticated and `copilot --resume` / `--continue` finds sessions from an earlier run. It is one of the agents the [shell wrappers](#shell-wrappers---run-agents-sandboxed-by-default) wrap.
- The older **`gh copilot` extension** uses `~/.config/github-copilot` and `~/.cache/github-copilot`, mounted as config and cache. It also works inside editors (Neovim, VS Code) running in the sandbox.

### Other AI Tools

Any CLI-based AI tool works in the sandbox. This includes Continue, Cline, and similar tools. If the tool runs as a CLI process, wrap it with `devsandbox`.

## rtk CLI Proxy

[rtk](https://github.com/rtk-ai/rtk) filters command output before it reaches an agent's context. It is usually installed as a
Claude Code hook, so it runs on almost every command the agent issues inside the sandbox and needs both its configuration and
its tracking database to be present.

rtk resolves its directories through XDG. devsandbox points `XDG_CONFIG_HOME` and `XDG_DATA_HOME` at `$HOME/.config` and
`$HOME/.local/share`, with `$HOME` being the host home path, so the host directories below are the same paths rtk resolves
inside the sandbox:

```
~/.config/rtk      → Sandbox (tmpoverlay - writes discarded)
~/.local/share/rtk → Sandbox (persistent overlay - writes kept per sandbox)
```

| Path | Contents |
|------|----------|
| `~/.config/rtk/config.toml` | Tracking, display, tee, telemetry and hook settings |
| `~/.config/rtk/filters.toml`, `~/.config/rtk/filters/*.toml` | Global output filters |
| `~/.local/share/rtk/history.db` | SQLite tracking database behind `rtk gain` |
| `~/.local/share/rtk/trusted_filters.json` | Trust store for project-local filters |
| `~/.local/share/rtk/tee/`, `hook-audit.log` | Tee'd command output and hook rewrite audit log |

Configuration is mounted with the default `tmpoverlay`, so your filters and `config.toml` are visible but `rtk config --create`
inside the sandbox cannot rewrite them. The data directory gets a persistent overlay: savings recorded in the sandbox accumulate
across runs of that sandbox, while the host's `history.db` is never modified.

A project's own `.rtk/filters.toml` lives in the project directory and is writable as usual. `rtk trust` marks it trusted inside
the sandbox only - the host trust store is untouched.

`rtk discover`, `rtk session` and `rtk learn` read Claude Code history from `~/.claude/projects`, which the `claude` tool already
mounts.

## Git

Git access is configurable via `[tools.git]` in `~/.config/devsandbox/config.toml`:

```toml
[tools.git]
mode = "readonly"  # "readonly" (default), "readwrite", or "disabled"
```

### Modes

#### readonly (default)

Maximum isolation - `.git` directory is mounted read-only:

- View history, diff, log, status
- Branch operations (local only)
- **No commits** (`.git` is read-only)
- **No SSH access**
- **No GPG signing**
- **No credential helpers**

This is the safest mode for running untrusted code that needs to read repository data.

#### readwrite

Full git access for trusted projects:

- All git operations including commits
- SSH keys (read-only access to `~/.ssh`)
- GPG signing (read-only access to `~/.gnupg`)
- Git credentials (read-only access to `~/.git-credentials`)
- SSH agent forwarding (`SSH_AUTH_SOCK`)

```toml
[tools.git]
mode = "readwrite"
```

**Security note:** SSH and GPG directories are mounted read-only to protect private keys.

#### disabled

No git configuration, but `.git` remains writable:

```toml
[tools.git]
mode = "disabled"
```

Git commands work but without user.name/email (commits require `--author`). Use this if you want to allow commits without exposing any credentials.

## Go

Go is fully supported with isolated caches:

```
GOPATH       → ~/.local/share/devsandbox/<project>/home/go
GOCACHE      → ~/.local/share/devsandbox/<project>/home/.cache/go-build
GOMODCACHE   → ~/.local/share/devsandbox/<project>/home/.cache/go-mod
GOTOOLCHAIN  → local (prevents auto-downloads)
```

### Module Cache

Each project has its own module cache. To share modules across projects, you can symlink the cache directories (outside
the sandbox).

## Node.js / npm

Works normally with mise-installed Node.js:

```bash
devsandbox npm install
devsandbox npm run build
devsandbox npx create-react-app my-app
```

### Global Packages

Global npm packages are installed to the sandbox home:

```
~/.local/share/devsandbox/<project>/home/.npm-global
```

## Python

Works normally with mise-installed Python:

```bash
devsandbox pip install -r requirements.txt
devsandbox python script.py
```

### Virtual Environments

Create venvs inside your project directory:

```bash
devsandbox python -m venv .venv
devsandbox source .venv/bin/activate
```

## Docker

Docker is supported via a socket proxy that provides **read-only access** to the host Docker daemon.

### Configuration

Enable Docker in your project's `.devsandbox.toml`:

```toml
[tools.docker]
enabled = true
```

Or in the global config `~/.config/devsandbox/config.toml`:

```toml
[tools.docker]
enabled = true
socket = "/run/docker.sock"  # Optional: custom socket path
```

### Allowed Operations

The Docker proxy allows:

| Operation Type | Allowed | Examples |
|----------------|---------|----------|
| Read operations | ✓ | `docker ps`, `docker images`, `docker inspect` |
| Container logs | ✓ | `docker logs <container>` |
| Container exec | ✓ | `docker exec -it <container> bash` |
| Container attach | ✓ | `docker attach <container>` |
| Create containers | ✗ | `docker run`, `docker create` |
| Delete containers | ✗ | `docker rm`, `docker kill` |
| Modify containers | ✗ | `docker stop`, `docker restart` |
| Build images | ✗ | `docker build` |
| Push images | ✗ | `docker push` |

This allows debugging and inspecting running containers without the ability to modify the Docker environment.

### Socket Auto-Detection

On **Linux**, the Docker socket defaults to `/run/docker.sock`.

On **macOS**, the socket location varies by Docker runtime. devsandbox probes these paths in order and uses the first one found:

| Priority | Path | Runtime |
|----------|------|---------|
| 1 | `~/.docker/run/docker.sock` | Docker Desktop |
| 2 | `/var/run/docker.sock` | OrbStack (symlink) |
| 3 | `~/.colima/default/docker.sock` | Colima |

To override auto-detection, set the socket path explicitly:

```toml
[tools.docker]
enabled = true
socket = "/path/to/docker.sock"
```

### How It Works

1. A Unix socket proxy is created at `$HOME/.run/<pid>/docker.sock` inside the sandbox, where `<pid>` is the devsandbox process that owns the session. Sandbox home is shared by every session for the project, so the socket is kept per-session to stop a second session from unlinking a live one's socket.
2. The `DOCKER_HOST` environment variable is set to point to this socket
3. All requests are filtered before being forwarded to the host Docker socket
4. Write operations are blocked with an HTTP 403 error

### Limitations

- **Only Unix socket access is supported** - TCP connections to Docker daemons are not proxied
- Exec/attach sessions allow interactive terminal access to existing containers

### Error Logging

Proxy errors are logged to `~/.local/share/devsandbox/<project>/logs/internal/tools-errors.log`

### Checking Docker Status

```bash
devsandbox tools check docker
```

Example output:

```
✓ docker
    ✓ /run/docker.sock
    mode: enabled (read-only + exec)
```

## Kitty Terminal

When running inside [kitty](https://sw.kovidgoyal.net/kitty/) with remote control enabled, devsandbox runs a **filtering proxy** for the kitty remote-control socket. The host kitty socket is **not** bind-mounted into the sandbox; only the proxy socket is. Sandboxed code can perform only the kitty operations declared by enabled tools, scoped to windows the sandbox itself opened (ownership tracking).

### Prerequisites

Add to your `~/.config/kitty/kitty.conf`:

```
allow_remote_control socket-only
listen_on unix:/tmp/kitty-{kitty_pid}
```

Restart kitty after changing the config.

### Activation

The proxy starts when all of the following hold:

1. `KITTY_LISTEN_ON` is set on the host.
2. The `kitty` binary is on PATH.
3. At least one enabled tool declares a kitty capability (or `mode = "enforce"` is set).

If no tool declares a capability and `mode = "auto"` (the default), the proxy stays inactive - zero attack surface when nothing needs it. `revdiff` is the built-in consumer: if the `revdiff` binary is on PATH, the proxy activates automatically.

### Configuration

```toml
[tools.kitty]
mode = "auto"                          # "auto" (default), "disabled", "enforce"
extra_capabilities = ["list_owned"]    # additive only; launch_* entries are rejected
```

| Mode | Behavior |
|---|---|
| `auto` | Proxy starts iff at least one enabled tool declares a capability. |
| `enforce` | Proxy always starts; with no capabilities declared, every request is denied (useful to verify no tool silently uses kitty). |
| `disabled` | Proxy never starts; `KITTY_LISTEN_ON` is not exposed inside the sandbox. |

### Capabilities

| Capability | Allows |
|---|---|
| `launch_overlay` | `kitty @ launch --type=overlay` |
| `launch_window` | `kitty @ launch --type=window` |
| `launch_tab` | `kitty @ launch --type=tab` |
| `launch_os_window` | `kitty @ launch --type=os-window` |
| `close_owned` | `close-window` scoped to windows the sandbox opened |
| `wait_owned` | `wait` scoped to windows the sandbox opened |
| `focus_owned` | `focus-window` scoped to windows the sandbox opened |
| `send_text_owned` | `send-text` scoped to windows the sandbox opened |
| `get_text_owned` | `get-text` scoped to windows the sandbox opened |
| `set_title_owned` | `set-window-title` scoped to windows the sandbox opened |
| `list_owned` | `ls` - response is filtered to owned windows only |

`launch_*` capabilities equal arbitrary host code execution and must be paired with command patterns declared by the tool that requests them. Shell metacharacters (`;`, `&`, `|`, `` ` ``, `$()`, `<`, `>`, etc.) in `sh -c` payloads are rejected outright.

Command patterns also pin the program to its **resolved absolute path** (`exec.LookPath` plus symlink resolution), not just its basename. Basename matching accepted any path ending in the allowed program name, including one inside a directory the sandbox can write - and the revdiff IPC directory is a write-through bind shared with the host at an identical path, so sandboxed code could plant its own `revdiff` there and have kitty run it on the host. If the binary cannot be resolved, every launch is denied rather than falling back.

### What Gets Mounted

| Resource | Mode | Purpose |
|----------|------|---------|
| Proxy socket (`$HOME/.run/<pid>/kitty.sock`) | read-write (proxy is local to the sandbox home) | kitty remote-control via the filtering proxy |
| `kitty` binary | read-only | CLI for `kitty @ launch`, `kitty @ ls`, etc. |

The host's real kitty socket is **not** bind-mounted into the sandbox.

### Environment Variables

- `KITTY_LISTEN_ON` - rewritten to `unix:$HOME/.run/<pid>/kitty.sock` inside the sandbox, where `<pid>` is the devsandbox process owning the session. Host value is never exposed.
- `KITTY_WINDOW_ID`, `KITTY_PID` - passed through from host (read-only signals about the host pane).

### Limitations

- Async / streaming kitty commands (`get-text --watcher`, async kittens) are denied in this MVP.
- `remote_control_password` in `kitty.conf` is unsupported - use `allow_remote_control = socket-only` instead.

Run `devsandbox tools check kitty` to confirm the tool is detected and see the active mode.

## herdr Terminal Workspace

When running inside a [herdr](https://herdr.dev) session, devsandbox runs a **filtering proxy** for the herdr control socket. The host socket is **not** bind-mounted into the sandbox; only the proxy socket is. Sandboxed code can perform only the herdr operations declared by enabled tools, scoped to tabs and panes the sandbox itself created.

This is the same model as kitty, and the opposite of zellij: herdr speaks newline-delimited JSON with named methods, which makes real per-method filtering practical.

### Why filtering is necessary

herdr exposes 86 methods. Handing the raw socket to sandboxed code would grant `pane.read` (read any pane's contents, including other terminals), `pane.send_text` and `agent.send` (type into any pane), `worktree.*` (mutate git state), `plugin.*` (load code), and `server.stop`. Only the methods below are reachable through the proxy; everything else is denied.

### Activation

The proxy starts when all of:

1. `HERDR_ENV=1` on the host (that is, devsandbox is running inside a herdr session).
2. The `herdr` binary is on PATH.
3. The host control socket exists (`$HERDR_SOCKET_PATH`, else `~/.config/herdr/herdr.sock`).
4. At least one enabled tool declares a herdr capability, or `mode = "enforce"` is set.

**Behavior change.** Launching a supported agent directly - `devsandbox claude`, `devsandbox pi`, `devsandbox codex` - now satisfies condition 4 on its own, because that launch enables `agent_reporting`. Such a command inside a herdr pane previously started no proxy at all; it now starts one, binds the proxy socket into the sandbox, and prints the usual `herdr proxy active` notice. Since `auto` mode can now reach the socket check for an ordinary agent launch, an unreachable or stale host socket is a warning there rather than a launch failure - the sandbox starts without a proxy. Under `mode = "enforce"`, where the proxy was asked for explicitly, it is still a hard error.

### Configuration

```toml
[tools.herdr]
mode = "auto"
```

| Mode | Behavior |
|------|----------|
| `auto` (default) | Proxy starts only when some enabled tool declares a capability. |
| `disabled` | Proxy never starts and `HERDR_SOCKET_PATH` is not exported, so the sandboxed CLI cannot reach herdr at all. |
| `enforce` | Proxy always starts; with no capabilities declared, every request is denied (useful to verify no tool silently uses herdr). |

There is deliberately no `extra_capabilities` setting as kitty has: with this few capabilities it would configure nothing, and each grants host-visible effects.

### Capabilities

| Capability | Permits |
|------------|---------|
| `launch_overlay` | `tab.create`, then `pane.send_input` and `tab.close` **scoped to the tab and pane that call returned** |
| `notify` | `notification.show` |
| `agent_reporting` | `pane.report_agent_session`, `pane.report_agent`, `pane.release_agent`, **all confined to this sandbox's own pane and the agent devsandbox launched** |

`ping` is permitted unconditionally, independent of declared capabilities. It is a liveness handshake that observes and mutates nothing, returning only the server's version, protocol number, and feature flags - strictly less than a successful `connect(2)` to the socket already reveals. Without it `herdr status` fails, which is a poor signal for no safety gain. One consequence: under `mode = "enforce"` the proxy answers `ping` and denies everything else.

Ownership is taken from the server's `tab.create` response, never from anything the client claims. `pane.send_input` is generic keystroke injection, so it is additionally restricted to exactly one command plus `Enter`, and the command must match the requesting tool's declared pattern.

### Launch scripts are validated and relocated

Unlike kitty, where the launcher passes an inline command, herdr's `pane run` is invoked as `sh <path>` - the payload is a *file*. That file lives in a directory the sandbox can write, yet it executes on the host, outside the sandbox.

The proxy therefore reads the script **once**, validates the bytes against the declared pattern, writes them to a host-only directory (`~/.cache/devsandbox/herdr-scripts/<session>`, mode `0700`, files `0500`, never bind-mounted into the sandbox), and rewrites the request to name that copy. Validation and execution operate on the same immutable bytes, so there is no window in which the sandbox can swap the script after it passes.

One consequence is user-visible: the command shown in the herdr pane names the relocated copy, not the original path. Relocated scripts are removed when the sandbox exits.

The program named inside the script is pinned to its resolved absolute path, for the same reason described in the kitty section.

### Agent session capture and restore

herdr can remember which agent a pane was running and, after the server restarts, resume that agent by typing its own resume command back into the restored pane (`claude --resume <id>`, `pi --session <id>`, `codex resume <id>`). Both halves need help to work for an agent running inside devsandbox: the report has to cross the filtered proxy, and the resume command has to re-enter the sandbox instead of running the agent on the host.

**What `agent_reporting` permits.** Exactly three methods, all of which only tell herdr *what* the pane is running:

| Method | Sent by | Purpose |
|--------|---------|---------|
| `pane.report_agent_session` | Claude, Pi, Codex | the agent's native session ID and/or transcript path |
| `pane.report_agent` | Pi | the same, plus a status label (`idle`, `working`, `blocked`) |
| `pane.release_agent` | Pi | the agent is exiting |

**What it deliberately does not permit.** No pane reads, no input injection, no host execution, no `pane.clear_agent_authority`. Every report is bound to two anchors derived on the host, never from the report itself: the pane ID herdr gave this devsandbox process (`HERDR_PANE_ID`), and the agent devsandbox was asked to launch. A report naming another pane, or claiming to be another agent, is denied. The session ID is restricted to `[A-Za-z0-9._-]` and capped at 128 bytes - herdr shell-quotes that value and types it into a host pane shell on restore, so it is treated as hostile input rather than trusted to a third-party quoter. The transcript path must be absolute, control-character free, restricted to `[A-Za-z0-9._-]` plus `/`, contain no `..`, and sit inside that agent's own session directory (`~/.claude/projects`, `~/.pi/agent/sessions`, `~/.codex/sessions`), so a report cannot name `auth.json`. The path carries the same charset restriction as the session ID because herdr persists a path-kind session ref exactly as it persists an ID and types either back into a host pane shell on restore - and Pi reports only a path. Confinement bounds where the path points, not what it is named, so a session under a directory whose name falls outside that charset is not captured.

**Activation.** `agent_reporting` is enabled only for a **direct** `devsandbox <agent>` launch inside a herdr pane - both anchors have to exist. Running `devsandbox` to get a shell and then typing `claude` inside it leaves the launched agent unknown, so the capability is never enabled and the in-sandbox integration's report is denied. `devsandbox tools check herdr` reports whether agent reporting is active and, when it is not, which anchor is missing.

**Setting up restore.** Capture alone is not enough: herdr's resume command is compiled into its binary as a bare `claude --resume <id>`, which would run the host agent against a different, host-side session store. herdr delivers that command as typed input to the pane's shell, so the [shell wrappers](#shell-wrappers---run-agents-sandboxed-by-default) intercept it and re-enter the sandbox.

1. Add the activation line to the startup file of **the shell herdr starts panes with**, which is not necessarily your login shell - herdr uses `[terminal] default_shell`, falling back to `$SHELL`. `devsandbox agent-wrappers activate <shell>` prints the definitions for any supported shell, and the line to add is in `devsandbox agent-wrappers --help`.
2. Restart the herdr server so new panes pick up the wrappers.

**What is written on the host.** Launching an agent from a herdr pane records one JSON file per pane under `$XDG_STATE_HOME/devsandbox/herdr-panes/` (`~/.local/state/devsandbox/herdr-panes/` when unset), directory `0700`, files `0600`. Each record names the pane, the agent, the project directory and the sandbox state root that launch used - it is what the restore guard below compares against. The pane ID is hashed into the filename, so an opaque herdr-supplied string never influences a path, and the store lives outside every path mounted into the sandbox, so sandboxed code cannot read or write it. Records are **not** pruned by `devsandbox sandboxes prune` or `--rm`; delete the directory to clear them.

**Restoring into the right sandbox.** A recorded session is reachable again only from the directory it was launched in: the agent's session store lives in the synthetic home under that launch's sandbox state root, and the agent keys its own sessions by project path. When a resume-shaped command reaches a pane, `run-agent` checks the record before re-entering, and refuses when the current directory would reach neither the same project directory nor the same sandbox state root. Resume-shaped means herdr's own argv (`claude --resume ID`, `pi --session ID`, `codex resume ID`) plus the agent's own equivalents you may type by hand - `claude -c` / `claude --continue` reach the same session store, so the guard sees them too. The error names the directory to change to, or - for a `--worktree` session - the original invocation to re-run. A record that exists but cannot be read fails closed too; a pane with no record at all re-enters normally.

**Rolling it back.** Remove the activation line from your rc file. Delete `~/.local/state/devsandbox/herdr-panes/` to drop the pane records. To stop herdr replaying resume commands at all, set `resume_agents_on_restore = false` under `[session]` in herdr's config. Setting `mode = "disabled"` under `[tools.herdr]` turns off the proxy, and with it capture.

**Session IDs are never logged.** The proxy logs one line per request carrying the method and the reason for the decision. Neither ever includes the session ID, the transcript path, or any part of a rejected value - including for denied reports, where the rejected string is the interesting one.

**Limitations.**

- Capture requires a direct `devsandbox <agent>` launch, as above.
- Interception only works for panes running fish, bash, or zsh. If herdr's `default_shell` is something else, no wrapper runs and herdr types the resume command to the host agent. That fails on functionality, not on isolation: the ID names a session inside the sandbox's overlay that the host agent cannot see, so it errors or starts fresh.
- fish's wrapper lives in `conf.d`, which fish also sources for non-interactive `fish -c`. bash and zsh wrap interactive shells only.
- Sessions launched with `--worktree` **fail closed** on restore, from either directory. Their sandbox state is keyed to the repo root while the session runs in the worktree, so re-entering from the repo root reaches the right state root under the wrong project, and re-entering from the worktree reaches a different state root entirely. Both open a different agent session store, so `run-agent` refuses with a message naming the original invocation to re-run instead.
- A resume typed from any other directory is refused for the same reason, with the directory to change to named in the error.
- `devsandbox --rm` destroys the sandbox state a recorded session lives in. herdr will still have the ID; the resume finds nothing and starts fresh.
- A pane that previously ran an *unsandboxed* agent may carry a herdr-recorded host session ID. Resuming it through the wrapper looks for that ID inside the sandbox, does not find it, and starts a fresh session.

### What Gets Mounted

| Resource | Mode | Purpose |
|----------|------|---------|
| Proxy socket (`$HOME/.run/<pid>/herdr.sock`) | read-write (proxy is local to the sandbox home) | herdr control via the filtering proxy |
| Proxy socket, second path (`$HOME/.config/herdr/herdr.sock`) | read-write bind mount | the path herdr's client derives on its own, for subcommands that ignore `HERDR_SOCKET_PATH` |
| `herdr` binary | read-only | CLI for `herdr tab create`, `herdr pane run`, etc. Skipped when the binary already sits under a mounted directory, as mise installs do. |

The host's real herdr socket is **not** bind-mounted into the sandbox, and neither is the script relocation directory.

The second socket path exists because a few subcommands bypass the environment variable. `herdr session list` connects directly to `<config dir>/herdr.sock` and reports the session `stopped` when that fails - which it always did in the sandbox, since the host socket is never mounted. That probe is `connect(2)` only, with no protocol traffic, so pointing it at the proxy makes the status correct without any request crossing the filter. It is the same filtered socket under a second name, so it grants no additional reach.

### Environment Variables

- `HERDR_SOCKET_PATH` - set to `$HOME/.run/<pid>/herdr.sock` inside the sandbox, and only while the proxy is running. The host value is never exposed.
- `HERDR_ENV`, `HERDR_SESSION`, `HERDR_WORKSPACE_ID`, `HERDR_TAB_ID`, `HERDR_PANE_ID` - passed through from the host as read-only signals about the calling pane. `HERDR_WORKSPACE_ID` matters: launchers read it from the environment rather than querying the API, which is why no read-only introspection capability is needed.

### Limitations

- Streaming methods (`events.subscribe`, `events.wait`, `pane.wait_for_output`) are denied in this iteration.
- Read-only introspection (`pane.list`, `tab.list`, `pane.current`, `session.snapshot`) is denied; nothing needs it yet. Commands built on them fail with a `forbidden` error rather than degrading quietly.
- Verified against herdr v0.7.4 (protocol 16). A protocol bump can change parameter shapes the filter validates; the failure mode is denial, not bypass.

Run `devsandbox tools check herdr` to confirm the tool is detected and see the active mode and capabilities.

## Zellij Terminal Multiplexer

When running inside a [zellij](https://zellij.dev/) session, devsandbox can forward the session socket and `ZELLIJ*` env vars into the sandbox so `zellij run`, `zellij action`, etc. attach to the host multiplexer.

**Disabled by default.** Unlike kitty, the zellij socket has no capability filtering - exposing it gives sandboxed code unrestricted control over the host multiplexer (execute commands in any pane, read pane contents, swap layouts). Opt in only if you trust the workload.

### Configuration

```toml
[tools.zellij]
enabled = true
```

### Activation

When `enabled = true`, the tool activates if all of:

1. `ZELLIJ` env var is set on the host.
2. The `zellij` binary is on PATH.
3. A zellij socket directory exists (`$ZELLIJ_SOCKET_DIR`, `$XDG_RUNTIME_DIR/zellij/`, or `/tmp/zellij-<uid>/`).

### What Gets Mounted

| Resource | Mode | Purpose |
|----------|------|---------|
| Zellij socket directory | read-write bind mount | IPC socket for `zellij` CLI commands |
| `zellij` binary | read-only | CLI for `zellij run`, `zellij action`, etc. |

### Environment Variables

`ZELLIJ`, `ZELLIJ_SESSION_NAME`, `ZELLIJ_PANE_ID` are forwarded from the host.

Run `devsandbox tools check zellij` to confirm the tool is detected and see the active state.

## XDG Desktop Portal (Linux only)

Sandboxed applications can send desktop notifications to the host via [XDG Desktop Portal](https://flatpak.github.io/xdg-desktop-portal/). This uses `xdg-dbus-proxy` to expose only the notification portal interface - no other D-Bus access is granted.

### Requirements

- `xdg-dbus-proxy` binary installed on the host
- `xdg-desktop-portal` running with a backend (e.g., `xdg-desktop-portal-gtk`, `xdg-desktop-portal-kde`)
- A D-Bus session bus available (`DBUS_SESSION_BUS_ADDRESS` set)

```bash
# Arch Linux
sudo pacman -S xdg-dbus-proxy xdg-desktop-portal xdg-desktop-portal-gtk

# Debian/Ubuntu
sudo apt install xdg-dbus-proxy xdg-desktop-portal xdg-desktop-portal-gtk

# Fedora
sudo dnf install xdg-dbus-proxy xdg-desktop-portal xdg-desktop-portal-gtk
```

### Configuration

Notifications are enabled by default when the requirements are met. To disable:

```toml
[tools.portal]
notifications = false
```

### How It Works

1. `xdg-dbus-proxy` starts as a background process, creating a filtered D-Bus socket
2. Only `org.freedesktop.portal.Desktop` and `org.freedesktop.portal.Notification` interfaces are allowed through
3. The proxy socket is bind-mounted into the sandbox at `$XDG_RUNTIME_DIR/.dbus-proxy/bus`
4. `DBUS_SESSION_BUS_ADDRESS` inside the sandbox points to the proxy socket
5. A `.flatpak-info` file is created so `xdg-desktop-portal` recognizes the sandbox as a valid Flatpak-like application

The proxy is started before the sandbox launches and stopped when the sandbox exits. Each session runs its own proxy with its own socket on the host, so starting or exiting a second session for the same project leaves a running session's notifications working.

### Checking Portal Status

```bash
devsandbox tools check portal
```

Example output:

```
✓ portal (/usr/bin/xdg-dbus-proxy)
    ✓ /run/user/1000/bus
    notifications: enabled
```

### Limitations

- Linux only (bwrap backend) - not available on macOS Docker backend
- Only the notification portal is currently supported
- The host must have a running D-Bus session bus with a unix socket

## Adding Custom Tools

To make additional tools available:

1. **Install via mise** (recommended):

   ```bash
   mise install mytool@version
   ```

2. **System tools** - Tools in `/usr/bin`, `/usr/local/bin` are available

3. **Project-local tools** - Place executables in your project directory

## See Also

- [Sandboxing](sandboxing.md) - how isolation works, overlay filesystem for writable tools
- [Sandboxing: Overlay Filesystem](sandboxing.md#overlay-filesystem) - allow mise to install tools inside the sandbox
- [Configuration: Tool Settings](configuration.md#tool-specific-configuration) - git, mise, and Docker config options
- [Use Cases: AI Coding Assistants](use-cases.md#ai-coding-assistants) - workflows for Claude Code and Copilot
- [Use Cases: Development Workflows](use-cases.md#development-workflows) - language-specific examples

[Back to docs index](index.md) | [Back to README](../README.md)
