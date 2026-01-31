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

Proxy mode requires [passt/pasta](https://passt.top/) for network namespace creation:

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
