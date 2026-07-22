//go:build linux

package cgroups

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// userManagerUnit is the cgroup component the derivation walks to.
func userManagerUnit() string {
	return fmt.Sprintf("user@%d.service", os.Getuid())
}

// conventionalCgroup is the fallback path used when /proc/self/cgroup carries
// no user manager component.
func conventionalCgroup() string {
	return fmt.Sprintf("/user.slice/user-%d.slice/%s", os.Getuid(), userManagerUnit())
}

// withCgroupRoot points the preflight checks at dir for the duration of a test.
func withCgroupRoot(t *testing.T, dir string) {
	t.Helper()
	prev := cgroupRoot
	cgroupRoot = dir
	t.Cleanup(func() { cgroupRoot = prev })
}

// withProcCgroup writes content to a temp file and points the derivation at it.
// An empty content writes no file at all, simulating an unreadable procfs.
func withProcCgroup(t *testing.T, content string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "cgroup")
	if content != "" {
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatalf("write fake /proc/self/cgroup: %v", err)
		}
	}
	prev := procCgroupPath
	procCgroupPath = file
	t.Cleanup(func() { procCgroupPath = prev })
}

// fakeHierarchy builds a cgroup v2 tree whose user manager cgroup advertises
// the given controllers, and returns its root.
func fakeHierarchy(t *testing.T, controllers string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), []byte("cpuset cpu io memory pids\n"), 0o600); err != nil {
		t.Fatalf("write root cgroup.controllers: %v", err)
	}
	dir := filepath.Join(root, filepath.FromSlash(conventionalCgroup()))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("create fake user manager cgroup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.controllers"), []byte(controllers), 0o600); err != nil {
		t.Fatalf("write user manager cgroup.controllers: %v", err)
	}
	return root
}

