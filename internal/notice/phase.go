// internal/notice/phase.go
package notice

import "sync/atomic"

type phase int32

const (
	PhaseStartup  phase = 0
	PhaseRunning  phase = 1
	PhaseTeardown phase = 2
)

var currentPhase atomic.Int32 // zero value = PhaseStartup

func Phase() phase     { return phase(currentPhase.Load()) }
func setPhase(p phase) { currentPhase.Store(int32(p)) }
func SetRunning()      { setPhase(PhaseRunning) }
func SetTeardown()     { setPhase(PhaseTeardown) }
