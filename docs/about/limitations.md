# Limitations

Known constraints of the current implementation.

## Linux (bwrap backend)

- Requires unprivileged user namespaces. Verify with `unshare --user true` (should succeed silently). See [How sandboxing works - Troubleshooting](../sandboxing.md#troubleshooting) for distro-specific guidance.
- SELinux or AppArmor may restrict namespace operations. See [Security Modules](../sandboxing.md#security-modules) for known interactions.
- MITM proxy may break tools with certificate pinning.
- GUI applications are not supported (no display server forwarding). Desktop notifications work via XDG Desktop Portal.

## macOS (Docker backend)

- Requires a running Docker daemon (OrbStack, Docker Desktop, or Colima).
- Project directory access goes through macOS virtualization (VirtioFS / gRPC-FUSE), which may be slower for I/O-heavy operations. Sandbox-internal operations (`npm install`, Go builds) use named Docker volumes with near-native speed.
- File watching (hot reload) may require polling mode. See [File Watching Limitations](../sandboxing.md#file-watching-limitations) for workarounds.
- Network isolation uses `HTTP_PROXY` instead of `pasta`.

## krun (microVM backend, experimental)

- Egress lockdown in proxy mode is applied host-side in the VMM's pasta network namespace and requires `nft` or `iptables` on the host.
- `devsandbox forward` is best-effort: the session is registered, but reaching a listener inside the guest through the microVM network namespace is not yet validated.
- macOS (Hypervisor.framework) is not yet validated, and **proxy mode is refused there** because the egress lockdown is Linux-only - krun + proxy on macOS fails closed rather than run with open egress. macOS also requires Apple Silicon; Intel Macs are rejected at launch.
- IPv6 is disabled in the guest under proxy mode: pasta is invoked with `-4`, so the guest has IPv4 only and no IPv6 egress path.
- Every launch boots a fresh microVM - there is no `keep_container` reuse, and no online boot-time install of the project's mise tools. See [Tools - mise](../tools.md#tool-management-with-mise).

See [krun backend](../getting-started/krun.md) for the full setup and trust-boundary discussion.

## Both platforms

- Docker socket access is read-only - no container creation, deletion, or modification from inside the sandbox. See [Supported tools - Docker](../tools.md#docker) for what does work.
- Nested Docker (running Docker inside the sandbox) is not supported.
