package isolator

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"devsandbox/internal/network"
	"devsandbox/internal/sandbox"
)

// containerEngine describes a container CLI that the OCI-image backend drives.
// The Docker and krun (podman + libkrun microVM) backends share the same
// run/create/exec/network command surface, so a single implementation
// (DockerIsolator) serves both, parameterized by this descriptor.
type containerEngine struct {
	// backend is the public backend identifier (BackendDocker / BackendKrun).
	backend Backend
	// binary is the container CLI to invoke ("docker" or "podman").
	binary string
	// runtimeArgs are injected immediately after the run/create verb,
	// e.g. {"--runtime", "krun"} to boot the image inside a libkrun microVM.
	runtimeArgs []string
	// hostAlias is the in-sandbox hostname mapped to the proxy gateway
	// ("host.docker.internal" for Docker, "host.containers.internal" for podman).
	hostAlias string
	// isolationType is the metadata tag persisted with the session.
	isolationType sandbox.IsolationType
	// microVM is true when the engine boots a hardware VM and therefore needs
	// KVM (Linux) or Hypervisor.framework (macOS).
	microVM bool
}

var dockerEngine = containerEngine{
	backend:       BackendDocker,
	binary:        "docker",
	hostAlias:     "host.docker.internal",
	isolationType: sandbox.IsolationDocker,
}

// krunEngine runs the standard OCI sandbox image inside a libkrun microVM via
// `podman --runtime krun`. The container is the packaging layer; the microVM is
// the isolation boundary - a separate guest kernel behind KVM/HVF, which a
// host-kernel exploit cannot cross the way it can escape bwrap or a plain
// container.
var krunEngine = containerEngine{
	backend:     BackendKrun,
	binary:      "podman",
	runtimeArgs: []string{"--runtime", "krun"},
	// The guest reaches the host-loopback-bound proxy through the pasta gateway
	// (configured with --map-host-loopback), mirroring the bwrap backend. podman's
	// own host.containers.internal points at a link-local host IP, which would
	// force binding the proxy to a non-loopback, LAN-exposed address.
	hostAlias:     network.PastaGatewayIP,
	isolationType: sandbox.IsolationKrun,
	microVM:       true,
}

// NewKrunIsolator creates an isolator that runs the sandbox image inside a
// libkrun microVM. It reuses the entire Docker/OCI path (image build, shim
// entrypoint, tool bindings, .env hiding, proxy wiring) and differs only in the
// container engine descriptor.
func NewKrunIsolator(cfg DockerConfig) *DockerIsolator {
	return &DockerIsolator{config: cfg, engine: krunEngine}
}

// MicroVMCheck is one structured prerequisite result for the krun microVM
// backend. doctor renders these as informational rows; Available() turns the
// first failing one into a fail-fast error. Keeping the probe logic in one place
// means both consumers agree on what "ready for krun" means.
type MicroVMCheck struct {
	// Name identifies the prerequisite: "podman", "runtime" (the krun OCI
	// runtime), "kvm", or "platform" (unsupported OS or CPU architecture).
	Name string
	// OK is true when the prerequisite is satisfied.
	OK bool
	// Summary is a concise one-line status suitable for a doctor table cell:
	// the resolved path when OK, a short reason when not.
	Summary string
	// Hint is multi-line remediation guidance, empty when OK. Available()
	// appends it so the fail-fast error stays actionable.
	Hint string
}

// CheckMicroVM probes the krun microVM prerequisites and returns one result per
// check: the host CPU architecture, the container engine (podman), the krun OCI
// runtime, and - on Linux only - accessible /dev/kvm. macOS uses
// Hypervisor.framework, which has no /dev/kvm equivalent to probe, so the KVM row
// is omitted there; an unsupported OS or CPU architecture yields a failing
// "platform" check. The architecture row is emitted first so the fail-fast error
// from Available() names the unusable hardware instead of a missing tool the user
// would install for nothing.
func CheckMicroVM() []MicroVMCheck {
	var checks []MicroVMCheck
	if err := microVMArchSupported(runtime.GOOS, runtime.GOARCH); err != nil {
		checks = append(checks, MicroVMCheck{
			Name:    "platform",
			OK:      false,
			Summary: fmt.Sprintf("unsupported on %s/%s", runtime.GOOS, runtime.GOARCH),
			Hint:    err.Error(),
		})
	}
	checks = append(checks, checkEngineBinary(krunEngine.binary), checkKrunRuntime())

	switch runtime.GOOS {
	case "linux":
		checks = append(checks, checkKVMAccess())
	case "darwin":
		// libkrun uses Hypervisor.framework on Apple Silicon; there is no
		// /dev/kvm equivalent to probe here.
	default:
		checks = append(checks, MicroVMCheck{
			Name:    "platform",
			OK:      false,
			Summary: fmt.Sprintf("unsupported on %s", runtime.GOOS),
			Hint:    "the krun microVM backend runs only on Linux (KVM) or macOS (Hypervisor.framework)",
		})
	}

	return checks
}

