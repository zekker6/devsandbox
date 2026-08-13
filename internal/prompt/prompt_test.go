package prompt

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/term"
)

// A prompt is asked on one stream and answered on another, so a terminal on
// only one of them is not a human who can answer. `devsandbox npm install
// 2>build.log` is the case that matters: stdin is still a terminal, and testing
// it alone puts the question in the log file and then blocks on it.
func TestIsInteractive_RequiresBothEnds(t *testing.T) {
	tty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	defer tty.Close() //nolint:errcheck

	// The pty master is a terminal on Linux but not on the BSD-derived systems
	// where allocating the slave side takes platform-specific ioctls. The
	// behavior under test is platform-independent, so skip rather than carry
	// that.
	if !term.IsTerminal(int(tty.Fd())) {
		t.Skip("the pty master is not a terminal on this platform")
	}

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
		{name: "output redirected", in: tty, out: w},
		{name: "input redirected", in: r, out: tty},
		{name: "neither", in: r, out: w},
		{name: "nil stream", in: nil, out: tty},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsInteractive(tt.in, tt.out); got != tt.want {
				t.Errorf("IsInteractive() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Reading past the newline would eat input meant for the sandbox workload,
// which inherits the same stdin once the prompt is answered.
func TestReadLine_StopsAtTheNewline(t *testing.T) {
	in := strings.NewReader("y\nwhat the workload should see\n")

	answer, err := ReadLine(in)
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if answer != "y" {
		t.Errorf("answer = %q, want %q", answer, "y")
	}

	rest, err := io.ReadAll(in)
	if err != nil {
		t.Fatalf("read rest: %v", err)
	}
	if string(rest) != "what the workload should see\n" {
		t.Errorf("stream left %q, want everything after the answer", rest)
	}
}

func TestReadLine_UnterminatedAnswerReportsEOF(t *testing.T) {
	answer, err := ReadLine(strings.NewReader("yes"))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
	if answer != "yes" {
		t.Errorf("answer = %q, want %q", answer, "yes")
	}
}

func TestReadLine_EmptyStream(t *testing.T) {
	answer, err := ReadLine(strings.NewReader(""))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
	if answer != "" {
		t.Errorf("answer = %q, want empty", answer)
	}
}

// A reader that hands over one byte per call and reports EOF alongside the last
// one, the way a pipe may.
type trickleReader struct{ s string }

func (r *trickleReader) Read(p []byte) (int, error) {
	if r.s == "" {
		return 0, io.EOF
	}
	p[0] = r.s[0]
	r.s = r.s[1:]
	if r.s == "" {
		return 1, io.EOF
	}
	return 1, nil
}

func TestReadLine_DataWithEOFOnTheSameRead(t *testing.T) {
	answer, err := ReadLine(&trickleReader{s: "no"})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
	if answer != "no" {
		t.Errorf("answer = %q, want %q", answer, "no")
	}
}

// stalledReader is the (0, nil) case the io.Reader contract permits: nothing
// happened, and nothing ever will.
type stalledReader struct{ reads int }

func (r *stalledReader) Read(p []byte) (int, error) {
	r.reads++
	return 0, nil
}

// Retrying a no-progress read is correct; retrying forever is a spin with the
// prompt still on screen and no way out.
func TestReadLine_GivesUpOnAReaderThatNeverProgresses(t *testing.T) {
	type result struct {
		answer string
		err    error
	}
	done := make(chan result, 1)
	in := &stalledReader{}

	go func() {
		answer, err := ReadLine(in)
		done <- result{answer, err}
	}()

	select {
	case got := <-done:
		if !errors.Is(got.err, io.ErrNoProgress) {
			t.Fatalf("err = %v, want io.ErrNoProgress", got.err)
		}
		if got.answer != "" {
			t.Errorf("answer = %q, want empty", got.answer)
		}
		if in.reads != maxEmptyReads {
			t.Errorf("gave up after %d reads, want %d", in.reads, maxEmptyReads)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReadLine did not return: a reader that never progresses spins forever")
	}
}

// Only *consecutive* empty reads count, so a slow stream that keeps delivering
// still reads a whole line however long it takes.
func TestReadLine_TolerantOfEmptyReadsBetweenBytes(t *testing.T) {
	answer, err := ReadLine(&stutteringReader{s: "yes\n", gap: maxEmptyReads - 1})
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if answer != "yes" {
		t.Errorf("answer = %q, want %q", answer, "yes")
	}
}

// stutteringReader returns gap empty reads before each byte of s.
type stutteringReader struct {
	s       string
	gap     int
	pending int
}

func (r *stutteringReader) Read(p []byte) (int, error) {
	if r.s == "" {
		return 0, io.EOF
	}
	if r.pending < r.gap {
		r.pending++
		return 0, nil
	}
	r.pending = 0
	p[0] = r.s[0]
	r.s = r.s[1:]
	return 1, nil
}

func TestIsYes(t *testing.T) {
	for _, answer := range []string{"y", "Y", "yes", "YES", "  yes  ", "y\r"} {
		if !IsYes(answer) {
			t.Errorf("IsYes(%q) = false, want true", answer)
		}
	}
	// The default of a [y/N] question is the one that changes nothing.
	for _, answer := range []string{"", " ", "n", "no", "yeah", "sure", "yy"} {
		if IsYes(answer) {
			t.Errorf("IsYes(%q) = true, want false", answer)
		}
	}
}
