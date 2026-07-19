# ---- Stage 1: Build Go shim ----
FROM golang:1.26-trixie AS shim-builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/devsandbox-shim/ ./cmd/devsandbox-shim/
COPY internal/notice/ ./internal/notice/
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /devsandbox-shim ./cmd/devsandbox-shim

# ---- Stage 2: Runtime ----
FROM debian:13-slim

# Build-time configuration.
#
# SANDBOX_HOME/SANDBOX_UID/SANDBOX_GID are the single definition of the sandbox
# account's home and ownership for this image; every path and ENV below derives
# from them. They are LOAD-BEARING and coupled to the Go code - `sandboxHome` in
# cmd/devsandbox-shim/main.go and `containerHome` in internal/isolator/docker.go
# hardcode the same home path (the shim also creates the user at runtime). Do not
# override them at build time without updating those consts, or the shim's
# privilege-drop and the isolator's mounts will break.
#
# NODE_VERSION is the baked node (the AI CLIs run on it). INSTALL_GH/INSTALL_NVIM
# gate optional tools that are off by default to keep the base image slim; rebuild
# with `--build-arg INSTALL_GH=true` / `INSTALL_NVIM=true` (or add them in a
# derived `FROM` Dockerfile) to re-enable.
ARG SANDBOX_HOME=/home/sandboxuser
ARG SANDBOX_UID=1000
ARG SANDBOX_GID=1000
ARG NODE_VERSION=22
ARG INSTALL_GH=false
ARG INSTALL_NVIM=false

# Avoid interactive prompts during package installation
ENV DEBIAN_FRONTEND=noninteractive

