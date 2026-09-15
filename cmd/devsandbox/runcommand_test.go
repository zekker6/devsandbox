package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/spf13/cobra"

	"devsandbox/internal/isolator"
)

// stubCommandSandbox replaces the sandbox runner for the duration of a test and
// records every invocation it receives.
func stubCommandSandbox(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	orig := runCommandSandbox
	runCommandSandbox = func(_ *cobra.Command, args []string) error {
		calls = append(calls, args)
		return nil
	}
	t.Cleanup(func() { runCommandSandbox = orig })
	return &calls
}

// unsetEnv removes key for the duration of a test. t.Setenv first registers the
// restore of the original value.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
}

func executeRunCommand(t *testing.T, argv ...string) (stderr string, err error) {
	t.Helper()
	cmd := newRunCommandCmd()
	var errBuf bytes.Buffer
	cmd.SetArgs(argv)
	cmd.SetOut(io.Discard)
	cmd.SetErr(&errBuf)
	err = cmd.Execute()
	return errBuf.String(), err
}

func TestPlanCommandInvocationRequiresValidName(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{""},
		{"./node_modules/.bin/npm"},
		{"/usr/bin/npm"},
		{"-rf"},
		{"npm;id"},
		{"npm-no-ds"},
		{"devsandbox"},
		{"claude"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			plan, err := planCommandInvocation(args, "", fakeLookPath(map[string]string{"npm": "/usr/bin/npm"}))
			if err == nil {
				t.Fatalf("expected an error for %#v, got plan %+v", args, plan)
			}
			if plan.Path != "" || plan.Argv != nil {
				t.Errorf("a rejected name must produce no invocation, got %+v", plan)
			}
		})
	}
}

func TestPlanCommandInvocationMissingNameIsActionable(t *testing.T) {
	_, err := planCommandInvocation(nil, "", fakeLookPath(nil))
	if err == nil || !strings.Contains(err.Error(), "requires a command name") {
		t.Fatalf("err = %v, want it to state the missing argument", err)
	}
}

func TestPlanCommandInvocationOutsideSandboxRoutesThroughRunner(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "no args", args: []string{"npm"}},
		{name: "flags", args: []string{"npm", "install", "--save-dev", "-g"}},
		{name: "joined flag", args: []string{"bun", "--cwd=/tmp/x"}},
		{name: "spaces and metacharacters", args: []string{"node", "-e", "console.log('a b'); $(id)"}},
		{name: "empty argument", args: []string{"npm", "", "run"}},
		{name: "double dash", args: []string{"npm", "--", "--proxy"}},
		{name: "management name config", args: []string{"config", "show"}},
		{name: "management name tools", args: []string{"tools", "list"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A host binary of the same name must not be consulted: the command is
			// resolved inside the sandbox, where PATH can differ.
			plan, err := planCommandInvocation(tt.args, "",
				fakeLookPath(map[string]string{tt.args[0]: "/usr/bin/" + tt.args[0]}))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if plan.Direct {
				t.Fatalf("outside a sandbox the command must go through the sandbox runner, got %+v", plan)
			}
			if plan.Path != "" {
				t.Errorf("path = %q, want no host resolution", plan.Path)
			}
			if !reflect.DeepEqual(plan.Argv, tt.args) {
				t.Errorf("argv = %#v, want %#v", plan.Argv, tt.args)
			}
		})
	}
}

func TestPlanCommandInvocationInsideSandboxExecsRealCommand(t *testing.T) {
	for _, marker := range []string{"1", "yes", " "} {
		t.Run(marker, func(t *testing.T) {
			args := []string{"npm", "install", "--ignore-scripts", "a b"}
			plan, err := planCommandInvocation(args, marker,
				fakeLookPath(map[string]string{"npm": "/opt/node/bin/npm"}))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !plan.Direct {
				t.Fatalf("inside a sandbox the command must run directly, got %+v", plan)
			}
			if plan.Path != "/opt/node/bin/npm" {
				t.Errorf("path = %q, want /opt/node/bin/npm", plan.Path)
			}
			if !reflect.DeepEqual(plan.Argv, args) {
				t.Errorf("argv = %#v, want %#v", plan.Argv, args)
			}
		})
	}
}

func TestPlanCommandInvocationInsideSandboxMissingCommand(t *testing.T) {
	_, err := planCommandInvocation([]string{"npm"}, "1", fakeLookPath(nil))
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("err = %v, want it to wrap exec.ErrNotFound", err)
	}
}

