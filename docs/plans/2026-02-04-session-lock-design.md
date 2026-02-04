# Session Lock Protection

Prevent `devsandbox sandboxes prune` from removing sandbox environments while a session is actively running.

## Overview

Use flock-based file locking to track active sessions. The lock is automatically released by the kernel when the devsandbox process exits (normal exit, crash, or signal).

## Lock Mechanism

**Location:** `~/.local/share/devsandbox/<project>/.lock`

**Type:** Shared lock (`LOCK_SH`) - allows multiple concurrent sessions to the same sandbox.

**Detection:** Attempt exclusive lock (`LOCK_EX | LOCK_NB`) - fails if any shared lock is held.

## API

```go
// internal/sandbox/lock.go

// AcquireSessionLock acquires a shared lock on the sandbox.
// Returns the file descriptor (caller must keep it open for session duration).
// The lock is automatically released when the file is closed or process exits.
func AcquireSessionLock(sandboxRoot string) (*os.File, error)

// IsSessionActive checks if any session holds a lock on the sandbox.
// Returns true if a session is active (lock is held).
func IsSessionActive(sandboxRoot string) bool
```

## Integration

### Session Start (main.go)

Both `runSandbox()` (bwrap) and `runDockerSandbox()` acquire the lock:

```go
func runSandbox(cmd *cobra.Command, args []string) error {
    // ... existing setup ...

    if err := cfg.EnsureSandboxDirs(); err != nil {
        return err
    }

    // Acquire session lock (released automatically on exit)
    lockFile, err := sandbox.AcquireSessionLock(cfg.SandboxRoot)
    if err != nil {
        return fmt.Errorf("failed to acquire session lock: %w", err)
    }
    defer lockFile.Close()

    // ... rest of function ...
}
```

### List Command (sandboxes.go)

Check lock status when building sandbox list:

```go
for _, s := range sandboxes {
    s.Active = sandbox.IsSessionActive(s.SandboxRoot)
}
```

Display in STATUS column:
- bwrap: "active" or empty
- Docker: "running, active" or "exited" etc.

### Prune Command (sandboxes.go)

Filter out active sandboxes in `SelectForPruning()`:

```go
func SelectForPruning(sandboxes []*Metadata, opts PruneOptions) []*Metadata {
    var result []*Metadata
    for _, s := range sandboxes {
        // Skip active sessions silently
        if IsSessionActive(s.SandboxRoot) {
            continue
        }
        // ... existing selection logic ...
    }
    return result
}
```

## Metadata Changes

Add `Active` field to `Metadata` struct (populated at runtime, not persisted):

```go
type Metadata struct {
    // ... existing fields ...
    Active bool `json:"active,omitempty"` // Session currently running (runtime only)
}
```

## Files to Modify

| File | Changes |
|------|---------|
| `internal/sandbox/lock.go` | New file - lock acquisition and checking |
| `internal/sandbox/metadata.go` | Add `Active` field to `Metadata` struct |
| `cmd/devsandbox/main.go` | Acquire lock in `runSandbox()` and `runDockerSandbox()` |
| `cmd/devsandbox/sandboxes.go` | Check locks in list/prune, display "active" status |

## Edge Cases

**Stale lock files:** Not possible with flock. Kernel releases lock on process exit. Empty `.lock` files may remain but are harmless.

**Multiple sessions:** Allowed via shared locks. All sessions protect the sandbox from pruning.

**Docker containers:** Lock provides consistent behavior across backends. Docker's own state (running/exited) is shown alongside "active" status.

## Testing

1. Start a sandbox session
2. In another terminal, run `devsandbox sandboxes list` - should show "active"
3. Run `devsandbox sandboxes prune --all` - should skip the active sandbox
4. Exit the session
5. Run list again - "active" should be gone
6. Prune should now include the sandbox
