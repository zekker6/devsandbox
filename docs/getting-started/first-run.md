# First run

A guided walkthrough — see exactly what becomes visible, invisible, and read-only inside a devsandbox.

## Setup

Create a throwaway project so we don't risk anything real:

```bash
mkdir -p /tmp/sandbox-test && cd /tmp/sandbox-test
echo "secret_token=fake_123" > .env
echo "# Sandbox test" > README.md
git init -q
```

`/tmp/sandbox-test` is now your "project". It has a `.env` file with a fake secret and a fresh git repo.

## Step 1 — what does the sandbox see?

```bash
devsandbox --info
```

Look for these lines:

- `cwd: /tmp/sandbox-test` — the sandbox root
- `home: <some path>` — the sandbox's own `$HOME`, isolated from yours
- `mounts:` — every host path that's exposed (and how)

## Step 2 — open a shell inside

```bash
devsandbox
```

You're now in a sandboxed shell. Try the following:

```bash
# CWD works as expected
ls
cat README.md

# Project files are read/write
echo "added inside sandbox" >> README.md
cat README.md

# .env is hidden — it shows /dev/null content
cat .env
# (empty output, because .env is masked)

# Your real SSH keys are gone
ls ~/.ssh 2>&1
# "No such file or directory"

# Other projects are invisible
ls ~/projects 2>&1
# "No such file or directory"

# Your real ~/.aws is gone
ls ~/.aws 2>&1

# But mise-managed tools still work — read-only
which go
go version
```

Exit with `exit` or Ctrl-D.

## Step 3 — verify changes from outside

Back on the host:

```bash
cat /tmp/sandbox-test/README.md
# Should show the line you added — bidirectional file sync works
```

## Step 4 — try a real AI tool

Replace the shell run with an actual agent:

```bash
cd /tmp/sandbox-test
devsandbox claude --dangerously-skip-permissions
```

The Claude Code permission prompts are gone. The agent can read/write files in `/tmp/sandbox-test`, install packages, run builds. It cannot read your SSH keys, your AWS creds, your other projects, or your `.env`. The sandbox is the security boundary.

## Step 5 — clean up

```bash
rm -rf /tmp/sandbox-test
```

That's it. You've seen the basic flow. From here:

- [Use cases](../use-cases.md) — recipes for common workflows
- [Proxy mode](../proxy.md) — log/filter all HTTP traffic
- [Configuration](../configuration.md) — tune mounts, git mode, network policy
