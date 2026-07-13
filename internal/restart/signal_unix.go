//go:build darwin || linux

package restart

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// ListenSIGUSR2 registers a handler for SIGUSR2 that calls fn when
// the signal is received. It returns a stop function that unregisters
// the handler and drains any pending signal. On platforms that do not
// support SIGUSR2, use ListenRestartSignal from signal_stub.go.
func ListenSIGUSR2(fn func(), logger *slog.Logger) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR2)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-done:
				return
			case <-ch:
				logger.Info("SIGUSR2 received, triggering restart",
					slog.String("component", "restart"))
				fn()
			}
		}
	}()

	return func() {
		signal.Stop(ch)
		close(done)
	}
}

// Supported reports whether this platform supports FD-passing restart.
func Supported() bool {
	return true
}
