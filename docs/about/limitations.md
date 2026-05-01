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

## Both platforms

- Docker socket access is read-only - no container creation, deletion, or modification from inside the sandbox. See [Supported tools - Docker](../tools.md#docker) for what does work.
- Nested Docker (running Docker inside the sandbox) is not supported.
