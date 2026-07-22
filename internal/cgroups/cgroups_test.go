// internal/cgroups/cgroups_test.go
package cgroups

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// stubSystemdRun pins the resolved systemd-run path so argv assertions do not
// depend on systemd being installed on the machine running the tests.
func stubSystemdRun(t *testing.T, path string) {
	t.Helper()
	orig := resolveSystemdRun
	resolveSystemdRun = func() (string, error) { return path, nil }
	t.Cleanup(func() { resolveSystemdRun = orig })
}

func TestLimits_IsZero(t *testing.T) {
	tests := []struct {
		name   string
		limits Limits
		want   bool
	}{
		{name: "all unset", limits: Limits{}, want: true},
		{name: "memory set", limits: Limits{Memory: "4g"}, want: false},
		{name: "cpus set", limits: Limits{CPUs: "2"}, want: false},
		{name: "pids set", limits: Limits{PIDs: 64}, want: false},
		{name: "all set", limits: Limits{Memory: "4g", CPUs: "2", PIDs: 64}, want: false},
		{name: "negative pids", limits: Limits{PIDs: -1}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.limits.IsZero(); got != tt.want {
				t.Errorf("IsZero() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLimits_Properties(t *testing.T) {
	tests := []struct {
		name   string
		limits Limits
		want   []string
	}{
		{name: "no limits", limits: Limits{}, want: nil},
		{name: "memory lowercase suffix", limits: Limits{Memory: "4g"}, want: []string{"MemoryMax=4G"}},
		{name: "memory megabytes", limits: Limits{Memory: "512m"}, want: []string{"MemoryMax=512M"}},
		{name: "memory already uppercase", limits: Limits{Memory: "2G"}, want: []string{"MemoryMax=2G"}},
		{name: "memory bare integer is bytes", limits: Limits{Memory: "1024"}, want: []string{"MemoryMax=1024"}},
		{name: "memory byte suffix", limits: Limits{Memory: "4096b"}, want: []string{"MemoryMax=4096B"}},
		{name: "cpus whole", limits: Limits{CPUs: "2"}, want: []string{"CPUQuota=200%"}},
		{name: "cpus half", limits: Limits{CPUs: "0.5"}, want: []string{"CPUQuota=50%"}},
		{name: "cpus fractional", limits: Limits{CPUs: "1.5"}, want: []string{"CPUQuota=150%"}},
		{name: "cpus smallest representable", limits: Limits{CPUs: "0.01"}, want: []string{"CPUQuota=1%"}},
		{name: "cpus rounds up", limits: Limits{CPUs: "0.006"}, want: []string{"CPUQuota=1%"}},
		{name: "pids only", limits: Limits{PIDs: 2048}, want: []string{"TasksMax=2048"}},
		{
			name:   "all three in stable order",
			limits: Limits{Memory: "4g", CPUs: "2", PIDs: 2048},
			want:   []string{"MemoryMax=4G", "CPUQuota=200%", "TasksMax=2048"},
		},
		{
			name:   "memory and pids skips cpu",
			limits: Limits{Memory: "512m", PIDs: 64},
			want:   []string{"MemoryMax=512M", "TasksMax=64"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.limits.properties()
			if err != nil {
				t.Fatalf("properties() unexpected error: %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("properties() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLimits_PropertiesErrors(t *testing.T) {
	tests := []struct {
		name    string
		limits  Limits
		wantErr string
	}{
		{name: "unparseable memory", limits: Limits{Memory: "lots"}, wantErr: "invalid memory limit"},
		{name: "memory wrong suffix", limits: Limits{Memory: "4gb"}, wantErr: "invalid memory limit"},
		{name: "memory negative", limits: Limits{Memory: "-1g"}, wantErr: "invalid memory limit"},
		{name: "memory empty suffix only", limits: Limits{Memory: "g"}, wantErr: "invalid memory limit"},
		{name: "memory zero", limits: Limits{Memory: "0"}, wantErr: "MemoryMax=0 would leave the sandbox no memory"},
		{name: "memory zero megabytes", limits: Limits{Memory: "0m"}, wantErr: "MemoryMax=0 would leave the sandbox no memory"},
		{name: "memory zero gigabytes", limits: Limits{Memory: "0g"}, wantErr: "MemoryMax=0 would leave the sandbox no memory"},
		{name: "memory padded zero", limits: Limits{Memory: "00"}, wantErr: "MemoryMax=0 would leave the sandbox no memory"},
		{name: "non-numeric cpus", limits: Limits{CPUs: "two"}, wantErr: "invalid cpu limit"},
		{name: "zero cpus", limits: Limits{CPUs: "0"}, wantErr: "invalid cpu limit"},
		{name: "negative cpus", limits: Limits{CPUs: "-1"}, wantErr: "invalid cpu limit"},
		{name: "nan cpus", limits: Limits{CPUs: "NaN"}, wantErr: "invalid cpu limit"},
		{name: "inf cpus", limits: Limits{CPUs: "Inf"}, wantErr: "invalid cpu limit"},
		{name: "absurd cpus", limits: Limits{CPUs: "1e9"}, wantErr: "invalid cpu limit"},
		{name: "negative pids", limits: Limits{PIDs: -1}, wantErr: "invalid pids limit"},
		{name: "cpus rounds to zero", limits: Limits{CPUs: "0.004"}, wantErr: "rounds to CPUQuota=0%"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.limits.properties()
			if err == nil {
				t.Fatalf("properties() = %v, want error containing %q", got, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("properties() error = %q, want it to contain %q", err, tt.wantErr)
			}
			if got != nil {
				t.Errorf("properties() = %v, want nil alongside the error", got)
			}
		})
	}
}

// A cpus value that rounds to zero must be rejected rather than emitted as
// CPUQuota=0%, which produces a sandbox that never schedules.
func TestLimits_PropertiesRejectsZeroRoundedCPUQuota(t *testing.T) {
	for _, cpus := range []string{"0.004", "0.001", "0.0049"} {
		t.Run(cpus, func(t *testing.T) {
			got, err := Limits{CPUs: cpus}.properties()
			if err == nil {
				t.Fatalf("properties() = %v, want an error for a cpu limit that rounds to 0%%", got)
			}
			for _, p := range got {
				if strings.Contains(p, "CPUQuota=0%") {
					t.Errorf("properties() emitted %q", p)
				}
			}
		})
	}
}

func TestWrap(t *testing.T) {
	const systemdRun = "/usr/bin/systemd-run"
	unit := "--unit=" + unitName()

	tests := []struct {
		name     string
		limits   Limits
		program  string
		args     []string
		wantProg string
		wantArgs []string
	}{
		{
			name:     "all three limits",
			limits:   Limits{Memory: "4g", CPUs: "2", PIDs: 2048},
			program:  "/usr/bin/bwrap",
			args:     []string{"--unshare-pid", "--", "bash"},
			wantProg: systemdRun,
			wantArgs: []string{
				"--user", "--scope", "--quiet", "--collect",
				unit, "--description=devsandbox sandbox",
				"-p", "MemoryMax=4G",
				"-p", "CPUQuota=200%",
				"-p", "TasksMax=2048",
				"--", "/usr/bin/bwrap", "--unshare-pid", "--", "bash",
			},
		},
		{
			name:     "memory only",
			limits:   Limits{Memory: "512m"},
			program:  "/usr/bin/pasta",
			args:     []string{"--config-net"},
			wantProg: systemdRun,
			wantArgs: []string{
				"--user", "--scope", "--quiet", "--collect",
				unit, "--description=devsandbox sandbox",
				"-p", "MemoryMax=512M",
				"--", "/usr/bin/pasta", "--config-net",
			},
		},
		{
			name:     "no args",
			limits:   Limits{PIDs: 64},
			program:  "/usr/bin/bwrap",
			args:     nil,
			wantProg: systemdRun,
			wantArgs: []string{
				"--user", "--scope", "--quiet", "--collect",
				unit, "--description=devsandbox sandbox",
				"-p", "TasksMax=64",
				"--", "/usr/bin/bwrap",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubSystemdRun(t, systemdRun)

			gotProg, gotArgs, err := Wrap(tt.limits, tt.program, tt.args)
			if err != nil {
				t.Fatalf("Wrap() unexpected error: %v", err)
			}
			if gotProg != tt.wantProg {
				t.Errorf("Wrap() program = %q, want %q", gotProg, tt.wantProg)
			}
			if !slices.Equal(gotArgs, tt.wantArgs) {
				t.Errorf("Wrap() args = %v, want %v", gotArgs, tt.wantArgs)
			}
		})
	}
}

// Zero limits must leave the launch byte-for-byte identical, which is what
// makes the feature opt-in.
func TestWrap_ZeroLimitsUnchanged(t *testing.T) {
	stubSystemdRun(t, "/usr/bin/systemd-run")

	args := []string{"--unshare-pid", "--", "bash"}
	gotProg, gotArgs, err := Wrap(Limits{}, "/usr/bin/bwrap", args)
	if err != nil {
		t.Fatalf("Wrap() unexpected error: %v", err)
	}
	if gotProg != "/usr/bin/bwrap" {
		t.Errorf("Wrap() program = %q, want it unchanged", gotProg)
	}
	if !slices.Equal(gotArgs, args) {
		t.Errorf("Wrap() args = %v, want them unchanged (%v)", gotArgs, args)
	}
}

func TestWrap_PropertiesAppearExactlyOnce(t *testing.T) {
	stubSystemdRun(t, "/usr/bin/systemd-run")

	_, args, err := Wrap(Limits{Memory: "4g", CPUs: "0.5", PIDs: 128}, "/usr/bin/bwrap", []string{"bash"})
	if err != nil {
		t.Fatalf("Wrap() unexpected error: %v", err)
	}

	exact := map[string]int{"MemoryMax=4G": 1, "CPUQuota=50%": 1, "TasksMax=128": 1, "-p": 3}
	for want, wantN := range exact {
		var n int
		for _, a := range args {
			if a == want {
				n++
			}
		}
		if n != wantN {
			t.Errorf("Wrap() args contain %q %d times, want %d: %v", want, n, wantN, args)
		}
	}

	for _, prefix := range []string{"--unit=", "--description="} {
		var n int
		for _, a := range args {
			if strings.HasPrefix(a, prefix) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("Wrap() args contain %d %q flags, want 1: %v", n, prefix, args)
		}
	}
}

// The separator must terminate systemd-run's own option parsing immediately
// before the wrapped program, or a bwrap flag would be read as a systemd-run one.
func TestWrap_SeparatorPrecedesProgram(t *testing.T) {
	stubSystemdRun(t, "/usr/bin/systemd-run")

	_, args, err := Wrap(Limits{Memory: "1g"}, "/usr/bin/bwrap", []string{"--unshare-pid"})
	if err != nil {
		t.Fatalf("Wrap() unexpected error: %v", err)
	}

	i := slices.Index(args, "--")
	if i < 0 {
		t.Fatalf("Wrap() args = %v, want them to contain a %q separator", args, "--")
	}
	if i+1 >= len(args) || args[i+1] != "/usr/bin/bwrap" {
		t.Errorf("Wrap() args = %v, want %q immediately after %q", args, "/usr/bin/bwrap", "--")
	}
}

// systemd defaults Description= to the full command line, which for bwrap lists
// every host path bound into the sandbox.
func TestWrap_DescriptionAlwaysSet(t *testing.T) {
	stubSystemdRun(t, "/usr/bin/systemd-run")

	limitSets := []Limits{
		{Memory: "4g"},
		{CPUs: "2"},
		{PIDs: 64},
		{Memory: "512m", CPUs: "0.5", PIDs: 2048},
	}

	for _, l := range limitSets {
		t.Run(fmt.Sprintf("%s/%s/%d", l.Memory, l.CPUs, l.PIDs), func(t *testing.T) {
			_, args, err := Wrap(l, "/usr/bin/bwrap", []string{"bash"})
			if err != nil {
				t.Fatalf("Wrap() unexpected error: %v", err)
			}
			var found bool
			for _, a := range args {
				if strings.HasPrefix(a, "--description=") {
					found = true
					if a == "--description=" {
						t.Errorf("Wrap() emitted an empty description")
					}
					if strings.Contains(a, "/usr/bin/bwrap") {
						t.Errorf("Wrap() description leaks the command line: %q", a)
					}
				}
			}
			if !found {
				t.Errorf("Wrap() args = %v, want a --description flag", args)
			}
		})
	}
}

func TestWrap_Errors(t *testing.T) {
	t.Run("untranslatable limit", func(t *testing.T) {
		stubSystemdRun(t, "/usr/bin/systemd-run")

		prog, args, err := Wrap(Limits{CPUs: "0.004"}, "/usr/bin/bwrap", nil)
		if err == nil {
			t.Fatalf("Wrap() = %q %v, want an error", prog, args)
		}
		if !strings.Contains(err.Error(), "rounds to CPUQuota=0%") {
			t.Errorf("Wrap() error = %q, want the translation error", err)
		}
	})

	t.Run("systemd-run unresolvable", func(t *testing.T) {
		orig := resolveSystemdRun
		resolveSystemdRun = func() (string, error) { return "", errors.New("executable file not found in $PATH") }
		t.Cleanup(func() { resolveSystemdRun = orig })

		prog, args, err := Wrap(Limits{Memory: "4g"}, "/usr/bin/bwrap", nil)
		if err == nil {
			t.Fatalf("Wrap() = %q %v, want an error when systemd-run cannot be resolved", prog, args)
		}
		if !strings.Contains(err.Error(), "systemd-run") {
			t.Errorf("Wrap() error = %q, want it to name systemd-run", err)
		}
		if prog != "" || args != nil {
			t.Errorf("Wrap() = %q %v, want empty results alongside the error", prog, args)
		}
	})

	t.Run("zero limits do not resolve systemd-run", func(t *testing.T) {
		orig := resolveSystemdRun
		resolveSystemdRun = func() (string, error) {
			t.Error("Wrap() resolved systemd-run for zero limits")
			return "", errors.New("unreachable")
		}
		t.Cleanup(func() { resolveSystemdRun = orig })

		if _, _, err := Wrap(Limits{}, "/usr/bin/bwrap", nil); err != nil {
			t.Fatalf("Wrap() unexpected error: %v", err)
		}
	})
}

func TestPreflight_ZeroLimits(t *testing.T) {
	if err := Preflight(Limits{}); err != nil {
		t.Errorf("Preflight(Limits{}) = %v, want nil", err)
	}
}
