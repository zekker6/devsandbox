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
```

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

| Attribute         | Value                         |
|-------------------|-------------------------------|
| `service.name`    | `devsandbox`                  |
| `service.version` | Build version (e.g., `1.0.0`) |
| `service.commit`  | Git commit hash               |

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
