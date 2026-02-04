# Sandboxing

devsandbox uses [bubblewrap](https://github.com/containers/bubblewrap) on Linux or Docker containers on macOS to create
isolated environments for running untrusted code.

## Isolation Backends

devsandbox supports two isolation backends:

| Backend | Platform | Description |
|---------|----------|-------------|
| `bwrap` | Linux only | Uses bubblewrap for namespace-based isolation. Preferred on Linux. |
| `docker` | Linux, macOS | Uses Docker containers. Required for macOS, optional on Linux. |

### Automatic Selection

By default (`--isolation=auto`), devsandbox selects the best backend for your platform:
- **Linux**: Uses `bwrap` (bubblewrap)
- **macOS**: Uses `docker`

### Manual Override

Force a specific backend with the `--isolation` flag:

```bash
# Use Docker on Linux
devsandbox --isolation=docker

# Explicitly use bwrap (Linux only)
devsandbox --isolation=bwrap
```

Or configure in `~/.config/devsandbox/config.toml`:

```toml
[sandbox]
isolation = "docker"  # "auto", "bwrap", or "docker"
```

## Security Model

| Resource                          | Access                              |
|-----------------------------------|-------------------------------------|
| Project directory                 | Read/Write                          |
| `.env` files                      | Hidden (overlaid with /dev/null)    |
| `~/.ssh`                          | Not mounted (configurable)          |
| `~/.gitconfig`                    | Sanitized copy (configurable)       |
| `~/.aws`, `~/.azure`, `~/.gcloud` | Not mounted                         |
| mise-managed tools                | Read-only or overlay                |
| Custom mount rules                | User-configurable (see below)       |
| Network (default)                 | Full access                         |
| Network (proxy mode)              | Isolated, routed through MITM proxy |

### What's Not Available (by default)

**SSH Keys** - The `~/.ssh` directory is not mounted by default:

- SSH authentication requires `git.mode = "readwrite"` (see [configuration](configuration.md))
- Use HTTPS for git operations in default mode
- SSH agent forwarding is available in readwrite mode

**Cloud Credentials** - These directories are not mounted:

- `~/.aws` (AWS CLI credentials)
- `~/.azure` (Azure CLI credentials)
- `~/.gcloud` (Google Cloud SDK credentials)
- `~/.config/gcloud` (Google Cloud config)

**Environment Files** - All `.env` and `.env.*` files in your project are overlaid with `/dev/null`, preventing secrets
from being read by sandboxed code.

**Git Credentials** - By default, `~/.gitconfig` is replaced with a sanitized copy containing only user.name and email.
Use `git.mode = "readwrite"` for full git access.

### What's Available

**Project Directory** - Full read/write access to the current directory and all subdirectories (except `.env` files).

**Development Tools** - All tools managed by [mise](https://mise.jdx.dev/) are available read-only:

- Node.js, Python, Go, Rust, etc.
- Package managers (npm, pip, cargo)
- Build tools

**Network** - By default, full network access is allowed. Use [proxy mode](proxy.md) to isolate and inspect traffic.

## How It Works

### Filesystem Isolation

Bubblewrap creates a new mount namespace with selective bind mounts:

```
/ (new root)
├── /usr, /lib, /lib64, /bin  → Host system (read-only)
├── /etc                       → Host /etc (read-only, some files masked)
├── /tmp                       → Fresh tmpfs
├── /home/user                 → Sandbox home (~/.local/share/devsandbox/<project>/home)
├── /home/user/.config/mise   → Host mise config (read-only)
└── /path/to/project          → Host project directory (read/write)
```

### PID Isolation

Processes inside the sandbox:

- Cannot see processes running outside
- Cannot send signals to external processes
- Have their own PID namespace (init is PID 1)

### User Namespace

devsandbox uses unprivileged user namespaces:

- No root privileges required
- UID/GID mapping preserves file ownership
- Works on most modern Linux distributions

## Data Locations

Sandbox data follows XDG conventions:

```
~/.local/share/devsandbox/<project-hash>/
├── home/                    # Sandbox $HOME (isolated per-project)
│   ├── .config/             # XDG_CONFIG_HOME
│   ├── .local/share/        # XDG_DATA_HOME
│   ├── .cache/              # XDG_CACHE_HOME
│   │   ├── go-build/        # Go build cache
│   │   └── go-mod/          # Go module cache
│   └── go/                  # GOPATH
├── .ca/                     # Proxy CA (if proxy mode used)
│   ├── ca.crt
│   └── ca.key
└── logs/
    ├── proxy/               # HTTP request logs
    └── internal/            # Internal error logs
```

### Project Naming

Sandbox directories are named `<project-basename>-<hash>` where the hash is derived from the full project path. This
ensures:

- Unique sandboxes for projects with the same name in different locations
- Predictable directory names for the same project

## Environment Variables

Inside the sandbox, these environment variables are set:

| Variable             | Value                | Description                             |
|----------------------|----------------------|-----------------------------------------|
| `DEVSANDBOX`         | `1`                  | Indicates running inside sandbox        |
| `DEVSANDBOX_PROJECT` | Project name         | Base name of project directory          |
| `DEVSANDBOX_PROXY`   | `1`                  | Set only in proxy mode                  |
| `GOTOOLCHAIN`        | `local`              | Prevents Go from downloading toolchains |
| `HOME`               | Sandbox home         | Points to isolated home directory       |
| `XDG_CONFIG_HOME`    | `$HOME/.config`      | XDG config directory                    |
| `XDG_DATA_HOME`      | `$HOME/.local/share` | XDG data directory                      |
| `XDG_CACHE_HOME`     | `$HOME/.cache`       | XDG cache directory                     |

## Managing Sandboxes

### Listing Sandboxes

```bash
# List all sandboxes
devsandbox sandboxes list

# Include disk usage (slower)
devsandbox sandboxes list --size

# JSON output for scripting
devsandbox sandboxes list --json

# Sort by last used
devsandbox sandboxes list --sort used
```

### Pruning Sandboxes

```bash
# Remove orphaned sandboxes (project directory no longer exists)
devsandbox sandboxes prune

# Keep only 5 most recently used
devsandbox sandboxes prune --keep 5

# Remove sandboxes unused for 30 days
devsandbox sandboxes prune --older-than 30d

# Remove all sandboxes
devsandbox sandboxes prune --all

# Preview what would be removed
devsandbox sandboxes prune --dry-run
```

## Troubleshooting

### Checking Installation

```bash
devsandbox doctor
```

This verifies:

- Required binaries (bwrap, shell)
- Optional binaries (pasta for proxy mode)
- User namespace support
- Directory permissions

### Debug Mode

```bash
# Show bwrap arguments
DEVSANDBOX_DEBUG=1 devsandbox
```

### Common Issues

**"User namespaces not enabled"**

- Check kernel config: `cat /proc/sys/kernel/unprivileged_userns_clone`
- Enable: `sudo sysctl kernel.unprivileged_userns_clone=1`

**"bwrap not found"**

- Install bubblewrap for your distribution (see main README)

**"Permission denied" on project files**

- Ensure your user owns the project directory
- Check for ACLs that might interfere

## Overlay Filesystem

By default, mise directories are mounted read-only. Use overlayfs to allow installing tools inside the sandbox without
modifying host files.

### Enabling Writable Mise

Configure in `~/.config/devsandbox/config.toml`:

```toml
[tools.mise]
writable = true    # Allow installing tools
persistent = false # Discard on exit (or true to persist)
```

### How Overlay Works

Overlayfs creates a layered filesystem:

```
┌─────────────────────────────────────┐
│  Sandbox view (merged)              │  ← What you see inside sandbox
├─────────────────────────────────────┤
│  Upper layer (writable)             │  ← New/modified files go here
├─────────────────────────────────────┤
│  Lower layer (host, read-only)      │  ← Original host files
└─────────────────────────────────────┘
```

- **tmpoverlay** (default): Upper layer is tmpfs, discarded on exit
- **overlay** (persistent): Upper layer stored in `~/.local/share/devsandbox/<project>/home/overlay/`

### Configuration

Set defaults in `~/.config/devsandbox/config.toml`:

```toml
[overlay]
# Master switch - disable to force read-only everywhere
enabled = true

[tools.mise]
writable = true    # Allow mise to install tools
persistent = false # Discard changes on exit (safer)
```

### Use Cases

- Install project-specific tool versions without polluting host
- Test new tool versions before committing to them
- Allow AI assistants to install tools they need temporarily

## Custom Mounts

Beyond the built-in security rules, you can configure custom mount rules to control exactly what the sandbox can access.

### Modes

| Mode         | Behavior                                                     |
|--------------|--------------------------------------------------------------|
| `readonly`   | Host path visible inside sandbox, but writes blocked         |
| `readwrite`  | Full read/write access to host path                          |
| `hidden`     | Path replaced with `/dev/null` (files only)                  |
| `overlay`    | Writes saved to sandbox home, host unchanged                 |
| `tmpoverlay` | Writes go to tmpfs, discarded on exit                        |

### How Custom Mounts Work

Custom mounts are processed **before** the sandbox home is mounted. This means:

1. Home directory paths (`~/.config/myapp`) are mounted from the **host**
2. Project-relative patterns (`**/secrets/**`) are applied to the project directory
3. First matching rule wins when patterns overlap

```
Mount Order:
1. System bindings (/usr, /lib, /etc)
2. Network bindings (/etc/resolv.conf, etc)
3. Custom mounts ← Your rules applied here
4. Sandbox home (~/)
5. Project directory
6. Tool bindings (mise, git, etc)
```

### Pattern Matching

Patterns use glob syntax with the [doublestar](https://github.com/bmatcuk/doublestar) library:

| Pattern             | Description                           |
|---------------------|---------------------------------------|
| `~/.config/myapp`   | Exact path (~ expanded to $HOME)      |
| `*.conf`            | Files ending in .conf (current dir)   |
| `**/*.key`          | All .key files at any depth           |
| `**/secrets/**`     | Anything under any "secrets" dir      |
| `/opt/tools`        | Absolute path (no expansion)          |

### Examples

**Mount application config:**

```toml
[[sandbox.mounts.rules]]
pattern = "~/.config/myapp"
mode = "readonly"
```

**Hide project secrets:**

```toml
[[sandbox.mounts.rules]]
pattern = "**/secrets/**"
mode = "hidden"
```

**Writable cache with persistence:**

```toml
[[sandbox.mounts.rules]]
pattern = "~/.cache/expensive-builds"
mode = "overlay"
```

**Temporary scratch space:**

```toml
[[sandbox.mounts.rules]]
pattern = "~/.local/share/myapp/tmp"
mode = "tmpoverlay"
```

### Viewing Active Mounts

Use `--info` to see what custom mounts are active:

```bash
devsandbox --info
```

Output includes a "Custom Mounts" section if rules are configured.

## Docker Backend

The Docker backend provides isolation via containers, enabling macOS support and simplified distribution.

### Docker-Specific Configuration

Configure Docker settings in `~/.config/devsandbox/config.toml`:

```toml
[sandbox]
isolation = "docker"

[sandbox.docker]
# Custom Docker image (default: ghcr.io/zekker6/devsandbox:latest)
image = "ghcr.io/zekker6/devsandbox:latest"

# Pull policy: "always", "missing", "never"
pull_policy = "missing"

# Hide .env files in container (requires --privileged or CAP_SYS_ADMIN)
hide_env_files = true

# Resource limits
[sandbox.docker.resources]
memory = "4g"
cpus = "2"
```

### How Docker Backend Works

The Docker backend:

1. **UID/GID Mapping**: Creates a container user matching your host UID/GID for proper file ownership
2. **Project Mount**: Mounts your project directory at `/workspace` (read/write)
3. **Sandbox Home**:
   - **Linux**: Bind mount for consistency with bwrap
   - **macOS**: Named volume for better performance
4. **Tool Configs**: Mounts nvim, tmux, starship configs read-only from host
5. **Proxy Support**: Uses `host.docker.internal` for proxy connections

### Docker Image

The default image (`ghcr.io/zekker6/devsandbox:latest`) includes:
- Debian slim base
- mise for tool management
- Common development tools (git, curl, bash, zsh)
- gosu for privilege dropping
- passt/pasta for network isolation (if needed)

### Building Custom Images

Extend the base image for project-specific needs:

```dockerfile
FROM ghcr.io/zekker6/devsandbox:latest

# Add project-specific tools
RUN apt-get update && apt-get install -y postgresql-client

# Pre-install mise tools
RUN mise install node@20 python@3.12
```

Then configure:

```toml
[sandbox.docker]
image = "my-custom-image:latest"
pull_policy = "never"
```

### Platform Differences

| Feature | Linux (bwrap) | Linux (docker) | macOS (docker) |
|---------|---------------|----------------|----------------|
| Sandbox home | Bind mount | Bind mount | Named volume |
| File performance | Native | Near-native | Slower (volume) |
| .env hiding | Overlay | Entrypoint | Entrypoint |
| Network isolation | pasta | HTTP_PROXY | HTTP_PROXY |
| Host integration | Full | Via mounts | Via mounts |

### Known Limitations (Docker)

- **No pasta network**: Network isolation uses HTTP_PROXY instead of pasta
- **Proxy filtering not supported**: The proxy server and request filtering features are not available with the Docker backend
- **.env hiding requires privileges**: The `hide_env_files` feature requires `--privileged` or `CAP_SYS_ADMIN`; without these, .env files remain visible (with a warning)
- **macOS file performance**: Named volumes are slower than native filesystem
- **inotify limitations**: File watching may be less reliable on macOS
- **No nested Docker**: Cannot run Docker commands inside the sandbox

## Limitations

### Bwrap Backend (Linux)
- **Linux only** - Uses Linux-specific namespaces and capabilities
- **User namespaces required** - Most modern distros have this enabled
- **No nested containers** - Running Docker inside the sandbox is not supported
- **Some tools may break** - Tools that require specific system access may fail
- **Overlay requires kernel support** - Most modern kernels (3.18+) support overlayfs

### Docker Backend (All Platforms)
- **Docker required** - Docker Desktop or Docker Engine must be installed and running
- **No pasta network** - Uses HTTP_PROXY for network isolation instead
- **Performance on macOS** - File operations may be slower due to volume mounts
- **Image pull** - First run requires downloading the base image (~200MB)
