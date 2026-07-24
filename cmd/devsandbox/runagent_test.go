package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"devsandbox/internal/agentid"
	"devsandbox/internal/herdrstate"
	"devsandbox/internal/sandbox"
)

func fakeSelf(path string) func() (string, error) {
	return func() (string, error) { return path, nil }
}

func fakeLookPath(found map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		if p, ok := found[name]; ok {
			return p, nil
		}
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
}

func TestPlanAgentInvocationRejectsUnknownAgent(t *testing.T) {
	for _, agent := range []string{"", "bash", "aider", "claude-wrapper", "/usr/local/bin/claude", "/usr/local/bin/pi", "/usr/local/bin/codex", "/usr/local/bin/opencode", "/usr/local/bin/copilot"} {
		t.Run(agent, func(t *testing.T) {
			_, err := planAgentInvocation(agent, nil, "", fakeSelf("/usr/bin/devsandbox"), fakeLookPath(map[string]string{"claude": "/usr/bin/claude"}))
			if err == nil {
				t.Fatalf("expected error for agent %q", agent)
			}
			if !strings.Contains(err.Error(), "claude") {
				t.Errorf("error should name the supported agents, got %q", err)
			}
		})
	}
}

func TestPlanAgentInvocationInsideSandboxExecsRealAgent(t *testing.T) {
	for _, marker := range []string{"1", "yes", " "} {
		t.Run(marker, func(t *testing.T) {
			inv, err := planAgentInvocation("claude", []string{"--resume", "abc"}, marker,
				fakeSelf("/usr/bin/devsandbox"), fakeLookPath(map[string]string{"claude": "/opt/bin/claude"}))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if inv.Path != "/opt/bin/claude" {
				t.Errorf("path = %q, want /opt/bin/claude", inv.Path)
			}
			want := []string{"claude", "--resume", "abc"}
			if !reflect.DeepEqual(inv.Argv, want) {
				t.Errorf("argv = %#v, want %#v", inv.Argv, want)
			}
			for _, a := range inv.Argv {
				if strings.Contains(a, "devsandbox") {
					t.Fatalf("in-sandbox invocation must not re-enter devsandbox: %#v", inv.Argv)
				}
			}
		})
	}
}

func TestPlanAgentInvocationInsideSandboxMissingBinary(t *testing.T) {
	_, err := planAgentInvocation("claude", nil, "1", fakeSelf("/usr/bin/devsandbox"), fakeLookPath(nil))
	if err == nil {
		t.Fatal("expected an error when the real agent is missing")
	}
	if !strings.Contains(err.Error(), "not found in PATH") {
		t.Errorf("error should be actionable, got %q", err)
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("error should wrap exec.ErrNotFound, got %v", err)
	}
}

func TestPlanAgentInvocationOutsideSandboxReEnters(t *testing.T) {
	tests := []struct {
		name   string
		marker string
		args   []string
		want   []string
	}{
		{name: "unset", marker: "", args: nil, want: []string{"devsandbox", "claude"}},
		{name: "empty is outside", marker: "", args: []string{"--resume", "ID"}, want: []string{"devsandbox", "claude", "--resume", "ID"}},
		{name: "joined flag", marker: "", args: []string{"--resume=ID"}, want: []string{"devsandbox", "claude", "--resume=ID"}},
		{name: "spaces stay one arg", marker: "", args: []string{"-p", "hello world; rm -rf /"}, want: []string{"devsandbox", "claude", "-p", "hello world; rm -rf /"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := planAgentInvocation("claude", tt.args, tt.marker,
				fakeSelf("/usr/local/bin/devsandbox"), fakeLookPath(nil))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if inv.Path != "/usr/local/bin/devsandbox" {
				t.Errorf("path = %q, want the running executable", inv.Path)
			}
			if !reflect.DeepEqual(inv.Argv, tt.want) {
				t.Errorf("argv = %#v, want %#v", inv.Argv, tt.want)
			}
		})
	}
}

