// Package prompt reads answers to interactive [y/N] questions.
package prompt

import (
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// IsInteractive reports whether there is a human at both ends of a prompt: one
// to read the question on out and one to answer it on in.
//
// Testing the answering end alone is the mistake this exists to prevent. Every
// devsandbox prompt asks on stderr and reads stdin, so with stderr redirected -
// `devsandbox npm install 2>build.log` from a terminal - a stdin-only test
// writes the question into the log file and then blocks forever on an answer to
// a question the user never saw.
func IsInteractive(in, out *os.File) bool {
	return isTerminal(in) && isTerminal(out)
}

func isTerminal(f *os.File) bool {
	return f != nil && term.IsTerminal(int(f.Fd()))
}

// IsYes reports whether an answer to a [y/N] question is an explicit yes.
// Anything else - an empty line, an unreadable answer, a stray word - is a no,
// because the default of these questions is the one that changes nothing.
func IsYes(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// ReadLine reads one line from in, and not a byte more.
//
// A buffered reader fills its buffer from a single read, which for a devsandbox
// prompt is a bug rather than an optimization: the sandbox workload inherits the
// same stdin seconds later, so anything read past the newline is eaten here
// instead of reaching it. A canonical-mode terminal happens to hand over one
// line per read, but that is the tty line discipline's guarantee, not this
// code's - a parent that left the terminal in raw mode voids it.
//
// The trailing newline is not included. An answer terminated by EOF rather than
// a newline is returned with io.EOF, since a caller may accept it or treat the
// closed stream as a refusal. A reader that stops making progress is reported
// as io.ErrNoProgress with whatever was read so far.
func ReadLine(in io.Reader) (string, error) {
	var answer []byte
	var b [1]byte
	empty := 0
	for {
		n, err := in.Read(b[:])
		switch {
		case n > 0:
			empty = 0
			if b[0] == '\n' {
				return string(answer), nil
			}
			answer = append(answer, b[0])
		case err == nil:
			// The io.Reader contract permits (0, nil): it means nothing
			// happened, not EOF, so retrying is the correct response. Retrying
			// without bound is not - a reader that never progresses would spin
			// here at full CPU with the prompt still on screen. bufio bounds
			// the same case the same way.
			empty++
			if empty >= maxEmptyReads {
				return string(answer), io.ErrNoProgress
			}
		}
		if err != nil {
			return string(answer), err
		}
	}
}

// maxEmptyReads bounds how many consecutive no-progress reads are tolerated
// before ReadLine gives up, matching bufio's limit for the same condition.
const maxEmptyReads = 100
