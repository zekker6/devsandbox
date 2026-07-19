# krun microVM setup

The `krun` backend runs the sandbox image inside a [libkrun](https://github.com/containers/libkrun) microVM via `podman --runtime krun`, putting the workload behind a hardware virtualization boundary (KVM on Linux, Hypervisor.framework on Apple Silicon) with its own guest kernel. Use it for genuinely untrusted code, where a host-kernel exploit must not be able to reach the host.

`krun` is **experimental** and **opt-in**: `--isolation=auto` never selects it. The `bwrap` and `docker` backends need no extra setup; this page is only for enabling `krun`. For the security model and trade-offs, see [How sandboxing works](../sandboxing.md#isolation-backends) and the [krun backend reference](../configuration.md#krun-microvm-backend-experimental).

## Prerequisites

| Requirement | Why | Check |
|---|---|---|
| `/dev/kvm` accessible | Hardware virtualization for the microVM | `ls -l /dev/kvm` |
| Rootless `podman` ready | Drives the microVM as your user | `/etc/subuid` and `/etc/subgid` have a line for your user; `newuidmap`/`newgidmap` present |
| Kernel with rootless overlay | Tool dirs (kernel 5.13+) | Any modern distro |
| `nft` **or** `iptables` (proxy mode only) | Port-scopes guest egress to the proxy inside the VMM netns; **krun + proxy on Linux fails closed without one** | `nft --version` or `iptables --version` (usually already present) |

On Linux you need bare-metal KVM or a VM with **nested virtualization** enabled. If `/dev/kvm` is missing, krun cannot run here. The subuid/subgid mappings are configured by default on most distributions when `podman` is installed. For **proxy mode** you also need `nft` or `iptables` on the host - one is almost always installed already, but if neither is present a krun + proxy launch aborts (`devsandbox doctor` reports this as `krun: firewall`).

## Install

You need three things: `podman` (the container CLI), the **`krun` OCI runtime** (a `crun` built with libkrun, which exposes a `krun` runtime on `PATH`), and `passt` (provides `pasta` for podman rootless networking, used for default networking and the proxy gateway).

### Arch Linux

```bash
sudo pacman -S --needed podman krun passt
```

The `krun` package installs `/usr/bin/krun` and pulls in `crun` + `libkrun`.

### Fedora

```bash
sudo dnf install -y podman passt crun-krun
```

`crun-krun` provides the `krun` runtime. On older releases it lives in a Copr:

```bash
sudo dnf copr enable -y slp/libkrunfw
sudo dnf copr enable -y slp/libkrun
sudo dnf copr enable -y slp/crun-krun
sudo dnf install -y crun-krun
```

### Debian / Ubuntu

`podman` and `passt` are packaged (`sudo apt install podman passt`), but the libkrun-based `krun` runtime is **not** packaged on most releases. Build `libkrun` and a libkrun-enabled `crun` from source per the [libkrun](https://github.com/containers/libkrun) and [crun](https://github.com/containers/crun/blob/main/krun.1.md) projects, ensuring a `krun` binary ends up on `PATH`.

### macOS (Apple Silicon)

libkrun uses Hypervisor.framework on Apple Silicon. This path is **not yet validated** for devsandbox; on macOS use the [Docker backend](install.md#macos) for now. Proxy mode is **refused** on macOS: the egress lockdown that forces guest traffic through the proxy is Linux-only, so krun + proxy would otherwise run with open egress. Run krun without proxy mode on macOS, or run on Linux for the full proxy egress lockdown.

## Verify

First, confirm podman can boot a microVM with the krun runtime:

```bash
podman run --rm --runtime krun docker.io/library/alpine true
```

This should exit `0` (it pulls a tiny image on first run). If it fails, krun is not set up correctly - fix this before involving devsandbox.

Then confirm devsandbox sees the prerequisites:

```bash
devsandbox doctor
```

The krun rows should read `ok` (on Linux `doctor` also reports a `krun: firewall`
row - the `nft`/`iptables` backend proxy mode needs):

```
│ krun: podman   │ ✓ ok   │ /usr/bin/podman       │
│ krun: runtime  │ ✓ ok   │ /usr/bin/krun         │
│ krun: kvm      │ ✓ ok   │ /dev/kvm accessible   │
│ krun: firewall │ ✓ ok   │ /usr/sbin/nft         │
```

These rows are informational - they `warn` rather than `error` when unmet, so `doctor` never fails for `bwrap`/`docker` users who do not use krun. A missing `krun: firewall` only matters for krun + proxy on Linux, which fails closed at launch without it.

## Run

```bash
cd ~/projects/untrusted-thing
devsandbox --isolation krun claude --dangerously-skip-permissions
```

Or make it the default in `~/.config/devsandbox/config.toml`:

```toml
[sandbox]
isolation = "krun"
```

**First run is slow.** krun uses `podman`, which keeps an image store separate from Docker's, so the base image (`ghcr.io/zekker6/devsandbox:latest`, ~2.5 GB) is pulled and the local image built from scratch the first time - expect a few minutes. Subsequent runs reuse the podman image and boot in seconds.

**Ephemeral by design.** Each launch boots a fresh microVM (a clean guest kernel every run); there is no `keep_container` reuse. Files the workload writes to your project come back owned by you (rootless `podman` with `--userns=keep-id`).

**Host mise tools are available.** On Linux hosts your `~/.local/share/mise/installs` is shared read-only into the guest and seeded into the sandbox's mise data dir at boot, so host-installed toolchains (go, node, task, ...) resolve inside the microVM without reinstalling or network access. In proxy mode the guest additionally runs mise **offline** (`MISE_OFFLINE=1`) - remote version-list lookups cannot traverse the egress lockdown and would otherwise stall listing commands for minutes; everything resolves from the seeded installs instead, and an explicit `MISE_OFFLINE=0 mise install ...` re-enables the network (through the proxy) when you really want an in-sandbox install. See [Tools: mise](../tools.md#tool-management-with-mise) for details and caveats.

### Confirm the microVM boundary

The point of krun is a separate guest kernel. Compare the kernel inside the sandbox with your host kernel - they differ:

```bash
uname -r                              # host, e.g. 7.0.13-arch1-1
devsandbox --isolation krun - uname -r   # guest, e.g. 6.12.91
```

A different kernel version inside the sandbox confirms the workload is running behind the hardware virtualization boundary, not just in a namespace on your host kernel.

## Security: disable the Docker tool

If `[tools.docker]` is enabled, devsandbox mounts a filtered Docker socket into the sandbox - and a loud warning is printed at startup:

```
Docker socket proxy enabled. The sandbox can access ALL existing Docker containers on this host.
```

Mounting your host Docker into a microVM meant for untrusted code hands the guest host-level access and defeats the whole point of krun. Turn it off:

```toml
[tools.docker]
enabled = false
```

## Build-time trust boundary

The microVM isolates the workload at **run** time, but the sandbox image is built **before** the guest boots. If you point krun at a project-provided Dockerfile (`sandbox.docker.dockerfile` or `--dockerfile`), devsandbox builds it with host `podman build`, so every `RUN` step in that Dockerfile executes **on the host** - outside the krun guest and outside the proxy egress lockdown.

krun prints a warning before such a build:

```
krun: building project Dockerfile <path> on the host via podman build; its RUN steps run outside the microVM guest and the proxy egress lockdown. Only build Dockerfiles you trust.
```

The build still proceeds - this is a disclosure, not a hard stop. Only build Dockerfiles you trust under krun; the run-time microVM boundary does not cover the image build. The default (auto-generated) Dockerfile is trusted devsandbox content and builds silently.

## Status and limitations

- **Egress lockdown** (proxy mode) is applied host-side in the VMM's pasta network namespace and is validated on a `/dev/kvm` host. See the [krun backend reference](../configuration.md#krun-microvm-backend-experimental) for the full networking and security-boundary explanation.
- **`devsandbox forward`** is best-effort for krun: the session is registered, but reaching a listener inside the guest through the microVM network namespace is not yet validated.
- **macOS (HVF)** is not yet validated, and **proxy mode is refused there** because the egress lockdown is Linux-only; krun + proxy on macOS fails closed rather than run with open egress. Non-proxy krun on macOS is unaffected.
- **IPv6 egress** is not covered by the lockdown (IPv4 only, matching the bwrap backend).

## Next step

Back to [Quick start](quickstart.md), or read [How sandboxing works](../sandboxing.md) for the full isolation model.