func checkEngineBinary(binary string) MicroVMCheck {
	path, err := exec.LookPath(binary)
	if err != nil {
		return MicroVMCheck{
			Name:    "podman",
			OK:      false,
			Summary: fmt.Sprintf("%s not installed", binary),
			Hint:    "Install podman: https://podman.io/docs/installation",
		}
	}
	return MicroVMCheck{Name: "podman", OK: true, Summary: path}
}

func checkKrunRuntime() MicroVMCheck {
	path, err := exec.LookPath("krun")
	if err != nil {
		return MicroVMCheck{
			Name:    "runtime",
			OK:      false,
			Summary: "krun OCI runtime not found",
			Hint: "Install crun built with libkrun support (it provides the 'krun' runtime), then verify:\n" +
				"  podman run --rm --runtime krun docker.io/library/alpine true",
		}
	}
	return MicroVMCheck{Name: "runtime", OK: true, Summary: path}
}

// checkKVMAccess verifies the host exposes an accessible /dev/kvm, which the
// microVM backend needs on Linux (bare-metal or a nested-virt-enabled VM).
func checkKVMAccess() MicroVMCheck {
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return MicroVMCheck{
				Name:    "kvm",
				OK:      false,
				Summary: "/dev/kvm not found",
				Hint: "the krun microVM backend requires hardware virtualization (KVM)\n" +
					"Run on bare-metal Linux or a VM with nested virtualization enabled",
			}
		}
		return MicroVMCheck{
			Name:    "kvm",
			OK:      false,
			Summary: fmt.Sprintf("/dev/kvm not accessible: %v", err),
			Hint:    "Add your user to the 'kvm' group ('sudo usermod -aG kvm $USER') and re-login",
		}
	}
	if cerr := f.Close(); cerr != nil {
		return MicroVMCheck{Name: "kvm", OK: false, Summary: fmt.Sprintf("/dev/kvm: %v", cerr)}
	}
	return MicroVMCheck{Name: "kvm", OK: true, Summary: "/dev/kvm accessible"}
}

// microVMArchSupported reports why the krun microVM backend cannot run on the
// given OS/CPU-architecture pair, or nil when the combination is usable. It is a
// pure function of its inputs so CheckMicroVM (called with runtime.GOOS /
// runtime.GOARCH) and the unit tests exercise the same decision on any host.
//
// libkrun's macOS backend is Hypervisor.framework on Apple Silicon; Intel Macs
// have no supported path. Refusing them here fails the launch fast with
// installation guidance instead of surfacing an opaque runtime error after the
// image build. Linux gates on /dev/kvm rather than the architecture, so no arch
// restriction applies there.
func microVMArchSupported(goos, goarch string) error {
	if goos != "darwin" || goarch == "arm64" {
		return nil
	}
	return fmt.Errorf("the krun microVM backend requires Apple Silicon (arm64) on macOS, but this host is "+
		"%s/%s: libkrun uses Hypervisor.framework, which devsandbox supports only on M-series hardware. "+
		"Use --isolation=docker on Intel Macs, or run krun on a Linux host with /dev/kvm", goos, goarch)
}

// microVMProxyUnsupported reports why the krun microVM backend cannot run in
// proxy mode on the given OS, or nil when the combination is allowed. It is a
// pure function of its inputs so the run path (called with runtime.GOOS) and the
// unit tests (called with an explicit "darwin"/"linux") exercise the same
// decision on any host.
//
// The egress lockdown that keeps a krun+proxy guest from bypassing the proxy
// (route surgery plus an in-netns firewall in the VMM's pasta namespace) is
// implemented Linux-only; macOS/HVF has no route-surgery lockdown yet, so proxy
// mode there would run with open egress - a silent fallback to weaker isolation.
// Refuse that fail-closed rather than run a workload the proxy cannot contain.
// Non-proxy krun and every non-microVM backend are unaffected.
func microVMProxyUnsupported(goos string, microVM, proxyEnabled bool) error {
	if !microVM || !proxyEnabled || goos == "linux" {
		return nil
	}
	return fmt.Errorf("krun proxy mode is not supported on %s: the egress lockdown that forces guest "+
		"traffic through the proxy is implemented on Linux only, so proxy mode here would run with open "+
		"egress (no route-surgery lockdown on macOS/HVF yet). Run on Linux, or disable proxy mode to use "+
		"krun without egress filtering", goos)
}

// availableMicroVM fails fast with actionable guidance when any krun
// prerequisite is missing, rather than silently degrading to a weaker isolation
// backend. It reuses CheckMicroVM so the run path and doctor agree on what krun
// needs.
func (d *DockerIsolator) availableMicroVM() error {
	for _, c := range CheckMicroVM() {
		if c.OK {
			continue
		}
		msg := c.Summary
		if c.Hint != "" {
			msg += "\n" + c.Hint
		}
		return errors.New(msg)
	}
	return nil
}
