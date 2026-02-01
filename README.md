# devsandbox

A sandbox for running untrusted development tools. Uses [bubblewrap](https://github.com/containers/bubblewrap)
for filesystem isolation and optionally [pasta](https://passt.top/) for network isolation (proxy mode).

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

**Requirements:** Linux with user namespaces, [bubblewrap](https://github.com/containers/bubblewrap), [mise](https://mise.jdx.dev/)

**Optional:** [passt](https://passt.top/) - only needed for proxy mode (`--proxy`)

```bash
# Arch Linux
sudo pacman -S bubblewrap
sudo pacman -S passt  # optional, for proxy mode

# Debian/Ubuntu
sudo apt install bubblewrap
sudo apt install passt  # optional, for proxy mode

# Fedora
sudo dnf install bubblewrap
sudo dnf install passt  # optional, for proxy mode
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

By default, git runs in **readonly** mode with a sanitized config containing only `user.name` and `user.email`. This allows commits without exposing credentials.

| Mode | Description |
|------|-------------|
| `readonly` | Safe gitconfig (name/email only), no credentials **(default)** |
| `readwrite` | Full access: credentials, SSH keys, GPG signing |
| `disabled` | No git config, commands work with defaults |

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

- Linux only (uses Linux namespaces)
- Requires user namespaces enabled
- MITM proxy may break certificate pinning
- Docker not supported inside sandbox

## License

MIT
