# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased](https://github.com/zekker6/devsandbox/compare/v0.17.3...HEAD)

### Breaking Changes

- **The container image no longer bakes in `gh` (GitHub CLI) or `neovim` by default; the image is slimmer.** These are now off unless the image is rebuilt with `--build-arg INSTALL_GH=true` / `--build-arg INSTALL_NVIM=true`, or added in a derived `FROM ghcr.io/zekker6/devsandbox` Dockerfile. Their host configs are still mounted into the sandbox as before; only the baked binaries changed. `node`, `claude`, `codex`, `opencode`, `mise`, `git`, the shells, and `docker` CLI remain baked. The Dockerfile also gained `SANDBOX_HOME`/`SANDBOX_UID`/`SANDBOX_GID`/`NODE_VERSION` build args (alongside `INSTALL_GH`/`INSTALL_NVIM`) so the sandbox account and baked node version are each defined once instead of being repeated throughout the file.

### Added

- **herdr terminal workspace support, through a capability-filtering proxy.** Running devsandbox inside a [herdr](https://herdr.dev) session now lets sandboxed tools open a review overlay in a herdr tab - `revdiff` is the first consumer. The host control socket is **not** bind-mounted; a proxy is, and the sandboxed `herdr` CLI is pointed at it via `HERDR_SOCKET_PATH`. herdr v0.7.4 (protocol 16) exposes 84 methods, including `pane.read` (read any pane), `agent.send`, `worktree.*`, `plugin.*` and `server.stop`; the proxy permits only what enabled tools declare - `tab.create`, `pane.send_input` and `tab.close` for `launch_overlay`, plus `notification.show` for `notify` - and denies everything else. Mutations are scoped to the tab and pane the sandbox itself created, taken from the server's response rather than from anything the client claims, and `pane.send_input` is further restricted to one declared command plus `Enter`. Because herdr runs launch payloads as `sh <path>` rather than inline, the proxy reads the script once, validates it, and copies it to a host-only directory before execution, so the sandbox cannot swap the contents after validation. Configure with `[tools.herdr] mode = "auto" | "disabled" | "enforce"`; see [herdr Terminal Workspace](docs/tools.md#herdr-terminal-workspace). `herdr status` and `herdr session list` report correctly from inside the sandbox: `ping` is permitted unconditionally (a liveness handshake that reveals less than the connect it replaces), and the proxy socket is also exposed at the default `~/.config/herdr/herdr.sock` path that some subcommands resolve on their own instead of honoring `HERDR_SOCKET_PATH`.
- **The container backends (`docker` and `krun`) now make host-installed mise tools available inside the sandbox on Linux hosts.** Previously the guest only had the image's pre-baked node plus whatever the project's `.mise.toml` installed at startup - a network-dependent reinstall of toolchains you already have, and the main reason a mise-centric workflow felt unusable under `krun`. The host's `~/.local/share/mise/installs` is now mounted read-only into the guest, and on startup the in-guest shim mirrors each host-installed version into the persistent sandbox mise data dir as a real version directory whose contents are symlinks into the read-only mount (a fully symlinked version dir breaks mise's aqua/ubi bin-path discovery for nested-layout tools like `uv` and `golangci-lint`; the child-level shape keeps them working), never clobbering sandbox-local installs, and regenerates the mise shims, so your host toolchain (go, node, task, ...) resolves inside the sandbox without reinstalling or network access. The guest also prefers the **host's own mise binary** (mounted via `~/.local/bin`, probe-gated with fallback to the image's) over the image's latest-at-build-time one: a guest mise newer than the host's re-maps a tool's stored backend to its current registry default (e.g. stored `ubi:sharkdp/bat` to registry `aqua:sharkdp/bat`) and then derives bin paths from an archive layout that does not match what is on disk, silently breaking host-installed tools like `bat` or `eza` - version parity removes that skew, and a mise-binary change forces a one-time shim regeneration in existing sandbox homes - this also removes the main trigger for the global-config `@latest` resolution hangs, since the tools those specs resolve to are already present. Versions installed inside the sandbox still take precedence and persist as before. Caveats: host tools compiled locally against a newer glibc than the guest image may fail in the guest (upstream prebuilt tools are unaffected; `mise uninstall <tool>@<version>` + `mise install` inside the sandbox yields a guest-local build - the uninstall only removes the seeded symlink, never the host install), and macOS hosts share nothing (the guest is Linux and cannot run darwin binaries). Only the installs directory itself is shared, so a tool whose runtime lives outside it does not work in-guest: a `pipx:` tool backed by a uv-managed Python, for example, is seeded as a version dir but its interpreter is not mounted, and it fails to start. Reinstall such a tool inside the sandbox. See [Tools: mise](docs/tools.md#tool-management-with-mise).
- **`tools.mise.ignore_global_config` config option to stop the sandbox reading the host's global mise config.** A large host `~/.config/mise/config.toml` with `@latest` `npm:`/`go:`/`pipx:` tools made the sandbox hang and could OOM the guest: every shell start ran `mise activate`, which resolved each `@latest` spec over the network (`npm view …`), and on a proxy/egress-locked sandbox those lookups time out (20s each) while a swarm of them exhausts guest memory. Setting `[tools.mise] ignore_global_config = true` points `MISE_GLOBAL_CONFIG_FILE` at `/dev/null` in the sandbox so the host global `config.toml` tool list is not read; the project `.mise.toml`, the image's system config (baked node), and `~/.config/mise/settings.toml` still apply. It defaults to `false` (the global config is respected, preserving current behavior) and applies to every backend (`bwrap`, `docker`, `krun`). The setting scopes shell and runtime mise invocations inside the sandbox; the docker backend's boot-time project-tool install has always run with `MISE_GLOBAL_CONFIG_FILE=/dev/null` and is unaffected either way.
- **Experimental `krun` microVM isolation backend.** A third isolation backend (`--isolation krun` / `sandbox.isolation = "krun"`) runs the same sandbox image inside a [libkrun](https://github.com/containers/libkrun) microVM via `podman --runtime krun`. Unlike bwrap and Docker, which share the host kernel, krun gives the workload its own guest kernel behind a hardware virtualization boundary (KVM on Linux, Hypervisor.framework on macOS) - the right boundary for running genuinely untrusted code, where a host-kernel exploit must not reach the host. It is opt-in (never auto-selected) and ephemeral (a fresh microVM per launch), runs rootless via `podman --userns=keep-id` so workload-created files stay owned by you, and supports proxy mode on Linux with the MITM proxy bound to host loopback (reached through the pasta gateway, never LAN-exposed). krun + proxy is refused fail-closed on macOS, because the egress lockdown below is Linux-only route surgery with no Hypervisor.framework equivalent yet; non-proxy krun on macOS is unaffected. Requires `podman`, a `crun` built with libkrun (provides the `krun` OCI runtime), and `/dev/kvm` on Linux or Apple Silicon on macOS; `devsandbox` fails fast with installation guidance when a prerequisite is missing. When `[sandbox.docker.resources]` is unset, krun applies sane microVM defaults (`memory = "4g"`, `cpus = "2"`) so the guest is neither starved nor oversized; explicit limits are respected and the docker backend is unaffected.
  - **Egress lockdown:** in proxy mode krun enforces a **host-side, deny-by-default** lockdown - after the microVM boots, `devsandbox` enters the VMM's network namespace (via `nsenter --user --net`) and installs a firewall that drops all egress except loopback, established/related return traffic, and TCP to the proxy gateway on the proxy port, so a destination is reachable only if a rule names it and the LAN (a router UI, a NAS, a LAN DNS resolver), cloud metadata at `169.254.169.254` with its IAM credentials, external DNS exfiltration, and every non-proxy gateway port are closed without being enumerated. The guest is given IPv4 only (pasta is invoked with `-4`) so no IPv6 path exists in the first place, and the default route is deleted alongside as defense in depth. The lockdown runs host-side because under libkrun TSI the guest has no routable interface - its `connect()` calls are executed by the VMM process and obey the VMM netns routing, which an in-guest `ip route del` cannot affect. It prefers `nft` and falls back to `iptables`; if neither is available, or the guest PID cannot be resolved to lock the netns, the microVM is torn down and the launch aborts fail-closed. The surgery is scoped to krun + proxy on Linux and grants no in-guest `NET_ADMIN`. The in-guest shim waits for the host to finish, via a sentinel in the sandbox home, before running the workload, so untrusted code never runs while direct egress is still open - and it waits immediately after reading the host UID/GID, ahead of every mise interaction (host-mise preference probe, `mise trust`, install seeding, `mise reshim`), which touches the guest-writable mise binary and data dir that a prior untrusted run could have planted. Because that home is persistent and guest-writable, the gate accepts only a regular file, written by the host with `O_CREATE|O_EXCL` and checked with `Lstat` (never `Stat`), so a symlink or a stale directory left at the sentinel path is neither followed nor accepted as the go-signal; the host clears the path with `RemoveAll`, verifies it is gone, and aborts before boot if it cannot.
  - **Guest tooling:** an egress-locked krun + proxy guest runs mise **offline** (`MISE_OFFLINE=1`), so `@latest`-style specs resolve instantly from installed and cached data instead of stalling on remote lookups that never traverse the proxy - with host installs seeded that is the full host toolchain. krun runs **no** online boot-time install of your project's tools - it is always ephemeral, and the online `mise install` pass only runs on the persistent-container `docker` paths - so a tool your `.mise.toml` pins that is neither seeded from the host nor already in the sandbox home is not fetched for you; install it in-sandbox with an explicit `MISE_OFFLINE=0 mise install ...`. The startup shim also runs its own mise invocations offline, since they are purely local operations.
  - **Overlay copies:** krun copies overlay/`tmpoverlay` tool directories into the guest on start (it cannot mount kernel overlayfs over virtio-fs), and non-regular entries in the source - a live unix socket, FIFO, or device node such as the `~/.claude/channels/matrix/mux.sock` a Claude Code MCP channel plugin creates - are skipped as the runtime artifacts they are, instead of failing the copy with `open ...: no such device or address` and aborting the whole launch; symlinks, directories, and regular files copy as before. Validated on a `/dev/kvm` host.
  - **Management parity:** `devsandbox doctor` reports krun prerequisites (podman, the `krun` runtime, `/dev/kvm`, a `platform` row when the host OS or CPU architecture cannot run krun at all, and on Linux an `nft`/`iptables` firewall for proxy mode, a system `pasta` binary for rootless podman networking, and `/etc/subuid`+`/etc/subgid` ranges for the `--userns=keep-id` mapping) as informational rows that warn rather than fail, printing each unmet row's remediation under a "How to fix" block below the table; `devsandbox sandboxes list`/`sandboxes prune` cover ephemeral krun sandboxes through on-disk metadata; `devsandbox forward` is best-effort (session registration works, but reaching an in-guest listener through the microVM netns is unvalidated). The in-guest shim no longer emits spurious `groupadd`/`useradd` "already exists" warnings under keep-id. Because krun runs one microVM per project at a time, a second launch while a session is already active now fails fast at startup - before the proxy, docker socket proxy, and image build run - with a copy-pasteable `podman rm -f <container>` remediation, instead of aborting only after the image was rebuilt. See [Isolation Backend](docs/configuration.md#krun-microvm-backend-experimental).

### Fixed

- **The kitty proxy no longer accepts a launch command that names a sandbox-planted binary.** Launch patterns matched `argv[0]` on basename alone, so any path ending in the allowed program name was accepted - including one inside a directory the sandbox can write. That matters because the revdiff IPC directory is a write-through bind mounted at an *identical path* on the host and inside the sandbox (unlike the rest of the sandbox home, whose overlay writes never reach the host). Sandboxed code could therefore drop its own executable at `<ipc-dir>/revdiff`, name it in a `kitty @ launch` request, pass validation on basename, and have kitty run it **on the host as the host user** - a full sandbox escape. Launch patterns are now pinned to the program's resolved absolute path (`exec.LookPath` + symlink evaluation), and patterns may additionally declare directory prefixes that can never supply the program. If the binary cannot be resolved, every launch request is denied rather than falling back to the old basename behavior.
- **A second session for the same project no longer breaks a running session's notifications, Docker access, and kitty remote control.** The portal, Docker, and kitty proxies each created their unix socket at a path keyed only on the project (`<sandbox home>/.dbus-proxy/bus`, `<sandbox home>/docker.sock`, `<sandbox home>/.kitty.sock`), but sandbox home is shared by every session for that project. Starting a second session unlinked the live session's socket and re-created it, and that session's exit deleted the path outright - leaving the first session with `DBUS_SESSION_BUS_ADDRESS`, `DOCKER_HOST`, and `KITTY_LISTEN_ON` pointing at a path that no longer existed. `notify-send` failed with `Could not connect: No such file or directory` for the rest of the session, with no way to recover short of restarting it. Each session's sockets now live in a directory private to the owning process (`<sandbox home>/.run/<pid>/`), so concurrent sessions cannot disturb each other; directories left by sessions that are gone are reclaimed on the next start. `DBUS_SESSION_BUS_ADDRESS` is unchanged; `DOCKER_HOST` and `KITTY_LISTEN_ON` now point at `$HOME/.run/<pid>/docker.sock` and `$HOME/.run/<pid>/kitty.sock` inside the sandbox.
- **A socket path too long for the kernel is now reported as such.** The proxy socket paths above sit under an already-long sandbox home, and `bind(2)` rejects anything past 107 bytes (103 on macOS) with a bare `invalid argument` - which the portal surfaced only as an opaque "proxy socket not created" timeout. The portal, Docker, kitty, and herdr proxies now fail with the path, its length, the limit, and the remedy (a shorter project directory name).
- **Proxy-mode sandboxes no longer stall for minutes resolving `@latest` mise tool specs.** mise refreshes the remote version list behind every `@latest`-style spec in a mise config (host global config included), some backends' lookups (npm registry, python-build) never traverse the proxy and hang to their 20s default timeout, and mise re-resolves the toolset per listed row with no negative cache - measured on an egress-locked guest, a single `mise ls` against a global config with two `@latest` npm specs ran for **14 minutes** across ~300 doomed lookups. All proxy-mode sandboxes now bound remote lookups with `MISE_FETCH_REMOTE_VERSIONS_TIMEOUT=3s` (was 20s), and a value you set through the sandbox env config takes precedence over that default on every backend, as the docs already promised. Under krun the offline-mise layer described above removes the lookups entirely: verified end-to-end on a KVM host, `mise ls` in an egress-locked guest went from 14 minutes with a wall of warnings to under a second with none.
- `tmpoverlay` config dirs that degrade to a copy-on-start overlay (krun on any OS, and the Docker backend on macOS) are now reset to the host source on every run; files written by a previous run no longer persist. On these backends a `tmpoverlay` tool dir cannot use kernel overlayfs (the libkrun guest rejects overlayfs over virtio-fs; Docker Desktop on macOS has no overlayfs), so it degrades to a copy into the *persistent* sandbox home. The copy only wrote source entries and never removed extraneous ones, so anything a previous - possibly untrusted - run left under the target (for example a hook planted in `~/.claude`) survived into the next session, defeating tmpoverlay's discard-on-exit promise. The shim now clears the target before copying; the clear is mount-aware, preserving any nested read-only bindings (e.g. `~/.claude/projects`) and their ancestor directories while removing everything else. Copyoverlay targets are nested (e.g. `~/.config/Claude`, with nothing mounted at `~/.config`), so if a previous run replaced the target - or any intermediate component of its path - with a symlink (pointing at the project dir or another read-write mount), the shim now walks every component from the sandbox home down to the target, removes any symlink it finds - never following it - and recreates a real directory before cleaning and copying, so the root-privileged clear and copy never delete or write outside the target.
- **Docker isolation no longer hangs at startup for non-root users when a tool uses an overlay mount.** The container drops all capabilities and the entrypoint shim runs as container-root to populate `/home/sandboxuser` and then drop privileges, but `CAP_DAC_OVERRIDE` was granted only to the krun backend. Under rootful Docker the home is bind-mounted from the host (owned by your non-root host UID) and the overlay manifest is a host temp file (mode 0600, host-owned), so without `DAC_OVERRIDE` container-root could not create files in the home (silent `fish_variables` / `ssh/environment` warnings) nor read the manifest - the manifest read is fatal, so the shim exited before signalling ready and the launch hung until the 90s readiness timeout. This only surfaced when a tool binding introduced a `tmpoverlay` (which generates the manifest) and went unnoticed because Linux defaults to the bwrap backend. `DAC_OVERRIDE` is now granted on the docker backend too, scoped to the shim's root setup phase: the shim still `setuid`s to the unprivileged user (clearing its capabilities) before exec'ing the workload, with `no-new-privileges` set, so the workload never holds the capability.
- **Piped stdin now reaches non-interactive krun (and docker) commands.** `devsandbox --isolation krun - cmd` ran the container without `-i`, so `data | devsandbox --isolation krun - tool` closed the workload's stdin and the piped input was silently lost (the bwrap backend, which runs the workload as a direct child, was unaffected). The container/exec is now started with `-i` for non-interactive runs, so stdin is forwarded on every backend. A TTY is added on the run and exec paths only for interactive sessions; the default `keep_container` create path still allocates one unconditionally (`-it`), because a container created for a one-off command may later be reused for an interactive shell.
- **A sandboxed command's exit code now propagates to the host instead of collapsing to `1`.** `devsandbox - sh -c 'exit 42'` exited `1` on every backend (only `0` passed through), because a non-zero command result was treated as a generic CLI error. The workload's exit status is now carried up and re-emitted as `devsandbox`'s own exit code (so `42` stays `42`), and a non-zero command exit no longer prints a spurious `Error:` line - matching how a shell surfaces a child's status. This now covers the **default `keep_container` create/exec path** on the docker backend too (`podman exec` was returned unwrapped, so its status still collapsed to `1` there). Genuine devsandbox setup failures (image build, container create, egress lockdown) still surface loudly and exit `1`; a container-engine launch failure (`podman`/`docker` exit `125` - a bad flag or a create/exec that never started the workload) is likewise surfaced as an error rather than propagated silently as if it were the command's own status.
- **The session/proxy lock files are no longer unlinked on release, and are opened with `O_NOFOLLOW`.** The file lock (used by the proxy lifecycle lock and the new krun per-project session lock) unlinked its lock file on release, which reopened a split-lock race: a second process could `flock` the old inode after the holder unlocked but before it unlinked, the unlink then removed a path a third process recreated and flocked independently, and two holders ran at once. The lock file is now created once and left in place - the kernel auto-releases the `flock` when the holder exits, so there is no stale file to clean up - and it is opened with `O_NOFOLLOW` so a co-tenant cannot pre-plant a symlink at the predictable temp path to make the (possibly privileged) holder truncate an arbitrary file.
- **The in-guest shim no longer silently discards sandbox user/group creation errors, and `USER` names the real account.** A failed `useradd`/`groupadd` (e.g. the uid or the `sandboxuser` name already claimed under a different name) was ignored, and `USER` was hardcoded to `sandboxuser` even when no such passwd entry existed - so `USER` could name an account that was not there. Creation failures are now warned (never silent), and `USER` is resolved from the passwd entry for the uid the shim drops to, falling back to `sandboxuser` only when there is genuinely no entry.
- **The container backends no longer reinstall the pre-baked node on every guest, and the new krun backend persists mise-installed tools across runs like docker already did.** `MISE_DATA_DIR` points at the sandbox home (`~/.local/share/mise`), which is bind-mounted and persistent, so a tool the project installs inside the sandbox (python, go, etc.) is installed once and reused on later runs instead of being re-downloaded and re-extracted on every fresh guest - unchanged for docker, and now the same under krun. What is new on both is the pre-baked node: the image's `node@22` (which the AI CLIs run on) lives in the ephemeral image path (`/opt/mise`), so to keep it resolving instantly the in-guest shim now mirrors it into the persistent data dir as version-level symlinks on startup; without this the baked node is orphaned and every fresh guest stalls for minutes reinstalling it - badly so alongside a large global `~/.config/mise` config, where the reinstall also dragged in unresolvable `@latest` `npm:`/`go:`/`pipx:` tools and produced `node is not a valid shim` / `Failed to resolve tool version` errors. The seed is version-level (not whole-tool) so a node version the project installs itself still persists to the home rather than being redirected back into the ephemeral image. Your global mise config is still respected; the download cache is still persisted across runs; the bwrap backend, which uses the host's real mise install, is unchanged.
- **krun now refuses to launch on an Intel Mac instead of failing obscurely later.** libkrun's macOS backend is Hypervisor.framework on Apple Silicon, and there is no supported path on `darwin/amd64`, but the prerequisite check had no architecture probe - so an Intel Mac passed preflight and the launch only broke after the image build, with an opaque runtime error. The krun prerequisites now include the host CPU architecture, checked first so the failure names the unusable hardware rather than a tool you would install for nothing, and the message points at `--isolation=docker` on Intel Macs or a Linux host with `/dev/kvm`. `devsandbox doctor` reports the same as a `krun: platform` row.
- **`devsandbox config init` no longer emits config keys the current schema rejects.** The generated template documented an `[overlay] enabled` switch that no longer exists (the key is `default`, naming the mount-mode policy) and emitted `[tools.mise] writable` / `persistent`, neither of which mise reads any more - so copying the template's own suggestions produced settings that silently did nothing. The template now documents `overlay.default` with its five modes and the real mise keys (`mount_mode`, `ignore_global_config`), and a round-trip test asserts every key the generator emits is one the loader recognizes.

### Changed

- Embedded `pasta` upgraded to passt `2026_07_16.090d739` (from `2026_06_11.a9c61ff`). The statically linked pasta binary that backs sandbox networking is rebuilt from the newer upstream passt release. Embedded `bwrap` is unchanged at `v0.11.2`.
- **krun warns when building a project-provided Dockerfile, whose build runs outside the microVM boundary.** A custom Dockerfile (the `sandbox.docker.dockerfile` config key) is built by host `podman build` before the microVM boots, so its `RUN` steps execute on the host - outside the krun guest isolation and outside the proxy egress lockdown. krun now prints a warning before such a build, advising that only trusted Dockerfiles be used; the auto-generated default (config-dir) Dockerfile and every docker-backend build stay silent. This is a trust-boundary disclosure, not a behavior change - the build still proceeds. See [Build-time trust boundary](docs/getting-started/krun.md#build-time-trust-boundary).
- **Docker/krun startup timeouts now report the container's logs instead of a bare deadline.** On the Docker (and krun) backends the container runs detached while `devsandbox` polls for a readiness sentinel, so when the in-guest shim fails before signalling ready (e.g. `read overlay manifest: permission denied`), its fatal output went to the container log and never reached the user - the launch only surfaced an opaque `container setup timed out after 90s`. The timeout error now appends the last 50 lines of `docker logs`/`podman logs` for the stuck container, so the actual cause is visible without re-running the engine by hand. The krun port-forward PID-resolution timeout includes the same log tail in its warning. Log retrieval is best-effort and never masks the original timeout.

## [v0.17.3](https://github.com/zekker6/devsandbox/releases/tag/v0.17.3) - 2026-06-24

### Added

- **`DEVSANDBOX_DEBUG=1` proxy lifecycle tracing.** The MITM proxy now logs a per-request `CONNECT` / `request` / `response` trace to the internal proxy log (`devsandbox logs internal --type proxy`), including response status, content-type, streaming detection, and time-to-headers. Query strings are stripped so tokens are never logged. Use it to pinpoint where a hung or timed-out request stalls. See [Debugging the Request/Response Lifecycle](docs/proxy.md#debugging-the-requestresponse-lifecycle).

### Fixed

- **MITM proxy no longer buffers response bodies before relaying headers.** goproxy relays a response to the client only after the `OnResponse` handler returns and does not flush the body until the handler-supplied `resp.Body` is read, but request logging read the entire body with `io.ReadAll` to capture it. For any streaming response the body stays open until generation finishes, so the proxy withheld the response *headers* for the full duration - codex aborted with `Codex SSE response headers timed out after 20000ms` while the proxy spent 10-80s reading the stream (one HTTP upgrade buffered for 82s). Crucially these responses are not always identifiable by `Content-Type` (codex's streamed responses carry an empty `Content-Type`), so media-type sniffing alone could not avoid the buffering. The response body is now wrapped so it streams to the client unchanged while a bounded prefix (256 KiB) is captured for logging; the log entry is written when the body closes. The proxy never buffers a body before relaying headers, so SSE, chunked, empty-`Content-Type`, and large responses all stream incrementally. This also unbreaks **WebSocket (WSS) and other HTTP upgrades through MITM**: 1xx/`101 Switching Protocols` responses are now left untouched, so goproxy can type-assert `resp.Body` to `io.ReadWriter` and relay the upgraded connection (the old code read the 101 body and replaced it with a `bytes.Reader`, stalling the stream for up to 82s and failing the relay).

## [v0.17.2](https://github.com/zekker6/devsandbox/releases/tag/v0.17.2) - 2026-06-15

### Changed

- Embedded `pasta` upgraded to passt `2026_06_11.a9c61ff` (from `2026_05_07.1afd4ed`). The statically linked pasta binary that backs sandbox networking is rebuilt from the newer upstream passt release. Embedded `bwrap` is unchanged at `v0.11.2`.

## [v0.17.1](https://github.com/zekker6/devsandbox/releases/tag/v0.17.1) - 2026-05-13

### Fixed

- **Proxy no longer panics on requests with a nil `URL`.** goproxy can dispatch HTTPS requests whose `http.Request.URL` is nil when its internal `url.Parse` fallback fails (the parse error is swallowed and the request is still handed off). Every downstream step - credential injection, filtering, redaction, ask-mode, request logging - dereferences `req.URL`, so any such request crashed the proxy worker. `RequestLogger.LogRequest` now falls back to `RequestURI` when `URL` is nil, and the request handler short-circuits with a 403 (`malformed request: missing URL`) instead of dispatching downstream.

## [v0.17.0](https://github.com/zekker6/devsandbox/releases/tag/v0.17.0) - 2026-04-30

### Added

- **Audit-grade structured logging.** Per-session fields (`session_id`, `sandbox_name`, `sandbox_path`, `project_dir`, `isolator`, `pid`, `devsandbox_version`) on every dispatched entry, plus synthesized `session.start` / `session.end` lifecycle events and security events (`proxy.filter.decision`, `proxy.redaction.applied`, `proxy.credential.injected`, `proxy.mitm.bypass`, `mount.decision`, `notice.overflow`). See [Audit Logging](docs/configuration.md#audit-logging).
- **OTLP `header_sources`.** Resolve receiver headers from `value` / `env` / `file` at runtime so secrets stay on the host. See [Authenticating to an Auth-Enforced Endpoint](docs/configuration.md#authenticating-to-an-auth-enforced-endpoint).
- `NODE_USE_ENV_PROXY=1` is now set automatically in proxy mode so Node.js ≥24's built-in `fetch` (undici) honors `HTTP(S)_PROXY` - fixes `ENETUNREACH` from npx-based tools like `mcp-remote`.

## [v0.16.0](https://github.com/zekker6/devsandbox/releases/tag/v0.16.0) - 2026-04-29

### Added

- **Proxy `log_skip` rules.** Drop matching requests from the proxy log (local + remote dispatchers); the request itself still passes through. See [Skipping Log Entries](docs/proxy.md#skipping-log-entries).

## [v0.15.0](https://github.com/zekker6/devsandbox/releases/tag/v0.15.0) - 2026-04-29

### Added

- `devsandbox sandboxes prune --orphaned` flag to restrict pruning to orphaned sandboxes (those whose original project directory no longer exists). The flag intersects with other selectors: `--orphaned --older-than 30d` removes orphans last used over 30 days ago; `--orphaned --keep N` prunes orphans outside the N most-recently-used set; `--orphaned --all` (or `--orphaned` alone) removes every orphan. Without the flag, the existing default (orphans-only when no other selector is set) is unchanged.
- **Generic credential injector for proxy.** Define credential injection by `host` + `header` + `value_format` + `[...source]` + `overwrite` in TOML - no Go code change required to add a new service. Built-in `github` preset preserves existing config compatibility (`[proxy.credentials.github] enabled = true` works unchanged, including `GITHUB_TOKEN` → `GH_TOKEN` fallback). Specificity-based ordering when multiple injectors could match the same request (exact host > longer literal > shorter glob, tie-break by name). `BuildCredentialInjectors` now returns an error for invalid configs (unknown preset, missing `host`/`header`, invalid glob).

## [v0.14.1](https://github.com/zekker6/devsandbox/releases/tag/v0.14.1) - 2026-04-28

### Changed

- **`zellij` tool is now disabled by default.** Unlike `kitty`, the zellij socket has no capability filtering - exposing it lets sandboxed code drive the host multiplexer (run commands in any pane, read pane contents, etc.). Auto-detection of an active `ZELLIJ` session no longer mounts the socket or forwards `ZELLIJ*` env vars on its own. Set `[tools.zellij] enabled = true` to opt back in. `devsandbox tools check zellij` reports the opt-in requirement.

## [v0.14.0](https://github.com/zekker6/devsandbox/releases/tag/v0.14.0) - 2026-04-27

### Added

- `codex`, `opencode`, and `pi` tools now honor their respective custom config-location env vars on the host and forward them into the sandbox so the CLIs resolve the same paths inside:
  - `codex`: `CODEX_HOME` overrides `~/.codex`. When set, the host value is passed through and the directory is mounted at the same path.
  - `opencode`: `OPENCODE_CONFIG_DIR` is mounted in addition to (not in place of) `~/.config/opencode`, matching opencode's load semantics; the env var is forwarded.
  - `pi`: `PI_CODING_AGENT_DIR` overrides `~/.pi/agent`. The agent dir is still tmpoverlayed (settings/credentials are write-discarded) and the `sessions/` subdirectory is still persisted; the env var is forwarded.

## [v0.13.3](https://github.com/zekker6/devsandbox/releases/tag/v0.13.3) - 2026-04-20

### Fixed

- `kitty` proxy revdiff launch pattern now accepts the unquoted `/usr/bin/env` prefix the launcher actually emits (only `ENV_PREFIX` assignments and the inner argv are single-quoted). The literal absolute path is required - bare `env` (PATH-relative) still rejects, so `$PATH` shadowing can't be used to bypass the inner-program check.

## [v0.13.2](https://github.com/zekker6/devsandbox/releases/tag/v0.13.2) - 2026-04-20

### Fixed

- `kitty` proxy revdiff launch pattern: added `MatchShellExecEnvSentinel`, accepting `sh -c "'/usr/bin/env' 'KEY=VAL' ... '<prog>' '<arg>'...; touch '<sentinel>'"`. The revdiff launcher injects an `env` wrapper so the kitty-spawned overlay inherits `EDITOR`/`VISUAL` from the caller's login shell; the previous pattern matched only the no-env form. Env-var names are restricted to `^[A-Z_][A-Z0-9_]*$`, the inner argv is still validated against the existing revdiff pattern, and the sentinel-tail rules (no shell metacharacters, canonical path) are unchanged.

## [v0.13.1](https://github.com/zekker6/devsandbox/releases/tag/v0.13.1) - 2026-04-18

### Fixed

- `revdiff` tool no longer wipes its shared IPC directory on `Start`/`Stop`. Because the dir is exported as `$TMPDIR` for every sandboxed process, long-lived tenants (Claude Code's per-session task cache under `$TMPDIR/claude-<uid>/…/tasks/`, Node's compile cache, Go's build cache) populate subtrees that must survive sandbox restarts for the same project - and parallel sandboxes on the same project share the directory, so wiping it from one tore state out from under the others. The old `RemoveAll` on `Start` could yank state out from under a running caller; Node's non-recursive `fs.mkdirSync` then failed with `ENOENT`, breaking every subsequent Claude Code Bash tool call. `Start` now only ensures the dir exists (0700); `Stop` is a no-op. Stale revdiff sentinels are harmless - the launcher uses `mktemp` with fresh names.

## [v0.13.0](https://github.com/zekker6/devsandbox/releases/tag/v0.13.0) - 2026-04-17

### Added

- `[sandbox.environment.<NAME>]` config block: declare sandbox environment variables using the same source model as proxy credentials (`value` / `env` / `file`, priority `value > env > file`). `env = "X"` with `X` unset on the host silently skips the variable; an unreadable `file = "..."` is a startup error. Declaring the same variable in both `env_passthrough` and `environment` fails at startup with a message naming the variable - each variable belongs in exactly one place.
- `pi` tool: integrates [Pi Coding Agent](https://github.com/badlogic/pi-mono). `~/.pi/agent` is mounted with credential protection; `~/.pi/agent/sessions` persists across runs.
- `[proxy.credentials.github] overwrite = true`: force-replace any existing `Authorization` header on outgoing `api.github.com` requests. Intended for the pattern where a sandboxed CLI (e.g. `gh`) refuses to start without a token in its environment - pass a placeholder through `env_passthrough` / `sandbox.environment` while the real token stays on the host and is swapped in by the proxy. Default remains `false` (existing tool-set headers are preserved).
- `revdiff` tool now provides a shared IPC directory (`~/.cache/devsandbox/revdiff-ipc/<session>/`) bind-mounted at the same path on both sides and exported as `TMPDIR`. The kitty-spawned overlay shell runs on the host and receives sentinel/output paths as literal strings, so host and sandbox must agree on the string - argv-shipped paths need `Source == Dest` equality, not just a shared inode.

### Changed

- **Kitty tool now runs a capability-filtering proxy instead of bind-mounting the host socket.** The host kitty remote-control socket is no longer exposed inside the sandbox; a local proxy at `$HOME/.kitty.sock` is exposed instead, and `KITTY_LISTEN_ON` is rewritten to point at it. Sandboxed processes can only issue kitty commands declared as capabilities by an enabled tool (`launch_overlay`, `launch_window`, `launch_tab`, `launch_os_window`, `close_owned`, `wait_owned`, `focus_owned`, `send_text_owned`, `get_text_owned`, `set_title_owned`, `list_owned`), and `*_owned` commands are scoped by ownership tracking to windows the sandbox itself opened. Shell metacharacters in `sh -c` payloads for `launch_*` are rejected outright. `remote_control_password` is unsupported - use `allow_remote_control = socket-only`. New `[tools.kitty]` fields: `mode` (`auto` default / `disabled` / `enforce`) and `extra_capabilities` (additive; `launch_*` entries rejected). Under `auto`, the proxy only starts when at least one enabled tool declares a capability - zero attack surface when no tool needs kitty. `revdiff` is the built-in consumer.

### Fixed

- macOS: shortened test directory names to stay under the platform's unix socket path length limit (affected `kittyproxy` and `kitty` tool tests).

## [v0.12.0](https://github.com/zekker6/devsandbox/releases/tag/v0.12.0) - 2026-04-16

### Changed

- Wrapper diagnostic output (port-forward notices, session warnings, proxy setup info, container progress) no longer writes directly to stderr while a child process owns the terminal. Messages are written to `$XDG_STATE_HOME/devsandbox/wrapper.log` (or `~/.local/state/devsandbox/wrapper.log`) and a one-line banner is shown on exit if anything was suppressed. This prevents wrapper output from corrupting TUI applications (Claude Code, aider, etc.) running inside the sandbox. Pass `--verbose` or set `DEVSANDBOX_DEBUG=1` to restore the old behavior of writing every message to stderr.

### Added

- `--worktree` and `--worktree-base` flags: opt-in git-worktree mode. Bare `--worktree` auto-generates `devsandbox/<session-or-timestamp>` off HEAD; `--worktree=<branch>` reuses or creates a named branch. The sandbox CWD is the worktree; the main checkout is untouched. With `--git-mode=readwrite`, commits land on the worktree branch only. `--rm` removes the worktree on exit via `git worktree remove --force` + `prune`. `--worktree` + `--git-mode=disabled` is rejected at flag-parse time. Worktrees live at `~/.local/share/devsandbox/<project-slug>/worktrees/<branch>/` and the slug is derived from the main repo root so sibling worktrees share sandbox state. `devsandbox sandboxes prune` and the `doctor` command are worktree-aware.

### Fixed

- `zellij` and `kitty` tool socket bindings are now explicit bind mounts (`Type: MountBind`) instead of inheriting the default tmpoverlay from `CategoryRuntime`. Overlayfs cannot expose a unix socket from its lower layer, so under the previous policy the host socket was invisible inside the sandbox and `zellij list-sessions` / `kitten @` silently failed.
- Auto port-forwarding no longer tries (and fails) to forward when the sandbox shares the host network namespace. Without proxy mode the sandbox uses bwrap's `--share-net`, so a tool listener inside the sandbox is the same kernel socket as the "host" bind the forwarder would attempt - producing a spurious `bind: address already in use` error for every detected port. Auto-detect now inspects the sandbox netns inode and skips forwarding (with a one-line explanatory message) when it matches the host; the sandbox ports are already directly reachable on `127.0.0.1`. For the rare case where auto-forward runs in a properly isolated netns but the host happens to already have that port in use, the forwarder falls back to an ephemeral host port and logs the mapping instead of silently dropping the service.

## [v0.11.0](https://github.com/zekker6/devsandbox/releases/tag/v0.11.0) - 2026-04-14

### Added

- `zellij` tool forwards an active Zellij session into the sandbox by mounting the session socket directory and the `zellij` binary. Auto-detected when `ZELLIJ` is set and the binary is on `PATH`, so `zellij` commands run inside the sandbox attach to the host multiplexer.
- `zellij` tool now also mounts `$XDG_RUNTIME_DIR/zellij/`, which is where zellij 0.41+ stores its IPC socket (the legacy `/tmp/zellij-$UID/` holds only cache/log files on modern releases). The override env var is `ZELLIJ_SOCKET_DIR` (previously the tool checked the incorrect `ZELLIJ_SOCK_DIR`).

## [v0.10.0](https://github.com/zekker6/devsandbox/releases/tag/v0.10.0) - 2026-04-10

### Added

- `kitty` tool forwards the Kitty remote-control socket into the sandbox so `kitten @` commands inside the sandbox can drive the host terminal.

## [v0.9.3](https://github.com/zekker6/devsandbox/releases/tag/v0.9.3) - 2026-04-08

- `~/.local/bin` and `~/.local/share/claude` are now read-only bind mounts instead of persistent writable overlays. Under the split-mode default introduced in v0.8.0 these host-managed tool-install directories were being treated as `CategoryData`, which let in-sandbox tool self-updaters (e.g. Claude Code's own updater) write partial/empty files into the per-project overlay upper-dir. Those writes shadowed the real host binaries in every subsequent session, causing failures like `fish: '/home/$USER/.local/bin/claude' exists but is not an executable file` (exit 126).

## [v0.9.2](https://github.com/zekker6/devsandbox/releases/tag/v0.9.2) - 2026-04-08

### Fixed

- HTTP proxy no longer intercepts the body of HEAD requests. The previous behavior broke `Content-Length` handling and caused errors for some clients (e.g. Helm pulling OCI charts).

## [v0.9.1](https://github.com/zekker6/devsandbox/releases/tag/v0.9.1) - 2026-04-07

### Fixed

- Sandbox removal now `chmod`s files recursively before deletion. Go populates its build cache with `0500` files, which previously caused sandbox cleanup to fail.

## [v0.9.0](https://github.com/zekker6/devsandbox/releases/tag/v0.9.0) - 2026-04-07

### Added

- `devsandbox scratchpad [name] [command...]` subcommand for running sandboxes in managed, clean working directories under `~/.local/share/devsandbox-scratchpads/`. State persists between runs. Name defaults to `default`.
- `devsandbox scratchpad list` and `devsandbox scratchpad list --json` list scratchpads with size and state info.
- `devsandbox scratchpad rm <name>` (with `--all`, `--keep-state`, `--force`) removes scratchpads and their sandbox state.

### Fixed

- Git tool now strips sensitive fields from `.git/config` in place instead of replacing the file wholesale. The previous full replacement caused the git CLI to refuse to operate even for read-only commands inside the sandbox.

## [v0.8.2](https://github.com/zekker6/devsandbox/releases/tag/v0.8.2) - 2026-04-06

### Fixed

- Claude tool stores project knowledge under the `data` section so chat history persists between sandbox runs.

## [v0.8.1](https://github.com/zekker6/devsandbox/releases/tag/v0.8.1) - 2026-04-06

### Added

- macOS support for the devsandbox shim via a platform-specific copy-on-start overlay implementation, split from the Linux path.
- `jq` is now included in the default Docker image.

### Changed

- Debian base image bumped in the Docker image.
- mise-managed tool dependencies bumped.
- Docker and lint CI workflows limit concurrency to avoid redundant runs.

### Fixed

- Restored shim source files that were missing from the v0.8.0 release and added CI coverage so the shim is built and verified on every run.

## [v0.8.0](https://github.com/zekker6/devsandbox/releases/tag/v0.8.0) - 2026-04-05

### Breaking Changes

- **`[overlay] enabled` removed** - replaced by `[overlay] default` which accepts:
  `split` (default), `overlay`, `tmpoverlay`, `readonly`, `readwrite`.
- **`[tools.mise] writable` and `persistent` removed** - use `[tools.mise] mount_mode` instead.
  Mise no longer has tool-specific overlay configuration; use the unified `mount_mode` system.
- **Default mount behavior changed** - tool mounts now default to `split` overlay policy
  (configs → tmpoverlay, caches/data/state → persistent overlay) instead of read-only
  bind mounts. This prevents supply chain attacks from poisoning host tool configurations
  through sandboxed package managers.

### Migration Guide

| Before | After |
|--------|-------|
| `[overlay] enabled = true` | `[overlay] default = "split"` (or omit - it's the default) |
| `[overlay] enabled = false` | `[overlay] default = "readonly"` |
| `[tools.mise] writable = true, persistent = true` | `[tools.mise] mount_mode = "overlay"` |
| `[tools.mise] writable = true, persistent = false` | `[tools.mise] mount_mode = "tmpoverlay"` |
| (no equivalent) | `[tools.git] mount_mode = "readwrite"` |

### Added

- **Binding categories** - tools now classify each mount as `config`, `cache`, `data`,
  `state`, or `runtime`, enabling differentiated overlay policies.
- **`[overlay] default`** - global mount mode for all tool bindings with five modes:
  `split`, `overlay`, `tmpoverlay`, `readonly`, `readwrite`.
- **Per-tool `mount_mode`** - override the global default for specific tools
  (e.g., `[tools.git] mount_mode = "readwrite"`). Accepts `disabled` to prevent
  a tool's config from being mounted entirely.

### Changed

- Tool bindings no longer hardcode `ReadOnly` or `Type` - the builder resolves these
  based on the mount mode policy chain (per-tool > global > split).
- Claude, Copilot, Codex, and OpenCode tools previously mounted configs read-write;
  they now follow the global mount mode (default: tmpoverlay for configs).
