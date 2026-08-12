// Package prompt reads answers to interactive [y/N] questions.
package prompt

import (
	"io"
	"strings"
)

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
// closed stream as a refusal.
func ReadLine(in io.Reader) (string, error) {
	var answer []byte
	var b [1]byte
	for {
		n, err := in.Read(b[:])
		if n > 0 {
			if b[0] == '\n' {
				return string(answer), nil
			}
			answer = append(answer, b[0])
		}
		if err != nil {
			return string(answer), err
		}
	}
}
