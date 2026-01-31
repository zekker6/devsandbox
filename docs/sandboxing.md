# Sandboxing

devsandbox uses [bubblewrap](https://github.com/containers/bubblewrap) to create isolated environments for running
untrusted code.

## Security Model

| Resource                          | Access                              |
|-----------------------------------|-------------------------------------|
| Project directory                 | Read/Write                          |
| `.env` files                      | Blocked (overlaid with /dev/null)   |
| `~/.ssh`                          | Blocked                             |
| `~/.gitconfig`                    | Sanitized (no credentials)          |
| `~/.aws`, `~/.azure`, `~/.gcloud` | Blocked                             |
| mise-managed tools                | Read-only                           |
| Network (default)                 | Full access                         |
| Network (proxy mode)              | Isolated, routed through MITM proxy |

### What's Blocked

**SSH Keys** - The `~/.ssh` directory is not mounted, preventing:

- SSH authentication to remote servers
- Git operations over SSH (use HTTPS instead)
- Any SSH-based tooling

**Cloud Credentials** - These directories are blocked:

- `~/.aws` (AWS CLI credentials)
- `~/.azure` (Azure CLI credentials)
- `~/.gcloud` (Google Cloud SDK credentials)
- `~/.config/gcloud` (Google Cloud config)

**Environment Files** - All `.env` and `.env.*` files in your project are overlaid with `/dev/null`, preventing secrets
from being read.

**Git Credentials** - The `~/.gitconfig` file is sanitized to remove credential helpers and tokens.

### What's Allowed

**Project Directory** - Full read/write access to the current directory and all subdirectories (except blocked files).

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

## Limitations

- **Linux only** - Uses Linux-specific namespaces and capabilities
- **User namespaces required** - Most modern distros have this enabled
- **No nested containers** - Running Docker inside the sandbox is not supported
- **Some tools may break** - Tools that require specific system access may fail
