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

// State is what a probe learned about a pid.
type State int

const (
	// Dead: the kernel reports the process as gone. This is the only answer
	// that may authorize reclaiming the state the pid owns.
	Dead State = iota
	// Live: the probe reached the process, so it exists and is the caller's
	// own. State keyed on this pid must be kept.
	Live
	// Unknown: the probe reached neither answer - EPERM above all, which is a
	// pid the kernel handed to another user's process. Alive reads this as
	// alive, so a location that keys deletion on the pid alone never reclaims
	// such an entry and needs an age backstop; that backstop belongs on this
	// answer and not on Live, or it reclaims state whose owner is running.
	Unknown
)

// String names the state, so a failure that reports one is readable.
func (s State) String() string {
	switch s {
	case Dead:
		return "dead"
	case Live:
		return "live"
	default:
		return "unknown"
	}
}

// Alive reports whether pid names a live process. It answers false only for a
// non-positive pid or a process the kernel reports as gone; see the package
// comment for why every other outcome is true.
func Alive(pid int) bool {
	return Probe(pid) != Dead
}

// Probe reports what the kernel says about pid. It is Alive with the two
// answers Alive folds together kept apart, for the callers that back the probe
// with an age rule: reclaiming by age is right for a pid nothing can inspect
// and wrong for one that is answering.
func Probe(pid int) State {
	return probe(pid, signalZero)
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

// probe is the seam Probe delegates to, with the signal injected so tests can
// pin how each result is read without depending on which pids the host has.
func probe(pid int, signal func(int) error) State {
	if pid <= 0 {
		return Dead
	}
	err := signal(pid)
	if err == nil {
		return Live
	}
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return Dead
	}
	return Unknown
}
