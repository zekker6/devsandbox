# Configuration

Complete configuration reference for devsandbox.

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

The proxy can inject authentication credentials into requests for specific domains, keeping tokens out of the sandbox environment. Credentials are read from host environment variables and added to matching requests transparently.

```toml
[proxy.credentials.github]
# Inject GitHub API token into requests to api.github.com.
# Reads from GITHUB_TOKEN or GH_TOKEN environment variable on the host.
# The token is added as a Bearer Authorization header if not already present.
enabled = true

# Optional: override the default token source.
# When configured, this takes precedence over the default environment variables.
# Exactly one of env, file, or value should be set.
#
# [proxy.credentials.github.source]
# env = "DEVSANDBOX_GITHUB_TOKEN"    # Read from a custom environment variable
# file = "~/.config/devsandbox/github-token"  # Read from a file (~ expanded)
# value = "github_pat_..."           # Static value (use env or file instead when possible)
```

**How it works:**

1. The proxy intercepts outgoing requests from the sandbox.
2. For each registered credential injector, it checks if the request matches (e.g., host is `api.github.com`).
3. If matched and no `Authorization` header is already present, the injector adds the credential header.
4. The sandbox process never sees the token -- it stays on the host side.

**Available injectors:**

| Name     | Matches              | Default Environment Variable  | Header                        |
|----------|----------------------|-------------------------------|-------------------------------|
| `github` | `api.github.com`     | `GITHUB_TOKEN` or `GH_TOKEN`  | `Authorization: Bearer <token>` |

Default environment variables are used when no `[proxy.credentials.<name>.source]` is configured. When a source is configured, it takes precedence and defaults are ignored.

**Source types:**

| Field   | Description                                | Example                                      |
|---------|--------------------------------------------|----------------------------------------------|
| `env`   | Read from an environment variable          | `env = "DEVSANDBOX_GITHUB_TOKEN"`            |
| `file`  | Read from a file (supports `~` expansion, whitespace trimmed) | `file = "~/.config/devsandbox/github-token"` |
| `value` | Static value in config                     | `value = "github_pat_..."`                   |

When multiple fields are set, priority is: `value` > `env` > `file`. Set exactly one for clarity.

**Notes:**

- Credential injection requires proxy mode (`--proxy`).
- Injectors are only active when explicitly `enabled = true` and the credential resolves to a non-empty value.
- Unknown injector names in the config produce a warning and are skipped.
- The injector never overwrites an existing `Authorization` header on the request.

> **AI agent workflow:** Credential injection is particularly useful for AI coding assistants like Claude Code that need GitHub API access. The token stays on the host -- the AI agent never sees it, but its API requests to github.com are automatically authenticated.

### Content Redaction

The proxy can scan outgoing requests for secrets and take action before they leave your machine. Redaction scanning checks request bodies, headers, and URLs against configured rules.

```toml
[proxy.redaction]
# Enable content redaction scanning (requires proxy mode)
enabled = true

# Action when a secret is detected and the rule has no override
# "block" (default) - reject the request with HTTP 403
# "redact" - replace the secret with [REDACTED:<rule-name>] and forward
# "log" - allow the request but log a warning
default_action = "block"
```

#### Rule Types

Rules detect secrets using either a **source** (exact value lookup) or a **pattern** (regex match). Each rule can optionally override the default action.

**Source-based rules** — resolve a secret value and scan for exact matches:

```toml
# From environment variable
[[proxy.redaction.rules]]
name = "api-key"
action = "block"
[proxy.redaction.rules.source]
env = "API_SECRET_KEY"

# From file (whitespace trimmed, supports ~ expansion)
[[proxy.redaction.rules]]
name = "db-password"
[proxy.redaction.rules.source]
file = "~/.config/myapp/db-password"

# From .env file in project directory
[[proxy.redaction.rules]]
name = "app-secret"
[proxy.redaction.rules.source]
env_file_key = "APP_SECRET"

# Static value (prefer env or file for real secrets)
[[proxy.redaction.rules]]
name = "test-token"
[proxy.redaction.rules.source]
value = "test-secret-value"
```

**Source types:**

| Field | Description | Example |
|-------|-------------|---------|
| `env` | Environment variable on the host | `env = "API_SECRET_KEY"` |
| `file` | File path (supports `~`, whitespace trimmed) | `file = "~/.secrets/token"` |
| `env_file_key` | Key in project `.env` file | `env_file_key = "DB_PASSWORD"` |
| `value` | Static value in config | `value = "literal-secret"` |

**Pattern-based rules** — match regex patterns anywhere in the request:

```toml
[[proxy.redaction.rules]]
name = "openai-keys"
action = "redact"
pattern = "sk-[a-zA-Z0-9]{20,}"

[[proxy.redaction.rules]]
name = "aws-keys"
action = "block"
pattern = "AKIA[0-9A-Z]{16}"
```

#### Actions

| Action | Behavior | Request | Logged |
|--------|----------|---------|--------|
| `block` | Reject with HTTP 403 | Not forwarded | Yes (redacted) |
| `redact` | Replace secret with `[REDACTED:<name>]` | Forwarded (modified) | Yes (redacted) |
| `log` | Allow unmodified | Forwarded (original) | Yes (original) |

