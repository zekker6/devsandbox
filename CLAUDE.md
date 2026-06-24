# devsandbox

## Coding practices

After completing the task always run:

- `task test` - to run tests
- `task lint` - to run lint

Always prefer reasonable defaults if that it possible. Reduce amount of work for the user to do when this is possible.
All defaults must be secure by default, and not cause any security issues if used without modification.
Never bind to all interfaces by default, and do not expose any ports by default.

Errors must always be handled and reported when present. Silent failures are not acceptable.

## Changelog

`CHANGELOG.md` follows the Keep a Changelog format with an `[Unreleased]` section at the top.

- Every user-facing change MUST be recorded under `[Unreleased]` in the same change that introduces it: new features, behavior/config/CLI/API changes, bug fixes, and breaking changes. Do not defer it to a later "docs" commit.
- Categorize each entry as `Added`, `Changed`, `Fixed`, or `Breaking Changes`. Describe what changed and why it matters to the user, not the implementation detail.
- Do not itemize routine dependency bumps or CI/workflow digest updates. Only note a dependency change when it is user-visible (e.g. an embedded binary such as `pasta` or `bwrap`).
- When cutting a release, rename `[Unreleased]` to `## [vX.Y.Z](https://github.com/zekker6/devsandbox/releases/tag/vX.Y.Z) - YYYY-MM-DD` and repoint the `[Unreleased]` compare link to the new tag, in the same commit that gets tagged. Never tag a release while its content is still under `[Unreleased]`.

## Tools to use

Tools management: mise
Create .mise.toml with all tools to be used and pin the dependencies.

Use `task` for setting up automation: create a taskfile which will cover build, lint, test, running the appliication.

CI: repository will be hosted at gitea. Update it to correlate with current project when needed.

Linters: golangci + go default linters.
