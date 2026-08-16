---
name: devsandbox-config
description: Answer devsandbox configuration and setup questions from the published documentation instead of from memory - which TOML key to set, what values it accepts, which file it belongs in, and how the files merge. Covers proxy mode, HTTP filtering, credential injection, content redaction, isolation backends (bwrap/docker/krun), resource limits, overlay and mount modes, port forwarding, remote logging and audit, and per-tool sections such as [tools.git] and [tools.docker]. Activates on "devsandbox config", "configure devsandbox", "devsandbox.toml", ".devsandbox.toml", "devsandbox settings", "how do I let the agent commit", "allow network access in devsandbox", "which isolation backend", "devsandbox memory limit", "devsandbox cpu limit", "where is the devsandbox config file", "devsandbox proxy settings", "devsandbox filter rules", "devsandbox credential injection", "why is my devsandbox setting ignored", "devsandbox per-project config".
allowed-tools: [WebFetch, Read, Grep, Glob, Bash, Edit, Write]
---

# devsandbox configuration

Answer configuration questions from the devsandbox documentation, fetched at the time you answer.

## The one rule

**Every section, key and value you name must come from a source you read in this session.**

devsandbox ignores a key it does not recognize rather than rejecting it, so an invented key produces a config that looks applied and does nothing. A guess here is worse than no answer. If you cannot find a setting, say it does not exist and name where you looked. Never derive a key from a similar one - `keep_container` has no `keep_containers`, and `[tools.git]` taking `mode` does not mean `[tools.docker]` does.

**If the documentation cannot be fetched, say so and stop.** No network means no grounding, and answering from memory is the failure this skill exists to prevent. Offer the local alternatives below instead.

## Sources, most authoritative first

1. **A devsandbox checkout, if one is at hand.** When the working directory is the devsandbox repository, read `docs/*.md` directly - those match the code in front of you, and `grep -n 'toml:"' internal/config/config.go` lists every key of the fixed schema. Prefer this over fetching.
2. **The user's own machine**, for anything that depends on their setup rather than on what the software supports:

   ```bash
   devsandbox config path        # where the global config lives
   devsandbox config show        # the resolved configuration, after merging
   devsandbox doctor             # installation, backend, firewall and config health
   devsandbox tools list         # which tools are detected here
   devsandbox tools info <tool>  # what a tool binds and what it accepts
   devsandbox filter show        # active HTTP filter rules
   devsandbox trust list         # directories whose .devsandbox.toml is trusted
   devsandbox --version          # compare against the docs, see "Version skew"
   ```

3. **The published documentation**, via `WebFetch`. This is the normal path.

## Fetching the documentation

Pages live under `https://zekker6.github.io/devsandbox/docs/`, one per source file, with the `.md` dropped and a trailing slash.

`WebFetch` answers your prompt against the page using a small model, so **ask it to quote, not to summarize**. A summarized TOML block is exactly where a wrong key name comes from. Phrase the prompt like:

> Quote verbatim the TOML example and the surrounding prose from the "HTTP Filtering" section. List every key it shows and the values each accepts. If the section does not exist, say so.

Start at `configuration/` and its **Quick Reference** table, which lists every configuration section with its keys - it is the authoritative index of what exists.

## Which page answers which question