func TestRequiredControllers(t *testing.T) {
	tests := []struct {
		name   string
		limits Limits
		want   []string
	}{
		{name: "no limits", limits: Limits{}, want: nil},
		{name: "memory only", limits: Limits{Memory: "4g"}, want: []string{"memory"}},
		{name: "cpus maps to cpu", limits: Limits{CPUs: "2"}, want: []string{"cpu"}},
		{name: "pids only", limits: Limits{PIDs: 64}, want: []string{"pids"}},
		{name: "zero pids is unset", limits: Limits{Memory: "1g", PIDs: 0}, want: []string{"memory"}},
		{
			name:   "all three",
			limits: Limits{Memory: "4g", CPUs: "2", PIDs: 64},
			want:   []string{"memory", "cpu", "pids"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requiredControllers(tt.limits); !slices.Equal(got, tt.want) {
				t.Errorf("requiredControllers() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUserManagerCgroup(t *testing.T) {
	unit := userManagerUnit()

	tests := []struct {
		name      string
		content   string
		want      string
		wantFound bool
	}{
		{
			name:      "walks to the user manager component",
			content:   "0::/user.slice/user-" + fmt.Sprint(os.Getuid()) + ".slice/" + unit + "/app.slice/app-foo.scope\n",
			want:      conventionalCgroup(),
			wantFound: true,
		},
		{
			name:      "already at the user manager",
			content:   "0::/user.slice/user-" + fmt.Sprint(os.Getuid()) + ".slice/" + unit + "\n",
			want:      conventionalCgroup(),
			wantFound: true,
		},
		{
			name:      "non-conventional parent slice is preserved",
			content:   "0::/custom.slice/" + unit + "/app.slice\n",
			want:      "/custom.slice/" + unit,
			wantFound: true,
		},
		{
			// The signal behind outsideUserManagerHint: the path still resolves
			// to a plausible fallback, but this process does not live under it.
			name:    "login session scope falls back and is not found",
			content: "0::/user.slice/user-1000.slice/session-5.scope\n",
			want:    conventionalCgroup(),
		},
		{
			name:    "nested cgroup namespace falls back",
			content: "0::/\n",
			want:    conventionalCgroup(),
		},
		{
			name:    "cgroup v1 layout falls back",
			content: "5:memory:/user.slice\n4:cpu,cpuacct:/user.slice\n1:name=systemd:/user.slice/session-5.scope\n",
			want:    conventionalCgroup(),
		},
		{
			name:      "hybrid layout still uses the unified line",
			content:   "1:name=systemd:/ignored\n0::/user.slice/user-" + fmt.Sprint(os.Getuid()) + ".slice/" + unit + "/app.slice\n",
			want:      conventionalCgroup(),
			wantFound: true,
		},
		{
			name:    "unreadable procfs falls back",
			content: "",
			want:    conventionalCgroup(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withProcCgroup(t, tt.content)
			got, found := userManagerCgroup()
			if got != tt.want {
				t.Errorf("userManagerCgroup() = %q, want %q", got, tt.want)
			}
			if found != tt.wantFound {
				t.Errorf("userManagerCgroup() found = %v, want %v", found, tt.wantFound)
			}
		})
	}
}

func TestDelegatedControllers(t *testing.T) {
	withProcCgroup(t, "0::/\n")
	withCgroupRoot(t, fakeHierarchy(t, "cpuset cpu memory pids\n"))

	got, err := delegatedControllers()
	if err != nil {
		t.Fatalf("delegatedControllers() unexpected error: %v", err)
	}
	if want := []string{"cpuset", "cpu", "memory", "pids"}; !slices.Equal(got, want) {
		t.Errorf("delegatedControllers() = %v, want %v", got, want)
	}
}

func TestDelegatedControllersMissingFile(t *testing.T) {
	withProcCgroup(t, "0::/\n")
	withCgroupRoot(t, t.TempDir())

	if got, err := delegatedControllers(); err == nil {
		t.Fatalf("delegatedControllers() = %v, want an error when the cgroup is absent", got)
	} else if !strings.Contains(err.Error(), "cannot read delegated cgroup controllers") {
		t.Errorf("delegatedControllers() error = %q, want it to name the unreadable file", err)
	}
}

func TestCheckDelegation(t *testing.T) {
	tests := []struct {
		name        string
		controllers string
		procCgroup  string
		root        func(t *testing.T, controllers string) string
		limits      Limits
		wantErr     string
	}{
		{
			name:        "all controllers delegated",
			controllers: "cpuset cpu io memory pids\n",
			procCgroup:  "0::/user.slice/user-" + fmt.Sprint(os.Getuid()) + ".slice/" + userManagerUnit() + "/app.slice\n",
			limits:      Limits{Memory: "4g", CPUs: "2", PIDs: 64},
		},
		{
			name:        "cpu missing while memory present",
			controllers: "memory pids\n",
			procCgroup:  "0::/user.slice/user-" + fmt.Sprint(os.Getuid()) + ".slice/" + userManagerUnit() + "\n",
			limits:      Limits{Memory: "4g", CPUs: "2"},
			wantErr:     `"cpu" cgroup controller delegated, so the configured cpus limit`,
		},
		{
			name:        "memory missing",
			controllers: "cpu pids\n",
			procCgroup:  "0::/\n",
			limits:      Limits{Memory: "4g"},
			wantErr:     `"memory" cgroup controller delegated, so the configured memory limit`,
		},
		{
			name:        "pids missing",
			controllers: "cpu memory\n",
			procCgroup:  "0::/\n",
			limits:      Limits{PIDs: 64},
			wantErr:     `"pids" cgroup controller delegated, so the configured pids limit`,
		},
		{
			name:        "undelegated controller is not reported when unused",
			controllers: "memory\n",
			procCgroup:  "0::/\n",
			limits:      Limits{Memory: "4g"},
		},
		{
			name:       "cgroup file absent",
			procCgroup: "0::/\n",
			root:       func(t *testing.T, _ string) string { t.Helper(); return t.TempDir() },
			limits:     Limits{Memory: "4g"},
			wantErr:    "cannot read delegated cgroup controllers",
		},
		{
			// A nested sandbox reports a plausible host path while nothing is
			// mounted underneath it. The derived path must fail loudly.
			name:       "nested sandbox reports a plausible but unmounted path",
			procCgroup: "0::/user.slice/user-1000.slice/session-5.scope\n",
			root:       func(t *testing.T, _ string) string { t.Helper(); return t.TempDir() },
			limits:     Limits{Memory: "4g", CPUs: "2"},
			wantErr:    "cgroup namespace or a nested sandbox",
		},
		{
			name:       "cgroup v1 layout",
			procCgroup: "5:memory:/user.slice\n1:name=systemd:/user.slice/session-5.scope\n",
			root:       func(t *testing.T, _ string) string { t.Helper(); return t.TempDir() },
			limits:     Limits{Memory: "4g"},
			wantErr:    "cannot read delegated cgroup controllers",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withProcCgroup(t, tt.procCgroup)
			root := fakeHierarchy
			if tt.root != nil {
				root = tt.root
			}
			withCgroupRoot(t, root(t, tt.controllers))

			err := checkDelegation(tt.limits)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("checkDelegation() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkDelegation() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("checkDelegation() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestCheckUnifiedHierarchy(t *testing.T) {
	t.Run("cgroup v2 present", func(t *testing.T) {
		withCgroupRoot(t, fakeHierarchy(t, "cpu memory pids\n"))
		if err := checkUnifiedHierarchy(); err != nil {
			t.Errorf("checkUnifiedHierarchy() = %v, want nil", err)
		}
	})

	t.Run("mount point absent", func(t *testing.T) {
		withCgroupRoot(t, filepath.Join(t.TempDir(), "missing"))
		err := checkUnifiedHierarchy()
		if err == nil {
			t.Fatal("checkUnifiedHierarchy() = nil, want an error when the mount point is absent")
		}
		if !strings.Contains(err.Error(), "cannot be accessed") {
			t.Errorf("checkUnifiedHierarchy() error = %q, want it to name the inaccessible mount point", err)
		}
	})

	t.Run("cgroup v1 layout", func(t *testing.T) {
		withCgroupRoot(t, t.TempDir())
		err := checkUnifiedHierarchy()
		if err == nil {
			t.Fatal("checkUnifiedHierarchy() = nil, want an error for a cgroup v1 layout")
		}
		if !strings.Contains(err.Error(), "cgroup v1") {
			t.Errorf("checkUnifiedHierarchy() error = %q, want it to name cgroup v1", err)
		}
	})
}

// Zero limits must short-circuit before any filesystem access, so an
// unconfigured sandbox is unaffected by the state of the host cgroup tree.
func TestPreflightZeroLimitsSkipsFilesystem(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	withCgroupRoot(t, missing)
	withProcCgroup(t, "")

	if err := Preflight(Limits{}); err != nil {
		t.Errorf("Preflight(Limits{}) = %v, want nil", err)
	}
}

func TestPreflightErrors(t *testing.T) {
	t.Run("untranslatable value is rejected first", func(t *testing.T) {
		withCgroupRoot(t, fakeHierarchy(t, "cpu memory pids\n"))
		withProcCgroup(t, "0::/\n")

		err := Preflight(Limits{CPUs: "0.004"})
		if err == nil {
			t.Fatal("Preflight() = nil, want an error for a cpu limit that rounds to zero")
		}
		if !strings.Contains(err.Error(), "rounds to CPUQuota=0%") {
			t.Errorf("Preflight() error = %q, want it to name the zero-rounding quota", err)
		}
	})

	t.Run("missing unified hierarchy", func(t *testing.T) {
		withCgroupRoot(t, filepath.Join(t.TempDir(), "absent"))
		withProcCgroup(t, "0::/user.slice/user-1000.slice/session-5.scope\n")

		err := Preflight(Limits{Memory: "4g"})
		if err == nil {
			t.Fatal("Preflight() = nil, want an error when no cgroup v2 hierarchy is mounted")
		}
		if !strings.Contains(err.Error(), "cgroup v2 unified hierarchy") {
			t.Errorf("Preflight() error = %q, want it to name the missing cgroup v2 hierarchy", err)
		}
	})

	t.Run("systemd-run not on PATH", func(t *testing.T) {
		withCgroupRoot(t, fakeHierarchy(t, "cpu memory pids\n"))
		withProcCgroup(t, "0::/\n")
		t.Setenv("PATH", t.TempDir())

		err := Preflight(Limits{Memory: "4g"})
		if err == nil {
			t.Fatal("Preflight() = nil, want an error when systemd-run is unavailable")
		}
		if !strings.Contains(err.Error(), "systemd-run") {
			t.Errorf("Preflight() error = %q, want it to name systemd-run", err)
		}
	})

	t.Run("user manager unreachable", func(t *testing.T) {
		withCgroupRoot(t, fakeHierarchy(t, "cpu memory pids\n"))
		withProcCgroup(t, "0::/\n")
		stubPath(t, "systemd-run")
		t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

		err := Preflight(Limits{Memory: "4g"})
		if err == nil {
			t.Fatal("Preflight() = nil, want an error when the systemd user manager is offline")
		}
		if !strings.Contains(err.Error(), "systemd user manager") {
			t.Errorf("Preflight() error = %q, want it to name the systemd user manager", err)
		}
	})

	// A downed user manager leaves its control socket file behind. Preflight must
	// blame the manager, not fall through to the opaque delegation read that a
	// bare stat check allowed. The cgroup root here has no user manager subtree,
	// so if the delegation step were reached it would fail with "cannot read
	// delegated" - which the assertion below forbids.
	t.Run("stale control socket reports the manager, not delegation", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), []byte("cpu memory pids\n"), 0o600); err != nil {
			t.Fatalf("write root cgroup.controllers: %v", err)
		}
		withCgroupRoot(t, root)
		withProcCgroup(t, "0::/user.slice/user-1000.slice/session-5.scope\n")
		stubPath(t, "systemd-run")
		t.Setenv("XDG_RUNTIME_DIR", staleUserManagerSocket(t))

		err := Preflight(Limits{Memory: "4g"})
		if err == nil {
			t.Fatal("Preflight() = nil, want an error when the user manager is down")
		}
		if !strings.Contains(err.Error(), "systemd user manager") {
			t.Errorf("Preflight() error = %q, want it to name the systemd user manager", err)
		}
		if strings.Contains(err.Error(), "cannot read delegated") {
			t.Errorf("Preflight() error = %q, misdiagnosed a downed manager as a delegation read failure", err)
		}
	})

	t.Run("controller not delegated", func(t *testing.T) {
		withCgroupRoot(t, fakeHierarchy(t, "memory pids\n"))
		withProcCgroup(t, "0::/\n")
		stubPath(t, "systemd-run")
		t.Setenv("XDG_RUNTIME_DIR", fakeUserManagerSocket(t))

		err := Preflight(Limits{CPUs: "2"})
		if err == nil {
			t.Fatal("Preflight() = nil, want an error when the cpu controller is not delegated")
		}
		if !strings.Contains(err.Error(), `"cpu" cgroup controller`) {
			t.Errorf("Preflight() error = %q, want it to name the undelegated cpu controller", err)
		}
	})

	// Every static gate can pass while the user manager still refuses the scope
	// at D-Bus time. That refusal is exit 1 from systemd-run, which is
	// indistinguishable from the workload's own most common exit status once the
	// sandbox is running, so it has to abort here instead.
	t.Run("user manager refuses the scope", func(t *testing.T) {
		withCgroupRoot(t, fakeHierarchy(t, "cpu memory pids\n"))
		withProcCgroup(t, "0::/\n")
		stubPath(t, "systemd-run")
		t.Setenv("XDG_RUNTIME_DIR", fakeUserManagerSocket(t))
		withProbe(t, func([]string) error {
			return fmt.Errorf("the systemd user manager refused a transient scope carrying the configured limits (MemoryMax=4G): Failed to start transient scope unit")
		})

		err := Preflight(Limits{Memory: "4g"})
		if err == nil {
			t.Fatal("Preflight() = nil, want an error when the scope is refused")
		}
		if !strings.Contains(err.Error(), "refused a transient scope") {
			t.Errorf("Preflight() error = %q, want it to report the refused scope", err)
		}
	})

	t.Run("all requirements met", func(t *testing.T) {
		withCgroupRoot(t, fakeHierarchy(t, "cpu memory pids\n"))
		withProcCgroup(t, "0::/\n")
		stubPath(t, "systemd-run")
		t.Setenv("XDG_RUNTIME_DIR", fakeUserManagerSocket(t))
		withProbe(t, func([]string) error { return nil })

		if err := Preflight(Limits{Memory: "4g", CPUs: "2", PIDs: 64}); err != nil {
			t.Errorf("Preflight() = %v, want nil when every requirement is met", err)
		}
	})
}

// withProbe replaces the live scope probe for the duration of a test, so
// Preflight can be exercised without a systemd user bus.
func withProbe(t *testing.T, fn func([]string) error) {
	t.Helper()
	prev := probeScope
	probeScope = fn
	t.Cleanup(func() { probeScope = prev })
}

// The probe must receive exactly the properties the real launch would carry -
// probing a different set would validate something the sandbox never runs with.
func TestPreflightProbesTheConfiguredProperties(t *testing.T) {
	withCgroupRoot(t, fakeHierarchy(t, "cpu memory pids\n"))
	withProcCgroup(t, "0::/\n")
	stubPath(t, "systemd-run")
	t.Setenv("XDG_RUNTIME_DIR", fakeUserManagerSocket(t))

	var got []string
	withProbe(t, func(props []string) error {
		got = props
		return nil
	})

	if err := Preflight(Limits{Memory: "4g", CPUs: "2", PIDs: 64}); err != nil {
		t.Fatalf("Preflight() = %v, want nil", err)
	}
	want := []string{"MemoryMax=4G", "CPUQuota=200%", "TasksMax=64"}
	if !slices.Equal(got, want) {
		t.Errorf("probed properties = %v, want %v", got, want)
	}
}

// Zero limits must not reach the probe at all: an unconfigured sandbox pays no
// D-Bus round trip and works on hosts with no user manager.
func TestPreflightZeroLimitsSkipsProbe(t *testing.T) {
	withProbe(t, func([]string) error {
		t.Error("probeScope must not run for zero limits")
		return nil
	})
	if err := Preflight(Limits{}); err != nil {
		t.Errorf("Preflight(Limits{}) = %v, want nil", err)
	}
}

// The probe reports systemd's own message rather than a bare exit status, so
// the user learns which property was rejected.
func TestRunScopeProbeReportsTheRefusal(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'Failed to start transient scope unit: Invalid argument' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "systemd-run"), []byte(script), 0o700); err != nil {
		t.Fatalf("write systemd-run stub: %v", err)
	}
	t.Setenv("PATH", dir)

	err := runScopeProbe([]string{"MemoryMax=4G"})
	if err == nil {
		t.Fatal("runScopeProbe() = nil, want an error when systemd-run fails")
	}
	if !strings.Contains(err.Error(), "Failed to start transient scope unit") {
		t.Errorf("runScopeProbe() error = %q, want systemd's own message", err)
	}
	if !strings.Contains(err.Error(), "MemoryMax=4G") {
		t.Errorf("runScopeProbe() error = %q, want it to name the rejected properties", err)
	}
}

// recordingSystemdRun installs a systemd-run stub that records its own argv, one
// argument per line, and exits with code. It returns a reader for that argv.
//
// The stub never runs the payload, so exactly one invocation is recorded.
func recordingSystemdRun(t *testing.T, code int, body string) func() []string {
	t.Helper()
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "recorded-argv")
	script := fmt.Sprintf("#!/bin/sh\nfor a in \"$@\"; do printf '%%s\\n' \"$a\" >> %q; done\n%s\nexit %d\n",
		argvFile, body, code)
	if err := os.WriteFile(filepath.Join(dir, "systemd-run"), []byte(script), 0o700); err != nil {
		t.Fatalf("write systemd-run stub: %v", err)
	}
	t.Setenv("PATH", dir)

	return func() []string {
		t.Helper()
		data, err := os.ReadFile(argvFile)
		if err != nil {
			t.Fatalf("read recorded argv: %v", err)
		}
		return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
}

// propValues returns the value of every -p flag in argv, in order.
func propValues(argv []string) []string {
	var out []string
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-p" {
			out = append(out, argv[i+1])
		}
	}
	return out
}

// The probe's own argv is what decides whether the guard means anything, and no
// amount of stubbing probeScope can see it. A probe that dropped -p would create
// an unconstrained scope and therefore succeed on any host with a user manager,
// leaving the refusal guard a permanent no-op while looking healthy; a probe
// that dropped --user would talk to the system manager and invert the result.
func TestRunScopeProbeArgv(t *testing.T) {
	recorded := recordingSystemdRun(t, 0, "")
	props := []string{"MemoryMax=4G", "CPUQuota=200%", "TasksMax=64"}

	if err := runScopeProbe(props); err != nil {
		t.Fatalf("runScopeProbe() = %v, want nil", err)
	}

	argv := recorded()
	for _, want := range []string{"--user", "--scope"} {
		if !slices.Contains(argv, want) {
			t.Errorf("probe argv = %v, want it to contain %q", argv, want)
		}
	}
	if got := propValues(argv); !slices.Equal(got, props) {
		t.Errorf("probe argv properties = %v, want %v", got, props)
	}
	sep := slices.Index(argv, "--")
	if sep < 0 || sep+1 >= len(argv) {
		t.Fatalf("probe argv = %v, want a -- separator before the payload", argv)
	}
	if !strings.HasSuffix(argv[sep+1], "systemd-run") {
		t.Errorf("probe payload = %q, want the systemd-run binary", argv[sep+1])
	}
}

// Probing one property set while launching with another would validate
// something the sandbox never runs with, so the two argvs are compared directly
// rather than each being asserted against its own hand-written expectation.
func TestRunScopeProbeCarriesTheLaunchProperties(t *testing.T) {
	recorded := recordingSystemdRun(t, 0, "")
	limits := Limits{Memory: "4g", CPUs: "2", PIDs: 64}

	props, err := limits.properties()
	if err != nil {
		t.Fatalf("properties() unexpected error: %v", err)
	}
	if err := runScopeProbe(props); err != nil {
		t.Fatalf("runScopeProbe() = %v, want nil", err)
	}
	_, launchArgs, err := Wrap(limits, "/usr/bin/bwrap", nil)
	if err != nil {
		t.Fatalf("Wrap() unexpected error: %v", err)
	}

	got, want := propValues(recorded()), propValues(launchArgs)
	if !slices.Equal(got, want) {
		t.Errorf("probe properties = %v, want the launch properties %v", got, want)
	}
	if len(want) == 0 {
		t.Fatal("the launch carried no properties, so this comparison proves nothing")
	}
}

// The payload runs under the configured limits, so a valid but small memory
// value OOM-kills it. systemd execs the payload only after the scope exists, so
// that death still answers the probe's question: the properties were accepted.
func TestRunScopeProbeTreatsPayloadDeathAsAccepted(t *testing.T) {
	recordingSystemdRun(t, 0, "kill -9 $$")

	if err := runScopeProbe([]string{"MemoryMax=4M"}); err != nil {
		t.Errorf("runScopeProbe() = %v, want nil when only the payload was killed", err)
	}
}

// A hung user manager is a different diagnosis from a rejected property, and it
// reaches the error path as the same SIGKILL a killed payload does.
func TestRunScopeProbeReportsTimeout(t *testing.T) {
	recordingSystemdRun(t, 0, "while :; do :; done")
	prev := scopeProbeTimeout
	scopeProbeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { scopeProbeTimeout = prev })

	err := runScopeProbe([]string{"MemoryMax=4G"})
	if err == nil {
		t.Fatal("runScopeProbe() = nil, want an error when the manager does not answer")
	}
	if !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("runScopeProbe() error = %q, want it to report a timeout rather than a refusal", err)
	}
	if strings.Contains(err.Error(), "refused") {
		t.Errorf("runScopeProbe() error = %q, want it not to claim a refusal", err)
	}
}