// The DEVSANDBOX marker is read with non-empty semantics, like the generated
// shell guard: unset and empty both mean "outside a sandbox".
func TestRunCommandCmdSandboxMarker(t *testing.T) {
	t.Run("unset routes through the runner", func(t *testing.T) {
		unsetEnv(t, "DEVSANDBOX")
		calls := stubCommandSandbox(t)
		if _, err := executeRunCommand(t, "npm", "install"); err != nil {
			t.Fatalf("execute: %v", err)
		}
		if want := [][]string{{"npm", "install"}}; !reflect.DeepEqual(*calls, want) {
			t.Errorf("runner calls = %#v, want %#v", *calls, want)
		}
	})

	t.Run("empty routes through the runner", func(t *testing.T) {
		t.Setenv("DEVSANDBOX", "")
		calls := stubCommandSandbox(t)
		if _, err := executeRunCommand(t, "npm", "install"); err != nil {
			t.Fatalf("execute: %v", err)
		}
		if want := [][]string{{"npm", "install"}}; !reflect.DeepEqual(*calls, want) {
			t.Errorf("runner calls = %#v, want %#v", *calls, want)
		}
	})

	t.Run("non-empty resolves directly", func(t *testing.T) {
		t.Setenv("DEVSANDBOX", "1")
		t.Setenv("PATH", t.TempDir())
		calls := stubCommandSandbox(t)
		stderr, err := executeRunCommand(t, "ds-missing-command-7f3a")
		if cmdExit, ok := errors.AsType[*isolator.CommandExitError](err); !ok || cmdExit.Code != 127 {
			t.Fatalf("err = %v, want a CommandExitError with code 127", err)
		}
		if len(*calls) != 0 {
			t.Errorf("the sandbox runner must not run inside a sandbox, got %#v", *calls)
		}
		if n := strings.Count(stderr, "ds-missing-command-7f3a"); n != 1 {
			t.Errorf("stderr names the command %d times, want once: %q", n, stderr)
		}
		if !strings.Contains(stderr, "command not found") {
			t.Errorf("stderr = %q, want a command-not-found diagnostic", stderr)
		}
	})
}

// Workload flags follow the command name and belong to the workload; cobra must
// neither claim them nor answer --help itself.
func TestRunCommandCmdPassesWorkloadFlagsThrough(t *testing.T) {
	tests := [][]string{
		{"npm", "--proxy"},
		{"npm", "--help"},
		{"npm", "-h"},
		{"npm", "--version"},
		{"npm", "--rm", "install"},
		{"npm", "--not-a-devsandbox-flag"},
		{"npm", "--", "--proxy"},
		{"node", "-e", "process.exit(3)"},
	}

	for _, argv := range tests {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			t.Setenv("DEVSANDBOX", "")
			cmd := newRunCommandCmd()
			var got []string
			var gotCmd *cobra.Command
			orig := runCommandSandbox
			runCommandSandbox = func(c *cobra.Command, args []string) error {
				got, gotCmd = args, c
				return nil
			}
			t.Cleanup(func() { runCommandSandbox = orig })

			cmd.SetArgs(argv)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !reflect.DeepEqual(got, argv) {
				t.Errorf("args = %#v, want %#v", got, argv)
			}
			if gotCmd != cmd {
				t.Fatal("the runner must receive the run-command command so it reads its flags")
			}
			if n := cmd.Flags().NFlag(); n != 0 {
				t.Errorf("%d sandbox flags were parsed from workload arguments", n)
			}
		})
	}
}

