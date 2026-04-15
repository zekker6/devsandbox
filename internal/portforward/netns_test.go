package portforward

import (
	"runtime"
	"testing"
)

func TestSharesHostNetNS_SelfIsShared(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc/self/ns/net is Linux-specific")
	}

	// The current process's own netns path must compare equal to itself.
	shared, err := SharesHostNetNS("/proc/self/ns/net")
	if err != nil {
		t.Fatalf("SharesHostNetNS: %v", err)
	}
	if !shared {
		t.Error("SharesHostNetNS(\"/proc/self/ns/net\") = false, want true")
	}
}

func TestSharesHostNetNS_MissingPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on Linux /proc semantics")
	}

	_, err := SharesHostNetNS("/proc/0/ns/net")
	if err == nil {
		t.Error("SharesHostNetNS on nonexistent path: err = nil, want error")
	}
}
