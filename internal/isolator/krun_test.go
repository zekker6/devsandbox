package isolator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// newKrunTestIsolator returns a krun isolator with an image tag preset so the
// pure arg-builders can run without invoking podman.
func newKrunTestIsolator() *DockerIsolator {
	iso := NewKrunIsolator(DockerConfig{})
	iso.imageTag = "devsandbox:local"
	return iso
}

func TestKrunIsolator_Identity(t *testing.T) {
	iso := NewKrunIsolator(DockerConfig{})
	if iso.Name() != BackendKrun {
		t.Errorf("Name() = %s, want %s", iso.Name(), BackendKrun)
	}
	if iso.IsolationType() != "krun" {
		t.Errorf("IsolationType() = %s, want krun", iso.IsolationType())
	}
	// The guest reaches the loopback-bound proxy via the pasta gateway (10.0.2.2),
	// not podman's link-local host.containers.internal.
	if host := iso.proxyHost(); host != "10.0.2.2" {
		t.Errorf("proxyHost() = %s, want 10.0.2.2 (pasta gateway)", host)
	}
}

func TestKrunIsolator_ImplementsInterface(t *testing.T) {
	var _ Isolator = (*DockerIsolator)(nil)
	var _ Isolator = NewKrunIsolator(DockerConfig{})
}

func TestKrunIsolator_RunArgs_InjectRuntimeAndEphemeral(t *testing.T) {
	iso := newKrunTestIsolator()
	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}

	args, err := iso.buildRunArgs(cfg)
	if err != nil {
		t.Fatalf("buildRunArgs failed: %v", err)
	}

	// "--runtime krun" must be injected immediately after the "run" verb.
	if len(args) < 3 || args[0] != "run" || args[1] != "--runtime" || args[2] != "krun" {
		t.Errorf("expected run --runtime krun prefix, got: %v", args[:min(4, len(args))])
	}
	// Ephemeral: a fresh microVM per launch implies --rm.
	if !slices.Contains(args, "--rm") {
		t.Errorf("krun run args must include --rm for ephemeral microVMs, got: %v", args)
	}
}

func TestKrunIsolator_CreateArgs_InjectRuntime(t *testing.T) {
	iso := newKrunTestIsolator()
	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}

	args, err := iso.buildCreateArgs(cfg, "devsandbox-test")
	if err != nil {
		t.Fatalf("buildCreateArgs failed: %v", err)
	}

	if len(args) < 3 || args[0] != "create" || args[1] != "--runtime" || args[2] != "krun" {
		t.Errorf("expected create --runtime krun prefix, got: %v", args[:min(4, len(args))])
	}
}

func TestKrunIsolator_ProxyHostAlias(t *testing.T) {
	iso := newKrunTestIsolator()
	cfg := &Config{
		ProjectDir:   "/tmp/test-project",
		SandboxHome:  "/tmp/test-sandbox",
		HomeDir:      "/home/testuser",
		Shell:        "/bin/bash",
		ProxyEnabled: true,
		ProxyPort:    8080,
	}

	args, err := iso.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("buildCommonArgs failed: %v", err)
	}
	argsStr := strings.Join(args, " ")

	// krun binds the proxy to host loopback and reaches it via the pasta gateway
	// (mapped with --map-host-loopback). It must NOT inject an --add-host or use
	// the docker alias, and must point the guest at the gateway IP. -4 gives the
	// guest IPv4 only so the IPv4 egress lockdown has no IPv6 path to miss.
	if !strings.Contains(argsStr, "--network pasta:-4,--map-host-loopback,10.0.2.2") {
		t.Errorf("krun proxy run must give the guest IPv4 only and map the pasta gateway to host loopback, got: %s", argsStr)
	}
	if strings.Contains(argsStr, "--add-host") {
		t.Errorf("krun must not inject --add-host, got: %s", argsStr)
	}
	if strings.Contains(argsStr, "host.docker.internal") {
		t.Error("krun backend must not use the docker host alias")
	}
	if !strings.Contains(argsStr, "PROXY_HOST=10.0.2.2") {
		t.Errorf("expected PROXY_HOST=10.0.2.2, got: %s", argsStr)
	}
	if !strings.Contains(argsStr, "HTTP_PROXY=http://10.0.2.2:8080") {
		t.Errorf("expected HTTP_PROXY via 10.0.2.2:8080, got: %s", argsStr)
	}
}

