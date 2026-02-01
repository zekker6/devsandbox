# Proxy Mode

Proxy mode creates a fully isolated network namespace where all HTTP/HTTPS traffic is routed through a local MITM (
Man-in-the-Middle) proxy. This allows inspection and logging of all network requests made by tools running inside the
sandbox.

## Why Use Proxy Mode?

- **Audit AI agent activity** - See exactly what API calls your AI coding assistant makes
- **Debug network issues** - Inspect request/response headers and bodies
- **Security monitoring** - Detect unexpected network connections
- **Compliance** - Log all external communications for review

## Requirements

Proxy mode requires [passt/pasta](https://passt.top/) for network namespace creation. This is the only feature that requires passt—basic sandboxing works without it.

```bash
# Arch Linux
sudo pacman -S passt

# Debian/Ubuntu
sudo apt install passt

# Fedora
sudo dnf install passt
```

Verify installation:

```bash
devsandbox doctor
```

If passt is not installed, the sandbox will still work but `--proxy` mode will be unavailable.

## Enabling Proxy Mode

### Command Line

```bash
# Enable proxy mode for this session
devsandbox --proxy

# With custom port
devsandbox --proxy --proxy-port 9090

# Run a command with proxy
devsandbox --proxy npm install
```

### Configuration File

Enable proxy mode by default in `~/.config/devsandbox/config.toml`:

```toml
[proxy]
enabled = true
port = 8080
```

## How It Works

1. **Network Isolation** - pasta creates a new network namespace with its own network stack
2. **Gateway Setup** - Traffic is routed through a virtual gateway (10.0.2.2)
3. **Proxy Server** - A local HTTP/HTTPS proxy runs on the host
4. **Traffic Enforcement** - All HTTP(S) traffic must go through the proxy
5. **TLS Interception** - A generated CA certificate enables HTTPS inspection

### Network Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                        Host System                          │
│  ┌─────────────────┐                                        │
│  │  Proxy Server   │◄─────┐                                 │
│  │  127.0.0.1:8080 │      │                                 │
│  └────────┬────────┘      │                                 │
│           │               │                                 │
│           ▼               │                                 │
│      Internet             │ pasta NAT                       │
│                           │ (10.0.2.2 → 127.0.0.1)          │
├───────────────────────────┼─────────────────────────────────┤
│                           │        Sandbox (netns)          │
│                    ┌──────┴──────┐                          │
│                    │   Gateway   │                          │
│                    │  10.0.2.2   │                          │
│                    └──────▲──────┘                          │
│                           │                                 │
│                    ┌──────┴──────┐                          │
│                    │ Application │                          │
│                    │ HTTP_PROXY= │                          │
│                    │ 10.0.2.2:   │                          │
│                    └─────────────┘                          │
└─────────────────────────────────────────────────────────────┘
```

## CA Certificate

A CA certificate is automatically generated for HTTPS interception and stored at:

```
~/.local/share/devsandbox/<project>/.ca/ca.crt
```

Inside the sandbox, the certificate is available at `/tmp/devsandbox-ca.crt` and automatically configured via
environment variables:

| Variable              | Purpose         |
|-----------------------|-----------------|
| `NODE_EXTRA_CA_CERTS` | Node.js         |
| `REQUESTS_CA_BUNDLE`  | Python requests |
| `CURL_CA_BUNDLE`      | curl            |
| `GIT_SSL_CAINFO`      | Git HTTPS       |
| `SSL_CERT_FILE`       | General SSL/TLS |

### Tools with Certificate Pinning

Some tools implement certificate pinning and won't work with the MITM proxy:

- Mobile app backends
- Some cloud SDKs
- Security-focused applications

## Viewing Logs

### Proxy Request Logs

View HTTP/HTTPS traffic captured in proxy mode:

```bash
# View all proxy logs for current project
devsandbox logs proxy

# View last 50 requests
devsandbox logs proxy --last 50

# Follow/tail logs in real-time
devsandbox logs proxy -f
```

### Filtering Logs

```bash
# Filter by time
devsandbox logs proxy --since 1h          # Last hour
devsandbox logs proxy --since today       # Since midnight
devsandbox logs proxy --since 2024-01-15  # Since specific date
devsandbox logs proxy --until 2024-01-15T12:00:00

# Filter by content
devsandbox logs proxy --url /api          # URL contains "/api"
devsandbox logs proxy --method POST       # Only POST requests
devsandbox logs proxy --status 200        # Specific status code
devsandbox logs proxy --status 400-599    # Status code range
devsandbox logs proxy --status ">=400"    # Comparison
devsandbox logs proxy --errors            # All errors (status >= 400)

# Combine filters
devsandbox logs proxy --method POST --url /api --since 1h
```

### Output Formats

```bash
# Table format (default)
devsandbox logs proxy

# Compact one-line format
devsandbox logs proxy --compact

# JSON output (for scripting)
devsandbox logs proxy --json

# Include request/response bodies
devsandbox logs proxy --body

# Show summary statistics
devsandbox logs proxy --stats

# Disable colors (for piping)
devsandbox logs proxy --no-color
```

### Example Output

**Table format:**

```
┌──────────┬────────┬────────┬──────────┬────────────────────────┐
│   TIME   │ METHOD │ STATUS │ DURATION │          URL           │
├──────────┼────────┼────────┼──────────┼────────────────────────┤
│ 10:30:05 │ GET    │ 200    │ 150ms    │ https://api.example.com│
│ 10:30:06 │ POST   │ 201    │ 89ms     │ https://api.example.com│
└──────────┴────────┴────────┴──────────┴────────────────────────┘
```

**Compact format:**

```
10:30:05 GET  200 150ms https://api.example.com/users
10:30:06 POST 201  89ms https://api.example.com/orders
```

**Stats output:**

```
Summary:
  Total requests: 150
  Success (2xx):  120 (80.0%)
  Redirect (3xx): 10 (6.7%)
  Client err (4xx): 15 (10.0%)
  Server err (5xx): 5 (3.3%)
  Avg duration: 245ms
```

## Log Storage

Logs are stored as gzip-compressed JSONL files:

```
~/.local/share/devsandbox/<project>/logs/
├── proxy/
│   ├── requests_20240115_0000.jsonl.gz
│   ├── requests_20240115_0001.jsonl.gz
│   └── ...
└── internal/
    ├── proxy_20240115_0000.log.gz
    └── logging-errors.log
```

### Log Rotation

- Files rotate when they reach 50MB
- Maximum 5 files kept per type
- Older files are automatically pruned

### Log Entry Format

Each log entry contains:

```json
{
  "ts": "2024-01-15T10:30:05.123Z",
  "method": "POST",
  "url": "https://api.example.com/users",
  "req_headers": {
    "Content-Type": [
      "application/json"
    ],
    "Authorization": [
      "Bearer ..."
    ]
  },
  "req_body": "eyJ1c2VyIjogImpvaG4ifQ==",
  "status": 201,
  "resp_headers": {
    "Content-Type": [
      "application/json"
    ]
  },
  "resp_body": "eyJpZCI6IDEyM30=",
  "duration_ns": 89000000,
  "error": ""
}
```

Note: Request/response bodies are base64-encoded.

## Internal Logs

View proxy server errors and warnings:

```bash
# View all internal logs
devsandbox logs internal

# Filter by log type
devsandbox logs internal --type proxy    # Proxy server logs
devsandbox logs internal --type logging  # Remote logging errors

# Follow internal logs
devsandbox logs internal -f

# Show last N lines
devsandbox logs internal --last 100
```

## Remote Logging

Proxy logs can be forwarded to remote destinations.
See [Configuration - Remote Logging](configuration.md#remote-logging) for setup instructions.

## HTTP Filtering

HTTP filtering allows you to control which requests are allowed, blocked, or require user approval.

### How It Works

Filtering is enabled by setting `default_action` which determines what happens to requests that don't match any rule:

| Default Action | Behavior |
|----------------|----------|
| `block` | Block unmatched requests (whitelist behavior) |
| `allow` | Allow unmatched requests (blacklist behavior) |
| `ask` | Prompt user for each unmatched request |

### Quick Start

```bash
# Whitelist behavior - only allow specific domains, block everything else
devsandbox --proxy --filter-default=block \
  --allow-domain="*.github.com" \
  --allow-domain="api.anthropic.com"

# Blacklist behavior - block specific domains, allow everything else
devsandbox --proxy --filter-default=allow \
  --block-domain="*.tracking.io" \
  --block-domain="ads.example.com"

# Ask mode - interactive approval for unmatched requests
devsandbox --proxy --filter-default=ask
```

### Configuration File

Add filter rules to `~/.config/devsandbox/config.toml`:

```toml
[proxy.filter]
# Enable filtering with default action for unmatched requests
default_action = "block"  # whitelist behavior
ask_timeout = 30
cache_decisions = true

[[proxy.filter.rules]]
pattern = "*.github.com"
action = "allow"
scope = "host"

[[proxy.filter.rules]]
pattern = "api.anthropic.com"
action = "allow"
scope = "host"

[[proxy.filter.rules]]
pattern = "*.internal.corp"
action = "block"
scope = "host"
reason = "Internal network blocked"
```

### Pattern Types

Default is `glob`. Patterns containing regex characters (`^$|()[]{}\+`) are auto-detected as regex.

| Type | Example | Description |
|------|---------|-------------|
| `glob` | `*.example.com` | Glob patterns (* and ?) - **default** |
| `exact` | `api.example.com` | Exact string match |
| `regex` | `^api\.(dev\|prod)\.com$` | Regular expressions |

### Scopes

Default is `host`.

| Scope | Description | Example Match |
|-------|-------------|---------------|
| `host` | Request host only - **default** | `api.example.com` |
| `path` | Request path only | `/api/v1/users` |
| `url` | Full URL | `https://api.example.com/v1/users` |

### Ask Mode

In ask mode, unmatched requests require user approval via a separate monitor terminal.

**Step 1**: Start the sandbox with ask mode:

```bash
devsandbox --proxy --filter-default=ask
```

The sandbox will display:

```
Filter: ask mode (default action for unmatched requests)

Run in another terminal to approve/deny requests:
  devsandbox proxy monitor

Requests without response within 30s will be rejected.
```

**Step 2**: Open another terminal (in the same project directory) and run the monitor:

```bash
devsandbox proxy monitor
```

The socket path is auto-detected from the current directory's sandbox. You can also specify it explicitly:

```bash
devsandbox proxy monitor /path/to/ask.sock
```

The monitor displays incoming requests:

```
┌──────────────────────────────────────────────────────────────────┐
│  Request #1                                                      │
├──────────────────────────────────────────────────────────────────┤
│  Method: GET                                                     │
│  Host:   api.example.com                                         │
│  Path:   /v1/users                                               │
├──────────────────────────────────────────────────────────────────┤
│  [A]llow    [B]lock    Allow [S]ession    Block [N]ever         │
└──────────────────────────────────────────────────────────────────┘
Decision:
```

**Keys** (instant response, no Enter needed):
- `a` - Allow this request
- `b` - Block this request
- `s` - Allow and remember for session
- `n` - Block and remember for session

**Timeout**: Requests that don't receive a response within 30 seconds are automatically rejected and logged to internal logs as unanswered.

### Generate Filter Rules from Logs

Analyze existing proxy logs to generate filter configuration:

```bash
# Generate whitelist rules from current project's logs (default: block unmatched)
devsandbox proxy filter generate

# Generate from specific log directory
devsandbox proxy filter generate --from-logs ~/.local/share/devsandbox/myproject/logs/proxy/

# Generate blacklist rules (allow unmatched)
devsandbox proxy filter generate --default-action allow

# Save to file
devsandbox proxy filter generate -o filter-rules.toml

# Only include domains with 5+ requests
devsandbox proxy filter generate --min-requests 5
```

### Show Current Configuration

```bash
devsandbox proxy filter show
```

### Filter Logs

Filter decisions are logged with requests:

```json
{
  "ts": "2024-01-15T10:30:05Z",
  "method": "GET",
  "url": "https://blocked.example.com/",
  "status": 403,
  "filter_action": "block",
  "filter_reason": "matched rule: *.blocked.com"
}
```

## Troubleshooting

### "proxy mode requires pasta"

Install passt:

```bash
# Check with doctor
devsandbox doctor

# Install (see Requirements above)
```

### Requests timing out

1. Check if the target allows proxy connections
2. Some services block known proxy IPs
3. Try accessing the URL directly to verify it's reachable

### Certificate errors

1. Ensure the CA environment variables are set correctly
2. Some tools require manual CA configuration
3. Certificate pinning may prevent interception

### No logs appearing

1. Verify proxy mode is enabled: `devsandbox --proxy --info`
2. Check the log directory exists
3. Make HTTP requests (not just TCP connections)
