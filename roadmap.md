# Roadma

review feedback:
- $PWD should be forwarded as is
- configuration for tools should be forwarded into the sandbox (e.g. nvim, mise, git etc)
- set up claude code, opencode, codex, copilot in provided container image

- forward specific unix sockets into the sandbox
    - allow read only docker access
        - use proxy for docker socket?
- forward specific tcp/udp ports into the sandbox
- support for GUI applications
- embed pasta/bwrap for simpler installation
- configuration support
    - allow to configure tools to be enabled
        - allow to generate config for tools based on detected tools availability
- capture more audit data (e.g. files access, attempts to access restricted files, attempts to access network etc)
    - collection modes:
        - ebpf - linux only, very fast and efficient
        - fsnotify - cross-platform, but less efficient

Backlog:

- macOS support
