# Persistent Docker Containers

**Date:** 2026-02-04
**Status:** Approved

## Problem

Docker sandbox startup is slow (~5-10s) because containers are created fresh each run (`--rm` flag). This affects:

1. **Startup latency** - Container creation and entrypoint execution on every run
2. **Lost tool installs** - Mise tools installed inside the container may not persist correctly

Users with mixed workflows (long sessions + quick commands) experience this friction repeatedly.

## Solution

Keep containers stopped instead of removing them. Subsequent runs restart the existing container (~1-2s) instead of creating a new one.

## Design

### Container Naming

Deterministic names based on project, matching volume naming pattern:

```
devsandbox-<project-name>-<hash>
```

Example: `devsandbox-myproject-a1b2c3d4`

### Container Labels

Labels on both containers and volumes for easy management:

```
devsandbox=true
devsandbox.project_dir=/home/user/myproject
devsandbox.project_name=myproject
devsandbox.created_at=2026-02-04T12:00:00Z
```

Benefits:
- Easy filtering: `docker ps -a --filter label=devsandbox=true`
- Metadata stored on container itself
- Containers and volumes can be correlated
- Simplifies pruning logic

### Lifecycle Flow

```
devsandbox (docker mode)
    │
    ├─ Container exists & stopped?
    │   └─ docker start -ai <container>
    │
    ├─ Container exists & running?
    │   └─ docker exec -it <container> <shell>
    │
    └─ Container doesn't exist?
        └─ docker create ... → docker start -ai <container>
```

On exit:
- Container stops naturally when shell exits
- Container remains (no `--rm`)
- State preserved: mise tools, apt installs, shell history

Performance:
- First run: ~5-10s (pull image, create container, run entrypoint)
- Subsequent runs: ~1-2s (start stopped container)
- Entrypoint only runs on first create

### Configuration

In `.devsandbox.toml`:

```toml
[sandbox.docker]
image = "ghcr.io/zekker6/devsandbox:latest"
keep_container = true  # default: true
```

CLI override for one-off fresh containers:

```bash
devsandbox --no-keep
```

### Pruning

The `sandboxes` command handles containers:

```bash
devsandbox sandboxes list
# Shows containers with TYPE=docker, STATE=running/stopped

devsandbox sandboxes prune
# Removes orphaned containers + volumes

devsandbox sandboxes prune --all
# Removes all devsandbox containers + volumes
```

Prune behavior:
- Stops running containers before removal
- Removes container first, then associated volume
- Respects `--dry-run` and `--force` flags

### Listing Output

```
NAME              TYPE    STATE    PROJECT DIR         LAST USED    STATUS
myproject-a1b2    docker  stopped  /home/.../myproj    2026-02-04
other-proj-c3d4   docker  running  /home/.../other     2026-02-04
old-proj-e5f6     docker  exited   (orphaned)          2026-01-15   orphaned
```

## Implementation

### docker.go Changes

```go
// Check if container exists and its state
func (d *DockerIsolator) containerExists(name string) (exists bool, running bool)

// Generate container name (same pattern as volumes)
func (d *DockerIsolator) containerName(projectDir string) string

// Build() returns different commands based on container state
```

Labels added to `docker create`:

```go
"--label", "devsandbox=true",
"--label", "devsandbox.project_dir=" + projectDir,
"--label", "devsandbox.project_name=" + projectName,
"--label", "devsandbox.created_at=" + time.Now().Format(time.RFC3339),
```

### Config Changes

```go
type DockerConfig struct {
    // ... existing fields
    KeepContainer bool  // default: true
}
```

### sandbox/docker.go Changes

- `ListDockerSandboxes()` - query containers by label, include state
- `RemoveDockerSandbox()` - stop if running, remove container, remove volume

## Edge Cases

| Scenario | Behavior |
|----------|----------|
| Image updated | Container uses old image until pruned/recreated |
| Config changed | Warn user, suggest `--no-keep` or prune |
| Container corrupted | `sandboxes prune` + fresh start |
| Multiple terminals | Second terminal does `docker exec` into running container |

## Future Considerations

Not in this design, but potential future additions:

- `devsandbox rebuild` to force recreate container with new image
- Auto-detect image updates and prompt user
