package kittyproxy

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadFrame_Single(t *testing.T) {
	input := "\x1bP@kitty-cmd{\"cmd\":\"ls\"}\x1b\\"
	r := bufio.NewReader(strings.NewReader(input))

	payload, err := ReadFrame(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(payload) != `{"cmd":"ls"}` {
		t.Errorf("payload = %q, want %q", payload, `{"cmd":"ls"}`)
	}
}

func TestReadFrame_TwoBackToBack(t *testing.T) {
	input := "\x1bP@kitty-cmd{\"cmd\":\"a\"}\x1b\\\x1bP@kitty-cmd{\"cmd\":\"b\"}\x1b\\"
	r := bufio.NewReader(strings.NewReader(input))

	first, err := ReadFrame(r)
	if err != nil {
		t.Fatalf("first frame: %v", err)
	}
	if string(first) != `{"cmd":"a"}` {
		t.Errorf("first = %q", first)
	}

	second, err := ReadFrame(r)
	if err != nil {
		t.Fatalf("second frame: %v", err)
	}
	if string(second) != `{"cmd":"b"}` {
		t.Errorf("second = %q", second)
	}
}

func TestReadFrame_JunkBetweenFrames(t *testing.T) {
	// Some clients emit stray bytes between frames; the reader should skip until DCS introducer.
	input := "garbage\x1bP@kitty-cmd{\"cmd\":\"ls\"}\x1b\\"
	r := bufio.NewReader(strings.NewReader(input))

	payload, err := ReadFrame(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(payload) != `{"cmd":"ls"}` {
		t.Errorf("payload = %q", payload)
	}
}

func TestReadFrame_EOFBetweenFrames(t *testing.T) {
	r := bufio.NewReader(strings.NewReader(""))
	_, err := ReadFrame(r)
	if !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want io.EOF", err)
	}
}

func TestReadFrame_TruncatedFrame(t *testing.T) {
	// Introducer present but no terminator before EOF.
	input := "\x1bP@kitty-cmd{\"cmd\":\"ls\""
	r := bufio.NewReader(strings.NewReader(input))

	_, err := ReadFrame(r)
	if err == nil || errors.Is(err, io.EOF) {
		t.Errorf("expected non-EOF error, got %v", err)
	}
}

func TestReadFrame_OversizeFrame(t *testing.T) {
	big := strings.Repeat("a", maxFrameBytes+1)
	input := "\x1bP@kitty-cmd" + big + "\x1b\\"
	r := bufio.NewReader(strings.NewReader(input))

	_, err := ReadFrame(r)
	if err == nil {
		t.Fatal("expected error for oversize frame")
	}
}

func TestWriteFrame_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	r := bufio.NewReader(&buf)
	got, err := ReadFrame(r)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if string(got) != `{"ok":true}` {
		t.Errorf("round-trip = %q", got)
	}
}
