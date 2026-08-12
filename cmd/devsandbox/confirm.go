// cmd/devsandbox/confirm.go
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"devsandbox/internal/notice"
	"devsandbox/internal/prompt"
	"golang.org/x/term"
)

// errLaunchDeclined is returned when the user answers no to the pre-launch
// warning prompt.
var errLaunchDeclined = errors.New("aborted: warnings were not confirmed")

// confirmWarnings holds the launch until the user acknowledges every warning
// raised while preparing the sandbox. A warning here means something the user
// asked for is not in effect - a config key that was ignored, a cleanup that
// did not run, a proxy control narrowed by --no-mitm - and the workload's own
// output scrolls it off the terminal seconds later.
//
// The prompt needs a human, so a non-interactive launch reprints the warnings
// and proceeds rather than failing a scripted run on a benign one.
func confirmWarnings(in io.Reader, out io.Writer, skip, interactive bool) error {
	entries, lost := notice.Raised()
	if len(entries) == 0 || skip {
		return nil
	}

	writeWarningSummary(out, entries, lost)

	if !interactive {
		fmt.Fprintf(out, "\nStarting anyway: not a terminal, so the confirmation prompt was skipped.\n") //nolint:errcheck
		return nil
	}

	fmt.Fprintf(out, "\nStart the sandbox anyway? [y/N]: ") //nolint:errcheck

	// A closed stdin is not a yes, so EOF falls through to the refusal below
	// rather than failing the launch with a read error.
	response, err := prompt.ReadLine(in)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read confirmation: %w", err)
	}
	if !prompt.IsYes(response) {
		return errLaunchDeclined
	}
	return nil
}

// confirmWarningsStdio is the wrapper runSandbox uses: os.Stdin for the answer,
// os.Stderr for the prompt, so the summary lands where the warnings themselves
// were written and stdout stays clean for the workload.
func confirmWarningsStdio(skip bool) error {
	return confirmWarnings(os.Stdin, os.Stderr, skip, promptIsInteractive(os.Stdin, os.Stderr))
}

// promptIsInteractive reports whether there is a human at both ends of the
// prompt: one to read the question and one to answer it.
//
// Stdin alone is not enough, because the question goes to stderr. With stderr
// redirected - `devsandbox npm install 2>build.log` from a terminal - a stdin
// test alone writes the prompt into the log file and then blocks on an answer
// to a question the user never saw.
func promptIsInteractive(in, out *os.File) bool {
	return isTerminal(in) && isTerminal(out)
}

func isTerminal(f *os.File) bool {
	return f != nil && term.IsTerminal(int(f.Fd()))
}

// writeWarningSummary reprints the raised notices as one block. They were each
// written when they happened, but sandbox setup is long enough that the first
// one is usually gone from view by the time the last one lands.
func writeWarningSummary(out io.Writer, entries []notice.Entry, lost int) {
	noun := "warning"
	if len(entries) > 1 {
		noun = "warnings"
	}
	fmt.Fprintf(out, "\n%d %s while preparing the sandbox:\n\n", len(entries), noun) //nolint:errcheck

	for _, e := range entries {
		for i, line := range strings.Split(strings.TrimRight(e.Msg, "\n"), "\n") {
			if i == 0 {
				fmt.Fprintf(out, "  [%s] %s\n", e.Level, line) //nolint:errcheck
				continue
			}
			fmt.Fprintf(out, "         %s\n", line) //nolint:errcheck
		}
	}

	if lost > 0 {
		if logPath := notice.LogPath(); logPath != "" {
			fmt.Fprintf(out, "  (+%d earlier, see %s)\n", lost, logPath) //nolint:errcheck
		} else {
			fmt.Fprintf(out, "  (+%d earlier)\n", lost) //nolint:errcheck
		}
	}
}
