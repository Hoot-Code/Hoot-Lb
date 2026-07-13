package l7

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"

	"github.com/Hoot-Code/Hoot-Lb/internal/ratelimit"
)

// L7Server is an HTTP reverse proxy server. It accepts inbound HTTP
// requests on a configured listener and forwards each one to a
// backend selected by the route table (or the default pool when no
// route matches). Traffic is proxied using httputil.ReverseProxy with
// streaming support and proper connection lifecycle management.
type L7Server struct {
	listener      net.Listener
	tcpListener   net.Listener
	server        *http.Server
	logger        *slog.Logger
	tlsTerminated bool
	rateLimit     *ratelimit.Limiter

	// connSem limits the number of concurrent connections. When nil,
	// the limit is unlimited (existing behavior).
	connSem chan struct{}

	// maxRequestBodyBytes limits the request body size. When zero,
	// no limit is applied.
	maxRequestBodyBytes int64

	mu      sync.Mutex
	closing bool
}

// Addr returns the listener's network address. This is primarily
// useful for tests that need the dynamically assigned port.
func (s *L7Server) Addr() net.Addr {
	return s.listener.Addr()
}

// Serve runs the accept loop, proxying each incoming HTTP request to a
// backend. It returns when the server is shut down via Close.
func (s *L7Server) Serve(ctx context.Context) {
	s.logger.Info("HTTP proxy started",
		slog.String("address", s.listener.Addr().String()))

	ln := s.listener
	if s.connSem != nil {
		ln = &semLimitedListener{Listener: ln, sem: s.connSem}
	}

	if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
		s.logger.Error("HTTP proxy serve error",
			slog.String("error", err.Error()))
	}
}

// Close gracefully shuts down the HTTP server, waiting for in-flight
// requests to complete before returning. It returns nil if all
// requests drain before ctx expires, or ctx.Err() on timeout.
func (s *L7Server) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()

	if err := s.server.Shutdown(ctx); err != nil {
		s.logger.Warn("HTTP proxy shutdown timed out, some requests may be abandoned")
		return ctx.Err()
	}

	s.logger.Info("HTTP proxy drained all requests")
	return nil
}

// IsTLS reports whether this server terminates TLS.
func (s *L7Server) IsTLS() bool {
	return s.tlsTerminated
}

// File returns a duplicated file descriptor for the underlying
// TCP listener. The returned *os.File is suitable for passing to a
// child process via exec.Cmd.ExtraFiles. For TLS listeners the
// returned FD is for the TCP socket underneath the TLS wrapper.
func (s *L7Server) File() (*os.File, error) {
	tcpLn, ok := s.tcpListener.(*net.TCPListener)
	if !ok {
		return nil, fmt.Errorf("HTTP listener is %T", s.tcpListener)
	}
	return tcpLn.File()
}

// Listener returns the underlying net.Listener (which may be a
// tls.Listener wrapping a TCP listener).
func (s *L7Server) Listener() net.Listener {
	return s.listener
}
