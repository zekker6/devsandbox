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

- add tools management subcommand
  - allow checking tools availability
  - allow checking binds/env for tools
- overlayfs usage
  - allow tools to use overlayfs to provide writable fs based on host fs
  - allow to define which paths should be writable/read-only/masked
  - for example - mise might use writable overlayfs to allow updating/changing tools without affecting host fs
- http filtering
  - ability to define whitelist/blacklist/ask mode for http(s) requests
- configuration support
  - allow to configure tools to be enabled, proxy config
    - allow to generate config from logs for proxy
    - allow to generate config for tools based on detected tools availability
  - allow to configure per-project settings
- improve doctor
  - check internal logs for recent issues
  - check overlayfs support
  - check available tools and their versions
- capture more audit data (e.g. files access, attempts to access restricted files, attempts to access network etc)
 
Backlog:
- macOS support
- tcp/udp proxying
