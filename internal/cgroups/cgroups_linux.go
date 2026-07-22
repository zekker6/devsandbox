//go:build linux

package cgroups

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// cgroupRoot is the cgroup v2 unified mount point and procCgroupPath describes
// this process' cgroup membership. Both are variables so tests can point the
// preflight checks at a fake hierarchy.
var (
	cgroupRoot     = "/sys/fs/cgroup"
	procCgroupPath = "/proc/self/cgroup"
)

// limitNames maps a cgroup v2 controller to the config field that needs it, so
// a delegation failure can name the limit the user actually wrote.
var limitNames = map[string]string{
	"memory": "memory",
	"cpu":    "cpus",
	"pids":   "pids",
}

// Preflight verifies the limits can actually be enforced before anything is
// launched, so a limit that would be silently ignored aborts the run instead.
// It reports nil when no limits are configured.
//
// The checks run in a deliberate order. The cgroup v2 mount is verified before
// anything derived from /proc/self/cgroup is trusted, because a nested sandbox
// can report a plausible-looking host cgroup path while no unified hierarchy is
// mounted at all. systemd-run being on PATH is checked before the user manager
// because the binary is present on hosts whose user manager is offline. The
// live scope probe runs last, once the cheap gates have narrowed the failure
// down to something only the manager itself can answer.
func Preflight(l Limits) error {
	if l.IsZero() {
		return nil
	}
	props, err := l.properties()
	if err != nil {
		return err
	}
	if err := checkUnifiedHierarchy(); err != nil {
		return err
	}
	if err := checkSystemdRun(); err != nil {
		return err
	}
	if err := checkUserManager(); err != nil {
		return err
	}
	if err := checkDelegation(l); err != nil {
		return err
	}
	return probeScope(props)
}

// probeScope is the live check that the user manager will accept a scope
// carrying these properties. It is a variable so tests can exercise Preflight
// without talking to a real bus.
var probeScope = runScopeProbe

// scopeProbeTimeout bounds the probe's D-Bus round trip, so an unresponsive
// user manager surfaces as a preflight error rather than a hung launch. It is a
// variable so tests can exercise the expiry without waiting it out.
var scopeProbeTimeout = 10 * time.Second

// runScopeProbe creates and immediately tears down a throwaway transient scope
// carrying the configured properties.
//
// This is the guard for the one non-enforcement source the static checks cannot
// reach: a property the manager refuses at D-Bus time. Delegation can be
// correct and every earlier gate pass while the scope is still rejected, and
// systemd-run reports that by exiting 1 - the same status the sandboxed
// workload most commonly exits with, so it cannot be told apart after the fact.
// Paying one round trip up front turns that ambiguity into a specific
// pre-launch error naming what systemd rejected.
//
// The payload is systemd-run itself, run as `--version`: it is the one program
// guaranteed to exist and exit 0 at this point, so a failed probe can only mean
// the scope was refused, never that the payload was missing.
func runScopeProbe(props []string) error {
	systemdRun, err := resolveSystemdRun()
	if err != nil {
		return fmt.Errorf("resource limits require systemd-run: %w", err)
	}

	args := scopeArgs(unitName()+"-probe", probeDescription, props, systemdRun, []string{"--version"})

	ctx, cancel := context.WithTimeout(context.Background(), scopeProbeTimeout)
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, systemdRun, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr == nil {
		return nil
	}

	// An unresponsive manager is a different diagnosis from a rejected
	// property, and it arrives as a SIGKILL from the context - so it has to be
	// recognised before the signal check below claims the scope was accepted.
	if ctx.Err() != nil {
		return fmt.Errorf(
			"the systemd user manager did not answer a transient scope request within %s (check 'systemctl --user is-system-running')",
			scopeProbeTimeout,
		)
	}

	// The probe payload runs under the configured limits, so a small but
	// perfectly valid memory value OOM-kills it. systemd only execs the payload
	// once the scope exists, so a payload killed by a signal still answers the
	// question the probe asks - the properties were accepted. Reporting it as a
	// refusal would send the user after cgroup delegation over their own value.
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return nil
		}
	}

	detail := strings.TrimSpace(stderr.String())
	if detail == "" {
		detail = runErr.Error()
	}
	msg := fmt.Sprintf(
		"the systemd user manager refused a transient scope carrying the configured limits (%s): %s",
		strings.Join(props, " "), detail,
	)
	// The static gates cannot reject this case outright - plenty of sessions
	// outside the user manager's own subtree still work - but when the scope is
	// refused it is by far the most likely cause, and the manager's own message
	// ("Permission denied") names neither the cause nor a remedy.
	if _, found := userManagerCgroup(); !found {
		msg += "\n" + outsideUserManagerHint
	}
	return errors.New(msg)
}

// probeDescription keeps the throwaway probe scope distinguishable from a real
// sandbox scope in the journal.
const probeDescription = "devsandbox resource limit probe"

