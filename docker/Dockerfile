FROM debian:bookworm-slim

# Avoid interactive prompts during package installation
ENV DEBIAN_FRONTEND=noninteractive

# Install core utilities
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    bash \
    zsh \
    fish \
    passt \
    gosu \
    iproute2 \
    procps \
    unzip \
    libatomic1 \
    openssh-client \
    && rm -rf /var/lib/apt/lists/*

# Install mise
RUN curl https://mise.run | sh && \
    mv /root/.local/bin/mise /usr/local/bin/mise && \
    chmod +x /usr/local/bin/mise

# Install Node.js via mise (needed for AI tools)
ENV MISE_DATA_DIR=/opt/mise
ENV MISE_CACHE_DIR=/opt/mise/cache
RUN mise install node@22 && \
    mise use --global node@22

# Add mise shims to PATH for subsequent commands
ENV PATH="/opt/mise/shims:${PATH}"

# Install neovim from GitHub releases (multi-arch: amd64 and arm64)
RUN ARCH=$(dpkg --print-architecture) && \
    if [ "$ARCH" = "arm64" ] || [ "$ARCH" = "aarch64" ]; then \
        NVIM_ARCH="arm64"; \
    else \
        NVIM_ARCH="x86_64"; \
    fi && \
    curl -fsSL "https://github.com/neovim/neovim/releases/latest/download/nvim-linux-${NVIM_ARCH}.tar.gz" | \
    tar xz -C /opt && \
    NVIM_DIR=$(ls -d /opt/nvim-linux-* | head -1) && \
    ln -s "${NVIM_DIR}/bin/nvim" /usr/local/bin/nvim

# Install Claude Code
RUN npm install -g @anthropic-ai/claude-code && \
    CLAUDE_BIN="$(npm prefix -g)/bin/claude" && \
    if [ -e "$CLAUDE_BIN" ]; then \
        ln -sf "$CLAUDE_BIN" /usr/local/bin/claude; \
    else \
        echo "ERROR: claude binary not found at $CLAUDE_BIN after npm install" >&2 && exit 1; \
    fi

# Install GitHub CLI (for Copilot)
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg && \
    chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list && \
    apt-get update && apt-get install -y gh && rm -rf /var/lib/apt/lists/*

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
RUN mkdir -p /home/sandboxuser && \
    chmod 755 /home/sandboxuser

# Make mise directories world-writable so sandboxuser can use them
# This allows the user to install additional tools if needed
RUN chmod -R 777 /opt/mise

# Create system-level mise config for container tools
# This ensures container-installed tools are available regardless of host config
RUN mkdir -p /etc/mise && \
    echo '[tools]' > /etc/mise/config.toml && \
    echo 'node = "22"' >> /etc/mise/config.toml

# Set runtime environment variables for both entrypoint (docker run) and docker exec.
# docker exec does NOT run the entrypoint, so ENV is the only way to ensure
# tools are available in PATH and mise/XDG directories are correctly set.
ENV HOME=/home/sandboxuser
ENV MISE_DATA_DIR=/home/sandboxuser/.local/share/mise
ENV MISE_CACHE_DIR=/home/sandboxuser/.cache/mise
ENV MISE_STATE_DIR=/home/sandboxuser/.local/state/mise
ENV MISE_SYSTEM_CONFIG_FILE=/etc/mise/config.toml
ENV XDG_CONFIG_HOME=/home/sandboxuser/.config
ENV XDG_DATA_HOME=/home/sandboxuser/.local/share
ENV XDG_CACHE_HOME=/home/sandboxuser/.cache
ENV XDG_STATE_HOME=/home/sandboxuser/.local/state
ENV PATH="/usr/local/bin:/home/sandboxuser/.local/share/mise/shims:/opt/mise/shims:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin"

# Copy entrypoint script
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Set working directory
WORKDIR /workspace

ENTRYPOINT ["/entrypoint.sh"]
CMD ["/bin/bash"]
