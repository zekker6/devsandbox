# Where to Start

**New to devsandbox?** Read in this order:

1. [Sandboxing](sandboxing.md) -- understand what's isolated and what's accessible
2. [Tools](tools.md) -- see how your development tools work inside the sandbox
3. [Use Cases](use-cases.md) -- set up shell aliases, autocompletion, and common workflows

**Want to inspect network traffic?**

- [Proxy Mode](proxy.md) -- enable the MITM proxy, view logs, set up HTTP filtering and content redaction

**Configuring for your team or project?**

- [Configuration](configuration.md) -- config file reference, per-project overrides, credential injection, remote logging

## Quick Links

| Task | Where to Look |
|---|---|
| Understand the security model | [Sandboxing: Security Model](sandboxing.md#security-model) |
| Enable proxy mode | [Proxy: Enabling Proxy Mode](proxy.md#enabling-proxy-mode) |
| View proxy traffic logs | [Proxy: Viewing Logs](proxy.md#viewing-logs) |
| Set up HTTP filtering | [Proxy: HTTP Filtering](proxy.md#http-filtering) |
| Scan requests for secrets | [Proxy: Content Redaction](proxy.md#content-redaction) |
| Configure git access | [Tools: Git](tools.md#git) |
| Set up AI assistants | [Tools: AI Coding Assistants](tools.md#ai-coding-assistants) |
| AI agent recommended config | [Configuration: AI Agent Config](configuration.md#ai-agent-recommended-config) |
| Run Claude Code autonomously | [Use Cases: Claude Code](use-cases.md#running-claude-code-autonomously) |
| Enable Docker socket access | [Tools: Docker](tools.md#docker) |
| Desktop notifications in sandbox | [Tools: XDG Portal](tools.md#xdg-desktop-portal-linux-only) |
| Per-project config files | [Configuration: Per-Project](configuration.md#per-project-configuration) |
| Remote logging (syslog, OTLP) | [Configuration: Remote Logging](configuration.md#remote-logging) |
| Port forwarding | [Configuration: Port Forwarding](configuration.md#port-forwarding) |
| Shell aliases and autocompletion | [Use Cases: Shell Autocompletion](use-cases.md#shell-autocompletion) |
| Custom mount rules | [Sandboxing: Custom Mounts](sandboxing.md#custom-mounts) |
| Overlay filesystem | [Sandboxing: Overlay Filesystem](sandboxing.md#overlay-filesystem) |
| Docker backend details | [Sandboxing: Docker Backend](sandboxing.md#how-it-works-docker) |
| Troubleshooting | [Sandboxing: Troubleshooting](sandboxing.md#troubleshooting) |

## Documentation Pages

| Page | Description |
|---|---|
| [Sandboxing](sandboxing.md) | How isolation works on each platform, filesystem layout, security model, overlay mounts, custom mounts, data locations, Docker backend internals |
| [Proxy Mode](proxy.md) | MITM proxy setup, traffic inspection, log viewing and filtering, HTTP request filtering with ask mode, content redaction, remote logging destinations |
| [Tools](tools.md) | mise integration, shell and editor configuration, prompt indicators, AI assistant setup, Git modes, Docker socket proxy, language-specific notes |
| [Configuration](configuration.md) | TOML config reference, proxy settings, credential injection, isolation backend, custom mounts, port forwarding, overlay settings, per-project configs, conditional includes |
| [Use Cases](use-cases.md) | AI assistant workflows, shell autocompletion (bash/zsh/fish), aliases, development workflows by language, security monitoring scripts |

[Back to README](../README.md)
