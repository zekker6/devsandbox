# devsandbox

## Coding practices

After completing the task always run:

- `task test` - to run tests
- `task lint` - to run lint

## Tools to use

Tools management: mise
Create .mise.toml with all tools to be used and pin the dependencies.

Use `task` for setting up automation: create a taskfile which will cover build, lint, test, running the appliication.

CI: repository will be hosted at gitea. Update it to correlate with current project when needed.

Linters: golangci + go default linters.

## TODOs

- http filtering
    - ability to define whitelist/blacklist/ask mode for http(s) requests
- configuration support
    - allow to configure tools to be enabled, proxy config
        - allow to generate config from logs for proxy
        - allow to generate config for tools based on detected tools availability
    - allow to configure per-project settings
- capture more audit data (e.g. files access, attempts to access restricted files, attempts to access network etc)
  - collection modes:
    - ebpf - linux only, very fast and efficient
    - fsnotify - cross-platform, but less efficient

Backlog:

- macOS support
    - bubblewrap is available
- tcp/udp proxying
