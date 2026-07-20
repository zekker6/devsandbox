package cmdpattern

import (
	"sync"
	"testing"
)

func TestOwnedSetInt(t *testing.T) {
	s := NewOwnedSet[int]()

	if s.Contains(1) {
		t.Error("Contains(1) = true on empty set, want false")
	}

	s.Add(1)
	s.Add(2)

	if !s.Contains(1) {
		t.Error("Contains(1) = false after Add(1), want true")
	}
	if !s.Contains(2) {
		t.Error("Contains(2) = false after Add(2), want true")
	}
	if s.Contains(3) {
		t.Error("Contains(3) = true, want false")
	}
}

func TestOwnedSetString(t *testing.T) {
	s := NewOwnedSet[string]()

	// herdr identifies tabs and panes by opaque string ids.
	s.Add("tab-abc")

	if !s.Contains("tab-abc") {
		t.Error(`Contains("tab-abc") = false after Add, want true`)
	}
	if s.Contains("tab-xyz") {
		t.Error(`Contains("tab-xyz") = true, want false`)
	}
	if s.Contains("") {
		t.Error(`Contains("") = true, want false`)
	}
}

func TestOwnedSetAddIsIdempotent(t *testing.T) {
	s := NewOwnedSet[string]()
	s.Add("x")
	s.Add("x")

	if !s.Contains("x") {
		t.Error("Contains(x) = false after duplicate Add, want true")
	}
}

// TestOwnedSetConcurrent exercises the mutex under -race. The proxy adds ids
// from the upstream-response pump while the request pump reads them, so
// concurrent Add/Contains is the normal operating mode, not an edge case.
func TestOwnedSetConcurrent(t *testing.T) {
	s := NewOwnedSet[int]()

	const workers = 8
	const perWorker = 100

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := range perWorker {
				s.Add(w*perWorker + i)
			}
		}()
		go func() {
			defer wg.Done()
			for i := range perWorker {
				s.Contains(w*perWorker + i)
			}
		}()
	}
	wg.Wait()

	for w := range workers {
		for i := range perWorker {
			id := w*perWorker + i
			if !s.Contains(id) {
				t.Fatalf("Contains(%d) = false after all writers finished, want true", id)
			}
		}
	}
}
