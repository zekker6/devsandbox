package procsignal

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestForwardTerminationSignal(t *testing.T) {
	received := make(chan os.Signal, 1)
	stop := Forward(func(sig os.Signal) error {
		received <- sig
		return nil
	})
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case sig := <-received:
		if sig != syscall.SIGTERM {
			t.Fatalf("received %v, want SIGTERM", sig)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("termination signal was not forwarded")
	}
}
