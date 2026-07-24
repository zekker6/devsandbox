# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased](https://github.com/zekker6/devsandbox/compare/v0.17.3...HEAD)

### Breaking Changes

- **bwrap proxy mode now requires `iproute2` and `nft` or `iptables` on the host, and its sandboxes are IPv4-only.** A `--proxy` launch on a host missing them, or missing the `nf_tables`/`ip_tables` and `nf_conntrack` kernel modules, now aborts naming what is missing instead of starting with egress open. There is no opt-out. `devsandbox doctor` reports the same check as the `proxy: firewall` row. Non-proxy launches are unaffected. See [Requirements](docs/proxy.md#requirements-bwrap-backend).
- **bwrap proxy mode now also requires a `pasta` that supports `--map-host-loopback`.** That option maps the proxy gateway, which is the lockdown's only permitted destination, so on an older `pasta` every connection would hang until it timed out. The launch is now refused up front. The embedded `pasta` always supports it, so this is reachable only with `use_embedded = false` or a failed extraction.
- **The container image no longer bakes in `gh` (GitHub CLI) or `neovim`; the image is slimmer.** Rebuild with `--build-arg INSTALL_GH=true` / `INSTALL_NVIM=true`, or add them in a derived image. Their host configs are still mounted as before. The Dockerfile also gained `SANDBOX_HOME`, `SANDBOX_UID`, `SANDBOX_GID` and `NODE_VERSION` build args.

### Added

- **New `rtk` tool, so the [rtk](https://github.com/rtk-ai/rtk) CLI proxy keeps its configuration and history inside the sandbox.** Your filters and `config.toml` now apply in the sandbox, and the tracking database behind `rtk gain` accumulates across runs instead of being recreated empty on every launch. The host's database is never modified. See [Tools: rtk CLI Proxy](docs/tools.md#rtk-cli-proxy).
- **herdr terminal workspace support, through a capability-filtering proxy.** Running devsandbox inside a [herdr](https://herdr.dev) session lets sandboxed tools open a review overlay in a herdr tab - `revdiff` is the first consumer. The host control socket is never bind-mounted; a proxy is, and it permits only the methods enabled tools declare, denying the rest of the 86 herdr exposes. Configure with `[tools.herdr] mode = "auto" | "disabled" | "enforce"`. See [herdr Terminal Workspace](docs/tools.md#herdr-terminal-workspace).
  - **herdr can now capture and restore a sandboxed agent's native session.** A new `agent_reporting` capability permits three reporting methods and nothing else, so herdr can record an agent's own session ID or transcript path and resume it after a restart. It activates only for a direct `devsandbox claude|pi|codex` launch inside a herdr pane, because that is the only case where devsandbox knows both anchors every report is checked against; starting a shell and typing `claude` inside it does not enable it. **Behavior change:** such a launch previously started no proxy and now starts the filtered one, and a herdr control socket that cannot be reached is no longer fatal in the default `auto` mode. See [Agent session capture and restore](docs/tools.md#agent-session-capture-and-restore).
  - **Launching an agent from a herdr pane now records which sandbox owns that pane, so a resume cannot silently start a new session.** `run-agent` refuses a resume-shaped invocation unless re-entry would reach the same project directory and the same sandbox state root. A `--worktree` launch is the case that makes this necessary: its state root is derived from the repo root while the session runs in the worktree, so re-entering would open a different session store and begin a fresh session while appearing to resume.
- **New `devsandbox agent-wrappers activate <shell>`, so supported agents run sandboxed by default.** It prints shell functions for you to evaluate from your own startup file, the way `mise activate` does - `devsandbox agent-wrappers activate fish | source`, or `eval "$(devsandbox agent-wrappers activate bash)"`. Typing `claude` then runs `devsandbox claude` in the current directory with every argument passed through, while `claude-no-ds` and `command claude` still reach the real binary. **Nothing is written to disk and no startup file is ever edited.** Because the definitions are regenerated at every shell start they cannot go stale: a newly installed agent, or an upgrade that moved the devsandbox binary, is picked up by the next shell. Nothing is wrapped inside a sandbox. The feature is independent of herdr and useful on its own. See [Tools: Shell wrappers](docs/tools.md#shell-wrappers---run-agents-sandboxed-by-default).
  - **The wrappers never resolve devsandbox through `PATH`.** Each definition carries the absolute path `activate` resolved for itself, and a path that has disappeared fails closed with `devsandbox: no executable at <path>` and exit 127 rather than falling through to the unwrapped agent or looking devsandbox up in `PATH`, which may name a directory sandboxed code can write to. Only the once-per-shell-start `activate` call itself goes through `PATH`, so an upgrade that moves the binary self-heals.
  - **New `devsandbox run-agent <agent> [args...]` command, the entrypoint for the wrappers.** It forwards every argument untouched, so `run-agent claude --resume ID` reaches the agent instead of being parsed as a devsandbox flag, and it executes the real agent directly when already inside a sandbox so a wrapper visible in-sandbox cannot recurse.
- **Resource limits are now available on every isolation backend, under a backend-neutral `[sandbox.resources]` section.** `memory`, `cpus` and the new `pids` are configured in one place instead of the docker-scoped block they used to live in. Limits are opt-in, and one that cannot be enforced aborts the launch rather than silently running unlimited. See [Resource Limits](docs/configuration.md#resource-limits).
  - **The bwrap backend can now cap memory, CPU and process count.** A runaway build or fork bomb previously consumed host resources unchecked on the default Linux backend. The sandbox now runs inside a systemd transient scope carrying the equivalent cgroup v2 controls, which requires cgroup v2 and a systemd user session with the needed controllers delegated. `memory` bounds resident memory but not swap, which is weaker than docker and krun. See [Sandboxing: Resource Limits](docs/sandboxing.md#resource-limits).
  - **The docker backend now honors `pids`** as `--pids-limit`, so a fork bomb inside the sandbox cannot exhaust the host's PID space. The first run after upgrading recreates any kept container once. krun skips the flag and warns: a host-side cap would limit the VMM's own threads, not the guest's processes.
- **The container backends (`docker` and `krun`) now make host-installed mise tools available inside the sandbox on Linux hosts.** The host's `~/.local/share/mise/installs` is mounted read-only and mirrored into the sandbox mise data dir on startup, so your host toolchain resolves in-guest without reinstalling or network access - previously the main reason a mise-centric workflow felt unusable under `krun`. Versions installed inside the sandbox still take precedence. Host tools compiled against a newer glibc than the guest image may not run there, and macOS hosts share nothing. See [Tools: mise](docs/tools.md#tool-management-with-mise).
  - **New `[tools.mise] ignore_global_config` option to stop the sandbox reading the host's global mise config.** A large host config full of `@latest` specs made every shell start resolve them over the network, which on a proxy or egress-locked sandbox hangs and can OOM the guest. Setting it points `MISE_GLOBAL_CONFIG_FILE` at `/dev/null` in the sandbox; the project `.mise.toml`, the image's system config and `settings.toml` still apply. Defaults to `false`.
- **Experimental `krun` microVM isolation backend.** `--isolation krun` runs the same sandbox image inside a [libkrun](https://github.com/containers/libkrun) microVM, so the workload gets its own guest kernel behind a hardware virtualization boundary - the right boundary for genuinely untrusted code, where a host-kernel exploit must not reach the host. It is opt-in, never auto-selected, ephemeral, and runs rootless. Requires `podman`, a `crun` built with libkrun, and `/dev/kvm` on Linux or Apple Silicon on macOS; `devsandbox` fails fast with installation guidance when a prerequisite is missing. See [krun microVM backend](docs/configuration.md#krun-microvm-backend-experimental) and the [getting-started guide](docs/getting-started/krun.md).
  - **Egress lockdown:** in proxy mode krun installs a host-side, deny-by-default firewall in the VMM's network namespace, permitting only loopback, established traffic and TCP to the proxy port - so the LAN, cloud metadata at `169.254.169.254`, external DNS and every non-proxy gateway port are closed without being enumerated. It runs host-side because under libkrun TSI the guest has no routable interface. The in-guest shim waits for it before running anything, so untrusted code never runs with egress open, and any failure tears the microVM down. See [krun backend](docs/proxy.md#krun-backend).
  - **Guest tooling:** an egress-locked guest runs mise offline, so `@latest` specs resolve instantly from seeded host installs instead of stalling on lookups that never traverse the proxy. krun runs no boot-time install pass, so a tool your `.mise.toml` pins that is neither seeded nor already present must be installed in-sandbox with `MISE_OFFLINE=0 mise install`.
  - **Overlay copies:** krun copies overlay and `tmpoverlay` tool directories into the guest, since it cannot mount kernel overlayfs over virtio-fs. Sockets, FIFOs and device nodes in the source are skipped as the runtime artifacts they are, instead of aborting the launch.
  - **Management parity:** `devsandbox doctor` reports krun prerequisites as advisory rows with remediation, `sandboxes list` and `sandboxes prune` cover ephemeral krun sandboxes, and `forward` is best-effort. A second launch while a session is already active fails fast with a copy-pasteable `podman rm -f`, instead of aborting only after the image was rebuilt.

### Fixed

- **Proxy mode no longer leaves the host's own loopback services directly reachable from the sandbox.** pasta forwards host ports into the namespace unless told otherwise, and loopback is the one interface the egress firewall must permit - so a local database or dev API stayed reachable at `127.0.0.1:<port>` even though the same service was correctly refused at the proxy gateway. Proxy-mode launches on bwrap and krun now disable automatic forwarding for every protocol without a configured outbound [port forwarding rule](docs/configuration.md#port-forwarding).
- **Claude and Pi sessions started inside the sandbox are no longer discarded on exit.** Their persistent session overlays were skipped when the host directory did not exist - exactly the case for a user who authenticated on the host and only ever runs the agent sandboxed - so every transcript vanished with the sandbox and herdr would resume into nothing. devsandbox now creates those directories on the host when the agent's own directory exists. `CLAUDE_CONFIG_DIR` and `PI_CODING_AGENT_DIR` are honored.
- **`claude -c` / `claude --continue` are now covered by the herdr worktree guard.** The guard recognized only herdr's own resume argv, but `--continue` reopens the same per-project session store `--resume` does - so in a pane launched with `--worktree` it silently began a new conversation.
- **A herdr pane record that cannot be parsed now names its file.** The failure refuses every resume in that pane, and deleting the file is the only way out - which the old error gave no way to find.
- **Codex sessions started inside the sandbox are no longer discarded on exit, so `codex resume` can find them.** All of `~/.codex` was mounted as a config directory and got a tmpoverlay, so every rollout file vanished with the sandbox and a later resume reported `no rollout found for thread id`. `~/.codex/sessions` is now bound separately with a persistent overlay while the Codex home keeps its tmpoverlay, and devsandbox creates the directory on the host when it is missing. `CODEX_HOME` is honored.
- **`revdiff` can now actually open its review overlay in a herdr tab.** The launcher shell-quotes the generated script path, sending `sh '<path>'`, but the proxy recognized only the unquoted form - so every launch was denied and the launcher reported only `herdr pane run failed`. The proxy now strips one layer of single quotes before validating the path; a remainder still containing a quote is left alone and still rejected.
- **The docker backend no longer aborts startup with `fork overlay child: operation not permitted`.** Realizing a `tmpoverlay` directory required namespace flags that Docker's default seccomp profile denies without `CAP_SYS_ADMIN`, so effectively every `isolation = "docker"` sandbox failed to start. Those directories now use the same copy-on-start path macOS and krun already used, so no capability or seccomp relaxation is needed and a previous run's writes are still never visible.
- **The `krun: system pasta` doctor row says when its warning may be a false positive.** The probe only searches `$PATH`, while podman also looks in `helper_binaries_dir`, so a host that installs pasta there was told to install what it already has. The remediation now names the limitation and gives the command that settles it.
- **The kitty proxy no longer accepts a launch command that names a sandbox-planted binary.** Launch patterns matched `argv[0]` on basename alone, and the revdiff IPC directory is a write-through bind mounted at an identical path on the host - so sandboxed code could drop its own executable there, name it in a `kitty @ launch` request, and have kitty run it on the host as the host user. Patterns are now pinned to the program's resolved absolute path, and an unresolvable binary denies every launch rather than falling back.
- **A second session for the same project no longer breaks a running session's notifications, Docker access and kitty remote control.** The portal, Docker and kitty sockets were keyed only on the project, so a second session unlinked the live session's socket and its exit deleted the path outright. Each session's sockets now live in a directory private to the owning process; `DOCKER_HOST` and `KITTY_LISTEN_ON` point at `$HOME/.run/<pid>/`.
- **A socket path too long for the kernel is now reported as such.** `bind(2)` rejects anything past 107 bytes with a bare `invalid argument`, which the portal surfaced only as an opaque timeout. The portal, Docker, kitty and herdr proxies now report the path, its length, the limit and the remedy.
- **Proxy-mode sandboxes no longer stall for minutes resolving `@latest` mise tool specs.** Some backends' lookups never traverse the proxy and hang to their 20s timeout, and mise re-resolves per listed row - a single `mise ls` was measured at 14 minutes. All proxy-mode sandboxes now bound remote lookups at 3s, and a value you set through the sandbox env config takes precedence. Under krun the offline-mise layer removes the lookups entirely: the same `mise ls` went to under a second.
- **`tmpoverlay` config dirs that degrade to a copy-on-start overlay (krun anywhere, docker on macOS) are now reset to the host source on every run.** The copy only wrote source entries and never removed extraneous ones, so anything a previous - possibly untrusted - run left under the target survived into the next session, defeating tmpoverlay's discard-on-exit promise. The clear is mount-aware, preserving nested read-only bindings, and it removes rather than follows any symlink planted at the target or an intermediate path component.
- **Docker isolation no longer hangs at startup for non-root users when a tool uses an overlay mount.** `CAP_DAC_OVERRIDE` was granted only to krun, so under rootful Docker container-root could not read the host-owned overlay manifest and exited before signalling ready - the launch then waited out the 90s readiness timeout. It is now granted on docker too, scoped to the shim's root setup phase; the workload never holds it.
- **A kept Docker container is now actually reused when a tool uses an overlay mount, instead of being destroyed and rebuilt on every launch.** The overlay manifest was a per-run temp file that the container binds permanently, so every later start mounted a path that no longer existed and fell back to recreating the container - `keep_container` bought nothing and all container state was lost each run. The manifest now lives at a stable per-project path and is rewritten in place.
- **A container whose startup fails now reports the failure immediately, instead of after the 90s readiness timeout.** The readiness probe only polled for the shim's ready sentinel, which fails identically for a container that is still booting and one that has already died. It now also checks container state and reports the exit code alongside the log tail.
- **Piped stdin now reaches non-interactive krun and docker commands.** The container ran without `-i`, so `data | devsandbox --isolation krun - tool` closed the workload's stdin and silently lost the input. bwrap, which runs the workload as a direct child, was unaffected.
- **A sandboxed command's exit code now propagates to the host instead of collapsing to `1`.** `devsandbox - sh -c 'exit 42'` exited `1` on every backend, because a non-zero command result was treated as a generic CLI error - which also printed a spurious `Error:` line. Genuine setup failures still exit `1` loudly, and a container-engine launch failure (exit `125`) is surfaced as an error rather than passed off as the workload's own status.
- **The session and proxy lock files are no longer unlinked on release, and are opened with `O_NOFOLLOW`.** Unlinking reopened a split-lock race in which two holders could run at once, and the predictable temp path let a co-tenant pre-plant a symlink for the holder to truncate.
- **The in-guest shim no longer silently discards sandbox user/group creation errors, and `USER` names the real account.** A failed `useradd`/`groupadd` was ignored, and `USER` was hardcoded to `sandboxuser` even when no such passwd entry existed. `USER` is now resolved from the passwd entry for the uid the shim drops to.
- **The container backends no longer reinstall the pre-baked node on every guest, and krun now persists mise-installed tools across runs like docker already did.** The image's `node@22` lives in the ephemeral image path, so without seeding it into the persistent data dir every fresh guest stalled for minutes reinstalling it, with `node is not a valid shim` errors alongside a large global mise config. The seed is version-level, so a node version the project installs itself still persists to the sandbox home.
- **krun now refuses to launch on an Intel Mac instead of failing obscurely later.** There is no supported libkrun path on `darwin/amd64`, but the prerequisite check had no architecture probe, so the launch only broke after the image build with an opaque runtime error. The check now runs first and points at `--isolation=docker`; `devsandbox doctor` reports it as a `krun: platform` row.
- **`devsandbox config init` no longer emits obsolete config keys.** The template documented an `[overlay] enabled` switch that no longer exists and emitted `[tools.mise] writable`/`persistent`, which mise no longer reads - so copying the template's own suggestions produced settings that silently did nothing. A round-trip test now asserts every key the generator emits is one the loader recognizes.
- **A `hidden` mount rule that hides nothing is now reported at launch, and its remediation names something that actually works.** A pattern that resolves to a directory cannot be replaced by `/dev/null`, and the skip was only written to the log file - so `pattern = "secrets/**"` gave a fully readable secrets directory with nothing on the terminal to say so. The skip is now a startup warning, and every surface gives the only remedy there is: match the files inside the directory (`**/secrets/**`).
- **`devsandbox doctor` no longer fails the run because past sandbox runs logged errors.** The `logs` row escalated to an error above 10 errors in 24h, so `doctor` exited `1` with "Please install missing dependencies" on a host where nothing was missing. Recent log errors say nothing about whether a sandbox can launch now, so the row is advisory at any count - which makes `doctor` usable as a CI gate again. The failure summary now names the rows that failed instead of always blaming missing dependencies.
- **The `hidden` mount mode is now documented as files-only, and the README no longer claims the sandbox exposes "nothing else".** The config template and the schema comment both described `hidden` as hiding a file or a directory, while the builder only overlays files. The README's isolation summary also omitted the read-only host system paths and sanitized configs the defaults mount; what is not mounted - SSH keys, cloud credentials, sibling projects - is unchanged. Documentation only.

### Changed

- **bwrap proxy mode is now enforced, not best-effort.** The old route surgery discarded its own errors and exec'd the workload regardless, so any host where `ip` is not on the user's `PATH` started with **egress fully open** and nothing reported it. Even where it applied, the sandbox's own subnet stayed reachable (deleting the default route does not remove the on-link route), so the router UI, a NAS, the LAN DNS resolver and a cloud host's `169.254.169.254` metadata endpoint were all still directly reachable, along with every host loopback port and all of IPv6. A proxy-mode sandbox now gets the same deny-by-default lockdown krun applies, installed before the workload exists, and any failing step aborts the launch. This scopes the sandbox's **path** to the proxy, not its reachable **destinations** - those are still decided by [HTTP filtering](docs/proxy.md#http-filtering), so a metadata or LAN address is refused as a direct socket and still served through the proxy, where it is visible and refusable. Configured outbound port-forwarding rules keep working; host loopback ports that were not configured as one are no longer reachable at the gateway. See [Backend-specific behavior](docs/proxy.md#backend-specific-behavior).
- **`devsandbox doctor` checks the proxy firewall prerequisite for every backend, and checks it by using it.** The old `krun: firewall` row only looked for an `nft` or `iptables` binary, and its name told bwrap users - the default backend, which now also aborts a `--proxy` launch without a working firewall - that it did not concern them. The new top-level `proxy: firewall` row applies the real lockdown rule set in a throwaway namespace, so a host with nftables installed but no loadable `nf_conntrack` is reported here instead of failing mid-launch. It stays advisory.
- **Resource limits moved to a backend-neutral `[sandbox.resources]` section, and gained a `pids` limit.** `[sandbox.docker.resources]` still works but is **deprecated**, and it stays scoped to the container backends, which merge it field by field with the new section. **bwrap does not read the deprecated block at all**: it never honored it, and since it aborts when a limit cannot be enforced, applying a docker-oriented config there would turn a working setup into a failed startup on any host without a systemd user session. Validation errors now name the block they came from. See [Resource Limits](docs/configuration.md#resource-limits).
- **`devsandbox doctor` no longer closes with "All checks passed!" when rows warned.** A run with no failures but one or more warnings now ends with `All required checks passed (N advisory warning(s))`, and the "How to fix" block sits directly above that line instead of being separated from it by the tools table. The exit code is unchanged - only errors fail the run.
- Embedded `pasta` upgraded to passt `2026_07_16.090d739` (from `2026_06_11.a9c61ff`). Embedded `bwrap` is unchanged at `v0.11.2`.
- **krun warns when building a project-provided Dockerfile, whose `RUN` steps execute on the host** - outside the microVM boundary and outside the proxy egress lockdown. The build still proceeds; the auto-generated default and every docker-backend build stay silent. See [Build-time trust boundary](docs/getting-started/krun.md#build-time-trust-boundary).
- **Docker and krun startup timeouts now report the container's logs instead of a bare deadline.** The in-guest shim's fatal output went to the container log and never reached the user, so a launch that failed a second into setup surfaced only as `container setup timed out after 90s`. The last 50 log lines are now appended, best-effort and never masking the original timeout.
- **Proxy-mode, content-redaction and `.env` masking claims now state their actual limits wherever they appear.** `--help`, the config template, the README, the landing page and the guides all described proxy mode as routing or logging *all* HTTP(S) traffic and `.env` masking as unconditional. Neither holds: Docker is env-var routing with no network-level enforcement, and `.env` masking scans 3 directory levels below the project root, skipping `node_modules`, `.git`, `vendor` and `.venv`. Content redaction only sees requests that reach the proxy, and HTTPS bodies, headers and URLs only when MITM is enabled. See [Redaction Coverage](docs/proxy.md#redaction-coverage). Documentation only.

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
