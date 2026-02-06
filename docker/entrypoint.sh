#!/bin/bash
set -e

# 1. Create user matching host UID/GID
HOST_UID=${HOST_UID:-1000}
HOST_GID=${HOST_GID:-1000}

# Create group if it doesn't exist
if ! getent group sandboxuser >/dev/null 2>&1; then
    groupadd -g "$HOST_GID" sandboxuser 2>/dev/null || true
fi

# Create user if it doesn't exist
if ! id sandboxuser >/dev/null 2>&1; then
    useradd -u "$HOST_UID" -g "$HOST_GID" -m -d /home/sandboxuser -s /bin/bash sandboxuser 2>/dev/null || true
fi

# Fix ownership of sandbox home
chown -R "$HOST_UID:$HOST_GID" /home/sandboxuser 2>/dev/null || true

# 2. Hide .env files (if enabled)
# PROJECT_DIR is passed from devsandbox, defaults to /workspace for backward compat
WORK_DIR="${PROJECT_DIR:-/workspace}"

if [ "$HIDE_ENV_FILES" = "true" ]; then
    env_hide_failed=0
    env_files_found=0
    while IFS= read -r f; do
        if [ -f "$f" ]; then
            env_files_found=1
            if ! mount --bind /dev/null "$f" 2>/dev/null; then
                env_hide_failed=1
            fi
        fi
    done < <(find "$WORK_DIR" -maxdepth 5 \( -name '.env' -o -name '.env.*' \) 2>/dev/null)

    if [ "$env_files_found" = "1" ] && [ "$env_hide_failed" = "1" ]; then
        echo "Warning: .env file hiding requires elevated privileges (--privileged or CAP_SYS_ADMIN)" >&2
        echo "         .env files are visible in this container" >&2
    fi
fi

# 3. Setup mise for project (as sandboxuser)
# Trust any mise config files that might be mounted from host
# Note: mise install is NOT run here to avoid slowing container startup
# Tools should be installed manually if needed: mise install
gosu sandboxuser bash -c 'mise trust --all 2>/dev/null || true'

if [ -f "$WORK_DIR/.mise.toml" ] || [ -f "$WORK_DIR/.tool-versions" ]; then
    gosu sandboxuser bash -c "cd '$WORK_DIR' && mise trust 2>/dev/null || true"
fi

# 4. Create cache directories if cache volume is mounted
if [ -d /cache ]; then
    mkdir -p /cache/mise /cache/mise/cache /cache/go/mod /cache/go/build 2>/dev/null || true
    chown -R "$HOST_UID:$HOST_GID" /cache 2>/dev/null || true
fi

# 5. Setup network isolation (if proxy mode enabled)
if [ "$PROXY_MODE" = "true" ]; then
    PROXY_HOST=${PROXY_HOST:-host.docker.internal}
    PROXY_PORT=${PROXY_PORT:-8080}

    # Try pasta-based isolation first
    if command -v pasta >/dev/null 2>&1 && [ -x /usr/bin/pasta ]; then
        # Pasta network isolation would be set up here
        # For now, fall back to HTTP_PROXY
        :
    fi

    # Fallback: Set HTTP_PROXY environment variables
    export HTTP_PROXY="http://${PROXY_HOST}:${PROXY_PORT}"
    export HTTPS_PROXY="$HTTP_PROXY"
    export http_proxy="$HTTP_PROXY"
    export https_proxy="$HTTPS_PROXY"
    export no_proxy="localhost,127.0.0.1"
fi

# 6. Set up environment for sandboxuser
export HOME=/home/sandboxuser
export USER=sandboxuser

# XDG directories - ensure these are set before any shell initialization
# These may be passed from Docker -e flags, but we set defaults to ensure
# fish and other tools can find their data directories
export XDG_CONFIG_HOME="${XDG_CONFIG_HOME:-/home/sandboxuser/.config}"
export XDG_DATA_HOME="${XDG_DATA_HOME:-/home/sandboxuser/.local/share}"
export XDG_CACHE_HOME="${XDG_CACHE_HOME:-/home/sandboxuser/.cache}"
export XDG_STATE_HOME="${XDG_STATE_HOME:-/home/sandboxuser/.local/state}"

# Fish shell data directory - explicitly set to avoid path resolution issues
# Fish uses this for universal variables (fish_variables file)
export __fish_user_data_dir="$XDG_DATA_HOME/fish"

# Mise directories in sandbox home for persistence across container runs
# This caches both downloads and installed tools
export MISE_DATA_DIR=/home/sandboxuser/.local/share/mise
export MISE_CACHE_DIR=/home/sandboxuser/.cache/mise
export MISE_STATE_DIR=/home/sandboxuser/.local/state/mise

# Create directories if they don't exist (mise, fish, ssh, etc.)
# Note: Some of these may be overwritten by read-only mounts from the host,
# but we create them anyway so tools have valid paths to work with
mkdir -p "$MISE_DATA_DIR" "$MISE_CACHE_DIR" "$MISE_STATE_DIR" \
    "$XDG_DATA_HOME/fish" \
    "$XDG_STATE_HOME" \
    "$XDG_CONFIG_HOME" \
    "$XDG_CACHE_HOME" \
    /home/sandboxuser/.ssh 2>/dev/null || true

# Always create a fresh fish_variables file
# This ensures universal variables from previous sessions (which may contain
# incorrect paths like /home/zekker instead of /home/sandboxuser) are cleared.
# Fish conf.d scripts like z.fish use set -U which requires this file.
: > "$XDG_DATA_HOME/fish/fish_variables" 2>/dev/null || true

# Create empty ssh environment file for fish-ssh-agent and similar scripts
# that expect this file to exist even when SSH is not configured
: > /home/sandboxuser/.ssh/environment 2>/dev/null || true
chmod 600 /home/sandboxuser/.ssh/environment 2>/dev/null || true

chown -R "$HOST_UID:$HOST_GID" /home/sandboxuser/.local /home/sandboxuser/.cache /home/sandboxuser/.config /home/sandboxuser/.ssh 2>/dev/null || true

# Ensure mise and tools are in PATH for the user
# Order: /usr/local/bin (claude, nvim, gh) -> user mise shims -> container mise shims
export PATH="/usr/local/bin:$MISE_DATA_DIR/shims:/opt/mise/shims:$PATH"
# Use system config as fallback for container-installed tools
export MISE_SYSTEM_CONFIG_FILE=/etc/mise/config.toml

# 7. Suppress ssh-agent when SSH is not forwarded
# Shell plugins (e.g. fish-ssh-agent) auto-start ssh-agent on every shell launch.
# When SSH_AUTH_SOCK is not forwarded, shadow the binary with a no-op wrapper.
if [ -z "$SSH_AUTH_SOCK" ]; then
    cat > /usr/local/bin/ssh-agent <<'WRAPPER'
#!/bin/sh
exit 0
WRAPPER
    chmod +x /usr/local/bin/ssh-agent
fi

# 8. Drop privileges and exec user command
exec gosu sandboxuser "$@"
