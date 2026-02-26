# devsandbox

Sandbox your AI coding assistants. Run Claude Code, Copilot, aider, and other tools without exposing SSH keys, cloud credentials, or secrets.

## The Problem

AI coding assistants execute shell commands, install packages, and make network requests on your machine -- with full access to your `~/.ssh` keys, `~/.aws` credentials, `.env` secrets, and everything else. An AI agent with unrestricted access could read your `~/.ssh/id_ed25519`, exfiltrate `~/.aws/credentials` via an API call, or `rm -rf` your home directory.

devsandbox removes that risk. It wraps any command in a sandbox that provides full read/write access to your project and all your development tools, while blocking access to credentials, keys, and secrets. An optional proxy mode logs every HTTP/HTTPS request for inspection.

## Quickstart

**Install:**

```bash
mise install github:zekker6/devsandbox
```

> Homebrew is not currently available. For direct binary download, see [Installation Details](#installation-details).

**Sandbox your AI agent:**

```bash
# 1. Run Claude Code in the sandbox
devsandbox claude --dangerously-skip-permissions

# 2. Verify what's protected
devsandbox --info
```

Everything after `devsandbox` is passed to the sandboxed command. `--dangerously-skip-permissions` is a Claude Code flag that skips permission prompts -- safe inside the sandbox because devsandbox provides the security boundary.

**Works with:** Claude Code, GitHub Copilot, aider, Cursor, Continue, Cline, OpenCode, and any CLI-based development tool.

That's it. No config files needed. On Linux, devsandbox includes embedded binaries -- zero dependencies. On macOS, a Docker runtime is required (see [Installation Details](#installation-details)).

Run `devsandbox doctor` to verify your setup.

> **macOS:** devsandbox runs your code inside a lightweight Linux container (Debian slim) via Docker. Your project files are mounted into the container, so edits sync bidirectionally. Ensure your Docker runtime is running before using devsandbox. The first start downloads a base Docker image (~200MB); subsequent starts reuse Docker layer caching and complete in 1-2 seconds.

## What Your AI Agent CAN and CANNOT Do

**CAN:** Read/write your project files, run build commands, install dependencies, make API calls (logged in proxy mode).

**CANNOT:** Read SSH keys, access cloud credentials (AWS/Azure/GCloud), read `.env` secrets, see other projects, push to git (by default), or modify your system.

### Security Details

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

- **Zero-config security** -- SSH keys, cloud credentials, `.env` files, and git credentials are blocked by default
- **Your tools, your shell** -- mise-managed tools, shell configs, editor setups (nvim, starship, tmux) all work inside the sandbox
- **MITM proxy** -- optional traffic inspection with log viewing, filtering, and export
- **HTTP filtering** -- whitelist/blacklist domains, or interactively approve requests one at a time
- **Content redaction** -- scan outgoing requests for secrets, block or replace them before they leave your machine
- **Cross-platform** -- [bubblewrap](https://github.com/containers/bubblewrap) namespaces on Linux (sub-second startup), Docker containers on macOS
- **Per-project isolation** -- each project gets its own sandbox home, caches, and logs
- **Git modes** -- readonly (default), readwrite (with SSH/GPG), or disabled
- **Desktop notifications** -- sandboxed apps can send notifications to the host via XDG Desktop Portal (Linux)

## How It Works

**Linux:** Uses [bubblewrap](https://github.com/containers/bubblewrap) to create namespace-based isolation. No root privileges, no Docker, no system packages required -- bwrap and pasta binaries are embedded. Startup is sub-second.

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

By default, `.git` is mounted read-only -- you can view history, diff, and status, but commits are blocked and no credentials are exposed.

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

## Proxy Mode -- Monitor Your AI Agent's Network Activity

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

See [Proxy Mode docs](docs/proxy.md) for filtering rules, log formats, and remote logging setup.

## Installation Details

**Linux:**

Requirements:
- Linux kernel with unprivileged user namespaces enabled (verify: `unshare --user true` should succeed silently)
- No system packages required (bwrap and pasta binaries are embedded)

```bash
# Option 1: mise
mise install github:zekker6/devsandbox

# Option 2: Download binary
curl -L https://github.com/zekker6/devsandbox/releases/latest/download/devsandbox-linux-amd64.tar.gz | tar xz
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

**macOS:** Install a Docker runtime (ensure it is running before using devsandbox):
- [OrbStack](https://orbstack.dev/) -- recommended for Apple Silicon (fastest startup, lowest resource usage)
- [Docker Desktop](https://docs.docker.com/desktop/install/mac-install/) -- most widely tested
- [Colima](https://github.com/abiosoft/colima) -- free and open-source

**Build from source:**

```bash
# Requires: Go 1.22+ and Task (https://taskfile.dev/)
# Or use mise to install dependencies: mise install
task build
```

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
| [Proxy Mode](docs/proxy.md) | Traffic inspection, log viewing/filtering/export, HTTP filtering, ask mode, content redaction, remote logging |
| [Tools](docs/tools.md) | mise integration, shell/editor/prompt setup, AI assistant configs, Git modes, Docker socket proxy |
| [Configuration](docs/configuration.md) | Config file reference, per-project configs, conditional includes, port forwarding, credential injection |
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
- Docker socket access is read-only (no container creation/deletion) -- see [Tools docs](docs/tools.md#docker)
- No nested Docker (cannot run Docker inside the sandbox)

## License

MIT
