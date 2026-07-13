package l7

import (
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"

	"github.com/Hoot-Code/Hoot-Lb/internal/ratelimit"
)

// rateLimitHandler wraps an http.Handler with per-client rate limiting.
type rateLimitHandler struct {
	inner     http.Handler
	rateLimit *ratelimit.Limiter
	logger    *slog.Logger
}

// ServeHTTP checks rate limits before forwarding to the inner handler.
// Clients exceeding their rate receive a 429 Too Many Requests
// response with a Retry-After header.
func (h *rateLimitHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	clientIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		clientIP = r.RemoteAddr
	}

	if !h.rateLimit.Allow(clientIP) {
		h.logger.Debug("rate limited HTTP client",
			slog.String("client", clientIP),
			slog.String("path", r.URL.Path))
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
		return
	}

	h.inner.ServeHTTP(w, r)
}

// semLimitedListener wraps a net.Listener and enforces a maximum
// number of concurrent connections via a semaphore channel. Once the
// limit is reached, newly accepted connections are closed immediately
// and Accept keeps looping internally until a connection is admitted
// or a real listener error occurs -- so callers (net/http.Server)
// never see a spurious Accept error.
type semLimitedListener struct {
	net.Listener
	sem chan struct{}
}

func (l *semLimitedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.sem <- struct{}{}:
			return &semReleaseConn{Conn: conn, sem: l.sem}, nil
		default:
			conn.Close()
		}
	}
}

// semReleaseConn wraps a net.Conn so that closing it releases its
// semaphore slot exactly once, however many times Close is called.
type semReleaseConn struct {
	net.Conn
	sem      chan struct{}
	released int32
}

func (c *semReleaseConn) Close() error {
	if atomic.CompareAndSwapInt32(&c.released, 0, 1) {
		<-c.sem
	}
	return c.Conn.Close()
}
