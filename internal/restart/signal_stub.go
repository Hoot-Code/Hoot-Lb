//go:build !darwin && !linux

package restart

import "log/slog"

// ListenSIGUSR2 is not available on this platform. It returns a no-op
// stop function.
func ListenSIGUSR2(fn func(), logger *slog.Logger) (stop func()) {
	logger.Warn("SIGUSR2 restart not supported on this platform",
		slog.String("component", "restart"))
	return func() {}
}

// Supported reports whether this platform supports FD-passing restart.
func Supported() bool {
	return false
}