// A session outside the user manager's own subtree cannot be migrated into a
// scope, and systemd reports only "Permission denied". The refusal must name the
// cause, because the static gates deliberately do not reject this case outright.
func TestRunScopeProbeExplainsSessionOutsideUserManager(t *testing.T) {
	t.Run("outside the user manager", func(t *testing.T) {
		recordingSystemdRun(t, 1, "echo 'Failed to attach: Permission denied' >&2")
		withProcCgroup(t, "0::/user.slice/user-"+fmt.Sprint(os.Getuid())+".slice/session-5.scope\n")

		err := runScopeProbe([]string{"MemoryMax=4G"})
		if err == nil {
			t.Fatal("runScopeProbe() = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "not running under the systemd user manager") {
			t.Errorf("runScopeProbe() error = %q, want it to name the session as the cause", err)
		}
	})

	t.Run("inside the user manager", func(t *testing.T) {
		recordingSystemdRun(t, 1, "echo 'Invalid argument' >&2")
		withProcCgroup(t, "0::/user.slice/user-"+fmt.Sprint(os.Getuid())+".slice/"+userManagerUnit()+"/app.slice\n")

		err := runScopeProbe([]string{"MemoryMax=4G"})
		if err == nil {
			t.Fatal("runScopeProbe() = nil, want a refusal")
		}
		if strings.Contains(err.Error(), "not running under the systemd user manager") {
			t.Errorf("runScopeProbe() error = %q, want no session hint when the session is fine", err)
		}
	})
}

func TestRunScopeProbeSucceeds(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "systemd-run"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write systemd-run stub: %v", err)
	}
	t.Setenv("PATH", dir)

	if err := runScopeProbe([]string{"MemoryMax=4G"}); err != nil {
		t.Errorf("runScopeProbe() = %v, want nil when the scope is accepted", err)
	}
}

