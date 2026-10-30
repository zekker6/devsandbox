# Limitations

Known constraints of the current implementation.

## Linux (bwrap backend)

- Requires unprivileged user namespaces. Verify with `unshare --user true` (should succeed silently). See [How sandboxing works - Troubleshooting](../sandboxing.md#troubleshooting) for distro-specific guidance.
- SELinux or AppArmor may restrict namespace operations. See [Security Modules](../sandboxing.md#security-modules) for known interactions.
- MITM proxy may break tools with certificate pinning.
- Proxy mode requires `nft` or `iptables` with the `nf_tables` (or `ip_tables`) and `nf_conntrack` kernel modules loaded, for its deny-by-default [egress lockdown](../proxy.md#bwrap-backend). The modules cannot be autoloaded from an unprivileged user namespace, so a missing one aborts the launch rather than degrading to open egress. `devsandbox doctor` reports this as the `proxy: firewall` row.
- IPv6 is disabled in proxy mode: pasta is invoked with `-4`, so a proxy-mode sandbox has IPv4 only and no IPv6 egress path.
- Direct DNS does not work in proxy mode - only the proxy port on the gateway is reachable, and the proxy resolves hostnames itself. A tool that queries a nameserver directly instead of using `HTTP(S)_PROXY` will fail.
- Host loopback ports are reachable in proxy mode only when declared as an `outbound` [port forwarding rule](../configuration.md#port-forwarding); the gateway is not open on every port.
- GUI applications are not supported (no display server forwarding). Desktop notifications work via XDG Desktop Portal.

## macOS (Docker backend)

- Requires a running Docker daemon (OrbStack, Docker Desktop, or Colima).
- Project directory access goes through macOS virtualization (VirtioFS / gRPC-FUSE), which may be slower for I/O-heavy operations. Sandbox-internal operations (`npm install`, Go builds) use named Docker volumes with near-native speed.
- File watching (hot reload) may require polling mode. See [File Watching Limitations](../sandboxing.md#file-watching-limitations) for workarounds.
- Network isolation uses `HTTP_PROXY` instead of `pasta`.

## macOS (Seatbelt backend, experimental)

- Explicit-only via `--isolation=seatbelt`; automatic selection still uses Docker on macOS.
- Runs native macOS host binaries, not Linux binaries. Uses deprecated `sandbox-exec` and unsupported SBPL policies that may change across OS releases.
- No IP listeners, host Unix sockets or Mach services. Private Unix sockets work under `$TMPDIR` and `$XDG_RUNTIME_DIR`. Host Keychain, GUI `open`, AppleEvents and SSH-agent attachment are unavailable. Clients requiring `trustd` may fail even with proxy CA environment variables.
- Ordinary outbound IP is allowed without proxy mode. With `--proxy`, only the actual localhost proxy port is directly reachable; the proxy's filters still decide which destinations it contacts.
- Tool state is not a real overlay. Read-only installations can use host-backed links; configuration and writable state remain private copies. Persistent copies do not merge later host changes and can consume significant disk space. Private-home writes outside temporary bindings persist.
- No custom mounts, resource caps, port forwarding, global or tool `readwrite` mount modes, multi-layer tool overlays, or remapping outside the private home.
- Default Git routes each invocation to a sanitized read-only snapshot, including nested repositories and registered linked worktrees. Refs remain fixed at launch; direct metadata readers and absolute Git paths bypass routing but cannot read original metadata. Git `readwrite` and `--worktree` creation remain rejected; `disabled` permits regular project Git writes without importing host authentication or configuration.
- One native session per project. Native launches appear in `devsandbox sessions` with or without a proxy, but have no namespace and cannot use `devsandbox forward`.
- Exit and cancellation kill the child process group, but detached child sessions may survive with sandbox restrictions intact. No VM or PID namespace guarantees.
- Linux compile checks cannot validate macOS policy enforcement.

See [Seatbelt setup and limitations](../getting-started/seatbelt.md) for copied-home modes, integration handling and validation requirements.

## krun (microVM backend, experimental)

- Egress lockdown in proxy mode is applied host-side in the VMM's pasta network namespace and requires `nft` or `iptables` on the host.
- `devsandbox forward` is best-effort: the session is registered, but reaching a listener inside the guest through the microVM network namespace is not yet validated.
- macOS (Hypervisor.framework) is not yet validated, and **proxy mode is refused there** because the egress lockdown is Linux-only - krun + proxy on macOS fails closed rather than run with open egress. macOS also requires Apple Silicon; Intel Macs are rejected at launch.
- IPv6 is disabled in the guest under proxy mode: pasta is invoked with `-4`, so the guest has IPv4 only and no IPv6 egress path.
- Every launch boots a fresh microVM - there is no `keep_container` reuse, and no online boot-time install of the project's mise tools. See [Tools - mise](../tools.md#tool-management-with-mise).

See [krun backend](../getting-started/krun.md) for the full setup and trust-boundary discussion.

## bwrap and container backends

- Docker socket access is read-only - no container creation, deletion, or modification from inside the sandbox. See [Supported tools - Docker](../tools.md#docker) for what does work.
- Nested Docker (running Docker inside the sandbox) is not supported.
