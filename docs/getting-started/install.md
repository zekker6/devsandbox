# Installation

devsandbox is a single binary for Linux and macOS. Check the platform requirements, install the binary, then run `devsandbox doctor`.

## Platform requirements

### Linux

Your kernel must support unprivileged user namespaces. Verify with:

```bash
unshare --user true
# Should succeed silently. If it fails, see Limitations.
```

No system packages are required - `bwrap` and `pasta` binaries ship embedded in the devsandbox binary.

> **Proxy mode needs two host packages.** `devsandbox --proxy` locks the sandbox's egress to the proxy, which is applied
> with `iproute2` and `nft` (or `iptables`), plus loadable `nf_tables` and `nf_conntrack` kernel modules. There is no
> embedded substitute and no degraded mode: a proxy launch aborts if they are missing. `devsandbox doctor` reports this
> as the `proxy: firewall` row. Launches without `--proxy` are unaffected. See
> [proxy mode requirements](../proxy.md#requirements-bwrap-backend).

> Want hardware-level isolation for untrusted code? The experimental `krun` microVM backend needs extra packages (`podman`, the `krun` runtime, KVM). See [krun microVM setup](krun.md).

### macOS

A Docker runtime is required, and it must be running before you start devsandbox:

- [OrbStack](https://orbstack.dev/) - recommended for Apple Silicon (fastest startup, lowest resource usage)
- [Docker Desktop](https://docs.docker.com/desktop/install/mac-install/) - most widely tested
- [Colima](https://github.com/abiosoft/colima) - free and open-source

## Install devsandbox

### With mise (recommended)

[mise](https://mise.jdx.dev/) is optional. devsandbox runs without it. It is the recommended install path for two reasons:

- One command installs the right binary for your OS and architecture, and `mise upgrade` picks up new releases.
- devsandbox shares your mise-managed tools (Go, Node, Python, kubectl, ...) with the sandbox read-only, so they work inside without a reinstall. See [Tool management with mise](../tools.md#tool-management-with-mise).

If you don't have mise yet, install and activate it ([mise getting started](https://mise.jdx.dev/getting-started.html)):

```bash
# Linux
curl https://mise.jdx.dev/install.sh | sh

# macOS
brew install mise
```

```bash
# bash
echo 'eval "$(~/.local/bin/mise activate bash)"' >> ~/.bashrc

# zsh
echo 'eval "$(~/.local/bin/mise activate zsh)"' >> ~/.zshrc

# fish
echo '~/.local/bin/mise activate fish | source' >> ~/.config/fish/config.fish
```

Then install devsandbox:

```bash
mise use -g github:zekker6/devsandbox
```

### Direct binary download

Every [release](https://github.com/zekker6/devsandbox/releases/latest) ships `devsandbox_<OS>_<arch>.tar.gz` for `Linux` and `Darwin`, on `x86_64` and `arm64`. For Linux on x86_64:

```bash
curl -L https://github.com/zekker6/devsandbox/releases/latest/download/devsandbox_Linux_x86_64.tar.gz | tar xz
sudo mv devsandbox /usr/local/bin/
```

Without mise, devsandbox works the same, but your toolchain is whatever the sandbox already sees: system packages on Linux, the container image on macOS. Install anything else inside the sandbox.

### Build from source

Requires Go 1.26+ and [Task](https://taskfile.dev/). With mise installed, `mise install` provides both:

```bash
mise install
task build
```

## Verify

```bash
devsandbox doctor
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

## Next step

Continue to [Quick start](quickstart.md).