// TestKrunIsolator_PrepareNetwork verifies krun skips the rootful bridge model
// and binds the proxy to host loopback (reached via host.containers.internal).
func TestKrunIsolator_PrepareNetwork(t *testing.T) {
	iso := NewKrunIsolator(DockerConfig{})
	info, err := iso.PrepareNetwork(context.Background(), "/tmp/test-project")
	if err != nil {
		t.Fatalf("PrepareNetwork failed: %v", err)
	}
	if info == nil || info.BindAddress != "127.0.0.1" {
		t.Errorf("expected proxy bind address 127.0.0.1, got %+v", info)
	}
	// No bridge network should be created (rootless-incompatible), so Cleanup
	// must be a no-op.
	if iso.networkName != "" {
		t.Errorf("krun must not create a per-session network, got %q", iso.networkName)
	}
	if err := iso.Cleanup(); err != nil {
		t.Errorf("Cleanup should be a no-op for krun, got: %v", err)
	}
}

// TestKrunIsolator_MicroVMCapsAndUserns verifies keep-id is the krun-only flag
// (rootless podman maps the host user 1:1 for correct file ownership), while
// DAC_OVERRIDE - which the shim's root-phase home setup needs on BOTH backends -
// is shared with docker.
func TestKrunIsolator_MicroVMCapsAndUserns(t *testing.T) {
	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}

	krun := newKrunTestIsolator()
	krunArgs, err := krun.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("krun buildCommonArgs failed: %v", err)
	}
	ks := strings.Join(krunArgs, " ")
	if !strings.Contains(ks, "--userns keep-id") {
		t.Errorf("krun must run with --userns keep-id, got: %s", ks)
	}
	if !strings.Contains(ks, "--cap-add DAC_OVERRIDE") {
		t.Errorf("krun must add DAC_OVERRIDE, got: %s", ks)
	}

	// keep-id is microVM-only, but the docker backend must still get DAC_OVERRIDE:
	// its bind-mounted /home/sandboxuser and :ro overlay manifest are owned by the
	// non-root host UID, so the shim's root-phase setup cannot populate the home or
	// read the manifest without it.
	docker := NewDockerIsolator(DockerConfig{})
	docker.imageTag = "test:latest"
	dockerArgs, err := docker.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("docker buildCommonArgs failed: %v", err)
	}
	ds := strings.Join(dockerArgs, " ")
	if strings.Contains(ds, "keep-id") {
		t.Error("docker backend must not use keep-id")
	}
	if !strings.Contains(ds, "--cap-add DAC_OVERRIDE") {
		t.Error("docker backend must add DAC_OVERRIDE for the shim's root-phase home setup")
	}
}

// TestKrunIsolator_EgressLockdown_WithProxy verifies that krun in proxy mode
// requests the HOST-side egress lockdown: the guest gets neither in-guest
// CAP_NET_ADMIN nor the gateway env (the host applies the route surgery in the
// VMM netns); on Linux it only gets DEVSANDBOX_EGRESS_LOCKDOWN=1, which tells the
// shim to wait for the host before running the workload.
func TestKrunIsolator_EgressLockdown_WithProxy(t *testing.T) {
	iso := newKrunTestIsolator()
	cfg := &Config{
		ProjectDir:   "/tmp/test-project",
		SandboxHome:  "/tmp/test-sandbox",
		HomeDir:      "/home/testuser",
		Shell:        "/bin/bash",
		ProxyEnabled: true,
		ProxyPort:    8080,
	}

	args, err := iso.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("buildCommonArgs failed: %v", err)
	}
	argsStr := strings.Join(args, " ")

	// The lockdown runs host-side now: no in-guest NET_ADMIN, no gateway env.
	if slices.Contains(args, "NET_ADMIN") {
		t.Errorf("krun must not add in-guest NET_ADMIN (lockdown is host-side), got: %s", argsStr)
	}
	if strings.Contains(argsStr, "DEVSANDBOX_PROXY_GATEWAY") {
		t.Errorf("krun must not pass DEVSANDBOX_PROXY_GATEWAY (host applies the surgery), got: %s", argsStr)
	}

	// The wait signal is Linux-only (the surgery uses nsenter into the VMM netns).
	if runtime.GOOS == "linux" {
		if !strings.Contains(argsStr, "DEVSANDBOX_EGRESS_LOCKDOWN=1") {
			t.Errorf("krun proxy mode on linux must request egress lockdown, got: %s", argsStr)
		}
	} else if strings.Contains(argsStr, "DEVSANDBOX_EGRESS_LOCKDOWN") {
		t.Errorf("krun egress lockdown is linux-only, got: %s", argsStr)
	}

	// The egress-locked guest runs mise offline: remote version-list lookups
	// that cannot traverse the proxy would otherwise retry-storm (see the
	// MISE_OFFLINE comment in buildCommonArgs).
	if !strings.Contains(argsStr, "MISE_OFFLINE=1") {
		t.Errorf("krun proxy mode must set MISE_OFFLINE=1, got: %s", argsStr)
	}
}

