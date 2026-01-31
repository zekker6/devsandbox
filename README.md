# devsandbox

A secure sandbox for running untrusted development tools. Uses [bubblewrap](https://github.com/containers/bubblewrap) for filesystem isolation and [pasta](https://passt.top/) for network isolation.

## Why?

AI coding assistants like Claude Code, GitHub Copilot, and others can execute arbitrary commands on your system. 
While useful, this creates security risks—especially when working with untrusted code or allowing agents to run with elevated permissions.

`devsandbox` provides a security boundary that:
- Allows full read/write access to your project directory
- Blocks access to SSH keys, cloud credentials, and secrets
- Optionally routes all network traffic through an inspectable proxy
- Preserves access to your development tools (via mise)

## Security Model

| Resource | Access |
|----------|--------|
| Project directory | Read/Write |
| `.env` files | Blocked (overlaid with /dev/null) |
| `~/.ssh` | Blocked |
| `~/.gitconfig` | Sanitized (no credentials) |
| `~/.aws`, `~/.azure`, `~/.gcloud` | Blocked |
| mise-managed tools | Read-only |
| Network (default) | Full access |
| Network (proxy mode) | Isolated, routed through MITM proxy |

## Requirements

- Linux (kernel 3.8+ with user namespaces)
- [bubblewrap](https://github.com/containers/bubblewrap) (`bwrap`)
- [mise](https://mise.jdx.dev/) (for tool management)
- [passt](https://passt.top/) (only for proxy mode)
- Supported shells: bash, zsh, fish (auto-detected from `$SHELL`)

### Installation

**Arch Linux:**
```bash
sudo pacman -S bubblewrap passt
```

**Debian/Ubuntu:**
```bash
sudo apt install bubblewrap passt
```

**Fedora:**
```bash
sudo dnf install bubblewrap passt
```

## Building

```bash
# Using task (recommended)
task build

# Or directly with go
go build -o bin/devsandbox ./cmd/devsandbox
```

## Usage

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
```

### Proxy Mode

Proxy mode creates a fully isolated network namespace where all HTTP/HTTPS traffic is routed through a local MITM proxy. This allows inspection and logging of all network requests.

```bash
# Enable proxy mode
devsandbox --proxy

# Custom proxy port
devsandbox --proxy --proxy-port 9090
```

In proxy mode:
- Network is isolated via pasta (user-mode networking)
- All traffic must go through the proxy (enforced via iptables)
- A CA certificate is auto-generated for HTTPS interception
- Requests are logged to `~/.local/share/devsandbox/<project>/logs/proxy/`

The CA certificate is automatically configured for common tools via environment variables:
- `NODE_EXTRA_CA_CERTS`
- `REQUESTS_CA_BUNDLE`
- `CURL_CA_BUNDLE`
- `GIT_SSL_CAINFO`
- `SSL_CERT_FILE`

## Data Locations

Sandbox data is stored following XDG conventions:

```
~/.local/share/devsandbox/<project>/
├── home/           # Sandbox $HOME (isolated per-project)
│   ├── .config/
│   ├── .cache/
│   │   ├── go-build/   # Isolated Go build cache
│   │   └── go-mod/     # Isolated Go module cache
│   └── go/             # Isolated GOPATH
├── .ca/            # Proxy CA certificate and key
│   ├── ca.crt
│   └── ca.key
└── logs/
    ├── proxy/      # HTTP request/response logs (gzip compressed)
    │   └── requests_20240115_0000.jsonl.gz
    └── internal/   # Internal logs
        ├── proxy_20240115_0000.log.gz   # Proxy server warnings/errors
        └── logging-errors.log            # Remote logging failures
```

## Environment Variables

Inside the sandbox, several environment variables are set:

| Variable | Value |
|----------|-------|
| `DEVSANDBOX` | `1` |
| `DEVSANDBOX_PROJECT` | Project directory name |
| `DEVSANDBOX_PROXY` | `1` (only in proxy mode) |
| `GOTOOLCHAIN` | `local` (prevents version conflicts) |

## How It Works

1. **Filesystem isolation**: bubblewrap creates a new mount namespace with selective bind mounts
2. **PID isolation**: Processes inside can't see or signal processes outside
3. **Network isolation** (proxy mode): pasta creates a user-mode network namespace with NAT
4. **Traffic enforcement** (proxy mode): iptables rules block all traffic except to the gateway IP

## Managing Sandboxes

List all sandbox instances:

```bash
# List sandboxes
devsandbox sandboxes list

# With sizes (slower, calculates disk usage)
devsandbox sandboxes list --size

# JSON output for scripting
devsandbox sandboxes list --json

# Sort by last used
devsandbox sandboxes list --sort used
```

Prune stale sandboxes:

```bash
# Remove only orphaned sandboxes (project dir no longer exists)
devsandbox sandboxes prune

# Keep 5 most recently used, remove the rest
devsandbox sandboxes prune --keep 5

# Remove sandboxes not used in 30 days
devsandbox sandboxes prune --older-than 30d

# Remove all sandboxes
devsandbox sandboxes prune --all

# Preview what would be removed
devsandbox sandboxes prune --dry-run
```

## Configuration

devsandbox can be configured via a TOML file at `~/.config/devsandbox/config.toml`.

Generate a default config file:

```bash
devsandbox config init
```

### Configuration Options

```toml
# Proxy mode settings
[proxy]
# Enable proxy mode by default (can be overridden with --proxy flag)
enabled = false

# Default proxy server port
port = 8080

# Sandbox settings
[sandbox]
# Base directory for sandbox homes
# Defaults to ~/.local/share/devsandbox if not set
# base_path = "~/.local/share/devsandbox"
```

### Remote Logging

Proxy request logs can be forwarded to remote destinations for centralized logging and monitoring. Multiple receivers can be configured simultaneously.

```toml
[logging]
# Custom attributes added to all log entries (OTLP resource attributes)
[logging.attributes]
environment = "development"
hostname = "myworkstation"
```

#### Local Syslog

Send logs to the local syslog daemon:

```toml
[[logging.receivers]]
type = "syslog"
facility = "local0"
tag = "devsandbox"
```

#### Remote Syslog

Send logs to a remote syslog server:

```toml
[[logging.receivers]]
type = "syslog-remote"
address = "logs.example.com:514"
protocol = "udp"  # or "tcp"
facility = "local0"
tag = "devsandbox"
```

#### OpenTelemetry (OTLP)

Send logs to an OpenTelemetry collector. Supports both HTTP and gRPC protocols.

**HTTP (default):**

```toml
[[logging.receivers]]
type = "otlp"
endpoint = "http://localhost:4318/v1/logs"
protocol = "http"
batch_size = 100
flush_interval = "5s"

# Optional: authentication headers
headers = { "Authorization" = "Bearer token" }
```

**gRPC:**

```toml
[[logging.receivers]]
type = "otlp"
endpoint = "localhost:4317"
protocol = "grpc"
insecure = true  # disable TLS for local testing
batch_size = 100
flush_interval = "5s"
```

OTLP logs include these resource attributes:
- `service.name`: "devsandbox"
- `service.version`: Build version
- `service.commit`: Git commit hash
- Plus any custom attributes from `[logging.attributes]`

#### Logging Errors

If remote logging fails (network issues, authentication errors, etc.), errors are logged locally to:

```
~/.local/share/devsandbox/<project>/logs/internal/logging-errors.log
```

## Troubleshooting

Check your installation with the doctor command:

```bash
devsandbox doctor
```

This verifies:
- Required binaries (bwrap, shell)
- Optional binaries (pasta for proxy mode)
- User namespace support
- Directory permissions and writability
- Kernel version

## Debugging

```bash
# Enable debug output (shows bwrap arguments)
DEVSANDBOX_DEBUG=1 devsandbox
```

## Limitations

- Linux only (uses Linux namespaces)
- Requires user namespaces enabled (most modern distros have this)
- Some tools may not work correctly with network isolation
- MITM proxy may break certificate pinning in some applications

## License

MIT
