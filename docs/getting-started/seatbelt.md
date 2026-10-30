# Seatbelt backend (experimental)

Run native macOS command-line tools without Docker, a VM, or a container image:

```sh
cd ~/projects/my-app
devsandbox --isolation=seatbelt --proxy pi
```

Install the macOS versions of your tools on the host first. Seatbelt does not run Linux binaries or install the tools bundled in the Docker image. It uses `/usr/bin/sandbox-exec` to apply a deny-by-default macOS Seatbelt policy before starting the command.

Seatbelt is explicit-only. `--isolation=auto` still selects Docker on macOS and bwrap on Linux. You can also opt in through your host configuration:

```toml
[sandbox]
isolation = "seatbelt"
```

## Experimental status

`sandbox-exec` is deprecated, and its SBPL policy language is not a supported third-party API. Apple may change behavior across macOS releases. Maintaining these profiles is a compatibility and security risk, not a supported substitute for Apple's App Sandbox entitlements. See [Apple DTS on sandbox-exec](https://developer.apple.com/forums/thread/661939).

[OpenAI Codex](https://github.com/openai/codex) and [Anthropic sandbox-runtime](https://github.com/anthropics/sandbox-runtime) also use Seatbelt for native macOS sandboxing. They are useful implementation references, not evidence that devsandbox's policy has the same permissions or guarantees.

The startup policy permits both `hw.pagesize` and `hw.pagesize_compat`. macOS libc uses the latter for `getpagesize()` and `sysconf(_SC_PAGESIZE)`; denying it can make zsh exit with status 1 or Rust tools such as mise panic while allocating a guard page. These are named read-only grants, not unrestricted sysctl access.

The baseline includes read-only grants for `/var/select/sh` and `/var/select/developer_dir`, including their `/private/var` spellings, plus `file-test-existence` for the developer selectors. These rules do not expose the surrounding directory tree. Shell selection works with these grants, but developer selection remains a known limitation: on macOS 15.7.3, Apple's launcher still receives a permission error for an absent selector instead of falling back to the installed Command Line Tools. Passing the host-selected `DEVELOPER_DIR` through `/usr/bin/env` inside the sandbox bypasses that lookup, but does not resolve unrelated filesystem denials.

Linux tests can compile both Darwin architectures, but cannot validate macOS enforcement. Actual filesystem, network, service-access and process behavior must be tested on macOS before relying on this experimental backend.

## Files and private home

System roots and selected tool sources are readable. The root directory `/` itself is readable for macOS dyld/libignition startup; this permits listing its entries, not recursively reading their contents. Without that literal-directory grant, the preflight can abort before `/usr/bin/true` starts. The project and a private native `HOME` are writable, subject to the denials below. The rest of your host home is not generally accessible. Selected host tool sources remain accessible read-only at their original paths so absolute shebangs can find their interpreters.

Ancestors of explicitly readable or writable paths permit metadata queries so tools such as Git can resolve absolute paths. For example, a project under `/Users/name/projects/repo` permits metadata queries on `/Users`, `/Users/name`, and `/Users/name/projects`. These literal grants do not permit listing those directories, reading sibling files, or writing outside the existing writable roots. Explicit read denials still override ancestor metadata grants.

The native home lives in separate sandbox state whose name ends in `-seatbelt`. It does not reuse a Docker volume or bwrap home. This is a copied home, not a mount namespace or a kernel overlay filesystem:

| Mode | Native behavior |
| --- | --- |
| `split`, the default | Config copies refresh from the host on each launch. Nested persistent data and caches survive the refresh. |
| `overlay` | Copies are seeded once and remain writable. Later host changes do not merge into them. |
| `tmpoverlay` | Copies refresh on each launch, discarding changes from the previous run. |
| `readonly` | Config copies refresh on each launch. Read-only data and installations can use host-backed links. All remain read-only to the workload. |
| `readwrite` | Rejected, globally and per tool. |

File bindings requested as overlays become read-only, matching the existing builder behavior. Agent seed files such as Claude's `.claude.json` remain persistent and writable. Missing optional tool paths are not copied. Writes elsewhere in the private home persist unless they fall under a declared temporary binding. A temporary tool config does not make the whole home temporary.

Read-only installations such as `~/.local/bin` and Claude's installed binaries use host-backed links instead of payload copies. Under the default `split` mode, existing mise versions are also linked read-only into the private data directory; missing host versions are seeded on later launches. Existing private version directories are preserved, and newly installed sandbox-only versions remain private and writable. Configuration and writable state are still copied. Explicit `overlay` and `tmpoverlay` modes retain their copy behavior for mise data.

Shared installations reflect host changes, so removing a host version can leave its link unusable. Persistent copies do not merge later host changes and can still consume significant disk space. This is not overlay copy-on-write storage.

`.env` hiding and `.devsandbox.toml` visibility use policy denials rather than empty-file masks. A denied file returns a permission error instead of reading as an empty file. Matching is case-insensitive. Existing protected project symlinks also protect their resolved targets; an unresolvable protected path aborts the launch. The settings remain [`hide_env_files` and `config_visibility`](../configuration.md#sandbox-settings); disabling hiding deliberately exposes those secrets.

The child environment is scrubbed rather than inherited wholesale. The proxy credential is passed in the environment, never in process arguments. Seatbelt reserves `HOME`, `PATH`, `TMPDIR`, `SHELL`, and variables starting with `XDG_`, `DEVSANDBOX`, `DYLD_`, or `GIT_`; attempts to override them through environment passthrough, explicit environment settings, or extra proxy variables fail.

## Git

The default `readonly` mode prepares sanitized, read-only metadata snapshots for repositories discovered beneath the project at launch, including registered linked worktrees and nested regular repositories. Linked worktrees use their own HEAD and index with the shared repository metadata. Original metadata remains inaccessible. Snapshot refs are fixed at launch, so host commits and ref changes do not appear until the next launch.

A protected `git` command wrapper chooses the snapshot for each invocation, including `git -C`. It does not export a project-wide `GIT_DIR` or `GIT_WORK_TREE`, so changing into a nested repository selects that repository rather than the outer one. Fresh `git init` and `git clone` operations are not redirected into an existing snapshot. Credential sections, embedded remote URL credentials, and repository config includes are removed from the snapshots; per-worktree config is sanitized too.

Use `git` through the supplied PATH. Absolute Git paths, clients that inspect `.git` directly, and shell configuration that replaces the wrapper do not get snapshot routing; original metadata is still denied rather than exposed as a fallback. The wrapper provides routing, not the security boundary.

`--git-mode=disabled` permits ordinary writes to a regular project's `.git`, including local commits, without importing host Git authentication or configuration. Linked worktrees require `readonly`. Git `readwrite` credential sharing, arbitrary gitdir indirection, and creating worktrees through `--worktree` remain unsupported. Snapshot preparation rejects symlinked metadata it would read, and configuration or worktree-pointer files larger than 1 MiB.

## Network and macOS services

The workload cannot bind or listen on IP ports. Unix socket bind, connect, and accept operations are allowed only beneath the private `$TMPDIR` and `$XDG_RUNTIME_DIR`. Tools can use these directories for local IPC without gaining access to host SSH-agent or Docker sockets. Symlinks do not authorize socket targets outside those roots. Mach services remain denied, including Keychain access, GUI `open` and AppleEvents integrations.

Without proxy mode, ordinary outbound IP connections are allowed. This is not an outbound destination filter and can include host loopback or LAN services.

With `--proxy`, the policy permits direct outbound IPv4 TCP only to the actual authenticated proxy port on localhost. IPv6 and UDP remain denied, including on that port. It does not permit every loopback port. The proxy runs outside the sandbox and still applies its own [destination filters](../proxy.md#http-filtering). Without a restrictive filter, destinations blocked as direct connections may remain reachable through the proxy. Use `--filter-default=block` and allow rules if you need a destination allowlist.

Node and OpenSSL-based clients may work with the exported CA environment variables. Clients that require macOS `trustd` or other denied services can fail even when the CA file is readable. There is no weaker-policy opt-out for those services; use another backend if your client needs them.

## Unsupported features and lifecycle

Seatbelt rejects settings it cannot enforce rather than silently ignoring them:

- Custom mounts and remapping outside the private home.
- Memory, CPU or process caps under `[sandbox.resources]`.
- Port forwarding, including `port_forwarding.auto_detect`.
- Global or per-tool `readwrite` mount modes and multi-layer tool overlays.
- Creating worktrees with `--worktree`, arbitrary gitdir indirection, and Git `readwrite` credential sharing.
- A second native session for the same project while one is running.

Optional active integrations, host-terminal integrations and shared-temp integrations are detached with an informational notice. Explicit requests for unsupported integrations fail. This includes host socket integrations rather than offering the filtered terminal proxies available on other backends.

Native launches appear in `devsandbox sessions` with or without proxy mode. Use `--name` for a chosen name, otherwise one is generated. The table identifies the `seatbelt` backend and marks forwarding unavailable. JSON records include `backend` and the workload PID, with no `network_ns`. Records are removed on exit, and stale records use the existing PID-based cleanup. `devsandbox forward` rejects native sessions before attempting any namespace operations.

On exit or cancellation, devsandbox kills the child process group. A child that detaches into its own session can survive, but retains its sandbox restrictions. Seatbelt shares the macOS kernel and provides neither a VM boundary nor a PID namespace. Process-group cleanup is not a guarantee that every descendant has exited.