// Sandbox flags ahead of the command name are devsandbox's, and the runner reads
// them from the command it is handed.
func TestRunCommandCmdParsesLeadingSandboxFlags(t *testing.T) {
	t.Setenv("DEVSANDBOX", "")
	cmd := newRunCommandCmd()
	var got []string
	orig := runCommandSandbox
	runCommandSandbox = func(c *cobra.Command, args []string) error {
		got = args
		if proxy, _ := c.Flags().GetBool("proxy"); !proxy {
			t.Error("--proxy before the command name must be parsed as a sandbox flag")
		}
		return nil
	}
	t.Cleanup(func() { runCommandSandbox = orig })

	cmd.SetArgs([]string{"--proxy", "npm", "--proxy"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if want := []string{"npm", "--proxy"}; !reflect.DeepEqual(got, want) {
		t.Errorf("args = %#v, want %#v", got, want)
	}
}

// runSandbox reads every sandbox flag by name, so run-command must register the
// same set with the same defaults as the root launch path.
func TestRunCommandCmdSandboxFlagDefaultsMatchRoot(t *testing.T) {
	root := &cobra.Command{}
	addSandboxFlags(root)
	cmd := newRunCommandCmd()

	// FlagUsages spells out every flag's name, type, default and no-option
	// default, so equal text means an identical flag set.
	if got, want := cmd.Flags().FlagUsages(), root.Flags().FlagUsages(); got != want {
		t.Errorf("run-command flags differ from the root launch path\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunCommandCmdRejectsMissingName(t *testing.T) {
	calls := stubCommandSandbox(t)
	if _, err := executeRunCommand(t); err == nil || !strings.Contains(err.Error(), "requires a command name") {
		t.Fatalf("err = %v, want a missing command name error", err)
	}
	if len(*calls) != 0 {
		t.Errorf("the sandbox runner must not run without a command, got %#v", *calls)
	}
}

// An external command named after a management subcommand reaches the sandbox
// as a workload when it comes through run-command, rather than being dispatched
// to devsandbox's own subcommand.
func TestRunCommandCmdManagementNamesAreWorkloads(t *testing.T) {
	for _, argv := range [][]string{
		{"run-command", "config", "--help"},
		{"run-command", "tools", "list"},
		{"run-command", "run-agent", "claude"},
		{"run-command", "run-command", "npm"},
	} {
		t.Run(strings.Join(argv[1:], " "), func(t *testing.T) {
			t.Setenv("DEVSANDBOX", "")
			calls := stubCommandSandbox(t)

			root := &cobra.Command{Use: "devsandbox", SilenceErrors: true, SilenceUsage: true}
			root.AddCommand(newConfigCmd(), newToolsCmd(), newRunAgentCmd(), newRunCommandCmd())
			var out bytes.Buffer
			root.SetArgs(argv)
			root.SetOut(&out)
			root.SetErr(&out)

			if err := root.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if want := [][]string{argv[1:]}; !reflect.DeepEqual(*calls, want) {
				t.Errorf("runner calls = %#v, want %#v", *calls, want)
			}
			if out.Len() != 0 {
				t.Errorf("a management subcommand produced output: %q", out.String())
			}
		})
	}
}

// runCommandChildEnv makes the test binary re-enter main() as a real devsandbox
// process, so the exit status is the one main's error mapping produces.
const runCommandChildEnv = "DEVSANDBOX_TEST_RUN_COMMAND_CHILD"

const runnerInvokedMarker = "SANDBOX RUNNER INVOKED"

func TestRunCommandMissingInSandboxExits127(t *testing.T) {
	const name = "ds-missing-command-7f3a"
	if os.Getenv(runCommandChildEnv) != "" {
		runCommandSandbox = func(*cobra.Command, []string) error {
			_, _ = os.Stderr.WriteString(runnerInvokedMarker + "\n")
			os.Exit(99)
			return nil
		}
		os.Args = []string{"devsandbox", "run-command", name, "--flag", "arg"}
		main()
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestRunCommandMissingInSandboxExits127$")
	cmd.Env = append(os.Environ(), runCommandChildEnv+"=1", "DEVSANDBOX=1", "PATH="+t.TempDir())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	ee, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		t.Fatalf("child error = %v (%T), want an *exec.ExitError; stderr: %q", err, err, stderr.String())
	}
	status, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || status.ExitStatus() != 127 {
		t.Fatalf("child exit = %v, want status 127; stderr: %q", ee, stderr.String())
	}
	if strings.Contains(stderr.String(), runnerInvokedMarker) {
		t.Fatalf("the sandbox runner was invoked inside a sandbox; stderr: %q", stderr.String())
	}
	if n := strings.Count(stderr.String(), name); n != 1 {
		t.Errorf("stderr names the command %d times, want exactly once: %q", n, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}

// The found-command path through main(), with flags the real root command also
// defines: run-command must hand every one of them to the program untouched and
// replace itself with it, never reaching the sandbox runner.
func TestRunCommandInSandboxExecsFoundCommand(t *testing.T) {
	const name = "ds-found-command-7f3a"
	argv := []string{"--help", "-h", "--version", "a b"}
	if os.Getenv(runCommandChildEnv) != "" {
		runCommandSandbox = func(*cobra.Command, []string) error {
			_, _ = os.Stderr.WriteString(runnerInvokedMarker + "\n")
			os.Exit(99)
			return nil
		}
		os.Args = append([]string{"devsandbox", "run-command", name}, argv...)
		main()
		os.Exit(98)
	}

	binDir := t.TempDir()
	script := "#!/bin/sh\nprintf 'arg0:%s\\n' \"$0\"\nfor a in \"$@\"; do printf 'arg:%s\\n' \"$a\"; done\n"
	prog := filepath.Join(binDir, name)
	if err := os.WriteFile(prog, []byte(script), 0o755); err != nil {
		t.Fatalf("write %s: %v", prog, err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestRunCommandInSandboxExecsFoundCommand$")
	cmd.Env = append(os.Environ(), runCommandChildEnv+"=1", "DEVSANDBOX=1", "PATH="+binDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("child failed: %v; stdout: %q; stderr: %q", err, stdout.String(), stderr.String())
	}
	want := "arg0:" + prog + "\narg:--help\narg:-h\narg:--version\narg:a b\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q; stderr: %q", stdout.String(), want, stderr.String())
	}
	if strings.Contains(stderr.String(), runnerInvokedMarker) {
		t.Errorf("the sandbox runner was invoked inside a sandbox; stderr: %q", stderr.String())
	}
}
