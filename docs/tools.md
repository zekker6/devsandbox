# Tools

devsandbox makes development tools available inside the sandbox while maintaining security boundaries.

## Inspecting Tools

Use the `tools` command to see which tools are available and how they're configured.

### List Available Tools

```bash
# List tools detected on your system
devsandbox tools list

# Include unavailable tools
devsandbox tools list --all

# JSON output for scripting
devsandbox tools list --json
```

Example output:

```
┌────────────┬───────────┬───────────────────────────────────────────────┐
│    NAME    │  STATUS   │                  DESCRIPTION                  │
├────────────┼───────────┼───────────────────────────────────────────────┤
│ claude     │ available │ Claude Code AI assistant                      │
│ git        │ available │ Git configuration (safe mode, no credentials) │
│ go         │ available │ Go language environment isolation             │
│ mise       │ available │ Tool version manager (node, python, go, etc.) │
│ nvim       │ available │ Neovim editor configuration                   │
│ starship   │ available │ Starship prompt with sandbox indicator        │
└────────────┴───────────┴───────────────────────────────────────────────┘
```

### Tool Details

View bindings, environment variables, and shell init for a specific tool:

```bash
# Show details for a tool
devsandbox tools info mise

# Show details for all tools
devsandbox tools info --all

# JSON output
devsandbox tools info mise --json
```

Example output:

```
Tool: mise
Status: available
Description: Tool version manager (node, python, go, etc.)
Binary: /home/user/.local/bin/mise

Bindings:
  ~/.local/bin                        (read-only, optional)
  ~/.config/mise                      (read-only, optional)
  ~/.local/share/mise                 (read-only, optional)

Environment Variables: (none)

Shell Init:
  if command -v mise &>/dev/null; then eval "$(mise activate bash)"; fi
```

### Verify Tool Setup

Check tool availability and verify binding paths exist:

```bash
# Check all tools
devsandbox tools check

# Check specific tools
devsandbox tools check mise git claude

# JSON output
devsandbox tools check --json
```

Example output:

```
Checking tools...

✓ mise (/home/user/.local/bin/mise)
    ✓ ~/.local/bin
    ✓ ~/.config/mise
    ✓ ~/.local/share/mise
✓ git (/usr/bin/git)
    ○ ~/.local/share/devsandbox/<project>/home/.gitconfig.safe (optional, missing)
✓ claude (/home/user/.local/bin/claude)
    ✓ ~/.claude
    ✓ ~/.claude.json
✗ starship (not available)
    ! starship binary not found in PATH

Summary: 3/4 tools available
```

## Tool Management with mise

