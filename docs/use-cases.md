# Use Cases

Common workflows and setup guides for devsandbox.

## AI Coding Assistants

### Claude Code

Run Claude Code with reduced risk:

```bash
# Basic usage
devsandbox claude

# Skip permission prompts (runs autonomously)
devsandbox claude --dangerously-skip-permissions

# With proxy mode for traffic inspection
devsandbox --proxy claude --dangerously-skip-permissions
```

**What's protected:**

- SSH keys cannot be exfiltrated
- Cloud credentials are inaccessible
- `.env` secrets are blocked
- Network activity can be monitored (proxy mode)

**What Claude can do:**

- Read/write project files
- Run build commands
- Install dependencies
- Make API calls (visible in proxy logs)

### GitHub Copilot

Copilot works inside editors running in the sandbox:

```bash
# Run Neovim with Copilot
devsandbox nvim

# VS Code (if installed via mise or system)
devsandbox code .
```

## Shell Autocompletion

### Bash

Add to `~/.bashrc`:

```bash
# devsandbox completion
if command -v devsandbox &> /dev/null; then
    eval "$(devsandbox completion bash)"
fi
```

Or generate and source from a file:

```bash
devsandbox completion bash > ~/.local/share/bash-completion/completions/devsandbox
```

### Zsh

Add to `~/.zshrc`:

```zsh
# devsandbox completion
if command -v devsandbox &> /dev/null; then
    eval "$(devsandbox completion zsh)"
fi
```

Or add to your completions directory:

```bash
devsandbox completion zsh > "${fpath[1]}/_devsandbox"
```

### Fish

```fish
# Add to ~/.config/fish/config.fish
devsandbox completion fish | source
```

Or generate completion file:

```bash
devsandbox completion fish > ~/.config/fish/completions/devsandbox.fish
```

## Useful Aliases

Add these to your shell configuration:

### Bash / Zsh

```bash
# ~/.bashrc or ~/.zshrc

# Quick sandbox shell
alias sb='devsandbox'

# Claude with permissions disabled
alias claude-safe='devsandbox claude --dangerously-skip-permissions'

# Claude with proxy monitoring
alias claude-monitored='devsandbox --proxy claude --dangerously-skip-permissions'

# View Claude's network activity
alias claude-logs='devsandbox logs proxy --last 50'

# Follow proxy logs in real-time
alias sb-watch='devsandbox logs proxy -f'

# Quick proxy mode
alias sbp='devsandbox --proxy'

# Run npm in sandbox
alias snpm='devsandbox npm'

# Run yarn in sandbox
alias syarn='devsandbox yarn'

# Run pnpm in sandbox
alias spnpm='devsandbox pnpm'

# Sandbox info
alias sb-info='devsandbox --info'

# Check sandbox health
alias sb-doctor='devsandbox doctor'
```

### Fish

```fish
# ~/.config/fish/config.fish

# Quick sandbox shell
alias sb='devsandbox'

# Claude with permissions disabled
alias claude-safe='devsandbox claude --dangerously-skip-permissions'

# Claude with proxy monitoring
alias claude-monitored='devsandbox --proxy claude --dangerously-skip-permissions'

# View Claude's network activity
alias claude-logs='devsandbox logs proxy --last 50'

# Follow proxy logs in real-time
alias sb-watch='devsandbox logs proxy -f'

# Quick proxy mode
alias sbp='devsandbox --proxy'
```

## Shell Functions

### Bash / Zsh

```bash
# Run command in sandbox and show proxy logs after
sb-run() {
    devsandbox --proxy "$@"
    echo ""
    echo "=== Network Activity ==="
    devsandbox logs proxy --last 20 --compact
}

# Watch proxy logs for specific URL pattern
sb-watch-url() {
    devsandbox logs proxy -f --url "$1"
}

# Quick audit of recent API calls
sb-audit() {
    local hours="${1:-1}"
    echo "=== API calls in last ${hours}h ==="
    devsandbox logs proxy --since "${hours}h" --stats
    echo ""
    devsandbox logs proxy --since "${hours}h" --compact
}
```

## Development Workflows

### Node.js Project

```bash
# Install dependencies
devsandbox npm install

# Run development server
devsandbox npm run dev

# Run tests
devsandbox npm test

# Build for production
devsandbox npm run build
```

### Python Project

```bash
# Create virtual environment (in project dir)
devsandbox python -m venv .venv

# Activate and install dependencies
devsandbox bash -c "source .venv/bin/activate && pip install -r requirements.txt"

# Run application
devsandbox bash -c "source .venv/bin/activate && python app.py"
```

### Go Project

```bash
# Download dependencies
devsandbox go mod download

# Run tests
devsandbox go test ./...

# Build
devsandbox go build ./cmd/myapp
```

### Rust Project

```bash
# Build
devsandbox cargo build

# Run tests
devsandbox cargo test

# Run application
devsandbox cargo run
```

## Security Monitoring

### Real-time Traffic Monitoring

Terminal 1 - Run the sandbox:

```bash
devsandbox --proxy claude --dangerously-skip-permissions
```

Terminal 2 - Watch traffic:

```bash
devsandbox logs proxy -f --compact
```

### Post-Session Audit

After a coding session:

```bash
# Summary statistics
devsandbox logs proxy --stats

# All external API calls
devsandbox logs proxy --url https:// --compact

# Any errors or failures
devsandbox logs proxy --errors

# Export for review
devsandbox logs proxy --json > session-traffic.json
```

### Alerting on Suspicious Activity

Create a script to check for suspicious patterns:

```bash
#!/bin/bash
# check-suspicious.sh

# Check for credential-related URLs
suspicious=$(devsandbox logs proxy --since 1h --json | \
    jq -r '.[] | select(.url | test("password|token|secret|credential|auth"; "i")) | .url')

if [ -n "$suspicious" ]; then
    echo "WARNING: Suspicious URLs accessed:"
    echo "$suspicious"
    exit 1
fi
```

## Troubleshooting Common Issues

### "Tool X not found"

The tool might not be installed via mise:

```bash
# Check if installed
mise list

# Install the tool
mise install node@20  # example
```

### "Permission denied" in sandbox

Check if the path is mounted:

```bash
devsandbox --info
```

### Slow startup

First run for a project creates sandbox directories. Subsequent runs are faster.

### Network timeout in proxy mode

Check if the service allows proxy connections:

```bash
# Test outside sandbox first
curl https://api.example.com

# Then in sandbox
devsandbox --proxy curl https://api.example.com
```