// TestKrunIsolator_NoEgressLockdown_WithoutProxy verifies the lockdown is gated
// on proxy mode: a krun run without the proxy must not get NET_ADMIN or the
// lockdown env vars (there is no proxy gateway to lock egress down to).
func TestKrunIsolator_NoEgressLockdown_WithoutProxy(t *testing.T) {
	iso := newKrunTestIsolator()
	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
		// ProxyEnabled deliberately false
	}

	args, err := iso.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("buildCommonArgs failed: %v", err)
	}
	argsStr := strings.Join(args, " ")

	if slices.Contains(args, "NET_ADMIN") {
		t.Errorf("krun without proxy must not add NET_ADMIN, got: %s", argsStr)
	}
	if strings.Contains(argsStr, "DEVSANDBOX_EGRESS_LOCKDOWN") {
		t.Errorf("krun without proxy must not request egress lockdown, got: %s", argsStr)
	}
	if strings.Contains(argsStr, "DEVSANDBOX_PROXY_GATEWAY") {
		t.Errorf("krun without proxy must not pass a proxy gateway, got: %s", argsStr)
	}
	// Without the proxy the guest has open (TSI) egress and mise resolution
	// works normally - offline mode must not be forced.
	if strings.Contains(argsStr, "MISE_OFFLINE") {
		t.Errorf("krun without proxy must not force offline mise, got: %s", argsStr)
	}
}

// TestDockerIsolator_NoEgressLockdown verifies the egress lockdown is microVM-only:
// the docker backend (even with the proxy on) must never request NET_ADMIN or the
// lockdown env vars, which only make sense for the krun guest.
func TestDockerIsolator_NoEgressLockdown(t *testing.T) {
	docker := NewDockerIsolator(DockerConfig{})
	docker.imageTag = "test:latest"
	cfg := &Config{
		ProjectDir:   "/tmp/test-project",
		SandboxHome:  "/tmp/test-sandbox",
		HomeDir:      "/home/testuser",
		Shell:        "/bin/bash",
		ProxyEnabled: true,
		ProxyPort:    8080,
	}

	args, err := docker.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("docker buildCommonArgs failed: %v", err)
	}
	argsStr := strings.Join(args, " ")

	if slices.Contains(args, "NET_ADMIN") {
		t.Errorf("docker backend must never add NET_ADMIN, got: %s", argsStr)
	}
	if strings.Contains(argsStr, "DEVSANDBOX_EGRESS_LOCKDOWN") {
		t.Errorf("docker backend must never request egress lockdown, got: %s", argsStr)
	}
	if strings.Contains(argsStr, "DEVSANDBOX_PROXY_GATEWAY") {
		t.Errorf("docker backend must never pass a proxy gateway, got: %s", argsStr)
	}
	// Offline mise is scoped to the egress-locked krun guest; docker proxy mode
	// has working proxy-env egress and keeps normal resolution.
	if strings.Contains(argsStr, "MISE_OFFLINE") {
		t.Errorf("docker backend must not force offline mise, got: %s", argsStr)
	}
}

// TestKrunResourceDefaults verifies the microVM defaults fill only empty limits
// and never override an explicit value.
func TestKrunResourceDefaults(t *testing.T) {
	tests := []struct {
		name        string
		inMem, inCP string
		wantMem     string
		wantCPU     string
	}{
		{"both unset -> defaults", "", "", defaultKrunMemory, defaultKrunCPUs},
		{"explicit respected", "8g", "4", "8g", "4"},
		{"only memory set", "16g", "", "16g", defaultKrunCPUs},
		{"only cpus set", "", "1", defaultKrunMemory, "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMem, gotCPU := krunResourceDefaults(tt.inMem, tt.inCP)
			if gotMem != tt.wantMem || gotCPU != tt.wantCPU {
				t.Errorf("krunResourceDefaults(%q,%q) = (%q,%q), want (%q,%q)",
					tt.inMem, tt.inCP, gotMem, gotCPU, tt.wantMem, tt.wantCPU)
			}
		})
	}
	// The defaults are the documented baseline; guard the values so the docs and
	// code cannot silently drift apart.
	if defaultKrunMemory != "4g" || defaultKrunCPUs != "2" {
		t.Errorf("documented krun defaults are 4g/2, got %s/%s", defaultKrunMemory, defaultKrunCPUs)
	}
}

