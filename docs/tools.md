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
│ portal        │ available │ XDG Desktop Portal (notifications)            │
│ powerlevel10k │ missing   │ Powerlevel10k zsh theme                       │
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

AI coding assistants execute arbitrary code -- installing packages, running builds, making network requests. Running them inside devsandbox ensures they can do their job without accessing your credentials, keys, or secrets.

### Claude Code

Claude Code is fully supported:

```bash
# Run Claude Code in sandbox
devsandbox claude

# With permissions disabled (recommended in sandbox)
devsandbox claude --dangerously-skip-permissions
```

Everything after `devsandbox` is passed to the sandboxed command. `--dangerously-skip-permissions` is a Claude Code flag that skips permission prompts -- safe inside the sandbox because devsandbox provides the security boundary.

Configuration directories are mounted read-write to allow Claude to save settings:

```
~/.claude           → Sandbox (read-write)
~/.config/Claude    → Sandbox (read-write)
~/.claude.json      → Sandbox (read-write)
```

These directories are isolated to the sandbox home -- not your real host directories. Claude's conversation state and settings persist across sandbox sessions for the same project but are not shared with your host.

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

### GitHub Copilot

Works inside editors (Neovim, VS Code) running in the sandbox.

### Other AI Tools

Any CLI-based AI tool works in the sandbox. This includes Continue, Cline, and similar tools. If the tool runs as a CLI process, wrap it with `devsandbox`.

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

1. A Unix socket proxy is created at `$HOME/docker.sock` inside the sandbox
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

If no tool declares a capability and `mode = "auto"` (the default), the proxy stays inactive — zero attack surface when nothing needs it. `revdiff` is the built-in consumer: if the `revdiff` binary is on PATH, the proxy activates automatically.

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
| `list_owned` | `ls` — response is filtered to owned windows only |

`launch_*` capabilities equal arbitrary host code execution and must be paired with command patterns declared by the tool that requests them. Shell metacharacters (`;`, `&`, `|`, `` ` ``, `$()`, `<`, `>`, etc.) in `sh -c` payloads are rejected outright.

### What Gets Mounted

| Resource | Mode | Purpose |
|----------|------|---------|
| Proxy socket (`$HOME/.kitty.sock`) | read-write (proxy is local to the sandbox home) | kitty remote-control via the filtering proxy |
| `kitty` binary | read-only | CLI for `kitty @ launch`, `kitty @ ls`, etc. |

The host's real kitty socket is **not** bind-mounted into the sandbox.

### Environment Variables

- `KITTY_LISTEN_ON` — rewritten to `unix:$HOME/.kitty.sock` inside the sandbox. Host value is never exposed.
- `KITTY_WINDOW_ID`, `KITTY_PID` — passed through from host (read-only signals about the host pane).

### Limitations

- Async / streaming kitty commands (`get-text --watcher`, async kittens) are denied in this MVP.
- `remote_control_password` in `kitty.conf` is unsupported — use `allow_remote_control = socket-only` instead.

Run `devsandbox tools check kitty` to confirm the tool is detected and see the active mode.

## XDG Desktop Portal (Linux only)

Sandboxed applications can send desktop notifications to the host via [XDG Desktop Portal](https://flatpak.github.io/xdg-desktop-portal/). This uses `xdg-dbus-proxy` to expose only the notification portal interface — no other D-Bus access is granted.

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

The proxy is started before the sandbox launches and stopped when the sandbox exits.

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

- Linux only (bwrap backend) — not available on macOS Docker backend
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

- [Sandboxing](sandboxing.md) -- how isolation works, overlay filesystem for writable tools
- [Sandboxing: Overlay Filesystem](sandboxing.md#overlay-filesystem) -- allow mise to install tools inside the sandbox
- [Configuration: Tool Settings](configuration.md#tool-specific-configuration) -- git, mise, and Docker config options
- [Use Cases: AI Coding Assistants](use-cases.md#ai-coding-assistants) -- workflows for Claude Code and Copilot
- [Use Cases: Development Workflows](use-cases.md#development-workflows) -- language-specific examples

[Back to docs index](README.md) | [Back to README](../README.md)
