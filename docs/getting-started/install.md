# Installation

devsandbox runs on Linux and macOS. Pick your platform.

## Prerequisites

devsandbox requires [mise](https://mise.jdx.dev/) for tool version management. Install it first.

### Linux

```bash
curl https://mise.jdx.dev/install.sh | sh
```

Activate mise in your shell ([setup guide](https://mise.jdx.dev/getting-started.html)):

```bash
# bash
echo 'eval "$(~/.local/bin/mise activate bash)"' >> ~/.bashrc

# zsh
echo 'eval "$(~/.local/bin/mise activate zsh)"' >> ~/.zshrc

# fish
echo '~/.local/bin/mise activate fish | source' >> ~/.config/fish/config.fish
```

Your kernel must support unprivileged user namespaces. Verify with:

```bash
unshare --user true
# Should succeed silently. If it fails, see Limitations.
```

No system packages are required — `bwrap` and `pasta` binaries ship embedded in the devsandbox binary.

### macOS

```bash
brew install mise
```

A Docker runtime is also required:

- [OrbStack](https://orbstack.dev/) — recommended for Apple Silicon (fastest startup, lowest resource usage)
- [Docker Desktop](https://docs.docker.com/desktop/install/mac-install/) — most widely tested
- [Colima](https://github.com/abiosoft/colima) — free and open-source

## Install devsandbox

The recommended path is via mise:

```bash
mise use -g github:zekker6/devsandbox
```

After install, verify:

```bash
devsandbox doctor
```

### Direct binary download (Linux)

```bash
curl -L https://github.com/zekker6/devsandbox/releases/latest/download/devsandbox_Linux_x86_64.tar.gz | tar xz
sudo mv devsandbox /usr/local/bin/
```

### Optional system packages (Linux fallback)

If embedded binary extraction fails, install system equivalents:

```bash
# Arch Linux
sudo pacman -S bubblewrap passt

# Debian/Ubuntu
sudo apt install bubblewrap passt

# Fedora
sudo dnf install bubblewrap passt
```

To prefer system binaries over embedded, set `use_embedded = false` in [Configuration](../configuration.md).

### Build from source

Requires Go 1.22+ and [Task](https://taskfile.dev/). With mise installed, `mise install` handles both:

```bash
mise install
task build
```

## Next step

Continue to [Quick start](quickstart.md).
