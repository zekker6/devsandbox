# Roadmap

## Completed

- [x] configuration support
  - allow to configure tools to be enabled
  - allow to generate config for tools based on detected tools availability
- [x] Docker isolation backend (Linux and macOS support)

## Planned

- [ ] dbus notifications support
- [ ] capture more audit data (e.g. files access, attempts to access restricted files, attempts to access network etc)
  - collection modes:
    - ebpf - linux only, very fast and efficient
    - fsnotify - cross-platform, but less efficient
- [ ] config show use generated code instead of manual crafting of toml (e.g. just toml.Marshal)
