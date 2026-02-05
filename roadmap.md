# Roadmap

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


# WIP followup 

1. how is docker image distributed?
can we use a localy built image with periodic pull to reduce amount of work
I'm aiming to have the latest versions of the tools most of the time, but not necessarily on every run. User must have control over this

1. seems like running a single command is not possible for docker mode, it always starts the shell

1do not share mise cache with host if current OS is macOS - this will lead to picking up mac binaries which will not run in linux sandbox. instead, create a shared cache volume for docker and bind mount it to the container. This will allow to have a shared cache between different runs, but without picking up host binaries.
