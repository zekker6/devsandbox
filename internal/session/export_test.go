package session

import "devsandbox/internal/procstate"

// SetProbe pins how the store reads a record's pid. The uncertain answer is
// the only one the age backstop acts on and no pid produces it reliably on
// every host - inside a PID namespace pid 1 is the runner's own init and
// answers Live - so the tests that cover the backstop inject it.
func (s *Store) SetProbe(probe func(int) procstate.State) {
	s.probe = probe
}