func TestRunScopeProbeRequiresSystemdRun(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := runScopeProbe([]string{"MemoryMax=4G"})
	if err == nil {
		t.Fatal("runScopeProbe() = nil, want an error when systemd-run cannot be resolved")
	}
	if !strings.Contains(err.Error(), "systemd-run") {
		t.Errorf("runScopeProbe() error = %q, want it to name systemd-run", err)
	}
}

// stubPath puts an executable stub of name on an otherwise empty PATH.
func stubPath(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write %s stub: %v", name, err)
	}
	t.Setenv("PATH", dir)
}

// fakeUserManagerSocket returns an XDG_RUNTIME_DIR whose systemd control socket
// is backed by a real listener, so the reachability dial in checkUserManager
// connects. The listener is closed when the test ends.
func fakeUserManagerSocket(t *testing.T) string {
	t.Helper()
	dir := shortRuntimeDir(t)
	ln, err := net.Listen("unix", filepath.Join(dir, "systemd", "private"))
	if err != nil {
		t.Fatalf("listen on fake systemd control socket: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return dir
}

// staleUserManagerSocket returns an XDG_RUNTIME_DIR whose systemd control socket
// is a leftover file with nothing listening, reproducing a user manager that has
// exited without removing its socket.
func staleUserManagerSocket(t *testing.T) string {
	t.Helper()
	dir := shortRuntimeDir(t)
	if err := os.WriteFile(filepath.Join(dir, "systemd", "private"), nil, 0o600); err != nil {
		t.Fatalf("write stale systemd control socket: %v", err)
	}
	return dir
}

// shortRuntimeDir builds an XDG_RUNTIME_DIR with a systemd subdir under a short
// base path. t.TempDir encodes the full test name into its path, which can push
// the AF_UNIX socket path past the sun_path length limit; a short mkdtemp base
// avoids that. The directory is removed when the test ends.
func shortRuntimeDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ds")
	if err != nil {
		t.Fatalf("create fake systemd runtime dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.MkdirAll(filepath.Join(dir, "systemd"), 0o750); err != nil {
		t.Fatalf("create fake systemd runtime dir: %v", err)
	}
	return dir
}
