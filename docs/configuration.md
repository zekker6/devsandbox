# Configuration

Complete configuration reference for devsandbox.

devsandbox can be configured via a TOML file at `~/.config/devsandbox/config.toml`.

## Getting Started

Generate a default configuration file:

```bash
devsandbox config init
```

This creates `~/.config/devsandbox/config.toml` with documented defaults.

## Quick Reference

| Section | Key Fields | Details |
|---|---|---|
| `[proxy]` | `enabled`, `port`, `mitm`, `extra_env`, `extra_ca_env` | [Proxy Settings](#proxy-settings) |
| `[proxy.credentials.<name>]` | `enabled`, `source.env/file/value` | [Proxy Credentials](#proxy-credentials) |
| `[proxy.redaction]` | `enabled`, `default_action`, `rules` | [Content Redaction](#content-redaction) |
| `[proxy.filter]` | `default_action`, `ask_timeout`, `cache_decisions`, `rules` | [Proxy Mode docs](proxy.md#http-filtering) |
| `[sandbox]` | `isolation`, `base_path`, `use_embedded`, `config_visibility` | [Sandbox Settings](#sandbox-settings) |
| `[sandbox.docker]` | `dockerfile`, `keep_container`, `resources` | [Isolation Backend](#isolation-backend) |
| `[sandbox.mounts.rules]` | `pattern`, `mode` | [Custom Mounts](#custom-mounts) |
| `[overlay]` | `default` | [Overlay Settings](#overlay-settings) |
| `[port_forwarding]` | `enabled`, `auto_detect`, `rules` | [Port Forwarding](#port-forwarding) |
| `[tools.git]` | `mode`, `mount_mode` | [Tool Settings](#tool-specific-configuration) |
| `[tools.mise]` | `mount_mode` | [Tool Settings](#tool-specific-configuration) |
| `[tools.docker]` | `enabled`, `socket` | [Tool Settings](#tool-specific-configuration) |
| `[tools.portal]` | `notifications` | [Tool Settings](#tool-specific-configuration) |
| `[logging]` | `attributes`, `receivers` | [Remote Logging](#remote-logging) |
| `[[include]]` | `if`, `path` | [Per-Project Configuration](#per-project-configuration) |

## Configuration Reference

### Proxy Settings

```toml
[proxy]
# Enable proxy mode by default
# Can be overridden with --proxy flag
enabled = false

# Default proxy server port
# Can be overridden with --proxy-port flag
port = 8080
```

### Proxy Extra Environment Variables

When proxy mode is active, devsandbox sets standard proxy environment variables
(`HTTP_PROXY`, `HTTPS_PROXY`, `YARN_HTTP_PROXY`, etc.) automatically. For tools
with non-standard proxy configuration, add custom variable names:

```toml
[proxy]
enabled = true
extra_env = ["GRADLE_OPTS_PROXY", "MY_CUSTOM_PROXY"]
```

Each variable in `extra_env` is set to the proxy URL (e.g., `http://10.0.2.2:8080`)
when proxy mode is active.

### Proxy Extra CA Environment Variables

When proxy mode is active with HTTPS interception, devsandbox sets standard CA bundle
environment variables (`SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`,
`CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`) automatically. For tools with non-standard CA
bundle configuration, add custom variable names:

```toml
[proxy]
enabled = true
extra_ca_env = ["MY_TOOL_CA_BUNDLE", "CUSTOM_SSL_CERT"]
```

Each variable in `extra_ca_env` is set to the CA certificate path
(e.g., `/tmp/devsandbox-ca.crt` for bwrap, `/etc/ssl/certs/devsandbox-ca.crt` for Docker)
when proxy mode is active.

### Proxy Credentials

Inject authentication credentials into requests for specific domains. Credentials are read from a configurable source (host env var, file, or static value) and added to matching requests. The token never enters the sandbox.

See [Proxy: Credential Injection](proxy.md#credential-injection) for how this works and when to use it.

Minimal configuration using the built-in `github` preset:

```toml
[proxy.credentials.github]
enabled = true

# Optional: override the default token source
# [proxy.credentials.github.source]
# env = "DEVSANDBOX_GITHUB_TOKEN"
# file = "~/.config/devsandbox/github-token"
# value = "github_pat_..."

# Optional: replace any header value already on the request.
# Useful when a CLI inside the sandbox (e.g. `gh`) refuses to run without a
# token set - pass a placeholder via env_passthrough while the real token
# stays on the host and is swapped in by the proxy.
# overwrite = true
```

Custom injector for any other service - no built-in preset required:

```toml
[proxy.credentials.gitlab]
enabled = true
host = "gitlab.com"
header = "PRIVATE-TOKEN"
value_format = "{token}"

  [proxy.credentials.gitlab.source]
  env = "GITLAB_TOKEN"
```

**Fields under `[proxy.credentials.<name>]`:**

| Field | Type | Default | Required when `enabled = true` |
|-------|------|---------|--------------------------------|
| `enabled` | bool | `false` | - |
| `host` | string (exact or glob) | preset value or `""` | yes |
| `header` | string (canonicalized) | preset value or `""` | yes |
| `value_format` | string with `{token}` placeholder | preset value or `"{token}"` | no |
| `overwrite` | bool | `false` | no |
| `preset` | string | `""` (or section name if it matches a built-in) | no |
| `[...source]` sub-table | `env` / `file` / `value` | preset's default source | no |

**Built-in presets:**

| Preset | `host` | `header` | `value_format` | Default source |
|--------|--------|----------|----------------|----------------|
| `github` | `api.github.com` | `Authorization` | `Bearer {token}` | `env = "GITHUB_TOKEN"` (with `GH_TOKEN` fallback when no explicit source is set) |

A section named after a built-in preset (e.g. `[proxy.credentials.github]`) auto-applies the preset; user fields override preset defaults.

**Source priority:** `value` > `env` > `file`. Set exactly one for clarity.

**Specificity ordering:** when multiple injectors could match the same host, the most-specific one wins (exact > longer literal > shorter glob), tie-broken by name alphabetically.

**Overwrite:** `overwrite = false` (default) preserves any existing value of the configured header - safer, but does nothing when the sandboxed tool sets its own. `overwrite = true` unconditionally replaces the header. Combine with a placeholder env var via `[sandbox.environment.<NAME>]` to satisfy tools that refuse to start without a token.

### Content Redaction

Scan outgoing requests for secrets and block or replace them. See [Proxy: Content Redaction](proxy.md#content-redaction) for actions, behavior, and when to use each.

```toml
[proxy.redaction]
enabled = true
default_action = "block"  # "block", "redact", or "log"
```

#### Rule Types

**Source-based** - match exact secret values:

```toml
[[proxy.redaction.rules]]
name = "api-key"
action = "block"          # Optional: override default_action
[proxy.redaction.rules.source]
env = "API_SECRET_KEY"    # Or: file, env_file_key, value
```

**Pattern-based** - match regex patterns:

```toml
[[proxy.redaction.rules]]
name = "openai-keys"
action = "redact"
pattern = "sk-[a-zA-Z0-9]{20,}"
```

**Source types:**

| Field | Description |
|---|---|
| `env` | Host environment variable |
| `file` | File path (supports `~`, whitespace trimmed) |
| `env_file_key` | Key in project `.env` file |
| `value` | Static value in config |

Redaction rules are always additive when merging configs. The `default_action` uses most-restrictive-wins (`block` > `redact` > `log`).

### Log Skip

Suppress matching requests from the proxy log entirely (both `logs/proxy/requests.jsonl` and any configured remote log dispatcher). The request still passes through — only the log entry is dropped. See [Proxy: Skipping Log Entries](proxy.md#skipping-log-entries) for the use case and behavior.

```toml
[[proxy.log_skip.rules]]
pattern = "telemetry.example.com"
# scope defaults to "host", type auto-detects glob

[[proxy.log_skip.rules]]
pattern = "*/v1/traces"
scope = "url"
type = "glob"
```

**Fields:**

| Field | Description | Default |
|---|---|---|
| `pattern` | Pattern to match (exact / glob / regex). Required. | — |
| `scope` | What to match against: `host`, `path`, or `url`. | `host` |
| `type` | Pattern type: `exact`, `glob`, or `regex`. Auto-detected as `regex` when the pattern contains regex metacharacters. | `glob` |

Skip is **absolute**: matched entries are dropped even when the request errored, was blocked by the security filter, or triggered a redaction rule. Rules are evaluated in order; first match wins.

### Avoiding GitHub Rate Limits

On macOS, mise downloads tool releases from GitHub inside a Docker container. Unauthenticated requests are limited to 60/hour. Enable credential injection with a read-only GitHub token to raise this to 5,000/hour.

See [Use Cases: Avoiding GitHub Rate Limits](use-cases.md#avoiding-github-rate-limits) for setup instructions.

### Isolation Backend

```toml
[sandbox]
# Isolation backend: "auto", "bwrap", or "docker"
# - "auto" (default): bwrap on Linux, docker on macOS
# - "bwrap": bubblewrap (Linux only)
# - "docker": Docker containers (Linux, macOS)
isolation = "auto"

# Docker-specific settings (only used when isolation = "docker")
[sandbox.docker]
# Path to Dockerfile used to build the sandbox image.
# Defaults to ~/.config/devsandbox/Dockerfile (auto-created with FROM ghcr.io/zekker6/devsandbox:latest)
# Can be an absolute path or relative to project directory.
# dockerfile = "/path/to/custom/Dockerfile"

# Keep container after exit for fast restarts (default: true)
# When true: containers are reused, startup ~1-2s
# When false: containers are removed on exit
keep_container = true

# Resource limits (optional)
[sandbox.docker.resources]
memory = "4g"
cpus = "2"
```

### Sandbox Settings

```toml
[sandbox]
# Base directory for sandbox data
# Defaults to ~/.local/share/devsandbox
# base_path = "~/.local/share/devsandbox"

# Use embedded bwrap and pasta binaries (Linux only, default: true)
# When false, only system-installed binaries are used.
# use_embedded = true

# Pass host environment variables into the sandbox.
# Listed variables are copied from the host; unset variables are silently skipped.
# env_passthrough = ["MY_API_KEY", "CUSTOM_TOOL_CONFIG"]

# Control visibility of .devsandbox.toml inside the sandbox
# - "hidden" (default): config file is not visible to sandboxed processes
# - "readonly": config file is visible but read-only
# - "readwrite": config file is visible and writable
config_visibility = "hidden"
```

### Sandbox Environment Variables

`[sandbox.environment.<NAME>]` sets explicit env vars inside the sandbox using the same source model as proxy credentials (`value` / `env` / `file`, priority `value > env > file`).

```toml
[sandbox.environment.GH_TOKEN]
value = "placeholder"

[sandbox.environment.PINNED_TOKEN]
env = "HOST_VAR_NAME"

[sandbox.environment.FROM_FILE]
file = "~/.config/devsandbox/token"
```

**Resolution semantics:**

- `value = "x"` - literal string passed to the sandbox.
- `env = "X"` with host `X` unset - variable is skipped entirely (same as `env_passthrough`).
- `env = "X"` with host `X` set - the host value is passed (empty string is passed as empty).
- `file = "..."` that cannot be read - startup error.

**Conflict with `env_passthrough`:** declaring the same variable name in both `env_passthrough` and `environment` is a configuration error - devsandbox fails startup with a message naming the variable. Declare each variable in exactly one place.

### Custom Mounts

Control how specific paths are mounted. See [Sandboxing: Custom Mounts](sandboxing.md#custom-mounts) for mount modes, pattern syntax, and mount ordering.

```toml
[[sandbox.mounts.rules]]
pattern = "~/.config/myapp"
mode = "readonly"

[[sandbox.mounts.rules]]
pattern = "**/secrets/**"
mode = "hidden"

[[sandbox.mounts.rules]]
pattern = "~/.cache/myapp"
mode = "overlay"
```

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

**Note:** Port forwarding requires proxy mode because network namespace isolation (via pasta on Linux or per-session Docker networks on macOS) is only active in proxy mode. Without it, the sandbox shares the host network stack and ports are directly accessible. Enable proxy mode (`--proxy`) or port forwarding will fail with an error.

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

Inside sandbox, connect to `10.0.2.2:5432` (pasta gateway IP on Linux) or `host.docker.internal:5432` (Docker backend on macOS).

#### Dynamic Port Forwarding

```toml
[port_forwarding]
# Automatically detect and forward listening ports inside the sandbox
auto_detect = false

# How often to scan for new listening ports
scan_interval = "2s"

# Ports to never auto-forward (e.g., internal services)
exclude_ports = [22, 80, 443]
```

Ports can also be forwarded on-the-fly to running sandboxes. See [Sandboxing: Runtime Port Forwarding](sandboxing.md#runtime-port-forwarding) for `devsandbox forward` and auto-detect behavior.

### Overlay Settings

Global overlayfs settings control the default mount mode for all tool bindings:

```toml
[overlay]
# Default mount mode for tool bindings.
# Accepted values: "split" (default), "overlay", "tmpoverlay", "readonly", "readwrite"
#
# split        - configs → tmpoverlay (discarded on exit); caches/data/state → persistent overlay
# overlay      - all bindings → persistent overlay (writes saved to sandbox home)
# tmpoverlay   - all bindings → tmpoverlay (writes discarded on sandbox exit)
# readonly     - all bindings → read-only bind mount to host
# readwrite    - all bindings → read-write bind mount to host
default = "split"
```

#### Why `split` Is the Default (Supply Chain Security)

The `split` default protects your host from malicious packages installed inside the sandbox. A compromised package running under a sandboxed package manager (npm, pip, cargo, mise, etc.) could attempt to poison your host configs - for example, injecting a backdoor into `~/.gitconfig`, `~/.npmrc`, or shell startup files.

With `split`, config directories are mounted via `tmpoverlay`: any writes to them are discarded when the sandbox exits. Caches, data, and state directories use a persistent overlay so tool installations survive across sessions without touching your real host paths. Your host config files are never modified by sandboxed processes.

Use a broader mode only when you explicitly trust the project and need write-through to the host.

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

# Override the global [overlay] default for this tool's bindings (optional).
# Accepted values: "split", "overlay", "tmpoverlay", "readonly", "readwrite"
# mount_mode = "readwrite"
```

**Mode Details:**

| Mode       | gitconfig | Credentials | SSH Keys | GPG Keys | Use Case                    |
|------------|-----------|-------------|----------|----------|-----------------------------|
| `readonly` | Safe copy | No          | No       | No       | Default, maximum isolation  |
| `readwrite`| Full      | Read-only   | Read-only| Read-only| Trusted projects, push/sign |
| `disabled` | None      | No          | No       | No       | Fully anonymous git         |

In `readwrite` mode, SSH and GPG directories are mounted read-only to protect private keys
while still allowing git operations that need them.

#### Per-Tool Mount Mode Override

Each tool supports a `mount_mode` field that overrides the global `[overlay] default` for that tool's bindings:

```toml
[tools.git]
mount_mode = "readwrite"  # Override global default for this tool

[tools.claude]
mount_mode = "disabled"   # Don't mount any claude config into the sandbox
```

Valid per-tool values: `split`, `overlay`, `tmpoverlay`, `readonly`, `readwrite`, `disabled`.

The `disabled` value prevents the tool's config/cache/data directories from being mounted entirely - the tool won't have access to any host configuration. This is useful for tools you have installed on the host but don't want visible inside the sandbox.

#### Migrating Overlay Data to Host

Accumulated overlay data can be promoted to the host filesystem. See [Sandboxing: Migrating Overlay Data](sandboxing.md#migrating-overlay-data-to-host) for the `devsandbox overlay migrate` command reference.

#### Mise

```toml
[tools.mise]
# Override the global [overlay] default for mise's bindings (optional).
# Accepted values: "split", "overlay", "tmpoverlay", "readonly", "readwrite"
#
# The global default ("split") is recommended: mise data/cache directories use a
# persistent overlay so installed tools survive across sessions, while mise config
# files are protected via tmpoverlay.
#
# Set to "overlay" to persist all mise state (config + data) across sessions.
# Set to "tmpoverlay" to discard all mise state on sandbox exit.
# mount_mode = "overlay"
```

#### Docker

```toml
[tools.docker]
# Enable Docker socket proxy (disabled by default)
# When enabled, provides read-only access to Docker daemon
enabled = false

# Path to host Docker socket (optional)
# On Linux: defaults to /run/docker.sock
# On macOS: auto-detected (Docker Desktop, OrbStack, Colima)
# Set explicitly to override auto-detection:
# socket = "/path/to/docker.sock"
```

**Note:** Docker access is read-only. You can list/inspect containers, view logs, and exec into
running containers, but cannot create, delete, or modify containers. Only Unix socket access is
supported; TCP connections to remote Docker daemons are not proxied.

> **Security Warning**: Enabling Docker socket forwarding grants the sandbox read access
> to all Docker state and the ability to exec into any container on the host.
> See [Docker Socket Forwarding](sandboxing.md#docker-socket-forwarding--security-warning) for details.

See [docs/tools.md](tools.md#docker) for full details on allowed operations.

#### XDG Desktop Portal (Linux only)

```toml
[tools.portal]
# Allow sandboxed apps to send desktop notifications via xdg-desktop-portal.
# Requires: xdg-dbus-proxy, xdg-desktop-portal + a backend (e.g., -gtk, -kde)
# Default: true (enabled when requirements are met)
notifications = true
```

When enabled, a filtered D-Bus proxy exposes only the notification portal interface to the sandbox.
No other D-Bus services are accessible.

See [docs/tools.md](tools.md#xdg-desktop-portal-linux-only) for requirements and details.

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

# Optional: non-secret metadata headers (stored verbatim in the config)
headers = { "X-Team" = "platform" }
```

#### Authenticating to an Auth-Enforced Endpoint

To send logs to an endpoint that requires an auth header, use `header_sources`
instead of `headers`. Sources resolve at runtime from a host environment
variable, a file, or a literal value, so secrets stay out of the config file
(and out of the sandbox — header sources are resolved on the host).

```toml
[[logging.receivers]]
type = "otlp"
endpoint = "https://otel.example.com/v1/logs"
protocol = "http"

# Resolve Authorization from a host env var (e.g. OTLP_AUTH_TOKEN="Bearer abc…")
[logging.receivers.header_sources.Authorization]
env = "OTLP_AUTH_TOKEN"

# Or read from a file (whitespace is trimmed; ~ is expanded)
[logging.receivers.header_sources."X-API-Key"]
file = "~/.config/devsandbox/otlp-key"
```

Each source must set exactly one of `value`, `env`, or `file` (priority:
`value` > `env` > `file`). If a source resolves to an empty string (e.g. the
env var is unset or the file is empty), startup fails — devsandbox will not
silently send unauthenticated logs.

When the same header name appears in both `headers` and `header_sources`, the
source value wins.

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

### Audit Logging

Every dispatched log entry — proxy request logs, isolator (`builder`/`mounts`/`docker`) logs, the new wrapper banners, and synthesized lifecycle/security events — carries a fixed set of per-session fields suitable for ad-hoc audit query.

#### Per-entry session fields

| Field | Type | Source |
|---|---|---|
| `session_id` | UUIDv7 string | Generated once per `devsandbox claude` invocation. Sortable by time. |
| `sandbox_name` | string | Auto-resolved when proxy is enabled (e.g., `bold-falcon-12`); may be empty when proxy is disabled and `--name` was not passed. |
| `sandbox_path` | string | Sandbox root directory under `~/.local/share/devsandbox/`. |
| `project_dir` | string | The user's working directory mounted into the sandbox. |
| `isolator` | string | `bwrap` or `docker`. |
| `pid` | int | Wrapper process PID. |
| `devsandbox_version` | string | Build-injected `internal/version.Version`. |

These fields are injected by the dispatcher at write time. They appear on both OTLP (as record attributes) and syslog (inside the existing `Fields` JSON object — see "Syslog payload shape" below).

#### Lifecycle events

Two synthesized entries bookend each session.

**`session.start`** (level: `info`, `event=session.start`) is emitted once after the dispatcher and notice sink are wired up. Payload:

| Field | Description |
|---|---|
| `host` | `os.Hostname()` |
| `host_user` | `user.Current().Username` |
| `proxy_enabled` | bool |
| `proxy_port` | int (omitted when proxy is disabled) |
| `proxy_mitm` | bool |
| `filter_mode` | `off` / `allow` / `block` / `ask` |
| `filter_rule_count` | int |
| `redaction_rule_count` | int |
| `log_skip_rule_count` | int |
| `credential_injectors` | `[]string` — names only, no resolved values |
| `command` | wrapped command argv joined with spaces |
| `tty` | bool — was stdin a terminal at startup |
| `start_time` | RFC3339 timestamp |

**`session.end`** (level: `info`, `event=session.end`) is emitted from a deferred function before the dispatcher is closed. Payload:

| Field | Description |
|---|---|
| `exit_code` | int — 0 on normal exit, the wrapped command's exit code on failure (extracted from `*exec.ExitError`), `-1` on signal-driven shutdown, `1` on generic error |
| `duration_ms` | int — `time.Since(start).Milliseconds()` |
| `end_time` | RFC3339 timestamp |
| `proxy_request_count` | int — non-skipped requests handled by the proxy (0 if proxy disabled) |

#### Security events

Each event is dispatched through the same path with `event=<name>` set as a Field. Secret values are deliberately excluded from every event — only metadata (rule names, header names, hosts) appears.

| Event | Level | Trigger | Payload |
|---|---|---|---|
| `proxy.filter.decision` | `info` (allow) / `warn` (block, ask) | Filter engine evaluates a request | `host`, `method`, `path` (path-only — query string stripped), `rule_action`, `rule_id`, `default_action_used` |
| `proxy.redaction.applied` | `info` | One event per match when the redaction engine rewrites or blocks | `host`, `secret_kind` (rule name), `location` (`url` / `body` / `header:<name>`), `rule_id` |
| `proxy.credential.injected` | `info` | Credential injector successfully writes an auth header | `host`, `injector` (name), `header_name` |
| `proxy.mitm.bypass` | `info` | First CONNECT to a host in no-MITM mode (deduped per host per session) | `host`, `reason` (currently always `global`) |
| `mount.decision` | `info` | One event per successfully resolved mount, emitted from the mounts engine | `source`, `dest`, `mode` (`readonly` / `readwrite` / `tmpoverlay` / `overlay` / `hidden`), `policy` (`persistent` / `scratchpad` / `runtime`), `pattern` |
| `notice.overflow` | `warn` | The notice ring buffer (256 entries) overflowed before the dispatcher was attached | `dropped` (count), `component=wrapper` |

**Note on filter decision volume:** by default, only `block` / `ask` decisions emit events. `allow` decisions are gated behind `[logging] log_filter_decisions = true` so the audit log isn't flooded by routine traffic. Enable for short audit windows only.

#### Wrapper notice events

User-facing wrapper output (`notice.Info` / `notice.Warn` / `notice.Error`) — startup banners, MITM warnings, container lifecycle messages, proxy runtime errors — is forwarded through the dispatcher with `component=wrapper` and the configured level. Lines emitted before the dispatcher is wired up are buffered (max 256 entries) and drained when the dispatcher attaches.

#### Syslog payload shape

The existing syslog writer JSON-encodes each entry via `json.Marshal(entry)`, producing one record per syslog line in the shape:

```json
{
  "Timestamp": "2026-04-29T10:00:00Z",
  "Level": "info",
  "Message": "session.start",
  "Fields": {
    "event": "session.start",
    "session_id": "01HF...",
    "sandbox_name": "bold-falcon-12",
    "host": "...",
    "proxy_enabled": true,
    ...
  }
}
```

Per-session fields and event-specific fields appear inside `Fields`. `json.Marshal` sorts map keys deterministically, so syslog text is grep-friendly.

#### Example LogsQL queries (VictoriaLogs)

```
# All deny decisions in the last hour
{event="proxy.filter.decision"} | unpack_json | rule_action="block"

# Every secret redaction by rule
{event="proxy.redaction.applied"} | unpack_json | stats count() by (secret_kind)

# Sessions that ran with MITM disabled
{event="session.start"} | unpack_json | proxy_mitm="false"

# A single session's full audit trail
{session_id="01HF..."} | sort by (Timestamp)
```

#### Configuration flag

```toml
[logging]
# When true, every filter decision (allow/block/ask) emits a
# proxy.filter.decision event. When false (default), only block and ask
# decisions emit events.
log_filter_decisions = false
```

## Complete Example

```toml
# ~/.config/devsandbox/config.toml

[sandbox]
# Use auto-detection (bwrap on Linux, docker on macOS)
isolation = "auto"
# Use custom location for sandbox data
# base_path = "/data/devsandbox"

# Docker settings (used when isolation = "docker")
[sandbox.docker]
# Uses default Dockerfile at ~/.config/devsandbox/Dockerfile
# Uncomment to use a custom Dockerfile:
# dockerfile = "/path/to/custom/Dockerfile"
keep_container = true  # Keep containers for fast restarts

[sandbox.docker.resources]
memory = "4g"
cpus = "2"

[proxy]
# Enable proxy mode by default for this machine
enabled = true
port = 8080

# Inject GitHub token into API requests (keeps token out of sandbox)
[proxy.credentials.github]
enabled = true

# Scan outgoing requests for secrets
[proxy.redaction]
enabled = true
default_action = "block"

[[proxy.redaction.rules]]
name = "anthropic-key"
[proxy.redaction.rules.source]
env = "ANTHROPIC_API_KEY"

[overlay]
# Default mount mode for all tool bindings (split is the secure default)
default = "split"

[tools.git]
# Use readonly mode for most projects
mode = "readonly"

[tools.mise]
# Use default mount mode (split): mise data persists, config files are protected

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
- Missing include files produce a warning and are skipped. Parse errors in include files are fatal.

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

**Non-interactive mode:** When running non-interactively (e.g., via an AI assistant or in CI), untrusted local configs are skipped with a warning. Pre-approve configs with `devsandbox trust add` before running in non-interactive mode.

### Config Priority

Settings are merged in this order (later overrides earlier):

1. Built-in defaults (secure defaults, no proxy, bwrap on Linux / docker on macOS)
2. Global config (`~/.config/devsandbox/config.toml`)
3. Matching includes (in order they appear)
4. Local config (`.devsandbox.toml`)
5. Command line flags (highest priority)

**CLI flag examples:**

```bash
# Override proxy setting from config
devsandbox --proxy          # Enable even if config has enabled = false

# Override port
devsandbox --proxy --proxy-port 9090

# Ephemeral mode - remove sandbox state after exit
devsandbox --rm             # Docker: don't keep container; bwrap: remove sandbox home
```

**Merge rules:**

- Scalar values: later source wins
- Maps (`[tools]`): deep merge
- Arrays (`[[proxy.filter.rules]]`): concatenate (later rules have higher priority)
- Redaction: most-restrictive-wins - later configs can enable but never disable; `default_action` takes the higher severity (`block` > `redact` > `log`); rules are always additive

## See Also

- [Sandboxing](sandboxing.md) -- security model, custom mounts, overlay filesystem details
- [Proxy Mode](proxy.md) -- proxy usage, log viewing, HTTP filtering
- [Tools](tools.md) -- tool-specific behavior (git modes, mise, Docker socket proxy)
- [Use Cases](use-cases.md) -- practical workflows using these configuration options

[Back to docs index](README.md) | [Back to README](../README.md)
