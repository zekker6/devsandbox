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

# Install neovim from GitHub releases (more reliable than mise for system install)
RUN curl -fsSL https://github.com/neovim/neovim/releases/latest/download/nvim-linux-x86_64.tar.gz | \
    tar xz -C /opt && \
    ln -s /opt/nvim-linux-x86_64/bin/nvim /usr/local/bin/nvim

# Install Claude Code
RUN npm install -g @anthropic-ai/claude-code && \
    ln -sf /opt/mise/installs/node/22.22.0/bin/claude /usr/local/bin/claude

# Install GitHub CLI (for Copilot)
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg && \
    chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list && \
    apt-get update && apt-get install -y gh && rm -rf /var/lib/apt/lists/*

# Install opencode
RUN curl -fsSL https://opencode.ai/install | bash -s -- --no-modify-path && \
    mv /root/.opencode/bin/opencode /usr/local/bin/opencode && \
    rm -rf /root/.opencode

# Install codex (OpenAI Codex CLI)
RUN npm install -g @openai/codex 2>/dev/null || echo "codex package not found, skipping"

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

# Copy entrypoint script
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Set working directory
WORKDIR /workspace

ENTRYPOINT ["/entrypoint.sh"]
CMD ["/bin/bash"]
