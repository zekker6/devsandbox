package isolator

import (
	"context"
	"testing"
	"time"

	"devsandbox/internal/sandbox"
)

// The full container ID is what OOM monitoring checks the resolved cgroup path
// against, so an empty or unreadable answer has to be an error rather than an empty
// string that would make any cgroup look like a match.
func TestInspectContainerID(t *testing.T) {
	const id = "9f2c1de4a7b8c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e"

	tests := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{
			name: "full id with trailing newline",
			body: `if [ "$1" = "inspect" ]; then echo "` + id + `"; exit 0; fi; exit 1`,
			want: id,
		},
		{
			name:    "empty answer",
			body:    `if [ "$1" = "inspect" ]; then echo ""; exit 0; fi; exit 1`,
			wantErr: true,
		},
		{
			name:    "inspect fails",
			body:    `exit 1`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := writeFakeEngine(t, tt.body)

			got, err := newFakeEngineIsolator(bin).inspectContainerID(context.Background(), "devsandbox-test")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("inspectContainerID() = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("inspectContainerID() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("inspectContainerID() = %q, want %q", got, tt.want)
			}
		})
	}
}

// An anonymous `docker run` - keep_container disabled - has no name to inspect, so
// there is no PID to resolve a cgroup from. The monitor must say so up front rather
// than spend the attach timeout asking about a container that cannot be named.
func TestStartOOMMonitorSkipsAnAnonymousContainer(t *testing.T) {
	d := newFakeEngineIsolator(writeFakeEngine(t, `exit 1`))
	cfg := &RunConfig{SandboxCfg: &sandbox.Config{SandboxRoot: t.TempDir()}}

	start := time.Now()
	m := d.startOOMMonitor(cfg, &DockerBuildResult{Action: DockerActionRun})
	m.finish(nil)

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("an anonymous container took %s to give up, want it skipped immediately", elapsed)
	}
	if m.monitored() {
		t.Error("a monitor was attached to a container that cannot be inspected")
	}
}

// A container that never reports a running PID must not hold the exit: the attach
// is bounded by oomContainerTimeout, which is generous precisely because finish
// cancels it rather than waiting it out.
func TestContainerOOMMonitorDoesNotWaitOutAContainerThatNeverStarts(t *testing.T) {
	// A created-but-not-running container reports PID 0, which keeps the resolver
	// polling for as long as it is allowed to.
	bin := writeFakeEngine(t, `if [ "$1" = "inspect" ]; then echo 0; exit 0; fi; exit 1`)
	d := newFakeEngineIsolator(bin)
	cfg := &RunConfig{SandboxCfg: &sandbox.Config{SandboxRoot: t.TempDir()}}

	m := d.startOOMMonitor(cfg, &DockerBuildResult{Action: DockerActionExec, ContainerName: "devsandbox-test"})

	start := time.Now()
	m.finish(nil)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("finish() took %s, want the attach abandoned promptly (timeout is %s)", elapsed, oomContainerTimeout)
	}
	if m.monitored() {
		t.Error("a monitor was attached to a container that never started")
	}
}
