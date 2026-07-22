package isolator

import (
	"runtime"
	"testing"

	"devsandbox/internal/cgroups"
)

// applyOptions runs the functional options the way New does, so the option layer
// can be tested without an engine or a bwrap binary being present.
func applyOptions(opts ...Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

func TestDetect_Auto_Linux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only test")
	}
	backend, err := Detect(BackendAuto)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if backend != BackendBwrap {
		t.Errorf("Expected bwrap on Linux, got %s", backend)
	}
}

func TestDetect_Explicit(t *testing.T) {
	tests := []struct {
		requested Backend
		expected  Backend
	}{
		{BackendBwrap, BackendBwrap},
		{BackendDocker, BackendDocker},
		{BackendKrun, BackendKrun},
	}
	for _, tt := range tests {
		backend, err := Detect(tt.requested)
		if err != nil {
			t.Fatalf("Detect(%s) failed: %v", tt.requested, err)
		}
		if backend != tt.expected {
			t.Errorf("Detect(%s) = %s, want %s", tt.requested, backend, tt.expected)
		}
	}
}

func TestDetect_UnknownBackend(t *testing.T) {
	_, err := Detect(Backend("unknown"))
	if err == nil {
		t.Error("Detect with unknown backend should return error")
	}
}

func TestNew_UnknownBackend(t *testing.T) {
	_, err := New(Backend("unknown"))
	if err == nil {
		t.Error("New with unknown backend should return error")
	}
}

// neutralResources is the common case, where nothing is configured under the
// deprecated [sandbox.docker.resources] section so both arguments carry the same
// limits.
func neutralResources(memory, cpus string, pids int) Option {
	l := cgroups.Limits{Memory: memory, CPUs: cpus, PIDs: pids}
	return WithResources(l, l)
}