// TestKrunIsolator_AppliesResourceDefaults verifies that an unset
// [sandbox.docker.resources] produces a krun isolator that emits the default
// --memory/--cpus through the single existing emission path, while an explicit
// config is passed through verbatim.
func TestKrunIsolator_AppliesResourceDefaults(t *testing.T) {
	// Unset -> defaults applied (mirrors the New(BackendKrun) wiring without
	// invoking Available(), which needs KVM).
	mem, cpu := krunResourceDefaults("", "")
	iso := NewKrunIsolator(DockerConfig{MemoryLimit: mem, CPULimit: cpu})
	iso.imageTag = "devsandbox:local"
	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}
	args, err := iso.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("buildCommonArgs failed: %v", err)
	}
	assertFlagValue(t, args, "--memory", defaultKrunMemory)
	assertFlagValue(t, args, "--cpus", defaultKrunCPUs)

	// Explicit override -> respected (and only emitted once).
	emem, ecpu := krunResourceDefaults("8g", "4")
	expl := NewKrunIsolator(DockerConfig{MemoryLimit: emem, CPULimit: ecpu})
	expl.imageTag = "devsandbox:local"
	exArgs, err := expl.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("buildCommonArgs failed: %v", err)
	}
	assertFlagValue(t, exArgs, "--memory", "8g")
	assertFlagValue(t, exArgs, "--cpus", "4")
	if got := countFlag(exArgs, "--memory"); got != 1 {
		t.Errorf("expected exactly one --memory emission, got %d: %v", got, exArgs)
	}
	if got := countFlag(exArgs, "--cpus"); got != 1 {
		t.Errorf("expected exactly one --cpus emission, got %d: %v", got, exArgs)
	}
}

// TestDockerIsolator_NoResourceDefaults verifies the microVM defaults never leak
// into the docker backend: with resources unset, the docker run args carry no
// --memory/--cpus at all (the default-fill lives in the krun New() branch only).
func TestDockerIsolator_NoResourceDefaults(t *testing.T) {
	docker := NewDockerIsolator(DockerConfig{})
	docker.imageTag = "test:latest"
	cfg := &Config{
		ProjectDir:  "/tmp/test-project",
		SandboxHome: "/tmp/test-sandbox",
		HomeDir:     "/home/testuser",
		Shell:       "/bin/bash",
	}
	args, err := docker.buildCommonArgs(cfg)
	if err != nil {
		t.Fatalf("buildCommonArgs failed: %v", err)
	}
	if slices.Contains(args, "--memory") {
		t.Errorf("docker backend must not get a default --memory, got: %v", args)
	}
	if slices.Contains(args, "--cpus") {
		t.Errorf("docker backend must not get a default --cpus, got: %v", args)
	}
}

// assertFlagValue asserts that args contains flag immediately followed by want.
func assertFlagValue(t *testing.T, args []string, flag, want string) {
	t.Helper()
	for i, a := range args {
		if a == flag {
			if i+1 >= len(args) {
				t.Fatalf("%s present but has no value: %v", flag, args)
			}
			if args[i+1] != want {
				t.Errorf("%s = %q, want %q", flag, args[i+1], want)
			}
			return
		}
	}
	t.Errorf("flag %s not found in args: %v", flag, args)
}

// countFlag returns how many times flag appears in args.
func countFlag(args []string, flag string) int {
	n := 0
	for _, a := range args {
		if a == flag {
			n++
		}
	}
	return n
}

// TestAcquireMicroVMSessionLock_Exclusive verifies the per-project lock is
// mutually exclusive across acquisitions and reusable after release - the
// guarantee that prevents two simultaneous same-project krun launches from both
// passing the running-container guard and clobbering each other's microVM.
func TestAcquireMicroVMSessionLock_Exclusive(t *testing.T) {
	name := fmt.Sprintf("devsandbox-locktest-%d", os.Getpid())

	lock, err := acquireMicroVMSessionLock(name)
	if err != nil {
		t.Fatalf("first lock acquisition failed: %v", err)
	}

	// A second same-project acquisition must fail fast while the first is held.
	if _, err := acquireMicroVMSessionLock(name); err == nil {
		t.Error("second acquisition succeeded while the first lock was held; the exclusion is not enforced")
	}

	// After release, the lock is reusable (next launch for the project can run).
	if err := lock.Release(); err != nil {
		t.Fatalf("release failed: %v", err)
	}
	lock2, err := acquireMicroVMSessionLock(name)
	if err != nil {
		t.Fatalf("re-acquisition after release failed: %v", err)
	}
	if err := lock2.Release(); err != nil {
		t.Fatalf("second release failed: %v", err)
	}
}

