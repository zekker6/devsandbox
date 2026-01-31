# devsandbox

A secure sandbox for running untrusted development tools. Uses [bubblewrap](https://github.com/containers/bubblewrap)
for filesystem isolation and [pasta](https://passt.top/) for network isolation.

## Why?

AI coding assistants like Claude Code, GitHub Copilot, and others can execute arbitrary commands on your system. While
useful, this creates security risks—especially when working with untrusted code or allowing agents to run with elevated
permissions.

`devsandbox` provides a security boundary that:

- Allows full read/write access to your project directory
- Blocks access to SSH keys, cloud credentials, and secrets
- Optionally routes all network traffic through an inspectable proxy
- Preserves access to your development tools (via mise)

## Quick Start

### Installation

**Requirements:** Linux with user
namespaces, [bubblewrap](https://github.com/containers/bubblewrap), [mise](https://mise.jdx.dev/)

```bash
# Arch Linux
sudo pacman -S bubblewrap passt

# Debian/Ubuntu
sudo apt install bubblewrap passt

# Fedora
sudo dnf install bubblewrap passt
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

| Resource                          | Access           |
|-----------------------------------|------------------|
| Project directory                 | Read/Write       |
| `.env` files                      | Blocked          |
| `~/.ssh`                          | Blocked          |
| `~/.aws`, `~/.azure`, `~/.gcloud` | Blocked          |
| mise-managed tools                | Read-only        |
| Network (default)                 | Full access      |
| Network (proxy mode)              | Isolated, logged |

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