// Re-entry must go through our own binary, so a "devsandbox" earlier in PATH
// cannot hijack it.
func TestPlanAgentInvocationIgnoresPathDevsandbox(t *testing.T) {
	inv, err := planAgentInvocation("claude", nil, "",
		fakeSelf("/opt/real/devsandbox"),
		fakeLookPath(map[string]string{"devsandbox": "/tmp/evil/devsandbox", "claude": "/usr/bin/claude"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inv.Path != "/opt/real/devsandbox" {
		t.Errorf("path = %q, want /opt/real/devsandbox", inv.Path)
	}
}

func TestPlanAgentInvocationSelfResolutionFailure(t *testing.T) {
	boom := errors.New("boom")
	_, err := planAgentInvocation("claude", nil, "",
		func() (string, error) { return "", boom }, fakeLookPath(nil))
	if err == nil {
		t.Fatal("expected an error when the executable cannot be resolved")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error should wrap the cause, got %v", err)
	}
}

// The root command sets ArbitraryArgs and SetInterspersed(false) on itself
// only; without both on the subcommand, cobra claims agent flags as its own.
func TestRunAgentCmdPassesFlagsThrough(t *testing.T) {
	tests := []struct {
		name string
		argv []string
	}{
		{name: "resume space", argv: []string{"claude", "--resume", "0d4c2b1e-6f8a"}},
		{name: "resume joined", argv: []string{"claude", "--resume=0d4c2b1e-6f8a"}},
		{name: "short flag", argv: []string{"claude", "-p", "explain this"}},
		{name: "help", argv: []string{"claude", "--help"}},
		{name: "unknown flag", argv: []string{"claude", "--not-a-devsandbox-flag"}},
		{name: "double dash", argv: []string{"claude", "--", "--resume"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newRunAgentCmd()
			var got []string
			cmd.RunE = func(_ *cobra.Command, args []string) error {
				got = args
				return nil
			}
			cmd.SetArgs(tt.argv)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)

			if err := cmd.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !reflect.DeepEqual(got, tt.argv) {
				t.Errorf("args = %#v, want %#v", got, tt.argv)
			}
		})
	}
}

func TestRunAgentCmdRejectsUnknownAgent(t *testing.T) {
	cmd := newRunAgentCmd()
	cmd.SetArgs([]string{"definitely-not-an-agent"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error for an unknown agent")
	}
	if !strings.Contains(err.Error(), "unknown agent") {
		t.Errorf("error = %q, want it to name the problem", err)
	}
}

// paneStore writes a record for paneID and returns a loader over it, so the
// guard tests exercise the real store rather than a hand-built stub.
func paneStore(t *testing.T, rec herdrstate.Record) func(string) (herdrstate.Record, error) {
	t.Helper()
	store := herdrstate.NewStore(t.TempDir())
	if err := store.Save(rec); err != nil {
		t.Fatalf("save record: %v", err)
	}
	return store.Load
}

func worktreeRecord(t *testing.T, paneID string) (rec herdrstate.Record, worktree, repoRoot string) {
	t.Helper()
	worktree = t.TempDir()
	repoRoot = t.TempDir()
	return herdrstate.Record{
		Version:     herdrstate.Version,
		PaneID:      paneID,
		Agent:       "claude",
		ProjectDir:  worktree,
		SandboxRoot: filepath.Join(t.TempDir(), sandbox.GenerateSandboxName(repoRoot)),
	}, worktree, repoRoot
}

// A resume typed into a pane whose session was launched with --worktree must
// fail closed: re-entering from the worktree would open a different synthetic
// home and start a new session while looking like a resume.
func TestResolveAgentRunWorktreeMappingFailsClosed(t *testing.T) {
	rec, worktree, _ := worktreeRecord(t, "w1F:p1C")
	guard := herdrRestoreContext{
		PaneID: "w1F:p1C",
		Cwd:    worktree,
		Load:   paneStore(t, rec),
	}

	inv, err := resolveAgentRun("claude", []string{"--resume", "0d4c2b1e"}, "", guard,
		fakeSelf("/usr/bin/devsandbox"), fakeLookPath(map[string]string{"claude": "/usr/bin/claude"}))
	if err == nil {
		t.Fatal("expected the worktree guard to refuse the resume")
	}
	if !strings.Contains(err.Error(), "--worktree") {
		t.Errorf("error %q should name the worktree cause", err)
	}
	if inv.Path != "" || inv.Argv != nil {
		t.Fatalf("no invocation may be produced when the guard fires, got %+v", inv)
	}
	if strings.Contains(err.Error(), "0d4c2b1e") {
		t.Errorf("error %q must not echo the session ID", err)
	}
}

// The repo root is where the guard actually runs: `devsandbox --worktree ...`
// never chdirs the host shell, so the herdr pane that launched the session - and
// the pane herdr later types the resume into - sits in the repo root, not the
// worktree. That is also the one cwd from which the recorded sandbox root
// re-derives exactly, so a sandbox-root comparison alone passes and the guard
// silently does nothing in its own headline case.
func TestResolveAgentRunWorktreeMappingFailsClosedFromRepoRoot(t *testing.T) {
	rec, _, repoRoot := worktreeRecord(t, "w1F:p1C")
	guard := herdrRestoreContext{
		PaneID: "w1F:p1C",
		Cwd:    repoRoot,
		Load:   paneStore(t, rec),
	}

	if !herdrstate.DerivesSameSandboxRoot(rec, repoRoot) {
		t.Fatal("precondition: the repo root must re-derive the recorded sandbox root")
	}

	inv, err := resolveAgentRun("claude", []string{"--resume", "0d4c2b1e"}, "", guard,
		fakeSelf("/usr/bin/devsandbox"), fakeLookPath(map[string]string{"claude": "/usr/bin/claude"}))
	if err == nil {
		t.Fatal("expected the worktree guard to refuse a resume typed in the repo root")
	}
	if !strings.Contains(err.Error(), "--worktree") {
		t.Errorf("error %q should name the worktree cause", err)
	}
	if inv.Path != "" || inv.Argv != nil {
		t.Fatalf("no invocation may be produced when the guard fires, got %+v", inv)
	}
	if strings.Contains(err.Error(), "0d4c2b1e") {
		t.Errorf("error %q must not echo the session ID", err)
	}
}

// A resume typed from an unrelated directory is refused too, but must not be
// blamed on --worktree: the remedy there is to change directory, not to re-run
// a worktree invocation.
func TestResolveAgentRunRefusesResumeFromAnotherDirectory(t *testing.T) {
	projectDir := t.TempDir()
	rec := herdrstate.Record{
		Version:     herdrstate.Version,
		PaneID:      "w1F:p1C",
		Agent:       "claude",
		ProjectDir:  projectDir,
		SandboxRoot: filepath.Join(t.TempDir(), sandbox.GenerateSandboxName(projectDir)),
	}
	guard := herdrRestoreContext{PaneID: rec.PaneID, Cwd: t.TempDir(), Load: paneStore(t, rec)}

	_, err := resolveAgentRun("claude", []string{"--resume", "ID"}, "", guard,
		fakeSelf("/usr/bin/devsandbox"), fakeLookPath(map[string]string{"claude": "/usr/bin/claude"}))
	if err == nil {
		t.Fatal("expected a resume from an unrelated directory to be refused")
	}
	if strings.Contains(err.Error(), "--worktree") {
		t.Errorf("error %q blames --worktree for a plain directory mismatch", err)
	}
	if !strings.Contains(err.Error(), projectDir) {
		t.Errorf("error %q should name the directory to change to", err)
	}
}

// A record that exists but cannot be read is the one Load failure that must not
// fall through: it may be the worktree mapping the guard exists to honor, and
// re-entering blind is the silent-new-session outcome. Only ErrNotFound - the
// ordinary "this pane never launched an agent" case - falls through.
func TestResolveAgentRunUnreadableMappingFailsClosed(t *testing.T) {
	guard := herdrRestoreContext{
		PaneID: "w1F:p1C",
		Cwd:    t.TempDir(),
		Load: func(string) (herdrstate.Record, error) {
			return herdrstate.Record{}, errors.New("parse record: unexpected end of JSON input")
		},
	}

	inv, err := resolveAgentRun("claude", []string{"--resume", "ID"}, "", guard,
		fakeSelf("/usr/bin/devsandbox"), fakeLookPath(map[string]string{"claude": "/usr/bin/claude"}))
	if err == nil {
		t.Fatal("expected an unreadable pane mapping to refuse the resume")
	}
	if inv.Path != "" || inv.Argv != nil {
		t.Fatalf("no invocation may be produced when the guard fires, got %+v", inv)
	}

	// The ordinary case still falls through.
	guard.Load = func(string) (herdrstate.Record, error) { return herdrstate.Record{}, herdrstate.ErrNotFound }
	if _, err := resolveAgentRun("claude", []string{"--resume", "ID"}, "", guard,
		fakeSelf("/usr/bin/devsandbox"), fakeLookPath(map[string]string{"claude": "/usr/bin/claude"})); err != nil {
		t.Fatalf("a pane with no mapping must re-enter normally, got %v", err)
	}
}

// Codex resumes through a subcommand rather than a flag, so the guard has to
// recognize `codex resume ID` the same way it recognizes `claude --resume ID`.
func TestResolveAgentRunWorktreeMappingFailsClosedForCodex(t *testing.T) {
	rec, worktree, _ := worktreeRecord(t, "w1F:p1C")
	rec.Agent = "codex"
	guard := herdrRestoreContext{
		PaneID: "w1F:p1C",
		Cwd:    worktree,
		Load:   paneStore(t, rec),
	}
	self, look := fakeSelf("/usr/bin/devsandbox"), fakeLookPath(map[string]string{"codex": "/usr/bin/codex"})

	if _, err := resolveAgentRun("codex", []string{"resume", "0d4c2b1e"}, "", guard, self, look); err == nil {
		t.Fatal("expected the worktree guard to refuse `codex resume ID`")
	}

	// A prompt that merely starts with something else is not a resume.
	inv, err := resolveAgentRun("codex", []string{"exec", "hi"}, "", guard, self, look)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"devsandbox", "codex", "exec", "hi"}; !reflect.DeepEqual(inv.Argv, want) {
		t.Errorf("argv = %#v, want %#v", inv.Argv, want)
	}
}

// Every case that is not "resume into a worktree-launched pane" falls through
// to ordinary re-entry, including the ones an earlier draft failed closed on.
func TestResolveAgentRunFallsThroughToReEntry(t *testing.T) {
	matching := func(t *testing.T) (herdrstate.Record, string) {
		t.Helper()
		projectDir := t.TempDir()
		return herdrstate.Record{
			Version:     herdrstate.Version,
			PaneID:      "w1F:p1C",
			Agent:       "claude",
			ProjectDir:  projectDir,
			SandboxRoot: filepath.Join(t.TempDir(), sandbox.GenerateSandboxName(projectDir)),
		}, projectDir
	}

	tests := []struct {
		name  string
		args  []string
		build func(t *testing.T) herdrRestoreContext
	}{
		{
			name: "mapping derives the same sandbox root",
			args: []string{"--resume", "ID"},
			build: func(t *testing.T) herdrRestoreContext {
				rec, projectDir := matching(t)
				return herdrRestoreContext{PaneID: rec.PaneID, Cwd: projectDir, Load: paneStore(t, rec)}
			},
		},
		{
			name: "no mapping for this pane",
			args: []string{"--resume", "ID"},
			build: func(t *testing.T) herdrRestoreContext {
				rec, _ := matching(t)
				return herdrRestoreContext{PaneID: "w9Z:p9Z", Cwd: t.TempDir(), Load: paneStore(t, rec)}
			},
		},
		{
			name: "mapping records a different agent",
			args: []string{"--resume", "ID"},
			build: func(t *testing.T) herdrRestoreContext {
				rec, worktree, _ := worktreeRecord(t, "w1F:p1C")
				rec.Agent = "someotheragent"
				return herdrRestoreContext{PaneID: rec.PaneID, Cwd: worktree, Load: paneStore(t, rec)}
			},
		},
		{
			name: "mapping project dir no longer exists",
			args: []string{"--resume", "ID"},
			build: func(t *testing.T) herdrRestoreContext {
				rec, worktree, _ := worktreeRecord(t, "w1F:p1C")
				load := paneStore(t, rec)
				if err := os.Remove(worktree); err != nil {
					t.Fatalf("remove project dir: %v", err)
				}
				return herdrRestoreContext{PaneID: rec.PaneID, Cwd: t.TempDir(), Load: load}
			},
		},
		{
			name: "non-resume args inside a herdr pane",
			args: []string{"-p", "hello"},
			build: func(t *testing.T) herdrRestoreContext {
				rec, worktree, _ := worktreeRecord(t, "w1F:p1C")
				return herdrRestoreContext{PaneID: rec.PaneID, Cwd: worktree, Load: paneStore(t, rec)}
			},
		},
		{
			name: "resume outside a herdr pane",
			args: []string{"--resume", "ID"},
			build: func(t *testing.T) herdrRestoreContext {
				rec, worktree, _ := worktreeRecord(t, "w1F:p1C")
				return herdrRestoreContext{PaneID: "", Cwd: worktree, Load: paneStore(t, rec)}
			},
		},
		{
			name: "no store available",
			args: []string{"--resume", "ID"},
			build: func(t *testing.T) herdrRestoreContext {
				return herdrRestoreContext{PaneID: "w1F:p1C", Cwd: t.TempDir()}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := resolveAgentRun("claude", tt.args, "", tt.build(t),
				fakeSelf("/usr/local/bin/devsandbox"), fakeLookPath(map[string]string{"claude": "/usr/bin/claude"}))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := append([]string{"devsandbox", "claude"}, tt.args...)
			if !reflect.DeepEqual(inv.Argv, want) {
				t.Errorf("argv = %#v, want %#v", inv.Argv, want)
			}
			if inv.Path != "/usr/local/bin/devsandbox" {
				t.Errorf("path = %q, want re-entry through devsandbox", inv.Path)
			}
		})
	}
}

// TestNoRunAgentBranchExecsAHostAgent sweeps every combination of the inputs
// that steer the resolution order and asserts the plan's governing invariant:
// outside a sandbox, run-agent either refuses or re-enters through devsandbox's
// own binary. A host agent binary is reachable only from branch 1, which runs
// when we are already inside the sandbox.
//
// The individual branch tests pin each case's argv; this one exists so a future
// branch cannot be added without the invariant being checked for it too.
func TestNoRunAgentBranchExecsAHostAgent(t *testing.T) {
	const self = "/usr/local/bin/devsandbox"
	hostAgents := map[string]string{
		"claude":     "/usr/bin/claude",
		"pi":         "/usr/bin/pi",
		"codex":      "/usr/bin/codex",
		"devsandbox": "/tmp/evil/devsandbox",
	}

	argSets := map[string][]string{
		"no args":        nil,
		"claude resume":  {"--resume", "0d4c2b1e"},
		"joined resume":  {"--resume=0d4c2b1e"},
		"codex resume":   {"resume", "0d4c2b1e"},
		"pi session":     {"--session", "0d4c2b1e"},
		"prompt":         {"-p", "hello world; rm -rf /"},
		"unknown flag":   {"--not-a-flag"},
		"after dashdash": {"--", "--resume", "0d4c2b1e"},
	}

	guards := map[string]func(t *testing.T, agent string) herdrRestoreContext{
		"no herdr pane": func(t *testing.T, _ string) herdrRestoreContext {
			t.Helper()
			return herdrRestoreContext{Cwd: t.TempDir()}
		},
		"pane without a mapping": func(t *testing.T, _ string) herdrRestoreContext {
			t.Helper()
			return herdrRestoreContext{PaneID: "w1F:p1C", Cwd: t.TempDir()}
		},
		"pane with a matching mapping": func(t *testing.T, agent string) herdrRestoreContext {
			t.Helper()
			projectDir := t.TempDir()
			rec := herdrstate.Record{
				Version:     herdrstate.Version,
				PaneID:      "w1F:p1C",
				Agent:       agent,
				ProjectDir:  projectDir,
				SandboxRoot: filepath.Join(t.TempDir(), sandbox.GenerateSandboxName(projectDir)),
			}
			return herdrRestoreContext{PaneID: rec.PaneID, Cwd: projectDir, Load: paneStore(t, rec)}
		},
		"pane with a worktree mapping": func(t *testing.T, agent string) herdrRestoreContext {
			t.Helper()
			rec, worktree, _ := worktreeRecord(t, "w1F:p1C")
			rec.Agent = agent
			return herdrRestoreContext{PaneID: rec.PaneID, Cwd: worktree, Load: paneStore(t, rec)}
		},
		"unusable cwd": func(t *testing.T, agent string) herdrRestoreContext {
			t.Helper()
			rec, _, _ := worktreeRecord(t, "w1F:p1C")
			rec.Agent = agent
			return herdrRestoreContext{PaneID: rec.PaneID, Load: paneStore(t, rec)}
		},
	}

	for _, agent := range agentid.KnownAgents() {
		for guardName, buildGuard := range guards {
			for argName, args := range argSets {
				t.Run(agent+"/"+guardName+"/"+argName, func(t *testing.T) {
					// An empty DEVSANDBOX means "outside a sandbox", in Go and
					// in every generated snippet alike.
					inv, err := resolveAgentRun(agent, args, "", buildGuard(t, agent),
						fakeSelf(self), fakeLookPath(hostAgents))
					if err != nil {
						// Failing closed is always acceptable; running a host
						// agent is not.
						if inv.Path != "" || inv.Argv != nil {
							t.Fatalf("error path produced an invocation: %+v", inv)
						}
						return
					}
					if inv.Path != self {
						t.Fatalf("path = %q, want re-entry through %q", inv.Path, self)
					}
					if len(inv.Argv) < 2 || inv.Argv[1] != agent {
						t.Fatalf("argv = %#v, want a devsandbox <agent> re-entry", inv.Argv)
					}
					for _, hostPath := range hostAgents {
						if slices.Contains(inv.Argv, hostPath) {
							t.Fatalf("argv %#v names a host binary path", inv.Argv)
						}
					}
				})
			}
		}
	}
}

// Branch 1 wins over the guard: inside a sandbox the wrapper must not recurse,
// and the recorded mapping describes the sandbox we are already in.
func TestResolveAgentRunInsideSandboxSkipsGuard(t *testing.T) {
	rec, worktree, _ := worktreeRecord(t, "w1F:p1C")
	guard := herdrRestoreContext{PaneID: rec.PaneID, Cwd: worktree, Load: paneStore(t, rec)}

	inv, err := resolveAgentRun("claude", []string{"--resume", "ID"}, "1", guard,
		fakeSelf("/usr/bin/devsandbox"), fakeLookPath(map[string]string{"claude": "/opt/bin/claude"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inv.Path != "/opt/bin/claude" {
		t.Errorf("path = %q, want the real agent", inv.Path)
	}
}

func TestResolveAgentRunUnknownAgentStillRejected(t *testing.T) {
	rec, worktree, _ := worktreeRecord(t, "w1F:p1C")
	guard := herdrRestoreContext{PaneID: rec.PaneID, Cwd: worktree, Load: paneStore(t, rec)}

	_, err := resolveAgentRun("definitely-not-an-agent", []string{"--resume", "ID"}, "", guard,
		fakeSelf("/usr/bin/devsandbox"), fakeLookPath(nil))
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("err = %v, want an unknown agent error", err)
	}
}

// An unusable current directory must not be reported as a matching sandbox
// root: it is the one case where the guard cannot answer the question.
func TestCheckHerdrWorktreeGuardRequiresAbsoluteCwd(t *testing.T) {
	rec, _, _ := worktreeRecord(t, "w1F:p1C")
	guard := herdrRestoreContext{PaneID: rec.PaneID, Cwd: "", Load: paneStore(t, rec)}

	err := checkHerdrWorktreeGuard("claude", []string{"--resume", "ID"}, guard)
	if err == nil {
		t.Fatal("expected an error when the current directory is unknown")
	}
	if !strings.Contains(err.Error(), "current directory") {
		t.Errorf("error %q should name the missing current directory", err)
	}
}

// loadHerdrPaneRecord must not create the store: a run-agent outside a herdr
// pane leaves no state behind.
func TestLoadHerdrPaneRecordDoesNotCreateStore(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	if _, err := loadHerdrPaneRecord("w1F:p1C"); !errors.Is(err, herdrstate.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	dir := filepath.Join(stateHome, "devsandbox", "herdr-panes")
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("store directory should not be created by a read, stat err = %v", err)
	}
}

func TestRunAgentWithoutArgs(t *testing.T) {
	if err := runAgent(nil); err == nil {
		t.Fatal("expected an error when no agent is given")
	} else if !strings.Contains(err.Error(), "requires an agent name") {
		t.Errorf("error = %q, want it to state the missing argument", err)
	}
}
