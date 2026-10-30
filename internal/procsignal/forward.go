// Package procsignal relays termination signals while a launcher supervises a child.
package procsignal

import (
	"errors"
	"os"
	"os/signal"
	"syscall"

	"devsandbox/internal/notice"
)

// Forward returns a stop function. send may address one process or a process group.
func Forward(send func(os.Signal) error) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case sig := <-ch:
				if err := send(sig); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
					notice.Alert("forward signal to sandbox: %v", err)
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
		<-finished
	}
}
