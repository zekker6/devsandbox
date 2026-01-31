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

- add telemetry sending via otel
- capture more audit data (e.g. files access, attempts to access restricted files, attempts to access network etc)
- subcommand "doctor" - to check if everything is ok with the installation
- restore file logger rotation
- collision for sandboxes names from same folders
