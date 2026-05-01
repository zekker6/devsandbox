# Sandboxing

How filesystem, process, and network isolation works in devsandbox.

devsandbox uses [bubblewrap](https://github.com/containers/bubblewrap) on Linux or Docker containers on macOS to create
isolated environments for running untrusted code. On Linux, bwrap and pasta binaries are embedded — no system packages
required. To use system-installed binaries instead, see [configuration](configuration.md).

> **macOS users:** devsandbox uses the Docker backend on macOS. Skip to [How It Works (Docker)](#how-it-works-docker) for platform-relevant details, or see [Platform Differences](#platform-differences) for a comparison table.

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

### Choosing a Backend (Linux)

| Choose bwrap when | Choose Docker when |
|---|---|
| You want sub-second startup | SELinux/AppArmor blocks namespace operations |
| You prefer minimal dependencies | You need Dockerfile customization |
| You want native filesystem performance | You want consistency with macOS teammates |
| You're on a standard kernel with user namespaces | You prefer container-based isolation |

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

**Environment Files** - Files matching `.env` and `.env.*` patterns (e.g., `.env`, `.env.local`, `.env.production`) in your project are overlaid with `/dev/null`, preventing secrets from being read by sandboxed code. Files like `config.env` that don't start with `.env` are not hidden.

**Git Credentials** - By default, `~/.gitconfig` is replaced with a sanitized copy containing only user.name and email.
Use `git.mode = "readwrite"` for full git access.

### What's Available

**Project Directory** - Full read/write access to the current directory and all subdirectories (except `.env` files).

**Development Tools** - All tools managed by [mise](https://mise.jdx.dev/) are available read-only:

- Node.js, Python, Go, Rust, etc.
- Package managers (npm, pip, cargo)
- Build tools

**Network** - By default, full network access is allowed. Use [proxy mode](proxy.md) to isolate and inspect traffic.

## How It Works (bwrap)

The following sections describe how the bwrap (bubblewrap) backend implements isolation on Linux. For Docker-specific behavior, see [Docker Backend](#docker-backend-all-platforms) below.

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

> **macOS (Docker backend):** The sandbox home directory is stored as a named Docker volume rather than at the host path shown above. Use `docker volume ls | grep devsandbox` to see volumes, or `devsandbox sandboxes list` to view all sandboxes with their storage type.

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

# Skip disk usage calculation (faster)
devsandbox sandboxes list --no-size

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

## Port Forwarding

### Runtime Port Forwarding

Instead of pre-configuring static port rules, you can forward ports to a running sandbox on demand.

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

When `auto_detect = true`, devsandbox monitors the sandbox for new TCP listeners and automatically forwards them to the same port on `127.0.0.1`. Auto-detect requires proxy mode: without an isolated network namespace the sandbox and host share the same kernel port space, so a userland forwarder would collide with the sandbox listener on the same port (and the port is already directly accessible on `127.0.0.1` anyway). If auto-detect is enabled without proxy mode, devsandbox logs a notice and skips auto-forward at session start. When the preferred host port is already in use, devsandbox falls back to an ephemeral host port chosen by the OS and logs the mapping; the actual host port is recorded in the session registry and visible via `devsandbox sessions`. Ports below 1024 are always excluded.

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

## Troubleshooting

### Checking Installation

```bash
devsandbox doctor
```

This verifies:

- Required binaries (bwrap — embedded or system-installed)
- Optional binaries (pasta for proxy mode — embedded or system-installed)
- User namespace support
- Directory permissions
- Overlayfs support (for tool writable layers)
- Docker availability and daemon status
- Docker base image (`ghcr.io/zekker6/devsandbox:latest`) presence
- Configuration file validity
- Kernel version
- Recent errors in internal logs
- Detected development tools (mise, editors, etc.)

### Debug Mode

```bash
# Show bwrap arguments
DEVSANDBOX_DEBUG=1 devsandbox
```

### Common Issues

**"Docker daemon not running" (macOS)**

Start your Docker runtime:

```bash
# Docker Desktop
open -a Docker

# OrbStack
open -a OrbStack

# Colima
colima start
```

Then verify with `devsandbox doctor`.

**"User namespaces not enabled"**

This varies by distribution:

- **Arch Linux / Fedora**: User namespaces are enabled by default. If you see this error, check for a hardened kernel with `kernel.userns_restrict` and adjust if needed.
- **Debian / Ubuntu (< 23.10)**: May need `sudo sysctl -w kernel.unprivileged_userns_clone=1`. The sysctl `kernel.unprivileged_userns_clone` is a Debian/Ubuntu-specific patch and does not exist on all kernels.
- **Ubuntu 23.10+**: AppArmor may restrict unprivileged user namespaces even when the sysctl is set. Run `sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0` or create an AppArmor profile exception for devsandbox.
- **Hardened kernels**: Check `kernel.userns_restrict` and adjust if needed.

If namespace restrictions cannot be resolved, use the Docker backend instead (`--isolation=docker`).

**"bwrap not found"**

- devsandbox includes embedded bwrap — this error means extraction failed and no system package is installed
- Check `devsandbox doctor` output for details (embedded vs system source)
- Install bubblewrap as a fallback: see [Installation](getting-started/install.md)
- To disable embedded binaries and use only system packages, set `use_embedded = false` in [configuration](configuration.md)

**"Permission denied" on project files**

- Ensure your user owns the project directory
- Check for ACLs that might interfere

### Security Modules

devsandbox does not include SELinux or AppArmor handling. On systems with these security modules, sandbox operations may be blocked.

**SELinux (Fedora, RHEL, CentOS):**

- SELinux policy may block bwrap namespace operations
- Workaround: `sudo setsebool -P user_namespace_allowed 1` or create a custom policy module
- Check audit log: `sudo ausearch -m avc -ts recent`

**AppArmor (Ubuntu, Debian):**

- On Ubuntu 23.10+, `kernel.apparmor_restrict_unprivileged_userns=1` is enabled by default
- Workaround: `sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0`
- Alternative: Create an AppArmor profile exception for devsandbox

**Fallback:** If security module restrictions cannot be resolved, use the Docker backend (`--isolation=docker`) which avoids direct namespace creation.

## Overlay Filesystem

By default, tool directories use the `split` mount mode: config paths get a tmpoverlay (discarded on exit) while caches and data get a persistent overlay. This protects host configs from supply chain attacks while preserving expensive caches across sessions. Use the `[overlay]` section to change the global default, or per-tool `mount_mode` overrides to fine-tune individual tools.

### Configuration

Set the global default in `~/.config/devsandbox/config.toml`:

```toml
[overlay]
# Default mount mode for all tool bindings
# split (default): configs → tmpoverlay, caches/data → persistent overlay
default = "split"

[tools.mise]
# Override for mise: persist all state across sessions
mount_mode = "overlay"
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

- **tmpoverlay**: Upper layer is tmpfs, discarded on exit
- **overlay** (persistent): Upper layer stored in `~/.local/share/devsandbox/<project>/home/overlay/`

### Choosing an Overlay Mode

| Mode | Writes persist? | Host modified? | Use when |
|---|---|---|---|
| `split` (default) | Caches/data: yes. Configs: no | Never | Default — protects host configs from supply chain attacks |
| `overlay` | Yes (all) | Never | You want all tool state to persist across sessions |
| `tmpoverlay` | No | Never | Disposable experiments, CI, untrusted code |
| `readonly` | No (writes fail) | Never | Maximum lockdown, no tool installation |
| `readwrite` | Yes | Yes | Trusted projects where you want write-through to host |

### Use Cases

- Install project-specific tool versions without polluting host (`overlay` or `split`)
- Test new tool versions before committing to them (`tmpoverlay`)
- Allow AI assistants to install tools they need temporarily (`split` keeps caches, discards config changes)
- Run untrusted code with no persistent state (`tmpoverlay`)
- Trusted projects where you want direct host access (`readwrite`)

### Migrating Overlay Data to Host

Under the default `split` policy, category-`data` and category-`cache` bindings mount as persistent overlays. Writes made inside the sandbox (e.g. Claude Code session JSONLs under `~/.claude/projects`, installed mise tools under `~/.local/share/mise`) accumulate in the sandbox's overlay upper directory under `~/.local/share/devsandbox/<sandbox>/home/overlay/.../upper/` and are **never** promoted to the real host path.

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

**Flags:**

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

**Safety model:**

- **Dry-run by default.** Nothing is written without `--apply`. The preview lists every create / overwrite / delete the apply phase would perform.
- **Stopped-sandbox check.** The command refuses to run if any targeted sandbox has an active session (`--force` bypasses).
- **Last-write-wins across stacked uppers.** The primary persistent upper comes first, followed by per-session uppers in mtime order. The most recent upper's version of any file wins.
- **Whiteouts honored.** Files the sandbox deleted (overlayfs char-device whiteouts) become host-file deletions on apply.
- **No automatic host backup.** If you want one, make it yourself before passing `--apply`.

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

## How It Works (Docker)

The Docker backend provides isolation via containers, enabling macOS support and simplified distribution. The following sections are Docker-specific. For bwrap behavior, see [How It Works (bwrap)](#how-it-works-bwrap) above.

### Docker-Specific Configuration

Configure Docker settings in `~/.config/devsandbox/config.toml`. See [Configuration: Isolation Backend](configuration.md#isolation-backend) for the full TOML reference.

### Container Persistence

By default, Docker containers are kept after exit to enable fast restarts. This significantly improves startup time from ~5-10 seconds to ~1-2 seconds.

**Container Lifecycle:**

| State | Behavior |
|-------|----------|
| Container doesn't exist | Creates new container, then starts it |
| Container stopped | Starts existing container (~1-2s) |
| Container running | Exec into running container (instant) |

**Container Naming:**

Containers are named `devsandbox-<project>-<hash>` where hash is derived from the project path:
- Same project directory always gets the same container
- Different directories with same project name get different containers

**Configuration:**

```toml
[sandbox.docker]
keep_container = true   # Keep container after exit (default)
keep_container = false  # Remove container on exit
```

**One-off Fresh Containers:**

Use `--rm` flag for ephemeral mode — sandbox state is removed after exit:

```bash
devsandbox --rm
```

This works for both backends:
- **Docker**: don't keep container after exit (fresh container each run)
- **bwrap**: remove sandbox home directory after exit

**Managing Containers:**

```bash
# List all sandboxes including Docker container state
devsandbox sandboxes list

# Prune stopped Docker containers
devsandbox sandboxes prune

# Force remove specific container
docker rm -f devsandbox-myproject-abc123
```

The `sandboxes list` command shows container state (running/stopped/exited) in the STATUS column.

### How Docker Backend Works

The Docker backend:

1. **UID/GID Mapping**: Creates a container user matching your host UID/GID for proper file ownership
2. **Project Mount**: Mounts your project directory at its host path (read/write)
3. **Sandbox Home**:
   - **Linux**: Bind mount for consistency with bwrap
   - **macOS**: Named volume for better performance
4. **Tool Configs**: Mounts nvim, tmux, starship configs read-only from host
5. **Proxy Support**: Uses `host.docker.internal` for proxy connections

### Docker Image

devsandbox uses a Dockerfile-based workflow. On first run, a default Dockerfile is created at
`~/.config/devsandbox/Dockerfile` with:

```dockerfile
FROM ghcr.io/zekker6/devsandbox:latest
```

The image is rebuilt on every sandbox start. Docker layer caching keeps this fast when the Dockerfile
hasn't changed.

The base image (`ghcr.io/zekker6/devsandbox:latest`) includes:
- Debian slim base
- mise for tool management
- Common development tools (git, curl, bash, zsh)
- gosu for privilege dropping
- passt/pasta for network isolation (if needed)

### Customizing the Image

Edit the default Dockerfile at `~/.config/devsandbox/Dockerfile` to add tools globally:

```dockerfile
FROM ghcr.io/zekker6/devsandbox:latest

# Add project-specific tools
RUN apt-get update && apt-get install -y postgresql-client

# Pre-install mise tools
RUN mise install node@20 python@3.12
```

The global Dockerfile produces a `devsandbox:local` image tag.

### Per-Project Dockerfiles

For project-specific customizations, point to a different Dockerfile in `.devsandbox.toml`:

```toml
[sandbox.docker]
dockerfile = "Dockerfile.devsandbox"
```

Per-project Dockerfiles produce a `devsandbox:<project-name>-<hash>` image tag.

### Manual Image Build

Build the image without starting a sandbox:

```bash
devsandbox image build
```

### Platform Differences

| Feature | Linux (bwrap) | Linux (docker) | macOS (docker) |
|---------|---------------|----------------|----------------|
| Sandbox home | Bind mount | Bind mount | Named volume |
| File performance | Native | Near-native | Slower (volume) |
| .env hiding | Overlay | Volume mount | Volume mount |
| Network isolation | pasta | HTTP_PROXY | HTTP_PROXY |
| Host integration | Full | Via mounts | Via mounts |

### macOS Docker Environments

#### Supported Runtimes

| Runtime | Notes |
|---------|-------|
| [Docker Desktop](https://docs.docker.com/desktop/install/mac-install/) | Official Docker runtime. Best compatibility. |
| [OrbStack](https://orbstack.dev/) | Lightweight alternative. Faster startup, lower resource usage. |
| [Colima](https://github.com/abiosoft/colima) | Free, open-source. Uses Lima VMs. |

All three provide a Docker-compatible daemon. devsandbox auto-detects the Docker socket location (see [tools.md](tools.md#docker) for socket detection details).

#### Recommended Resources

Docker Desktop (or equivalent) should be configured with at least:
- **RAM**: 4 GB+
- **CPUs**: 2+

These are Docker Desktop minimum allocations. devsandbox containers use these resources but can be further constrained via `[sandbox.docker.resources]` in your config. The container limit cannot exceed what Docker Desktop allocates. Lower values work but may slow builds and tool installations inside the sandbox.

#### Volume Performance

On macOS, the sandbox home directory uses a **named Docker volume** instead of a bind mount. This is necessary because bind mounts on macOS go through a virtualization layer (VirtioFS or gRPC-FUSE) that can be 2-5x slower than native filesystem access.

Named volumes store data inside the Docker VM's filesystem, providing near-native performance for operations like `npm install`, Go builds, and other I/O-heavy tasks. The tradeoff is that volume contents are not directly accessible from the macOS Finder — use `devsandbox sandboxes list` to view sizes and `devsandbox sandboxes prune` to reclaim space. To see Docker volumes directly: `docker volume ls | grep devsandbox`.

#### File Watching Limitations

File watching (inotify) across the macOS ↔ Docker boundary can be unreliable. If your dev server doesn't detect file changes, configure it to use polling:

```bash
# Next.js / webpack
WATCHPACK_POLLING=true devsandbox npm run dev

# Vite
devsandbox -- npx vite --force

# Generic: set CHOKIDAR_USEPOLLING for chokidar-based watchers
CHOKIDAR_USEPOLLING=true devsandbox npm run dev
```

### Docker Socket Forwarding — Security Warning

When `[tools.docker] enabled = true`, the sandbox gains filtered access to the host
Docker daemon. The proxy allows:

- **Read operations**: List all containers, images, volumes, and networks on the host
- **Exec/attach**: Execute commands inside ANY running container on the host

This is **intentional** — it enables Docker-in-Docker workflows. However, it
effectively grants the sandbox **host-level access via Docker**, which is often
equivalent to root. **Only enable this for trusted code.**

Container creation, deletion, and image manipulation are blocked by the proxy filter.

### Known Limitations (Docker)

- **No pasta network**: Network isolation uses HTTP_PROXY instead of pasta
- **Proxy filtering**: Proxy mode with request filtering is supported via per-session Docker networks
- **.env hiding**: Uses Docker volume mounts (`-v /dev/null`) to hide `.env` files — no special privileges required
- **macOS file performance**: Named volumes are slower than native filesystem
- **inotify limitations**: File watching may be less reliable on macOS
- **No nested Docker**: Cannot run Docker commands inside the sandbox

## Limitations

### Bwrap Backend (Linux)
- **Linux only** - Uses Linux-specific namespaces and capabilities
- **User namespaces required** - Most modern distros have this enabled
- **No nested containers** - Running Docker inside the sandbox is not supported
- **Some tools may break** - Tools that require specific system access may fail
- **Overlay requires kernel support** - Unprivileged overlayfs (inside user namespaces) requires kernel 5.11+

### Docker Backend (All Platforms)
- **Docker required** - Docker Desktop or Docker Engine must be installed and running
- **No pasta network** - Uses HTTP_PROXY for network isolation instead
- **Performance on macOS** - File operations may be slower due to volume mounts
- **Image build** - First run builds the image from a Dockerfile (requires downloading the base image ~200MB)

## See Also

- [Proxy Mode](proxy.md) -- network isolation and traffic inspection
- [Tools](tools.md) -- how development tools are made available inside the sandbox
- [Configuration](configuration.md) -- config file reference, custom mounts, overlay settings
- [Use Cases](use-cases.md) -- workflows and shell setup

[Back to docs index](README.md) | [Back to README](../README.md)