[mise](https://mise.jdx.dev/) is the recommended tool manager. All mise-managed tools are automatically available inside
the sandbox.

### How It Works

By default, mise directories are bind-mounted read-only:

```
~/.config/mise      → Sandbox (read-only)
~/.local/share/mise → Sandbox (read-only)
~/.local/state/mise → Sandbox (read-only)
```

This means:

- All installed tool versions are available
- Tool configurations (`.mise.toml`) are respected
- New tools cannot be installed from inside the sandbox (by default)

### Writable Mise (Overlay Mode)

Enable overlayfs to allow installing tools inside the sandbox by configuring `~/.config/devsandbox/config.toml`:

```toml
[tools.mise]
writable = true    # Allow installing tools
persistent = false # Discard on exit (safer)
```

With overlay enabled:

- Sandbox can install new tool versions
- Host mise directories remain unchanged
- Changes are isolated to the sandbox

See [sandboxing.md](sandboxing.md#overlay-filesystem) for more details on overlay configuration.

### Supported Tools

Any tool installable via mise works inside the sandbox:

| Category         | Examples                              |
|------------------|---------------------------------------|
| Languages        | Node.js, Python, Go, Rust, Ruby, Java |
| Package Managers | npm, pnpm, yarn, pip, cargo           |
| Build Tools      | make, cmake, ninja                    |
| Utilities        | jq, yq, gh, aws-cli                   |

### Installing Tools

Install tools on your host system (outside the sandbox):

```bash
# Install Node.js
mise install node@20

# Install Python
mise install python@3.12

# Install multiple tools
mise install node@20 python@3.12 go@1.22
```

Tools are then automatically available in all sandboxes.

## Shell Configuration

devsandbox detects your shell from `$SHELL` and loads appropriate configurations:

### Bash

```
~/.bashrc       → Sandbox (read-only)
~/.bash_profile → Sandbox (read-only)
~/.profile      → Sandbox (read-only)
```

### Zsh

```
~/.zshrc        → Sandbox (read-only)
~/.zshenv       → Sandbox (read-only)
~/.zprofile     → Sandbox (read-only)
```

### Fish

```
~/.config/fish/ → Sandbox (read-only)
```

**Note:** Shell configurations are read-only. Changes made inside the sandbox won't persist.

## Editor Support

### Neovim

Neovim configuration is available inside the sandbox:

```
~/.config/nvim      → Sandbox (read-only)
~/.local/share/nvim → Sandbox (read-only)
~/.local/state/nvim → Sandbox (read-only)
```

This includes:

- Your init.lua/init.vim
- Installed plugins
- LSP configurations
- Color schemes

### Other Editors

For other editors, mount their config directories manually or use the sandbox home.

## AI Coding Assistants

### Claude Code

Claude Code is fully supported:

```bash
# Run Claude Code in sandbox
devsandbox claude

# With permissions disabled (recommended)
devsandbox claude --dangerously-skip-permissions
```

Configuration directories are mounted:

```
~/.claude      → Sandbox (read-only)
~/.claude.json → Sandbox (read-only)
```

### GitHub Copilot

Works inside editors (Neovim, VS Code) running in the sandbox.

## Git

Git is available but with restrictions:

### Allowed Operations

- View history (`git log`, `git show`, `git diff`)
- Stage changes (`git add`)
- Commit (`git commit`)
- Branch operations (`git branch`, `git checkout`)
- Fetch/pull via HTTPS (`git fetch`, `git pull`)
- Push via HTTPS (if credentials in URL or credential helper)

### Blocked Operations

- SSH-based operations (no `~/.ssh` access)
- Credential helpers that access blocked paths
- GPG signing (unless keys are in sandbox home)

### HTTPS Authentication

For HTTPS authentication, use one of:

1. **Token in URL** (not recommended for shared repos):
   ```bash
   git remote set-url origin https://TOKEN@github.com/user/repo.git
   ```

2. **Environment variable**:
   ```bash
   GIT_ASKPASS=/path/to/helper devsandbox git push
   ```

3. **Credential in sandbox**:
   Store credentials in the sandbox's home directory.

## Go

Go is fully supported with isolated caches:

```
GOPATH       → ~/.local/share/devsandbox/<project>/home/go
GOCACHE      → ~/.local/share/devsandbox/<project>/home/.cache/go-build
GOMODCACHE   → ~/.local/share/devsandbox/<project>/home/.cache/go-mod
GOTOOLCHAIN  → local (prevents auto-downloads)
```

### Module Cache

Each project has its own module cache. To share modules across projects, you can symlink the cache directories (outside
the sandbox).

## Node.js / npm

Works normally with mise-installed Node.js:

```bash
devsandbox npm install
devsandbox npm run build
devsandbox npx create-react-app my-app
```

### Global Packages

Global npm packages are installed to the sandbox home:

```
~/.local/share/devsandbox/<project>/home/.npm-global
```

## Python

Works normally with mise-installed Python:

```bash
devsandbox pip install -r requirements.txt
devsandbox python script.py
```

### Virtual Environments

Create venvs inside your project directory:

```bash
devsandbox python -m venv .venv
devsandbox source .venv/bin/activate
```

## Docker

**Not supported** - Docker requires privileged access that the sandbox blocks.

For container workflows, consider:

- Running Docker commands outside the sandbox
- Using Podman in rootless mode (experimental)

## Adding Custom Tools

To make additional tools available:

1. **Install via mise** (recommended):
   ```bash
   mise install mytool@version
   ```

2. **System tools** - Tools in `/usr/bin`, `/usr/local/bin` are available

3. **Project-local tools** - Place executables in your project directory