When multiple rules match, the most severe action wins: **block > redact > log**.

#### AI Agent Example

Prevent an AI coding assistant from leaking your API keys:

```toml
[proxy]
enabled = true

[proxy.redaction]
enabled = true
default_action = "block"

# Block if any of these secrets appear in outgoing requests
[[proxy.redaction.rules]]
name = "anthropic-key"
[proxy.redaction.rules.source]
env = "ANTHROPIC_API_KEY"

[[proxy.redaction.rules]]
name = "openai-pattern"
pattern = "sk-[a-zA-Z0-9]{20,}"

# Redact GitHub token (allow request with token replaced)
[[proxy.redaction.rules]]
name = "github-token"
action = "redact"
[proxy.redaction.rules.source]
env = "GITHUB_TOKEN"
```

**Notes:**

- Content redaction requires proxy mode (`--proxy`).
- All source values must resolve at startup. If an environment variable is missing or a file is unreadable, devsandbox exits with an error (fail-closed).
- Log entries for blocked and redacted requests have secrets replaced — secrets never appear in proxy logs.
- Redaction rules are always additive when merging configs. The default action uses most-restrictive-wins (see [Config Priority](#config-priority)).
- Redaction rules must not match values used by credential injectors. If a redaction rule (source or pattern) would match an injected credential, devsandbox exits with an error at startup. This prevents the confusing situation where credential injection adds a token and redaction immediately blocks it.

### Avoiding GitHub Rate Limits (Recommended for macOS)

This is optional but strongly recommended. Without it, tool installation via mise inside the sandbox may hit GitHub's 60 requests/hour limit, causing transient failures during initial setup.

On macOS, devsandbox uses the Docker backend. When [mise](tools.md#tool-management-with-mise) installs or updates tools inside the sandbox, it downloads releases from GitHub. Unauthenticated GitHub API requests are limited to **60 per hour** — easily exhausted when populating the tool cache for the first time.

Enabling credential injection with a **read-only** GitHub token raises this limit to **5,000 requests per hour**. A token with no permissions granted is sufficient — it only needs to authenticate requests, not access private resources.

**Step 1: Create a fine-grained personal access token**

1. Go to [GitHub Settings → Fine-grained tokens](https://github.com/settings/personal-access-tokens/new)
2. Set a descriptive name (e.g., `devsandbox-mise`)
3. Set expiration as desired
4. Under **Repository access**, select "Public Repositories (read-only)"
5. Under **Permissions**, grant nothing — leave all permissions at "No access"
6. Click **Generate token**

**Step 2: Set the environment variable**

Add to your shell profile (`~/.zshrc`, `~/.bashrc`, etc.):

```bash
export GITHUB_TOKEN="github_pat_..."
```

**Step 3: Enable credential injection**

In `~/.config/devsandbox/config.toml`:

```toml
[proxy]
enabled = true

[proxy.credentials.github]
enabled = true
```

The proxy injects the token into GitHub API requests automatically. The token never enters the sandbox environment — it stays on the host side and is added to matching requests by the proxy.

> **Tip:** To avoid conflicts with `gh` CLI or other tools that read `GITHUB_TOKEN`, use a dedicated environment variable:
>
> ```bash
> export DEVSANDBOX_GITHUB_TOKEN="github_pat_..."
> ```
>
> ```toml
> [proxy.credentials.github]
> enabled = true
>
> [proxy.credentials.github.source]
> env = "DEVSANDBOX_GITHUB_TOKEN"
> ```

> **Security note:** A fine-grained token with no permissions granted provides only public read access. This is the minimum needed to avoid rate limits. Do not use tokens with write permissions for this purpose.

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

Instead of pre-configuring port rules, you can forward ports to a running sandbox on demand.

**Auto-detect listening ports** (opt-in, disabled by default for security):

```toml
[port_forwarding]
# Automatically detect and forward listening ports inside the sandbox
auto_detect = false

# How often to scan for new listening ports
scan_interval = "2s"

# Ports to never auto-forward (e.g., internal services)
exclude_ports = [22, 80, 443]
```

When `auto_detect = true`, devsandbox monitors the sandbox for new TCP listeners and automatically forwards them to the same port on `127.0.0.1`. Auto-detect requires proxy mode: without an isolated network namespace the sandbox and host share the same kernel port space, so a userland forwarder would collide with the sandbox listener on the same port (and the port is already directly accessible on `127.0.0.1` anyway). If auto-detect is enabled without proxy mode, devsandbox logs a notice and skips auto-forward at session start. When the preferred host port genuinely happens to be in use on the host under proxy mode, devsandbox falls back to an ephemeral host port chosen by the OS and logs the mapping (e.g. `Auto-forwarding port 37127 → 127.0.0.1:45821 (host port 37127 was in use)`); the actual host port is recorded in the session registry and visible via `devsandbox sessions`. Ports below 1024 are always excluded.

**Manual forwarding to a running sandbox:**

```bash
# Forward a single port (host:3000 → sandbox:3000)
devsandbox forward 3000

# Forward sandbox port 3000 to host port 8080
devsandbox forward 3000:8080

# Target a specific sandbox by name
devsandbox forward --name myapp 3000

# Bind to all interfaces (for LAN access)
devsandbox forward --bind 0.0.0.0 3000

# Forward multiple ports at once
devsandbox forward 3000 5173 9090
```

Port spec format: `<sandbox_port>[:<host_port>]`. The sandbox port (the dev server you want to reach) comes first. If `host_port` is omitted, it defaults to the same as `sandbox_port`.

**Named sessions:**

Use `--name` when starting a sandbox to give it a human-readable identifier:

```bash
devsandbox --proxy --name myapp
```

If omitted, the name is auto-generated from the working directory basename.

**List running sessions:**

```bash
devsandbox sessions
devsandbox sessions --json
```

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

The `split` default protects your host from malicious packages installed inside the sandbox. A compromised package running under a sandboxed package manager (npm, pip, cargo, mise, etc.) could attempt to poison your host configs — for example, injecting a backdoor into `~/.gitconfig`, `~/.npmrc`, or shell startup files.

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

The `disabled` value prevents the tool's config/cache/data directories from being mounted entirely — the tool won't have access to any host configuration. This is useful for tools you have installed on the host but don't want visible inside the sandbox.

#### Migrating Overlay Data to Host

Under the default `split` policy, category-`data` and category-`cache` bindings mount as persistent overlays. Writes made inside the sandbox (e.g. Claude Code session JSONLs under `~/.claude/projects`, installed mise tools under `~/.local/share/mise`) accumulate in the sandbox's overlay upper directory under `~/.local/share/devsandbox/<sandbox>/home/overlay/…/upper/` and are **never** promoted to the real host path.

If you want to flip a binding from `overlay`/`split` to `readwrite` — or just surface accumulated sandbox state onto the host — use `devsandbox overlay migrate`:

```bash
# Preview (dry-run, default): shows what would be written, overwritten, deleted.
devsandbox overlay migrate --sandbox my-project --tool claude

# Apply for real:
devsandbox overlay migrate --sandbox my-project --tool claude --apply

# Promote overlay data from every sandbox into the host path in one go:
devsandbox overlay migrate --all-sandboxes --tool claude --apply

# Target an arbitrary host path rather than a tool:
devsandbox overlay migrate --sandbox my-project --path ~/.local/share/mise --apply

# After migration, flip the binding to readwrite so future writes go straight to host:
devsandbox overlay migrate --sandbox my-project --tool claude --apply --set-mode readwrite
```

Flags:

| Flag | Purpose |
|---|---|
| `--sandbox NAME` | Operate on one sandbox (mutually exclusive with `--all-sandboxes`). |
| `--all-sandboxes` | Iterate every sandbox under `~/.local/share/devsandbox/`. |
| `--path HOST_PATH` | Promote a specific host path (mutually exclusive with `--tool`). |
| `--tool NAME` | Shorthand: expand to every overlay binding the named tool declares. |
| `--primary-only` | Ignore per-session uppers; only promote the primary persistent upper. |
| `--apply` | Actually perform the migration (default is dry-run). |
| `--set-mode MODE` | After a successful apply, set the tool's `mount_mode` in `.devsandbox.toml`. Requires `--tool`. |
| `--force` | Proceed even if a targeted sandbox appears to have an active session (racy — only use when you're sure). |
| `--yes` | Skip the multi-sandbox confirmation prompt when `--set-mode` would touch more than one config file. |

Safety model:

- **Dry-run by default.** Nothing is written without `--apply`. The preview lists every create / overwrite / delete the apply phase would perform.
- **Stopped-sandbox check.** The command refuses to run if any targeted sandbox has an active session (`--force` bypasses).
- **Last-write-wins across stacked uppers.** The primary persistent upper comes first, followed by per-session uppers in mtime order. The most recent upper's version of any file wins.
- **Whiteouts honored.** Files the sandbox deleted (overlayfs char-device whiteouts) become host-file deletions on apply.
- **No automatic host backup.** If you want one, make it yourself before passing `--apply`.

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

# Ephemeral mode — remove sandbox state after exit
devsandbox --rm             # Docker: don't keep container; bwrap: remove sandbox home
```

**Merge rules:**

- Scalar values: later source wins
- Maps (`[tools]`): deep merge
- Arrays (`[[proxy.filter.rules]]`): concatenate (later rules have higher priority)
- Redaction: most-restrictive-wins — later configs can enable but never disable; `default_action` takes the higher severity (`block` > `redact` > `log`); rules are always additive

## See Also

- [Sandboxing](sandboxing.md) -- security model, custom mounts, overlay filesystem details
- [Proxy Mode](proxy.md) -- proxy usage, log viewing, HTTP filtering
- [Tools](tools.md) -- tool-specific behavior (git modes, mise, Docker socket proxy)
- [Use Cases](use-cases.md) -- practical workflows using these configuration options

[Back to docs index](README.md) | [Back to README](../README.md)
