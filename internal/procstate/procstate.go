// Package procstate answers whether a pid names a live process, for callers
// that record a pid as the owner of on-disk state and reclaim that state once
// the owner is gone.
//
// Alive is fail-safe: it answers dead only when the kernel says the process
// does not exist (ESRCH, or os.ErrProcessDone when os.Process learned that
// first). Every other probe result answers alive, and an unknown answer is
// deliberately alive rather than dead, because the two mistakes do not cost
// the same. A kept entry is a leak of a few bytes; a removed one can be a live
// session's socket directory or its overlay upper. An uncertain probe must
// never authorize a deletion.
//
// The case that makes this conservative rather than correct is EPERM, which
// signal 0 returns for a live process owned by another user. Every directory
// that relies on this probe is per-user, so a pid devsandbox recorded cannot
// legitimately belong to another user's process: EPERM means the recorded
// owner exited and the kernel handed its pid to someone else. Alive is still
// the answer, and the entry survives.
//
// pid <= 0 is dead without a probe. kill(2) reads 0 as "every process in the
// caller's process group" and a negative value as a process group id, and some
// of the directories that record a pid in an entry name are sandbox-writable,
// so a planted `0` must not turn the probe into a group-wide signal.
//
// The consequence for a location that keys deletion on the pid alone: an entry
// whose owner answers EPERM is kept forever. Such a location needs an age
// backstop, or an accepted and documented leak, named in internal/reclaim.
package procstate

import (
	"errors"
	"os"
	"syscall"
)

// Alive reports whether pid names a live process. It answers false only for a
// non-positive pid or a process the kernel reports as gone; see the package
// comment for why every other outcome is true.
func Alive(pid int) bool {
	return alive(pid, signalZero)
}

// signalZero probes pid with signal 0, which performs the existence and
// permission checks of kill(2) without delivering anything.
func signalZero(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.Signal(0))
}

// alive is the seam Alive delegates to, with the probe injected so tests can
// pin how each result is read without depending on which pids the host has.
func alive(pid int, signal func(int) error) bool {
	if pid <= 0 {
		return false
	}
	err := signal(pid)
	if err == nil {
		return true
	}
	return !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH)
}
