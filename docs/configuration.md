# Configuration

devsandbox can be configured via a TOML file at `~/.config/devsandbox/config.toml`.

## Getting Started

Generate a default configuration file:

```bash
devsandbox config init
```

This creates `~/.config/devsandbox/config.toml` with documented defaults.

## Configuration Reference

### Proxy Settings

```toml
[proxy]
# Enable proxy mode by default
# Can be overridden with --proxy / --no-proxy flags
enabled = false

# Default proxy server port
# Can be overridden with --proxy-port flag
port = 8080
```

### Sandbox Settings

```toml
[sandbox]
# Base directory for sandbox data
# Defaults to ~/.local/share/devsandbox
# base_path = "~/.local/share/devsandbox"

# Control visibility of .devsandbox.toml inside the sandbox
# - "hidden" (default): config file is not visible to sandboxed processes
# - "readonly": config file is visible but read-only
# - "readwrite": config file is visible and writable
config_visibility = "hidden"
```

### Custom Mounts

Control how specific paths are mounted in the sandbox. Use this to:

- Mount additional host directories (e.g., app configs)
- Hide sensitive files within your project
- Set up overlay mounts for caches

```toml
# Mount app configuration as read-only
[[sandbox.mounts.rules]]
pattern = "~/.config/myapp"
mode = "readonly"

# Hide secrets directory within the project
[[sandbox.mounts.rules]]
pattern = "**/secrets/**"
mode = "hidden"

# Mount cache with overlay (persistent writes)
[[sandbox.mounts.rules]]
pattern = "~/.cache/myapp"
mode = "overlay"
```

#### Mount Modes

| Mode         | Description                                                  |
|--------------|--------------------------------------------------------------|
| `readonly`   | Bind mount as read-only (default)                            |
| `readwrite`  | Bind mount as read-write                                     |
| `hidden`     | Overlay with `/dev/null` (files only, not directories)       |
| `overlay`    | Persistent overlayfs (writes saved to sandbox home)          |
| `tmpoverlay` | Temporary overlayfs (writes discarded on sandbox exit)       |

#### Pattern Syntax

Patterns support glob syntax with home directory expansion:

| Pattern             | Matches                                          |
|---------------------|--------------------------------------------------|
| `~/.config/myapp`   | Exact path with home expansion                   |
| `*.conf`            | Single-level wildcard                            |
| `**/secrets/**`     | Recursive match (any depth)                      |
| `/opt/tools`        | Absolute path                                    |

**Note:** The `hidden` mode only works for files. To hide a directory, use `readonly` or `tmpoverlay` instead.

### Port Forwarding

Forward TCP/UDP ports between host and sandbox. Requires network isolation (proxy mode).

```toml
[port_forwarding]
enabled = true

# Inbound: host can connect to services running inside sandbox
[[port_forwarding.rules]]
name = "devserver"
direction = "inbound"
protocol = "tcp"
host_port = 3000
sandbox_port = 3000

# Outbound: sandbox can connect to services on host
[[port_forwarding.rules]]
name = "database"
direction = "outbound"
host_port = 5432
sandbox_port = 5432
```

#### Directions

| Direction  | Description                                           | Example Use Case                    |
|------------|-------------------------------------------------------|-------------------------------------|
| `inbound`  | Host connects to sandbox (host:port → sandbox:port)   | Access dev server from browser      |
| `outbound` | Sandbox connects to host (via gateway IP 10.0.2.2)    | Connect to database in Docker       |

#### Fields

| Field         | Required | Default | Description                              |
|---------------|----------|---------|------------------------------------------|
| `name`        | No       | Auto    | Identifier for the rule                  |
| `direction`   | Yes      | -       | `inbound` or `outbound`                  |
| `protocol`    | No       | `tcp`   | `tcp` or `udp`                           |
| `host_port`   | Yes      | -       | Port on host side (1-65535)              |
| `sandbox_port`| Yes      | -       | Port on sandbox side (1-65535)           |

**Note:** Port forwarding requires network isolation. Enable proxy mode (`--proxy`) or port forwarding will fail with an error.

#### Examples

**Development Server (inbound)**

Access a web server running inside the sandbox from your host browser:

```toml
[[port_forwarding.rules]]
name = "nextjs"
direction = "inbound"
host_port = 3000
sandbox_port = 3000
```

Then run `devsandbox --proxy npm run dev` and open `http://localhost:3000` on host.

**Database Access (outbound)**

Connect to PostgreSQL running on the host from inside the sandbox:

```toml
[[port_forwarding.rules]]
name = "postgres"
direction = "outbound"
host_port = 5432
sandbox_port = 5432
```

Inside sandbox, connect to `10.0.2.2:5432` (pasta gateway IP).

### Overlay Settings

Global overlayfs settings:

