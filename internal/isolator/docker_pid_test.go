package isolator

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"devsandbox/internal/sandbox"
)

// TestParseContainerPID covers the `podman inspect --format '{{.State.Pid}}'`
// output shapes the run path must handle: a live PID, the not-yet-running 0,
// and malformed/empty output.
func TestParseContainerPID(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    int
		wantErr bool
	}{
		{name: "running pid", output: "12345\n", want: 12345},
		{name: "running pid no newline", output: "12345", want: 12345},
		{name: "surrounding whitespace", output: "  789  \n", want: 789},
		{name: "not running yet", output: "0\n", wantErr: true},
		{name: "negative", output: "-1", wantErr: true},
		{name: "empty", output: "", wantErr: true},
		{name: "whitespace only", output: "   \n", wantErr: true},
		{name: "non-numeric", output: "abc\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseContainerPID(tt.output)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseContainerPID(%q) = %d, nil; want error", tt.output, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseContainerPID(%q) unexpected error: %v", tt.output, err)
			}
			if got != tt.want {
				t.Errorf("parseContainerPID(%q) = %d, want %d", tt.output, got, tt.want)
			}
		})
	}
}

// TestContainerNetnsPath verifies the derived netns path matches the
// /proc/<pid>/ns/net form OnSandboxStart and the namespace dialer consume.
func TestContainerNetnsPath(t *testing.T) {
	if got, want := containerNetnsPath(4242), "/proc/4242/ns/net"; got != want {
		t.Errorf("containerNetnsPath(4242) = %q, want %q", got, want)
	}
}

// TestKrunRunArgs_NamesContainerForPIDResolution verifies the krun ephemeral run
// is named (so the run path can `podman inspect` its PID) while the docker
// ephemeral run stays anonymous.
func TestKrunRunArgs_NamesContainerForPIDResolution(t *testing.T) {
	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}
	wantName := sandbox.DockerContainerName(cfg.ProjectDir)

	krun := newKrunTestIsolator()
	krunArgs, err := krun.buildRunArgs(cfg)
	if err != nil {
		t.Fatalf("krun buildRunArgs failed: %v", err)
	}
	idx := slices.Index(krunArgs, "--name")
	if idx == -1 || idx+1 >= len(krunArgs) || krunArgs[idx+1] != wantName {
		t.Errorf("krun run args must include --name %s, got: %v", wantName, krunArgs)
	}
	if imgIdx := slices.Index(krunArgs, krun.imageTag); imgIdx != -1 && idx > imgIdx {
		t.Errorf("--name must precede the image, got: %v", krunArgs)
	}
	// --replace lets a relaunch recover from a stale same-named container left by
	// a prior hard-killed run, rather than failing with a name collision.
	if !slices.Contains(krunArgs, "--replace") {
		t.Errorf("krun run args must include --replace to recover from a stale named container, got: %v", krunArgs)
	}

	docker := NewDockerIsolator(DockerConfig{})
	docker.imageTag = "test:latest"
	dockerArgs, err := docker.buildRunArgs(cfg)
	if err != nil {
		t.Fatalf("docker buildRunArgs failed: %v", err)
	}
	if slices.Contains(dockerArgs, "--name") {
		t.Errorf("docker ephemeral run must stay anonymous (no --name), got: %v", dockerArgs)
	}
	if slices.Contains(dockerArgs, "--replace") {
		t.Errorf("docker ephemeral run must not use --replace (it is podman-only), got: %v", dockerArgs)
	}
}

// TestUsesMicroVMSession_ProxyGated verifies the non-blocking session path runs
// for a microVM engine in proxy mode, regardless of whether a callback is wired:
// the path also drives the host-side egress lockdown, which must run on every
// krun+proxy launch (it is aligned with the DEVSANDBOX_EGRESS_LOCKDOWN env). A
// krun run without proxy, and any docker run, fall through to the blocking run.
func TestUsesMicroVMSession_ProxyGated(t *testing.T) {
	tests := []struct {
		name         string
		microVM      bool
		proxyEnabled bool
		want         bool
	}{
		{name: "krun proxy", microVM: true, proxyEnabled: true, want: true},
		{name: "krun without proxy", microVM: true, proxyEnabled: false, want: false},
		{name: "docker proxy", microVM: false, proxyEnabled: true, want: false},
		{name: "docker without proxy", microVM: false, proxyEnabled: false, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &DockerIsolator{engine: containerEngine{microVM: tt.microVM}}
			if got := d.usesMicroVMSession(tt.proxyEnabled); got != tt.want {
				t.Errorf("usesMicroVMSession(proxy=%v) with microVM=%v = %v, want %v",
					tt.proxyEnabled, tt.microVM, got, tt.want)
			}
		})
	}
}

// newUnresolvableMicroVMIsolator returns a krun-shaped isolator whose engine
// binary cannot be found, so inspectContainerPID fails deterministically without
// requiring podman/docker on the test host.
func newUnresolvableMicroVMIsolator() *DockerIsolator {
	return &DockerIsolator{
		engine: containerEngine{
			binary:  "/nonexistent/devsandbox-test-engine",
			microVM: true,
		},
	}
}

// TestWaitForContainerPID_ContextCancelled verifies the poll loop returns
// promptly (with the context error) when its context is cancelled, e.g. because
// the container exited and runMicroVMSession cancelled the resolve context.
func TestWaitForContainerPID_ContextCancelled(t *testing.T) {
	iso := newUnresolvableMicroVMIsolator()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	var gotErr error
	go func() {
		_, gotErr = iso.waitForContainerPID(ctx, "devsandbox-test", time.Minute)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waitForContainerPID did not return promptly on a cancelled context")
	}
	if gotErr == nil {
		t.Fatal("waitForContainerPID on cancelled context: err = nil, want context error")
	}
}

// TestWaitForContainerPID_Timeout verifies the poll loop times out (naming the
// container) when the engine never reports a running PID.
func TestWaitForContainerPID_Timeout(t *testing.T) {
	iso := newUnresolvableMicroVMIsolator()
	_, err := iso.waitForContainerPID(context.Background(), "devsandbox-test", 50*time.Millisecond)
	if err == nil {
		t.Fatal("waitForContainerPID: err = nil, want timeout error")
	}
	if !strings.Contains(err.Error(), "devsandbox-test") {
		t.Errorf("timeout error should name the container, got: %v", err)
	}
}