# Install core utilities (gosu removed — shim drops privileges directly)
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    bash \
    zsh \
    fish \
    passt \
    iproute2 \
    procps \
    unzip \
    libatomic1 \
    openssh-client \
    jq \
    && rm -rf /var/lib/apt/lists/*

# Install mise
RUN curl https://mise.run | sh && \
    mv /root/.local/bin/mise /usr/local/bin/mise && \
    chmod +x /usr/local/bin/mise

# Install Node.js via mise (needed for AI tools). Build-time MISE_DATA_DIR points
# at /opt/mise so the baked node lands there; the runtime data dir (set at the end)
# is the persistent sandbox home, into which the in-guest shim seeds this node.
ENV MISE_DATA_DIR=/opt/mise
ENV MISE_CACHE_DIR=/opt/mise/cache
RUN mise install node@${NODE_VERSION} && \
    mise use --global node@${NODE_VERSION}

# Add mise shims to PATH for subsequent commands
ENV PATH="/opt/mise/shims:${PATH}"

# Install neovim from GitHub releases (multi-arch: amd64 and arm64). Optional:
# off by default, enable with --build-arg INSTALL_NVIM=true.
RUN if [ "$INSTALL_NVIM" = "true" ]; then \
        ARCH=$(dpkg --print-architecture) && \
        if [ "$ARCH" = "arm64" ] || [ "$ARCH" = "aarch64" ]; then \
            NVIM_ARCH="arm64"; \
        else \
            NVIM_ARCH="x86_64"; \
        fi && \
        curl -fsSL "https://github.com/neovim/neovim/releases/latest/download/nvim-linux-${NVIM_ARCH}.tar.gz" | \
        tar xz -C /opt && \
        NVIM_DIR=$(ls -d /opt/nvim-linux-* | head -1) && \
        ln -s "${NVIM_DIR}/bin/nvim" /usr/local/bin/nvim; \
    fi

# Install Claude Code
RUN npm install -g @anthropic-ai/claude-code && \
    CLAUDE_BIN="$(npm prefix -g)/bin/claude" && \
    if [ -e "$CLAUDE_BIN" ]; then \
        ln -sf "$CLAUDE_BIN" /usr/local/bin/claude; \
    else \
        echo "ERROR: claude binary not found at $CLAUDE_BIN after npm install" >&2 && exit 1; \
    fi

# Install GitHub CLI (for Copilot). Optional: off by default, enable with
# --build-arg INSTALL_GH=true.
RUN if [ "$INSTALL_GH" = "true" ]; then \
        curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg && \
        chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg && \
        echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list && \
        apt-get update && apt-get install -y gh && rm -rf /var/lib/apt/lists/*; \
    fi

# Install Docker CLI (for Docker socket proxy access inside sandbox)
RUN install -m 0755 -d /etc/apt/keyrings && \
    curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc && \
    chmod a+r /etc/apt/keyrings/docker.asc && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian bookworm stable" > /etc/apt/sources.list.d/docker.list && \
    apt-get update && apt-get install -y --no-install-recommends docker-ce-cli && rm -rf /var/lib/apt/lists/*

# Install opencode (multi-arch: amd64 and arm64)
RUN ARCH=$(dpkg --print-architecture) && \
    curl -fsSL https://opencode.ai/install | bash -s -- --no-modify-path && \
    if [ -f /root/.opencode/bin/opencode ]; then \
        mv /root/.opencode/bin/opencode /usr/local/bin/opencode; \
    elif [ -f /usr/local/bin/opencode ]; then \
        true; \
    else \
        echo "ERROR: opencode binary not found after install" >&2 && exit 1; \
    fi && \
    rm -rf /root/.opencode && \
    opencode --help >/dev/null 2>&1 || (echo "ERROR: opencode is not functional" >&2 && exit 1)

# Install codex (OpenAI Codex CLI)
RUN npm install -g @openai/codex || echo "Warning: codex package install failed, skipping" >&2

# Create sandbox user directory structure
RUN mkdir -p ${SANDBOX_HOME} && \
    chmod 755 ${SANDBOX_HOME}

# Grant the sandbox user ownership of mise directories so they can use/install tools.
# 775 allows owner+group full access; others can read+execute but not write.
RUN chown -R ${SANDBOX_UID}:${SANDBOX_GID} /opt/mise && chmod -R 775 /opt/mise

# Create system-level mise config for container tools
# This ensures container-installed tools are available regardless of host config
RUN mkdir -p /etc/mise && \
    printf '[tools]\nnode = "%s"\n' "${NODE_VERSION}" > /etc/mise/config.toml

# Runtime environment. This ENV block is the single source of truth for the
# container's static paths: the isolator inherits these (and `docker exec` inherits
# them too) and deliberately does NOT restate them as -e overrides. docker exec does
# NOT run the entrypoint, so ENV is the only way to make them available there.
ENV HOME=${SANDBOX_HOME}
ENV MISE_DATA_DIR=${SANDBOX_HOME}/.local/share/mise
ENV MISE_CACHE_DIR=${SANDBOX_HOME}/.cache/mise
ENV MISE_STATE_DIR=${SANDBOX_HOME}/.local/state/mise
ENV MISE_SYSTEM_CONFIG_FILE=/etc/mise/config.toml
ENV XDG_CONFIG_HOME=${SANDBOX_HOME}/.config
ENV XDG_DATA_HOME=${SANDBOX_HOME}/.local/share
ENV XDG_CACHE_HOME=${SANDBOX_HOME}/.cache
ENV XDG_STATE_HOME=${SANDBOX_HOME}/.local/state
# Fish stores universal variables here; set it so they land under the sandbox home.
ENV __fish_user_data_dir=${SANDBOX_HOME}/.local/share/fish
ENV PATH="/usr/local/bin:${SANDBOX_HOME}/.local/share/mise/shims:/opt/mise/shims:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin"

# Copy Go shim binary from builder stage
COPY --from=shim-builder /devsandbox-shim /devsandbox-shim

# Set working directory
WORKDIR /workspace

ENTRYPOINT ["/devsandbox-shim"]
CMD ["/bin/bash"]