```toml
[overlay]
# Master switch for overlay filesystem support
# When disabled, all tools use read-only bind mounts
enabled = true
```

### Tool-Specific Configuration

Each tool can have its own configuration section under `[tools.<name>]`.

#### Git

```toml
[tools.git]
# Git access mode:
# - "readonly" (default): safe gitconfig with only user.name/email, no credentials
# - "readwrite": full access with credentials, SSH keys, GPG keys
# - "disabled": no git configuration (git commands work without user config)
mode = "readonly"
```

**Mode Details:**

| Mode       | gitconfig | Credentials | SSH Keys | GPG Keys | Use Case                    |
|------------|-----------|-------------|----------|----------|-----------------------------|
| `readonly` | Safe copy | No          | No       | No       | Default, maximum isolation  |
| `readwrite`| Full      | Read-only   | Read-only| Read-only| Trusted projects, push/sign |
| `disabled` | None      | No          | No       | No       | Fully anonymous git         |

In `readwrite` mode, SSH and GPG directories are mounted read-only to protect private keys
while still allowing git operations that need them.

#### Mise

```toml
[tools.mise]
# Allow mise to install/update tools via overlayfs
# When enabled, mise directories are mounted with a writable overlay layer
writable = false

# Persist mise changes across sandbox sessions
# When false: changes are discarded when sandbox exits (safer)
# When true: changes are stored in ~/.local/share/devsandbox/<project>/overlay/
persistent = false
```

#### Docker

```toml
[tools.docker]
# Enable Docker socket proxy (disabled by default)
# When enabled, provides read-only access to Docker daemon
enabled = false

# Path to host Docker socket
# Defaults to /run/docker.sock
socket = "/run/docker.sock"
```

**Note:** Docker access is read-only. You can list/inspect containers, view logs, and exec into
running containers, but cannot create, delete, or modify containers. Only Unix socket access is
supported; TCP connections to remote Docker daemons are not proxied.

