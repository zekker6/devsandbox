package seatbelt

import (
	"context"
	"os/exec"
	"strconv"
)

func checkCommand(ctx context.Context, profile string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, Executable, "-p", profile, "/usr/bin/true")
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	return cmd
}

// The child can continue writing after the retained prefix fills. Returning the
// full write length prevents a diagnostic limit from changing process behavior.
type boundedOutput struct {
	limit     int
	data      []byte
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - len(b.data)
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return n, nil
}

func (b *boundedOutput) String() string {
	s := strconv.Quote(string(b.data))
	if b.truncated {
		s += " (truncated)"
	}
	return s
}
