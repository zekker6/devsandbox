// internal/notice/phase_test.go
package notice

import "testing"

func TestPhaseDefaultIsStartup(t *testing.T) {
	p := Phase()
	if p != PhaseStartup {
		t.Fatalf("default phase = %v, want %v", p, PhaseStartup)
	}
}

func TestSetPhase(t *testing.T) {
	t.Cleanup(func() { setPhase(PhaseStartup) })
	setPhase(PhaseRunning)
	if Phase() != PhaseRunning {
		t.Fatal("setPhase(PhaseRunning) did not take effect")
	}
	setPhase(PhaseTeardown)
	if Phase() != PhaseTeardown {
		t.Fatal("setPhase(PhaseTeardown) did not take effect")
	}
}