See [docs/tools.md](tools.md#docker) for full details on allowed operations.

## Remote Logging

Proxy request logs can be forwarded to remote destinations for centralized logging and monitoring. Multiple receivers
can be configured simultaneously.

### Global Attributes

Add custom attributes to all log entries:

```toml
[logging]
[logging.attributes]
environment = "development"
hostname = "myworkstation"
team = "platform"
```

These attributes are included in:

- OTLP resource attributes
- Syslog structured data

### Local Syslog

Send logs to the local syslog daemon:

```toml
[[logging.receivers]]
type = "syslog"
facility = "local0"  # local0-local7, user, daemon, etc.
tag = "devsandbox"   # Syslog tag/program name
```

**Facility options:** `kern`, `user`, `mail`, `daemon`, `auth`, `syslog`, `lpr`, `news`, `uucp`, `cron`, `authpriv`,
`ftp`, `local0`-`local7`

### Remote Syslog

Send logs to a remote syslog server:

```toml
[[logging.receivers]]
type = "syslog-remote"
address = "logs.example.com:514"
protocol = "udp"     # "udp" or "tcp"
facility = "local0"
tag = "devsandbox"
```

### OpenTelemetry (OTLP)

Send logs to an OpenTelemetry collector.

#### HTTP Protocol

```toml
[[logging.receivers]]
type = "otlp"
endpoint = "http://localhost:4318/v1/logs"
protocol = "http"
batch_size = 100
flush_interval = "5s"

# Optional: authentication
headers = { "Authorization" = "Bearer your-token" }
```

#### gRPC Protocol

```toml
[[logging.receivers]]
type = "otlp"
endpoint = "localhost:4317"
protocol = "grpc"
insecure = true      # Disable TLS for local testing
batch_size = 100
flush_interval = "5s"
```

#### OTLP with TLS

```toml
[[logging.receivers]]
type = "otlp"
endpoint = "otel.example.com:4317"
protocol = "grpc"
insecure = false     # Enable TLS (default)
batch_size = 100
flush_interval = "5s"
```

### OTLP Resource Attributes

OTLP logs automatically include these resource attributes:

| Attribute            | Value                                        |
|----------------------|----------------------------------------------|
| `service.name`       | `devsandbox`                                 |
| `service.version`    | Build version (e.g., `1.0.0`)                |
| `service.commit`     | Git commit hash                              |
| `service.dirty`      | `true` if built with uncommitted changes     |
| `service.dirty_hash` | Hash of uncommitted changes (if dirty build) |

Plus any custom attributes from `[logging.attributes]`.

### Multiple Receivers

Configure multiple receivers to send logs to different destinations:

```toml
[logging]
[logging.attributes]
environment = "development"

# Send to local syslog
[[logging.receivers]]
type = "syslog"
facility = "local0"
tag = "devsandbox"

# Also send to OTLP collector
[[logging.receivers]]
type = "otlp"
endpoint = "http://localhost:4318/v1/logs"
protocol = "http"
batch_size = 100
flush_interval = "5s"

# And to remote syslog for compliance
[[logging.receivers]]
type = "syslog-remote"
address = "compliance-logs.internal:514"
protocol = "tcp"
facility = "local1"
tag = "devsandbox-audit"
```

### Logging Errors

If remote logging fails (network issues, authentication errors, etc.), errors are logged locally to:

```
~/.local/share/devsandbox/<project>/logs/internal/logging-errors.log
```

View logging errors:

```bash
devsandbox logs internal --type logging
```

## Complete Example

```toml
# ~/.config/devsandbox/config.toml

[proxy]
# Enable proxy mode by default for this machine
enabled = true
port = 8080

[sandbox]
# Use custom location for sandbox data
# base_path = "/data/devsandbox"

[overlay]
# Master switch for overlay filesystem support
enabled = true

[tools.git]
# Use readonly mode for most projects
mode = "readonly"

[tools.mise]
# Allow mise to install tools inside sandbox
writable = true
# Don't persist changes (safer default)
persistent = false

[port_forwarding]
# Enable port forwarding for dev server access
enabled = true

# Forward dev server for browser access
[[port_forwarding.rules]]
name = "devserver"
direction = "inbound"
host_port = 3000
sandbox_port = 3000

# Custom mount rules
[[sandbox.mounts.rules]]
pattern = "~/.config/myapp"
mode = "readonly"

[[sandbox.mounts.rules]]
pattern = "**/credentials/**"
mode = "hidden"

[logging]
# Custom attributes for all log entries
[logging.attributes]
environment = "development"
hostname = "dev-laptop"
user = "alice"

# Local syslog for immediate visibility
[[logging.receivers]]
type = "syslog"
facility = "local0"
tag = "devsandbox"

# OTLP for centralized monitoring
[[logging.receivers]]
type = "otlp"
endpoint = "http://otel-collector.internal:4318/v1/logs"
protocol = "http"
batch_size = 50
flush_interval = "10s"
headers = { "X-Team" = "platform" }
```

## Environment Variables

Some settings can be overridden via environment variables:

| Variable           | Description                         |
|--------------------|-------------------------------------|
| `DEVSANDBOX_DEBUG` | Enable debug output (`1` to enable) |

## Command Line Overrides

Command line flags take precedence over configuration file settings:

```bash
# Override proxy setting from config
devsandbox --proxy          # Enable even if config has enabled = false
devsandbox --no-proxy       # Disable even if config has enabled = true

# Override port
devsandbox --proxy --proxy-port 9090
```

## Per-Project Configuration

devsandbox supports two mechanisms for per-project settings:

### Conditional Includes

Add `[[include]]` blocks to your global config to apply different settings based on project location:

```toml
# ~/.config/devsandbox/config.toml

# Default settings
[proxy]
enabled = false

# Work projects: enable proxy by default
[[include]]
if = "dir:~/work/**"
path = "~/.config/devsandbox/work.toml"

# Client projects: strict filtering
[[include]]
if = "dir:~/clients/acme/**"
path = "~/.config/devsandbox/acme.toml"
```

**Pattern syntax:**
- `dir:` prefix required
- `*` matches any single directory level
- `**` matches any number of directories (recursive)
- `~` expands to home directory

**Include file format:**
- Same structure as main config
- Nested `[[include]]` blocks are ignored
- Missing files produce a warning but don't fail

### Local Config Files

Create a `.devsandbox.toml` in your project root:

```toml
# /path/to/project/.devsandbox.toml

[proxy]
enabled = true

[tools.git]
mode = "readwrite"
```

**Security:** Local configs require trust approval. When you first run devsandbox in a directory with `.devsandbox.toml`, you'll see a prompt:

```
Local config found: .devsandbox.toml

  [proxy]
  enabled = true

  [tools.git]
  mode = "readwrite"

Trust this configuration? [y/N]:
```

If the file changes, you'll be prompted again.

**Managing trust:**

```bash
# List trusted directories
devsandbox trust list

# Trust config in current directory (for CI/scripts)
devsandbox trust add

# Trust config in a specific directory
devsandbox trust add /path/to/project

# Remove trust for current directory
devsandbox trust remove

# Remove trust for a specific directory
devsandbox trust remove /path/to/project
```

**Non-interactive mode:** In scripts or CI, local configs are skipped with a warning. Use `devsandbox trust add` to pre-approve configs without interactive prompts.

### Config Priority

Settings are merged in this order (later overrides earlier):

1. Global config (`~/.config/devsandbox/config.toml`)
2. Matching includes (in order they appear)
3. Local config (`.devsandbox.toml`)

**Merge rules:**
- Scalar values: later source wins
- Maps (`[tools]`): deep merge
- Arrays (`[[proxy.filter.rules]]`): concatenate (later rules have higher priority)
