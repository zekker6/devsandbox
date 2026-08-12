// cmd/devsandbox/confirm_test.go
package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"devsandbox/internal/notice"
)

// raiseNotices routes notice output into a throwaway buffer and emits the given
// warnings, so each case starts from a known set of raised entries.
func raiseNotices(t *testing.T, msgs ...string) {
	t.Helper()
	if err := notice.Setup("", false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := notice.Setup("", false, new(bytes.Buffer)); err != nil {
			t.Fatal(err)
		}
	})
	for _, m := range msgs {
		notice.Warn("%s", m)
	}
}

func TestConfirmWarnings_NoWarningsDoesNotPrompt(t *testing.T) {
	raiseNotices(t)

	var out bytes.Buffer
	if err := confirmWarnings(strings.NewReader(""), &out, false, true); err != nil {
		t.Fatalf("confirmWarnings() = %v, want nil", err)
	}
	if out.Len() != 0 {
		t.Fatalf("wrote %q with no warnings raised, want nothing", out.String())
	}
}

func TestConfirmWarnings_AcceptedAnswers(t *testing.T) {
	for _, answer := range []string{"y\n", "Y\n", "yes\n", "  YES  \n", "y"} {
		t.Run(strings.TrimSpace(answer), func(t *testing.T) {
			raiseNotices(t, "unknown config key")

			var out bytes.Buffer
			if err := confirmWarnings(strings.NewReader(answer), &out, false, true); err != nil {
				t.Fatalf("confirmWarnings(%q) = %v, want nil", answer, err)
			}
			if !strings.Contains(out.String(), "Start the sandbox anyway?") {
				t.Fatalf("prompt missing from output: %q", out.String())
			}
		})
	}
}

func TestConfirmWarnings_DeclinedAnswers(t *testing.T) {
	// An empty line is the default, and EOF is a closed stdin on what still
	// claims to be a terminal: both must abort rather than launch.
	for _, answer := range []string{"n\n", "no\n", "\n", "", "whatever\n"} {
		t.Run(strings.TrimSpace(answer), func(t *testing.T) {
			raiseNotices(t, "unknown config key")

			var out bytes.Buffer
			err := confirmWarnings(strings.NewReader(answer), &out, false, true)
			if !errors.Is(err, errLaunchDeclined) {
				t.Fatalf("confirmWarnings(%q) = %v, want errLaunchDeclined", answer, err)
			}
		})
	}
}

// The workload inherits this stdin right after the prompt, so whatever the user
// typed ahead has to still be there for it to read.
func TestConfirmWarnings_LeavesTypeAheadForTheWorkload(t *testing.T) {
	raiseNotices(t, "unknown config key")

	in := strings.NewReader("y\nthe workload's first input\n")
	var out bytes.Buffer
	if err := confirmWarnings(in, &out, false, true); err != nil {
		t.Fatalf("confirmWarnings() = %v, want nil", err)
	}

	rest, err := io.ReadAll(in)
	if err != nil {
		t.Fatalf("read rest of stdin: %v", err)
	}
	if string(rest) != "the workload's first input\n" {
		t.Errorf("stdin left %q, want the line after the answer untouched", rest)
	}
}

func TestConfirmWarnings_SkipBypassesPrompt(t *testing.T) {
	raiseNotices(t, "unknown config key")

	var out bytes.Buffer
	if err := confirmWarnings(strings.NewReader("n\n"), &out, true, true); err != nil {
		t.Fatalf("confirmWarnings() with skip = %v, want nil", err)
	}
	if out.Len() != 0 {
		t.Fatalf("--yes still wrote %q, want nothing", out.String())
	}
}

// TestConfirmWarnings_NonInteractiveProceeds pins the deliberate choice not to
// fail scripted launches: with no terminal there is nobody to answer, so the
// warnings are reprinted and the sandbox starts.
func TestConfirmWarnings_NonInteractiveProceeds(t *testing.T) {
	raiseNotices(t, "unknown config key")

	var out bytes.Buffer
	if err := confirmWarnings(strings.NewReader("n\n"), &out, false, false); err != nil {
		t.Fatalf("confirmWarnings() non-interactive = %v, want nil", err)
	}
	if strings.Contains(out.String(), "Start the sandbox anyway?") {
		t.Fatalf("prompted without a terminal: %q", out.String())
	}
	for _, want := range []string{"unknown config key", "not a terminal"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q; got %q", want, out.String())
		}
	}
}

// The question is written to stderr and answered on stdin, so a terminal on
// only one of them is not a human who can answer. `devsandbox npm install
// 2>build.log` is the case that matters: stdin is still a terminal, and testing
// it alone puts the prompt in the log file and then blocks on it.
func TestPromptIsInteractive_RequiresBothEnds(t *testing.T) {
	tty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	defer tty.Close() //nolint:errcheck

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close() //nolint:errcheck
	defer w.Close() //nolint:errcheck

	tests := []struct {
		name string
		in   *os.File
		out  *os.File
		want bool
	}{
		{name: "terminal on both", in: tty, out: tty, want: true},
		{name: "stderr redirected", in: tty, out: w},
		{name: "stdin redirected", in: r, out: tty},
		{name: "neither", in: r, out: w},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := promptIsInteractive(tt.in, tt.out); got != tt.want {
				t.Errorf("promptIsInteractive() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWriteWarningSummary_ListsEveryEntry(t *testing.T) {
	raiseNotices(t, "first problem", "second problem")
	notice.Error("third problem")

	entries, lost := notice.Raised()
	var out bytes.Buffer
	writeWarningSummary(&out, entries, lost)

	got := out.String()
	for _, want := range []string{
		"3 warnings while preparing the sandbox",
		"[warn] first problem",
		"[warn] second problem",
		"[error] third problem",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q; got %q", want, got)
		}
	}
}

func TestWriteWarningSummary_SingularAndMultiline(t *testing.T) {
	raiseNotices(t, "line one\nline two")

	entries, lost := notice.Raised()
	var out bytes.Buffer
	writeWarningSummary(&out, entries, lost)

	got := out.String()
	if !strings.Contains(got, "1 warning while preparing") {
		t.Errorf("summary not singular for one entry; got %q", got)
	}
	// The continuation line must stay indented under its entry rather than
	// reading as a second, unlabelled warning.
	if !strings.Contains(got, "  [warn] line one\n         line two\n") {
		t.Errorf("multi-line warning not indented; got %q", got)
	}
}

func TestWriteWarningSummary_ReportsDropped(t *testing.T) {
	raiseNotices(t, "kept")

	entries, _ := notice.Raised()
	var out bytes.Buffer
	writeWarningSummary(&out, entries, 7)

	if !strings.Contains(out.String(), "+7 earlier") {
		t.Errorf("dropped count not reported; got %q", out.String())
	}
}
