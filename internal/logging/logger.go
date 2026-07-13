// Package logging provides a thin wrapper around the standard library's
// log/slog package, giving the rest of the project a single place to
// configure log output and a shared, consistent set of field names.
//
// Every component in the project should log through a *slog.Logger
// obtained from this package (typically via New, then narrowed with
// slog.Logger.With) rather than constructing its own handler, so that
// log level and output format stay consistent across the whole binary.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Field name constants. Every component that logs structured fields
// should reuse these keys rather than inventing new ones, so that log
// lines stay greppable and consistent across the codebase.
const (
	// ComponentKey identifies which subsystem emitted the log line,
	// e.g. "config", "listener", "healthcheck".
	ComponentKey = "component"
	// ListenerKey identifies the listener a log line relates to, by
	// its configured name.
	ListenerKey = "listener"
	// PoolKey identifies the backend pool a log line relates to, by
	// its configured name.
	PoolKey = "pool"
	// BackendKey identifies the backend a log line relates to, by its
	// dial address.
	BackendKey = "backend"
)

// ParseLevel converts a textual log level ("debug", "info", "warn", or
// "error", case-insensitive) into a slog.Level. It returns an error for
// any other value so that misconfigured log levels are caught at
// startup rather than silently defaulting.
func ParseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (want one of: debug, info, warn, error)", level)
	}
}

// New constructs the project's standard *slog.Logger, writing
// human-readable text records at the given level to w. Using a single
// constructor here means the output format and handler options can be
// changed in one place (e.g. switched to JSON) without touching every
// call site.
func New(level slog.Level, w io.Writer) *slog.Logger {
	handler := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
	})
	return slog.New(handler)
}
