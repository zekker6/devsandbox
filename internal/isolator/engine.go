package isolator

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"slices"
	"strconv"
	"strings"

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
	// runtime), "kvm", "platform" (unsupported OS or CPU architecture),
	// "system pasta", or "rootless id mapping".
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
	if row, gap := microVMArchGap(runtime.GOOS, runtime.GOARCH); gap {
		checks = append(checks, row)
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

// CheckSystemPasta reports whether the host provides a system-wide pasta binary.
// Rootless podman needs one to give the krun guest a network, which is a
// separate requirement from the pasta devsandbox embeds for the bwrap backend:
// the embedded copy is extracted into the devsandbox cache and podman never
// looks there. Like the proxy firewall row this is advisory - doctor warns so
// the gap surfaces before a launch trips over it. The probe only searches $PATH, while
// podman also consults helper_binaries_dir from containers.conf, so the warning
// says it may be a false positive rather than parsing that config.
func CheckSystemPasta() MicroVMCheck {
	return checkSystemPasta(exec.LookPath)
}

func checkSystemPasta(lookPath func(string) (string, error)) MicroVMCheck {
	path, err := lookPath("pasta")
	if err != nil {
		return MicroVMCheck{
			Name:    systemPastaName,
			OK:      false,
			Summary: "pasta not installed (rootless podman networking)",
			Hint: "Install the 'passt' package, which provides pasta:\n" +
				"  Arch:          sudo pacman -S passt\n" +
				"  Fedora:        sudo dnf install passt\n" +
				"  Debian/Ubuntu: sudo apt install passt\n" +
				"The pasta devsandbox embeds for the bwrap backend does not satisfy podman.\n" +
				"This probe searches $PATH only, so a host that installs pasta into podman's\n" +
				"helper_binaries_dir (containers.conf, e.g. /usr/libexec/podman) may already be\n" +
				"configured correctly; verify with: podman info --format '{{.Host.Pasta.Executable}}'",
		}
	}
	return MicroVMCheck{Name: systemPastaName, OK: true, Summary: path}
}

// subUIDPath and subGIDPath are the shadow-utils subordinate id databases that
// rootless podman consults when building the guest user namespace.
const (
	subUIDPath = "/etc/subuid"
	subGIDPath = "/etc/subgid"
)

// Row names for the probes whose verdict is built in more than one place.
const (
	systemPastaName       = "system pasta"
	rootlessIDMappingName = "rootless id mapping"
)

// CheckRootlessIDMapping reports whether the current user owns subordinate uid
// and gid ranges. The krun backend runs the guest under rootless podman with
// --userns=keep-id, which cannot build its user namespace without them. Most
// distributions provision the ranges when podman is installed, so the row is
// advisory: doctor warns rather than failing a host that never runs krun.
func CheckRootlessIDMapping() MicroVMCheck {
	return checkRootlessIDMapping(user.Current, func(path string) (io.ReadCloser, error) { return os.Open(path) })
}

func checkRootlessIDMapping(currentUser func() (*user.User, error), open func(string) (io.ReadCloser, error)) MicroVMCheck {
	u, err := currentUser()
	if err != nil {
		return MicroVMCheck{
			Name:    rootlessIDMappingName,
			OK:      false,
			Summary: fmt.Sprintf("cannot resolve the current user: %v", err),
			// Not a subordinate-id gap: devsandbox is built CGO_ENABLED=0, so
			// this is the pure-Go /etc/passwd parser failing to find the user.
			// Adding ranges would not help; the name resolution is the problem.
			Hint: "devsandbox could not resolve your account from /etc/passwd. On a host that resolves\n" +
				"users through LDAP/SSSD or systemd-homed, this probe cannot verify the subordinate id\n" +
				"ranges rootless podman needs; check the mapping manually with: podman unshare cat /proc/self/uid_map",
		}
	}
	// A root podman does not map subordinate ids at all, so an empty database
	// is not a gap there and must not be reported as one.
	if u.Uid == "0" {
		return MicroVMCheck{
			Name:    rootlessIDMappingName,
			OK:      true,
			Summary: "running as root (no subordinate ranges needed)",
		}
	}

	owners := []string{u.Username, u.Uid}
	for _, path := range []string{subUIDPath, subGIDPath} {
		mapped, ferr := subIDFileMapped(open, path, owners)
		if ferr != nil {
			return MicroVMCheck{
				Name:    rootlessIDMappingName,
				OK:      false,
				Summary: fmt.Sprintf("%s: %v", path, ferr),
				Hint:    subIDHint(u.Username),
			}
		}
		if !mapped {
			return MicroVMCheck{
				Name:    rootlessIDMappingName,
				OK:      false,
				Summary: fmt.Sprintf("no %s range for %s", path, u.Username),
				Hint:    subIDHint(u.Username),
			}
		}
	}
	return MicroVMCheck{
		Name:    rootlessIDMappingName,
		OK:      true,
		Summary: fmt.Sprintf("%s and %s map %s", subUIDPath, subGIDPath, u.Username),
	}
}

func subIDFileMapped(open func(string) (io.ReadCloser, error), path string, owners []string) (bool, error) {
	f, err := open(path)
	if err != nil {
		return false, err
	}
	mapped, err := subIDMapped(f, owners)
	if cerr := f.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return mapped, err
}

// subIDMapped reports whether the /etc/subuid- or /etc/subgid-formatted content
// in r allocates a non-empty subordinate range to any of owners. Lines are
// "owner:start:count"; blanks, comments, and malformed lines are skipped the way
// shadow-utils tolerates them, so a stray line does not mask a valid entry
// further down. Both the login name and the numeric id are accepted as owners
// because either may key a line.
func subIDMapped(r io.Reader, owners []string) (bool, error) {
	s := bufio.NewScanner(r)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) != 3 || !slices.Contains(owners, fields[0]) {
			continue
		}
		if _, err := strconv.ParseUint(fields[1], 10, 32); err != nil {
			continue
		}
		if count, err := strconv.ParseUint(fields[2], 10, 32); err != nil || count == 0 {
			continue
		}
		return true, nil
	}
	return false, s.Err()
}