// TestKrunIsolator_OverlayUsesCopyOverlay verifies krun routes tmpoverlay dirs
// through the copy strategy (no kernel overlayfs mount, which fails in the
// libkrun guest) even on Linux, mirroring the macOS path.
func TestKrunIsolator_OverlayUsesCopyOverlay(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home", "testuser")
	fishConfig := filepath.Join(homeDir, ".config", "fish")
	if err := os.MkdirAll(filepath.Join(fishConfig, "functions"), 0o755); err != nil {
		t.Fatal(err)
	}
	sandboxHome := filepath.Join(tmpDir, "sandbox")
	if err := os.MkdirAll(sandboxHome, 0o755); err != nil {
		t.Fatal(err)
	}

	iso := NewKrunIsolator(DockerConfig{})
	cfg := &Config{
		ProjectDir:       filepath.Join(tmpDir, "project"),
		SandboxHome:      sandboxHome,
		HomeDir:          homeDir,
		Shell:            "fish",
		DefaultMountMode: "split", // config dirs become tmpoverlay candidates
	}

	_, _, manifest := iso.getToolBindings(cfg)
	if manifest == nil || len(manifest.Overlays) == 0 {
		t.Fatal("expected overlay manifest entries for split mode")
	}
	sawCopy := false
	for _, e := range manifest.Overlays {
		if e.Type == "tmpoverlay" {
			t.Errorf("krun must not emit kernel-overlayfs (tmpoverlay) entries, got %+v", e)
		}
		if e.Type == "copyoverlay" {
			sawCopy = true
		}
	}
	if !sawCopy {
		t.Errorf("expected at least one copyoverlay entry for krun, got %+v", manifest.Overlays)
	}
}

// TestMicroVMProxyUnsupported verifies krun+proxy is rejected fail-closed on any
// non-Linux OS (macOS/HVF has no route-surgery egress lockdown yet, so proxy mode
// would run with open egress), while Linux krun+proxy, non-proxy krun on any OS,
// and the non-microVM backends stay allowed. Driven with explicit OS strings so
// the darwin cases run on this Linux test host.
func TestMicroVMProxyUnsupported(t *testing.T) {
	tests := []struct {
		name         string
		goos         string
		microVM      bool
		proxyEnabled bool
		wantErr      bool
	}{
		{"darwin krun+proxy rejected", "darwin", true, true, true},
		{"darwin krun without proxy allowed", "darwin", true, false, false},
		{"linux krun+proxy allowed", "linux", true, true, false},
		{"linux krun without proxy allowed", "linux", true, false, false},
		{"darwin docker+proxy unaffected", "darwin", false, true, false},
		{"windows krun+proxy rejected", "windows", true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := microVMProxyUnsupported(tt.goos, tt.microVM, tt.proxyEnabled)
			if tt.wantErr && err == nil {
				t.Fatalf("microVMProxyUnsupported(%q, %v, %v) = nil, want fail-closed error",
					tt.goos, tt.microVM, tt.proxyEnabled)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("microVMProxyUnsupported(%q, %v, %v) = %v, want nil",
					tt.goos, tt.microVM, tt.proxyEnabled, err)
			}
			if tt.wantErr {
				msg := err.Error()
				// The error must name the reason (egress lockdown is Linux-only) and
				// the remedy (run on Linux, or disable proxy mode).
				for _, want := range []string{tt.goos, "egress", "Linux", "proxy mode"} {
					if !strings.Contains(msg, want) {
						t.Errorf("error %q missing %q; reason and remedy must be actionable", msg, want)
					}
				}
			}
		})
	}
}