// outsideUserManagerHint explains the refusal a session outside the systemd user
// manager's cgroup subtree hits. Migrating a process into a transient scope
// needs write access to the cgroup.procs of the common ancestor of the source
// and destination cgroups. An SSH or bare-TTY login sits in
// user-<uid>.slice/session-N.scope while the scope is created under
// user@<uid>.service, so that common ancestor is the root-owned user-<uid>.slice
// and the unprivileged user manager cannot perform the move. Desktop sessions
// are already under user@<uid>.service and are unaffected, which is what makes
// this easy to miss locally and common on the servers most likely to want limits.
const outsideUserManagerHint = "this session is not running under the systemd user manager " +
	"(no user@<uid>.service component in /proc/self/cgroup), which is the usual cause on SSH and bare-TTY logins: " +
	"start devsandbox from a session the user manager owns (for example via 'machinectl shell'), " +
	"or remove the [sandbox.resources] limits to run unlimited"

// requiredControllers lists the cgroup v2 controllers the limits need, in the
// same order properties emits them.
func requiredControllers(l Limits) []string {
	var out []string
	if l.Memory != "" {
		out = append(out, "memory")
	}
	if l.CPUs != "" {
		out = append(out, "cpu")
	}
	if l.PIDs > 0 {
		out = append(out, "pids")
	}
	return out
}

func checkUnifiedHierarchy() error {
	if _, err := os.Stat(cgroupRoot); err != nil {
		return fmt.Errorf("resource limits require a cgroup v2 unified hierarchy, but %s cannot be accessed: %w", cgroupRoot, err)
	}
	controllers := filepath.Join(cgroupRoot, "cgroup.controllers")
	if _, err := os.Stat(controllers); err != nil {
		return fmt.Errorf("resource limits require a cgroup v2 unified hierarchy, but %s has no cgroup.controllers file, so this host is using cgroup v1", cgroupRoot)
	}
	return nil
}

func checkSystemdRun() error {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return fmt.Errorf("resource limits require systemd-run, which was not found on PATH: %w", err)
	}
	return nil
}

// userManagerDialTimeout bounds the liveness dial to the user manager control
// socket, so a wedged listener surfaces as a preflight error rather than a hang.
const userManagerDialTimeout = 2 * time.Second

func checkUserManager() error {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	socket := filepath.Join(dir, "systemd", "private")

	// Dial, do not stat: the control socket file outlives the manager, so a
	// stale socket passes a stat check while systemd-run itself gets a refused
	// connection. Statting here would let a downed manager surface one step
	// later as an opaque "cannot read delegated controllers" that blames a
	// cgroup namespace instead of the real cause.
	conn, err := net.DialTimeout("unix", socket, userManagerDialTimeout)
	if err != nil {
		return fmt.Errorf(
			"resource limits require a running systemd user manager, but connecting to its control socket %s failed: %w; "+
				"start it with 'loginctl enable-linger %d' or run from a session the manager owns (for example via 'machinectl shell'), then verify with 'systemctl --user is-system-running'",
			socket, err, os.Getuid(),
		)
	}
	_ = conn.Close()
	return nil
}

func checkDelegation(l Limits) error {
	available, err := delegatedControllers()
	if err != nil {
		return fmt.Errorf("%w (a cgroup namespace or a nested sandbox can hide the systemd user manager cgroup)", err)
	}
	for _, c := range requiredControllers(l) {
		if !slices.Contains(available, c) {
			return fmt.Errorf(
				"the systemd user manager does not have the %q cgroup controller delegated, so the configured %s limit would be silently ignored: delegate it with a drop-in on user@.service setting 'Delegate=cpu cpuset io memory pids'",
				c, limitNames[c],
			)
		}
	}
	return nil
}

// delegatedControllers reads the controllers made available to the systemd user
// manager cgroup, which is the subtree an unprivileged transient scope lands in.
func delegatedControllers() ([]string, error) {
	managerCgroup, _ := userManagerCgroup()
	file := filepath.Join(cgroupRoot, filepath.FromSlash(managerCgroup), "cgroup.controllers")
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("cannot read delegated cgroup controllers from %s: %w", file, err)
	}
	return strings.Fields(string(data)), nil
}

// userManagerCgroup returns the cgroup path of the systemd user manager,
// relative to the unified mount point, and whether this process was actually
// found inside it. It is derived from the 0:: line of /proc/self/cgroup by
// walking to the user@<uid>.service component, and falls back to the
// conventional layout when that component is not present.
//
// The caller-visible flag matters as much as the path: a false means the scope
// this process asks for will be created in a subtree it does not itself live in,
// which is what makes the migration fail. See outsideUserManagerHint.
func userManagerCgroup() (string, bool) {
	unit := fmt.Sprintf("user@%d.service", os.Getuid())
	fallback := path.Join("/user.slice", fmt.Sprintf("user-%d.slice", os.Getuid()), unit)

	data, err := os.ReadFile(procCgroupPath)
	if err != nil {
		return fallback, false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "0::")
		if !ok {
			continue
		}
		parts := strings.Split(rest, "/")
		if i := slices.Index(parts, unit); i >= 0 {
			return strings.Join(parts[:i+1], "/"), true
		}
		break
	}
	return fallback, false
}
