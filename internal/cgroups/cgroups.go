// Package cgroups translates backend-neutral sandbox resource limits into
// systemd transient scope properties and wraps a launch so those properties are
// applied to the sandbox process.
//
// It knows nothing about any particular backend. The limit values it accepts
// are the ones the config layer accepts, and every value it cannot translate
// exactly is rejected with a specific error rather than emitted as a property
// systemd would either refuse opaquely or accept with the wrong meaning.
package cgroups

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"

	"devsandbox/internal/cmdpattern"
)

// Limits describes the resource caps applied to a sandbox. Empty memory and
// cpus strings, and a zero pids count, mean unlimited.
type Limits struct {
	Memory string
	CPUs   string
	PIDs   int
}

// IsZero reports whether no limits are configured.
func (l Limits) IsZero() bool {
	return l.Memory == "" && l.CPUs == "" && l.PIDs == 0
}

// memoryPattern matches a byte count with an optional base-1024 unit suffix.
var memoryPattern = regexp.MustCompile(`^\d+[bkmgBKMG]?$`)

// zeroMemoryPattern matches the accepted forms that denote zero bytes.
var zeroMemoryPattern = regexp.MustCompile(`^0+[bkmgBKMG]?$`)

// maxCPUs bounds the cpus value so the percent conversion cannot overflow.
const maxCPUs = 1e6

// ValidateMemory checks a memory limit string against the forms this package can
// translate. It is exported so the config layer validates exactly those forms
// rather than keeping a second copy of the rules that could drift from these.
//
// Zero is accepted here on purpose: it is a valid value for the config layer,
// where docker's `--memory 0` is the documented way to say unlimited. Only the
// systemd scope path rejects it, in validateSystemdMemory below.
func ValidateMemory(s string) error {
	if s == "" {
		return nil
	}
	if !memoryPattern.MatchString(s) {
		return fmt.Errorf("invalid memory limit %q: use a byte count with an optional b, k, m or g suffix, like '512m' or '2g'", s)
	}
	return nil
}

// validateSystemdMemory adds the constraint that only holds under a transient
// scope: systemd's MemoryMax=0 grants the scope no memory at all, so a sandbox
// launched with it is OOM-killed the moment it starts. Docker reads the same
// value as unlimited, which is why this rejection lives here and not in
// ValidateMemory.
func validateSystemdMemory(s string) error {
	if err := ValidateMemory(s); err != nil {
		return err
	}
	if zeroMemoryPattern.MatchString(s) {
		return fmt.Errorf("invalid memory limit %q: MemoryMax=0 would leave the sandbox no memory at all and it would be OOM-killed on start; omit the setting for unlimited", s)
	}
	return nil
}

// properties converts the limits to systemd unit properties, in a stable order:
// memory, cpu, pids.
func (l Limits) properties() ([]string, error) {
	var props []string

	if l.Memory != "" {
		if err := validateSystemdMemory(l.Memory); err != nil {
			return nil, err
		}
		// systemd's MemoryMax uses base-1024 uppercase suffixes and treats a
		// bare integer as bytes, matching the config form once uppercased.
		//
		// No MemorySwapMax accompanies it, so memory.swap.max stays at max and
		// swap is not bounded here. That is weaker than the container backends:
		// docker and krun pass --memory with --memory-swap unset, which the
		// engine expands to a memory+swap ceiling of twice the value.
		//
		// Pinning swap to zero would make the configured number a hard ceiling,
		// but it cannot be verified: memory.swap.max exists only with kernel
		// swap accounting, and without it systemd logs a warning and starts the
		// scope anyway, which no preflight check can observe. Asserting a
		// guarantee the host may not be enforcing is worse than documenting the
		// weaker one bwrap actually delivers.
		props = append(props, "MemoryMax="+strings.ToUpper(l.Memory))
	}

	if l.CPUs != "" {
		v, err := strconv.ParseFloat(l.CPUs, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > maxCPUs {
			return nil, fmt.Errorf("invalid cpu limit %q: must be a positive number like '0.5' or '2'", l.CPUs)
		}
		percent := int(math.Round(v * 100))
		if percent == 0 {
			return nil, fmt.Errorf("cpu limit %q rounds to CPUQuota=0%%, which would never schedule: use at least '0.01'", l.CPUs)
		}
		props = append(props, fmt.Sprintf("CPUQuota=%d%%", percent))
	}

	if l.PIDs < 0 {
		return nil, fmt.Errorf("invalid pids limit %d: must be zero (unlimited) or a positive number", l.PIDs)
	}
	if l.PIDs > 0 {
		props = append(props, fmt.Sprintf("TasksMax=%d", l.PIDs))
	}

	return props, nil
}

// resolveSystemdRun returns the absolute, symlink-resolved path to systemd-run.
// It is a variable so tests can pin a path on hosts without systemd installed.
var resolveSystemdRun = func() (string, error) {
	return cmdpattern.ResolveProgram("systemd-run")
}

// unitSuffix disambiguates the scope name beyond the PID, which is unique only
// among live processes: a scope leaked by an earlier run plus PID reuse would
// otherwise collide and fail the launch with "unit already exists". It is a
// variable so argv tests can pin it.
var unitSuffix = randomSuffix()

func randomSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Only the collision-avoidance quality degrades, and the PID alone is
		// what this already relied on, so there is nothing to fail here.
		return "0"
	}
	return hex.EncodeToString(b[:])
}

// unitName returns the transient scope's unit name, unique per run. The PID is
// the only per-run identifier reachable here, and on the exec-replace launch
// path it is also the PID that ends up inside the scope.
func unitName() string {
	return fmt.Sprintf("devsandbox-%d-%s", os.Getpid(), unitSuffix)
}

// scopeDescription replaces the description systemd would otherwise derive from
// the full command line. The bwrap argv lists every host path bound into the
// sandbox, so an explicit description keeps that list out of the journal and out
// of `systemctl --user list-units`.
const scopeDescription = "devsandbox sandbox"

// scopeArgs assembles systemd-run's arguments for a transient scope, excluding
// argv[0]. Wrap and the preflight probe both build their invocation here, so the
// properties the probe validates cannot drift from the ones the launch carries.
func scopeArgs(unit, description string, props []string, program string, args []string) []string {
	out := make([]string, 0, 6+2*len(props)+2+len(args))
	out = append(out,
		"--user",
		"--scope",
		"--quiet",
		"--collect",
		"--unit="+unit,
		"--description="+description,
	)
	for _, p := range props {
		out = append(out, "-p", p)
	}
	out = append(out, "--", program)
	return append(out, args...)
}

// Wrap rewrites a program and its arguments to run inside a systemd transient
// scope carrying the limits. It returns the inputs unchanged when no limits are
// configured, so an unconfigured sandbox launches exactly as it did before.
//
// The returned args exclude argv[0]. Callers apply their own convention, which
// differs between syscall.Exec and exec.Command.
func Wrap(l Limits, program string, args []string) (string, []string, error) {
	if l.IsZero() {
		return program, args, nil
	}

	props, err := l.properties()
	if err != nil {
		return "", nil, err
	}

	systemdRun, err := resolveSystemdRun()
	if err != nil {
		return "", nil, fmt.Errorf("resource limits require systemd-run: %w", err)
	}

	return systemdRun, scopeArgs(unitName(), scopeDescription, props, program, args), nil
}