// TestShouldWarnHostBuild verifies the host-build trust-boundary warning fires
// only for a project-provided Dockerfile under krun. A krun build of a
// user-supplied Dockerfile runs via host `podman build` outside the microVM guest
// and the proxy egress lockdown, so it warrants disclosure; the auto-generated
// config-dir Dockerfile (empty projectDockerfile) is trusted devsandbox content
// and stays silent, as does every docker-backend build.
func TestShouldWarnHostBuild(t *testing.T) {
	tests := []struct {
		name              string
		microVM           bool
		projectDockerfile string
		want              bool
	}{
		{"krun + project Dockerfile (relative)", true, "Dockerfile.dev", true},
		{"krun + project Dockerfile (absolute)", true, "/abs/Dockerfile", true},
		{"krun + default config-dir Dockerfile", true, "", false},
		{"docker + project Dockerfile", false, "Dockerfile.dev", false},
		{"docker + default config-dir Dockerfile", false, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldWarnHostBuild(tt.microVM, tt.projectDockerfile); got != tt.want {
				t.Errorf("shouldWarnHostBuild(%v, %q) = %v, want %v",
					tt.microVM, tt.projectDockerfile, got, tt.want)
			}
		})
	}
}

// TestKrunIsolator_Available_FailsFast verifies the microVM preflight returns an
// actionable error when its prerequisites (podman, krun runtime, KVM) are absent,
// rather than silently degrading. This holds in CI and inside devsandbox itself.
func TestKrunIsolator_Available_FailsFast(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("krun backend unsupported on %s", runtime.GOOS)
	}

	err := NewKrunIsolator(DockerConfig{}).Available()
	if err == nil {
		t.Skip("krun prerequisites present on this host; nothing to assert")
	}

	msg := err.Error()
	wantsOne := []string{"podman", "krun", "/dev/kvm"}
	found := false
	for _, w := range wantsOne {
		if strings.Contains(msg, w) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Available() error should name the missing prerequisite (one of %v), got: %q", wantsOne, msg)
	}
}

// TestCheckMicroVM_StructuredRows asserts the structured preflight always
// reports the podman and krun-runtime checks, gates the KVM row to Linux, and
// gives every failing check an actionable summary.
func TestCheckMicroVM_StructuredRows(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("krun backend unsupported on %s", runtime.GOOS)
	}

	checks := CheckMicroVM()

	byName := make(map[string]MicroVMCheck, len(checks))
	for _, c := range checks {
		byName[c.Name] = c
		if c.Summary == "" {
			t.Errorf("check %q has empty Summary", c.Name)
		}
		if !c.OK && c.Hint == "" && c.Name != "kvm" {
			// kvm "not accessible" close-error path legitimately has no hint;
			// every other failing prerequisite must carry remediation guidance.
			t.Errorf("failing check %q has no remediation Hint", c.Name)
		}
	}

	for _, name := range []string{"podman", "runtime"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("CheckMicroVM missing required row %q; got %+v", name, checks)
		}
	}

	_, hasKVM := byName["kvm"]
	if runtime.GOOS == "linux" && !hasKVM {
		t.Errorf("expected a kvm row on linux; got %+v", checks)
	}
	if runtime.GOOS == "darwin" && hasKVM {
		t.Errorf("kvm row must be absent on darwin (HVF has no /dev/kvm); got %+v", checks)
	}
}

// TestCheckMicroVM_AvailableConsistency asserts Available() and CheckMicroVM
// agree: an error iff at least one structured check fails.
func TestCheckMicroVM_AvailableConsistency(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("krun backend unsupported on %s", runtime.GOOS)
	}

	anyFailed := false
	for _, c := range CheckMicroVM() {
		if !c.OK {
			anyFailed = true
			break
		}
	}

	err := NewKrunIsolator(DockerConfig{}).Available()
	if anyFailed && err == nil {
		t.Error("a structured check failed but Available() returned nil")
	}
	if !anyFailed && err != nil {
		t.Errorf("all structured checks passed but Available() returned %v", err)
	}
}

// TestMicroVMSessionActiveError checks the active-session error is actionable:
// it names the container and gives a copy-pasteable podman remediation command.
func TestMicroVMSessionActiveError(t *testing.T) {
	iso := NewKrunIsolator(DockerConfig{})
	const container = "devsandbox-example-abc123"
	msg := iso.microVMSessionActiveError(container).Error()

	for _, want := range []string{container, "podman rm -f " + container, "already active"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

// TestPreflight_DockerNoOp asserts the non-microVM backend has no launch-time
// conflict and never shells out to inspect a container.
func TestPreflight_DockerNoOp(t *testing.T) {
	iso := NewDockerIsolator(DockerConfig{})
	if err := iso.Preflight(context.Background(), "/tmp/test-project"); err != nil {
		t.Errorf("Preflight() on docker backend = %v, want nil", err)
	}
}
