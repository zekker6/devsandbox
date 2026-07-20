package cmdpattern

import "sync"

// OwnedSet is a concurrent-safe set of resource IDs the sandbox created itself.
//
// Proxies use it to scope mutating operations: a request naming an ID that is
// not in the set is denied, so a sandbox can only act on windows/tabs/panes it
// opened, never on the user's own. The type parameter exists because kitty
// identifies windows by int and herdr identifies tabs and panes by string.
type OwnedSet[T comparable] struct {
	mu  sync.RWMutex
	ids map[T]struct{}
}

func NewOwnedSet[T comparable]() *OwnedSet[T] {
	return &OwnedSet[T]{ids: make(map[T]struct{})}
}

func (s *OwnedSet[T]) Add(id T) {
	s.mu.Lock()
	s.ids[id] = struct{}{}
	s.mu.Unlock()
}

func (s *OwnedSet[T]) Contains(id T) bool {
	s.mu.RLock()
	_, ok := s.ids[id]
	s.mu.RUnlock()
	return ok
}