// TestWithResources_BwrapConfig verifies the bwrap backend receives the
// configured limits verbatim, and that no limits stay no limits: bwrap is opt-in
// and must launch exactly as before when nothing is configured.
func TestWithResources_BwrapConfig(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
		want cgroups.Limits
	}{
		{"unset", nil, cgroups.Limits{}},
		{
			"all limits",
			[]Option{neutralResources("512m", "0.5", 64)},
			cgroups.Limits{Memory: "512m", CPUs: "0.5", PIDs: 64},
		},
		{
			"pids only",
			[]Option{neutralResources("", "", 2048)},
			cgroups.Limits{PIDs: 2048},
		},
		{
			"docker config does not set limits",
			[]Option{WithDockerConfig("Dockerfile", "/etc/devsandbox", true)},
			cgroups.Limits{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyOptions(tt.opts...).bwrapConfig().Limits
			if got != tt.want {
				t.Errorf("bwrapConfig().Limits = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestWithResources_DockerConfig verifies docker gets no resource defaults: an
// unset config must produce empty limits so no --memory/--cpus is emitted.
func TestWithResources_DockerConfig(t *testing.T) {
	unset := applyOptions(WithDockerConfig("Dockerfile", "/etc/devsandbox", true)).dockerConfig()
	if unset.MemoryLimit != "" || unset.CPULimit != "" || unset.PIDsLimit != 0 {
		t.Errorf("docker must get no resource defaults, got memory=%q cpus=%q pids=%d",
			unset.MemoryLimit, unset.CPULimit, unset.PIDsLimit)
	}
	if unset.Dockerfile != "Dockerfile" || unset.ConfigDir != "/etc/devsandbox" || !unset.KeepContainer {
		t.Errorf("docker config not threaded through: %+v", unset)
	}

	// pids has to be asserted here and not only in TestDockerIsolator_PIDsLimit,
	// which starts from a hand-built DockerConfig and so cannot see this hop. A
	// dropped assignment here means a configured pids silently applies to
	// nothing: no --pids-limit flag, and no warning either.
	explicit := applyOptions(neutralResources("8g", "4", 2048)).dockerConfig()
	if explicit.MemoryLimit != "8g" || explicit.CPULimit != "4" || explicit.PIDsLimit != 2048 {
		t.Errorf("docker limits = (%q,%q,%d), want (8g,4,2048)",
			explicit.MemoryLimit, explicit.CPULimit, explicit.PIDsLimit)
	}
}

// The deprecated [sandbox.docker.resources] section is scoped to the container
// backends. It reaches devsandbox as the container argument of WithResources
// only, and bwrap must see nothing from it: bwrap never honored that section,
// and it aborts the run when the host cannot enforce a limit, so applying it
// there would turn a working docker-scoped config into a refusal to start.
func TestWithResources_DeprecatedDockerSectionSkipsBwrap(t *testing.T) {
	// What config.ResolvedResources produces for a config that sets only
	// [sandbox.docker.resources]: an empty neutral section, a populated merged one.
	container := cgroups.Limits{Memory: "4g", CPUs: "2"}
	o := applyOptions(WithResources(cgroups.Limits{}, container))

	if got := o.bwrapConfig().Limits; !got.IsZero() {
		t.Errorf("bwrap limits = %+v, want none: the deprecated docker section must not opt bwrap into enforcement", got)
	}
	if got := o.dockerConfig(); got.MemoryLimit != "4g" || got.CPULimit != "2" {
		t.Errorf("docker limits = (%q,%q), want (4g,2): the deprecated section must keep working for docker",
			got.MemoryLimit, got.CPULimit)
	}
	if got := o.krunConfig(); got.MemoryLimit != "4g" || got.CPULimit != "2" {
		t.Errorf("krun limits = (%q,%q), want (4g,2): the deprecated section must keep working for krun",
			got.MemoryLimit, got.CPULimit)
	}
}

// TestWithResources_KrunConfig verifies krun's 4g/2 defaults survive the option
// layer unchanged: unset values are filled, explicit values are never overridden.
func TestWithResources_KrunConfig(t *testing.T) {
	tests := []struct {
		name     string
		opts     []Option
		wantMem  string
		wantCPU  string
		wantPIDs int
	}{
		{"unset -> defaults", nil, defaultKrunMemory, defaultKrunCPUs, 0},
		{"explicit respected", []Option{neutralResources("8g", "4", 0)}, "8g", "4", 0},
		{"only memory set", []Option{neutralResources("16g", "", 0)}, "16g", defaultKrunCPUs, 0},
		{"only cpus set", []Option{neutralResources("", "1", 0)}, defaultKrunMemory, "1", 0},
		// krun deliberately forwards a configured pids rather than zeroing it:
		// the emission site is what suppresses the flag, and it can only tell
		// the user which value it dropped if the value survives to that point.
		{"pids forwarded for the emission site to report", []Option{neutralResources("", "", 512)}, defaultKrunMemory, defaultKrunCPUs, 512},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyOptions(tt.opts...).krunConfig()
			if got.MemoryLimit != tt.wantMem || got.CPULimit != tt.wantCPU {
				t.Errorf("krunConfig() limits = (%q,%q), want (%q,%q)",
					got.MemoryLimit, got.CPULimit, tt.wantMem, tt.wantCPU)
			}
			if got.PIDsLimit != tt.wantPIDs {
				t.Errorf("krunConfig() pids = %d, want %d", got.PIDsLimit, tt.wantPIDs)
			}
			// krun is always ephemeral, whatever the keep-container setting says.
			if got.KeepContainer {
				t.Error("krun config must never keep the container")
			}
		})
	}
}

// TestNew_BwrapReceivesResources exercises the full New() wiring rather than the
// option accessor, so a branch that stops passing limits through is caught.
func TestNew_BwrapReceivesResources(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only test")
	}
	iso, err := New(BackendBwrap, neutralResources("512m", "0.5", 64))
	if err != nil {
		t.Skipf("bwrap unavailable: %v", err)
	}
	b, ok := iso.(*BwrapIsolator)
	if !ok {
		t.Fatalf("New(BackendBwrap) returned %T, want *BwrapIsolator", iso)
	}
	want := cgroups.Limits{Memory: "512m", CPUs: "0.5", PIDs: 64}
	if b.config.Limits != want {
		t.Errorf("bwrap limits = %+v, want %+v", b.config.Limits, want)
	}
}
