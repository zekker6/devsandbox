# devsandbox

A sandbox for running untrusted development tools. Uses [bubblewrap](https://github.com/containers/bubblewrap)
on Linux or Docker containers on macOS for filesystem isolation, and optionally [pasta](https://passt.top/) for
network isolation (proxy mode on Linux).

## Why?

AI coding assistants like Claude Code, GitHub Copilot, and others can execute arbitrary commands on your system. While
useful, this creates security risks—especially when working with untrusted code or allowing agents to run with elevated
permissions.

`devsandbox` provides a security boundary that:

- Preserves access to your development tools
- Allows full read/write access to your project directory
- Keeps SSH keys, cloud credentials, and secrets out of reach
- Optionally routes all network traffic through an inspectable proxy

## Quick Start

### Installation

**Linux (bwrap backend):**
- [bubblewrap](https://github.com/containers/bubblewrap) (required)
- [passt](https://passt.top/) (optional, for proxy mode)

**macOS (docker backend):**
- [Docker Desktop](https://docs.docker.com/desktop/install/mac-install/) (required)

**Optional (all platforms):**
- [mise](https://mise.jdx.dev/) - for tool version management

```bash
# Linux: Install bubblewrap
# Arch Linux
sudo pacman -S bubblewrap

# Debian/Ubuntu
sudo apt install bubblewrap

# Fedora
sudo dnf install bubblewrap

# macOS: Install Docker Desktop
# Download from https://docs.docker.com/desktop/install/mac-install/

# Install devsandbox via mise
mise install github:zekker6/devsandbox
```

**Build from source:**

```bash
task build
# or: go build -o bin/devsandbox ./cmd/devsandbox
```

### Basic Usage

```bash
# Interactive shell in sandbox
devsandbox

# Run a specific command
devsandbox npm install
devsandbox bun run dev

# Run AI coding assistant with reduced risk
devsandbox claude --dangerously-skip-permissions

# Using mise-managed tools inside sandbox
mise exec "github:zekker6/devsandbox@latest" -- claude --dangerously-skip-permissions

# Show sandbox configuration
devsandbox --info

# Check installation
devsandbox doctor
```

### Proxy Mode

Route all HTTP/HTTPS traffic through an inspectable proxy:

```bash
# Enable proxy mode
devsandbox --proxy

# Run command with traffic logging
devsandbox --proxy npm install

# View captured traffic
devsandbox logs proxy --last 50

# Follow logs in real-time
devsandbox logs proxy -f
```

## Security Model

| Resource                          | Access                     |
|-----------------------------------|----------------------------|
| Project directory                 | Read/Write                 |
| `.env` files                      | Hidden                     |
| `~/.ssh`                          | Not mounted (configurable) |
| `~/.aws`, `~/.azure`, `~/.gcloud` | Not mounted                |
| Git config                        | Safe mode (configurable)   |
| mise-managed tools                | Read-only (configurable)   |
| Network (default)                 | Full access                |
| Network (proxy mode)              | Isolated, logged           |

### Git Integration

By default, git runs in **readonly** mode where `.git` is mounted read-only, preventing commits. This is the safest option for running untrusted code.

| Mode | .git | Commits | Credentials |
|------|------|---------|-------------|
| `readonly` | read-only | ❌ blocked | ❌ none **(default)** |
| `readwrite` | read-write | ✅ allowed | ✅ SSH, GPG, credentials |
| `disabled` | read-write | ✅ allowed | ❌ none |

Configure in `~/.config/devsandbox/config.toml`:

```toml
[tools.git]
mode = "readonly"  # or "readwrite", "disabled"
```

## Documentation

- **[Sandboxing](docs/sandboxing.md)** - How isolation works, data locations, managing sandboxes
- **[Proxy Mode](docs/proxy.md)** - Network isolation, traffic inspection, viewing logs
- **[Tools](docs/tools.md)** - mise integration, supported tools, editor setup
- **[Configuration](docs/configuration.md)** - Config file reference, remote logging (syslog, OTLP)
- **[Use Cases](docs/use-cases.md)** - Workflows, shell aliases, autocompletion setup

## Quick Reference

### Managing Sandboxes

```bash
devsandbox sandboxes list           # List all sandboxes
devsandbox sandboxes prune          # Remove orphaned sandboxes
devsandbox sandboxes prune --all    # Remove all sandboxes
```

### Viewing Logs

```bash
devsandbox logs proxy               # View proxy request logs
devsandbox logs proxy -f            # Follow/tail logs
devsandbox logs proxy --errors      # Show only errors
devsandbox logs proxy --stats       # Show statistics
devsandbox logs internal            # View internal error logs
```

### Inspecting Tools

```bash
devsandbox tools list               # List available tools
devsandbox tools info mise          # Show tool details
devsandbox tools check              # Verify tool setup
```

### Configuration

```bash
devsandbox config init              # Generate config file
```

Config file: `~/.config/devsandbox/config.toml`

```toml
[proxy]
enabled = false
port = 8080
```

## Limitations

**Linux (bwrap backend):**
- Requires user namespaces enabled
- MITM proxy may break certificate pinning

**macOS (docker backend):**
- Requires Docker Desktop running
- File operations may be slower due to volume mounts
- Network isolation uses HTTP_PROXY instead of pasta

**Both:**
- Docker access is read-only (no container creation/deletion, see [docs/tools.md](docs/tools.md#docker))

## License

MIT
