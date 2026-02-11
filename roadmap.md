# Roadmap

## Completed

- [x] configuration support
    - allow to configure tools to be enabled
    - allow to generate config for tools based on detected tools availability
- [x] Docker isolation backend (Linux and macOS support)

## Planned

- support for GUI applications
- embed pasta/bwrap for simpler installation
- per-session networking (isolated network namespace per sandbox session)
- bwrap-in-Docker (run bwrap inside a Docker container for enhanced isolation)
- capture more audit data (e.g. files access, attempts to access restricted files, attempts to access network etc)
    - collection modes:
        - ebpf - linux only, very fast and efficient
        - fsnotify - cross-platform, but less efficient