| The question is about | Fetch | Ask about the section |
|---|---|---|
| whether a section or key exists at all | `.../docs/configuration/` | Quick Reference |
| `[proxy]` - `enabled`, `port`, `mitm`, `extra_env`, `extra_ca_env` | `.../docs/configuration/` | Proxy Settings; Proxy Extra Environment Variables; Proxy Extra CA Environment Variables |
| `[proxy.credentials.<name>]` - keeping a token on the host | `.../docs/configuration/`, then `.../docs/proxy/` | Proxy Credentials; then Credential Injection for presets, source types, specificity, `overwrite` |
| `[proxy.filter]` - allowing or blocking destinations | `.../docs/proxy/` | HTTP Filtering - pattern types, scopes, ask mode, generating rules from logs |
| `[proxy.redaction]` - secrets in outgoing requests | `.../docs/proxy/` | Content Redaction |
| `[proxy.log_skip]` - keeping entries out of the log | `.../docs/proxy/` | Skipping Log Entries |
| proxy mode failing to start, pasta or egress lockdown errors | `.../docs/proxy/` | Requirements (bwrap backend); Troubleshooting |
| `[sandbox] isolation` - bwrap vs docker vs krun | `.../docs/configuration/`, `.../docs/sandboxing/` | Isolation Backend; Isolation Backends |
| `[sandbox.resources]` - `memory`, `cpus`, `pids` | `.../docs/configuration/`, `.../docs/sandboxing/` | Resource Limits |
| `[sandbox]` - `base_path`, `use_embedded`, `config_visibility`, `env_passthrough` | `.../docs/configuration/` | Sandbox Settings; Sandbox Environment Variables |
| `[sandbox.mounts.rules]` - exposing an extra host path | `.../docs/configuration/`, `.../docs/sandboxing/` | Custom Mounts |
| `[overlay]` and per-tool `mount_mode` - what persists between runs | `.../docs/configuration/`, `.../docs/sandboxing/` | Overlay Settings; Overlay Filesystem |
| `[port_forwarding]` - reaching a listener inside the sandbox | `.../docs/configuration/`, `.../docs/sandboxing/` | Port Forwarding |
| `[tools.git] mode` - letting an agent commit | `.../docs/tools/` | Git; Modes |
| `[tools.docker]`, `[tools.mise]`, `[tools.portal]`, `[tools.kitty]`, `[tools.herdr]`, `[tools.zellij]` | `.../docs/tools/` | Docker; Tool Management with mise; XDG Desktop Portal; Kitty Terminal; herdr Terminal Workspace; Zellij Terminal Multiplexer |
| running a specific agent (Claude Code, codex, copilot, aider, opencode, pi) | `.../docs/tools/`, `.../docs/use-cases/` | AI Coding Assistants |
| shell wrappers that make `claude` mean `devsandbox claude` | `.../docs/tools/` | Shell wrappers: run agents sandboxed by default |
| `[logging]` - syslog, OTLP, audit events | `.../docs/configuration/` | Remote Logging; Audit Logging |
| `[[include]]`, `.devsandbox.toml`, trust, merge order | `.../docs/configuration/` | Per-Project Configuration; Config Priority |
| a key that was ignored, or a value that failed the load | `.../docs/configuration/` | Unrecognized Keys; Values of the Wrong Type |
| what the sandbox blocks by default | `.../docs/sandboxing/` | Security Model |
| whether something is possible at all on this backend | `.../docs/about/limitations/` | whole page |
| setting up the krun microVM backend | `.../docs/getting-started/krun/` | whole page |

## Ground truth when the docs do not settle it

The docs describe intent; the Go structs are what the decoder actually matches. When a section is silent or looks stale, fetch the source and ask for the tags verbatim:

- `https://raw.githubusercontent.com/zekker6/devsandbox/main/internal/config/config.go` - every key of the fixed schema, as a `toml:"…"` tag. It also holds `GenerateDefault()`, the annotated template `devsandbox config init` writes.
- `https://raw.githubusercontent.com/zekker6/devsandbox/main/internal/sandbox/tools/git.go` and its siblings in that directory - the keys of each `[tools.<name>]` section, in a struct named `<tool>Config`.

A tag that is not there is not a key. Prefer a local checkout for this when one exists: grep enumerates tags exactly, where a fetched summary can miss one.

## Shape of a good answer

1. The TOML snippet, with only the keys the question needs.
2. Which file it goes in - `~/.config/devsandbox/config.toml` for a global setting, `.devsandbox.toml` in the project root for a per-project one, an `[[include]]` target for a directory pattern. A `.devsandbox.toml` needs trust approval on first use, and is skipped with a warning when nothing can answer the prompt.
3. Where you read it: the page and the section name.
4. Anything that gets overridden - CLI flags beat every file, and merge order is defaults, global config, includes, local config, flags.
5. The verification command below.

Keep it to the question asked. Do not tour adjacent settings.

## Verify before handing it over

devsandbox reports both failure modes itself, so the check is cheap:

```bash
devsandbox config show
```

An unrecognized key is listed on stderr under its full dotted path and stays ignored. A recognized key with a value of the wrong type fails the load and names the section. Silence on both means the config parsed and the keys are real. `devsandbox doctor` reports the same in its `config` row.

This matters more than usual here: the documentation you fetched describes the current release, not necessarily the binary in front of you.

## Version skew

The published site tracks the default branch. The user's `devsandbox` may be older, so a key that exists in the docs may not exist in their build. When an answer hinges on a recent setting, compare `devsandbox --version` against the changelog at `https://zekker6.github.io/devsandbox/docs/about/changelog/` and say which version introduced it.