func subIDHint(username string) string {
	return fmt.Sprintf("Rootless podman maps your user into the guest with --userns=keep-id, which needs\n"+
		"subordinate id ranges. Add them, then reload podman's user namespace:\n"+
		"  sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 %s\n"+
		"  podman system migrate\n"+
		"Most distributions provision these when podman is installed. This probe reads the files\n"+
		"directly, so a host that serves its ranges through libsubid/NSS (LDAP, SSSD) may already be\n"+
		"configured correctly; verify with: podman unshare cat /proc/self/uid_map", username)
}

// microVMArchSupported reports whether the krun microVM backend can run on the
// given OS/CPU-architecture pair. It is a pure function of its inputs so
// CheckMicroVM (called with runtime.GOOS / runtime.GOARCH) and the unit tests
// exercise the same decision on any host.
//
// libkrun's macOS backend is Hypervisor.framework on Apple Silicon; Intel Macs
// have no supported path. Refusing them here fails the launch fast with
// installation guidance instead of surfacing an opaque runtime error after the
// image build. Linux gates on /dev/kvm rather than the architecture, so no arch
// restriction applies there.
func microVMArchSupported(goos, goarch string) bool {
	return goos != "darwin" || goarch == "arm64"
}

// microVMArchGap renders the architecture verdict as a doctor row, reporting
// gap=true (and the row) only when the pair is unusable so CheckMicroVM emits
// nothing on supported hardware. Splitting it out of CheckMicroVM keeps the row's
// shape - name, concise summary, wrapped remediation - testable on any host,
// which CheckMicroVM itself is not.
//
// The Hint is hand-wrapped because it is also the fail-fast launch error
// (firstMicroVMGap appends it to the Summary) and printDoctorHints indents what
// it prints, so a single long line would wrap arbitrarily on a normal terminal.
func microVMArchGap(goos, goarch string) (MicroVMCheck, bool) {
	if microVMArchSupported(goos, goarch) {
		return MicroVMCheck{}, false
	}
	return MicroVMCheck{
		Name:    "platform",
		OK:      false,
		Summary: fmt.Sprintf("unsupported on %s/%s", goos, goarch),
		Hint: fmt.Sprintf("the krun microVM backend requires Apple Silicon (arm64) on macOS, but this\n"+
			"host is %s/%s: libkrun uses Hypervisor.framework, which devsandbox\n"+
			"supports only on M-series hardware.\n"+
			"Use --isolation=docker on Intel Macs, or run krun on a Linux host with /dev/kvm", goos, goarch),
	}, true
}

// microVMProxyUnsupported reports why the krun microVM backend cannot run in
// proxy mode on the given OS, or nil when the combination is allowed. It is a
// pure function of its inputs so the run path (called with runtime.GOOS) and the
// unit tests (called with an explicit "darwin"/"linux") exercise the same
// decision on any host.
//
// The egress lockdown that keeps a krun+proxy guest from bypassing the proxy
// (route surgery plus a deny-by-default in-netns firewall in the VMM's pasta
// namespace) is implemented Linux-only; macOS/HVF has no equivalent yet, so
// proxy mode there would run with open egress - a silent fallback to weaker
// isolation.
// Refuse that fail-closed rather than run a workload the proxy cannot contain.
// Non-proxy krun and every non-microVM backend are unaffected.
func microVMProxyUnsupported(goos string, microVM, proxyEnabled bool) error {
	if !microVM || !proxyEnabled || goos == "linux" {
		return nil
	}
	return fmt.Errorf("krun proxy mode is not supported on %s: the egress lockdown that forces guest "+
		"traffic through the proxy is implemented on Linux only, so proxy mode here would run with open "+
		"egress (no egress lockdown on macOS/HVF yet). Run on Linux, or disable proxy mode to use "+
		"krun without egress filtering", goos)
}

// availableMicroVM fails fast with actionable guidance when any krun
// prerequisite is missing, rather than silently degrading to a weaker isolation
// backend. It reuses CheckMicroVM so the run path and doctor agree on what krun
// needs.
func (d *DockerIsolator) availableMicroVM() error {
	return firstMicroVMGap(CheckMicroVM())
}

// firstMicroVMGap turns the first unmet prerequisite into the fail-fast error,
// which is why CheckMicroVM orders the architecture row ahead of the tool rows:
// on unusable hardware the user must hear that before being told to install
// podman for nothing.
func firstMicroVMGap(checks []MicroVMCheck) error {
	for _, c := range checks {
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
