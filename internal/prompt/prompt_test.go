package prompt

import (
	"errors"
	"io"
	"strings"
	"testing"
)

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
