package l4

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
	"github.com/Hoot-Code/Hoot-Lb/internal/metrics"
	"github.com/Hoot-Code/Hoot-Lb/internal/ratelimit"
)

// TCPServer is an L4 TCP proxy. It accepts connections on a configured
// listener and forwards each one to a backend selected by a
// balancer.LoadBalancer. Traffic is relayed bidirectionally with proper
// half-close handling.
type TCPServer struct {
	listener   net.Listener
	poolGetter PoolStateGetter
	cfg        config.ListenerConfig
	logger     *slog.Logger
	metrics    *ProxyMetrics
	accessLog  *metrics.AccessLogger
	rateLimit  *ratelimit.Limiter

	// connSem limits the number of concurrent connections. When nil,
	// the limit is unlimited (existing behavior).
	connSem chan struct{}

	// idleTimeout is the maximum idle duration for TCP relays. When
	// zero, no idle timeout is applied (existing behavior).
	idleTimeout time.Duration

	// mu protects closing and serializes with wg.Wait in Close.
	mu sync.Mutex

	// closing is set to true by Close before calling wg.Wait, so
	// that Serve can check it under mu before calling wg.Add(1),
	// preventing the WaitGroup misuse panic.
	closing bool

	// wg tracks in-flight connection handler goroutines. Close waits
	// on this to drain before returning.
	wg sync.WaitGroup

	// shutdown is closed by Close to signal Serve to exit.
	shutdown chan struct{}

	// active is incremented while a handler goroutine is running; used
	// by tests to wait for cleanup.
	active atomic.Int64
}

// NewTCPServer creates a TCPServer bound to the address in cfg. The
// caller must call Serve to begin accepting connections. The poolGetter
// is called per-connection to get the current LoadBalancer,
// FailureReporter, and Outcome, supporting hot reload of pool state.
// m and al may be nil to disable metrics and access logging. rl may
// be nil to disable rate limiting. maxConn limits concurrent connections
// per-listener (0 = unlimited). idleTimeout sets the TCP relay idle
// timeout (0 = disabled).
func NewTCPServer(cfg config.ListenerConfig, poolGetter PoolStateGetter, logger *slog.Logger, m *ProxyMetrics, al *metrics.AccessLogger, rl *ratelimit.Limiter, maxConn int, idleTimeout time.Duration) (*TCPServer, error) {
	ln, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("TCP listen on %s: %w", cfg.Address, err)
	}

	var connSem chan struct{}
	if maxConn > 0 {
		connSem = make(chan struct{}, maxConn)
	}

	return &TCPServer{
		listener:    ln,
		poolGetter:  poolGetter,
		cfg:         cfg,
		logger:      logger.With(logging.ComponentKey, "tcp_proxy", logging.ListenerKey, cfg.Name),
		metrics:     m,
		accessLog:   al,
		rateLimit:   rl,
		connSem:     connSem,
		idleTimeout: idleTimeout,
		shutdown:    make(chan struct{}),
	}, nil
}

// NewTCPServerFromListener creates a TCPServer using a pre-bound
// listener. This is used during handoff reconstruction when the child
// process inherits file descriptors from the parent.
func NewTCPServerFromListener(cfg config.ListenerConfig, ln net.Listener, poolGetter PoolStateGetter, logger *slog.Logger, m *ProxyMetrics, al *metrics.AccessLogger, rl *ratelimit.Limiter, maxConn int, idleTimeout time.Duration) *TCPServer {
	var connSem chan struct{}
	if maxConn > 0 {
		connSem = make(chan struct{}, maxConn)
	}

	return &TCPServer{
		listener:    ln,
		poolGetter:  poolGetter,
		cfg:         cfg,
		logger:      logger.With(logging.ComponentKey, "tcp_proxy", logging.ListenerKey, cfg.Name),
		metrics:     m,
		accessLog:   al,
		rateLimit:   rl,
		connSem:     connSem,
		idleTimeout: idleTimeout,
		shutdown:    make(chan struct{}),
	}
}

// Serve runs the accept loop, proxying each incoming connection to a
// backend. It returns when the listener is closed (via Close) or when
// ctx is cancelled.
func (s *TCPServer) Serve(ctx context.Context) {
	s.logger.Info("TCP proxy started",
		slog.String("address", s.listener.Addr().String()))

	var retryDelay time.Duration
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.shutdown:
				s.logger.Info("TCP proxy stopped accepting connections")
				return
			case <-ctx.Done():
				s.logger.Info("TCP proxy context cancelled")
				return
			default:
			}
			if delay, ok := acceptRetryDelay(err, retryDelay); ok {
				retryDelay = delay
				s.logger.Error("TCP proxy temporary accept error, retrying",
					slog.String("error", err.Error()),
					slog.Duration("retry_in", retryDelay))
				time.Sleep(retryDelay)
				continue
			}
			s.logger.Error("TCP proxy accept error",
				slog.String("error", err.Error()))
			return
		}
		retryDelay = 0

		if s.connSem != nil {
			select {
			case s.connSem <- struct{}{}:
			default:
				conn.Close()
				continue
			}
		}

		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			conn.Close()
			if s.connSem != nil {
				<-s.connSem
			}
			return
		}
		s.wg.Add(1)
		s.mu.Unlock()
		s.active.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.active.Add(-1)
			if s.connSem != nil {
				defer func() { <-s.connSem }()
			}
			s.handleConn(ctx, conn)
		}()
	}
}

// Addr returns the listener's network address. This is primarily
// useful for tests that need the dynamically assigned port.
func (s *TCPServer) Addr() net.Addr {
	return s.listener.Addr()
}

// File returns a duplicated file descriptor for the underlying
// listener. The returned *os.File is suitable for passing to a child
// process via exec.Cmd.ExtraFiles. The original listener remains
// usable after this call.
func (s *TCPServer) File() (*os.File, error) {
	tcpLn, ok := s.listener.(*net.TCPListener)
	if !ok {
		return nil, fmt.Errorf("TCP listener is %T, not *net.TCPListener", s.listener)
	}
	return tcpLn.File()
}

// Listener returns the underlying net.Listener.
func (s *TCPServer) Listener() net.Listener {
	return s.listener
}

// Close stops accepting new connections immediately, then waits for
// in-flight connections to finish. It returns nil if all connections
// drain before ctx expires, or ctx.Err() if the deadline is exceeded.
func (s *TCPServer) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()

	close(s.shutdown)
	s.listener.Close()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.logger.Info("TCP proxy drained all connections")
		return nil
	case <-ctx.Done():
		s.logger.Warn("TCP proxy shutdown timed out, some connections may be abandoned")
		return ctx.Err()
	}
}

// handleConn proxies a single client connection through to a backend.
func (s *TCPServer) handleConn(ctx context.Context, clientConn net.Conn) {
	defer clientConn.Close()

	clientIP, _, err := net.SplitHostPort(clientConn.RemoteAddr().String())
	if err != nil {
		s.logger.Error("failed to parse client address",
			slog.String("error", err.Error()))
	} else {
		ctx = context.WithValue(ctx, balancer.ClientKey{}, clientIP)
	}

	if s.rateLimit != nil && clientIP != "" && !s.rateLimit.Allow(clientIP) {
		s.logger.Debug("rate limited client",
			slog.String("client", clientIP))
		return
	}

	lb, fr, outcome := s.poolGetter()
	backend, err := lb.Pick(ctx)
	if err != nil {
		s.logger.Error("failed to pick backend",
			slog.String("error", err.Error()))
		return
	}

	start := time.Now()

	// Track the connection on the backend if the balancer supports it
	// (e.g. LeastConnections). This is a no-op for algorithms that
	// don't implement ConnReleaser.
	if cr, ok := lb.(balancer.ConnReleaser); ok {
		cr.Acquire(backend)
		defer cr.Release(backend)
	}

	if s.metrics != nil {
		s.metrics.ConnectionsTotal.With(s.cfg.Name, s.cfg.Pool, backend.Address(), "tcp").Add(1)
		s.metrics.ConnectionsActive.With(s.cfg.Name, s.cfg.Pool, backend.Address(), "tcp").Inc()
		defer s.metrics.ConnectionsActive.With(s.cfg.Name, s.cfg.Pool, backend.Address(), "tcp").Dec()
	}

	s.logger.Debug("proxying TCP connection",
		slog.String("client", clientConn.RemoteAddr().String()),
		slog.String(logging.BackendKey, backend.Address()))

	dialer := net.Dialer{Timeout: defaultConnectTimeout}
	backendConn, err := dialer.DialContext(ctx, "tcp", backend.Address())
	if err != nil {
		s.logger.Error("failed to dial backend",
			slog.String(logging.BackendKey, backend.Address()),
			slog.String("error", err.Error()))
		if fr != nil {
			fr.ReportFailure(backend)
		}
		if outcome != nil {
			outcome.RecordFailure(backend)
		}
		if s.metrics != nil {
			s.metrics.DialFailures.With(s.cfg.Name, s.cfg.Pool, backend.Address()).Add(1)
		}
		return
	}
	defer backendConn.Close()

	if outcome != nil {
		outcome.RecordSuccess(backend)
	}

	sent, received := relay(clientConn, backendConn, s.idleTimeout)

	if s.metrics != nil {
		s.metrics.BytesTransferred.With(s.cfg.Name, s.cfg.Pool, backend.Address(), "upstream").Add(uint64(sent))
		s.metrics.BytesTransferred.With(s.cfg.Name, s.cfg.Pool, backend.Address(), "downstream").Add(uint64(received))
	}

	if s.accessLog != nil {
		s.accessLog.Log(metrics.AccessLogEntry{
			Listener:      s.cfg.Name,
			Pool:          s.cfg.Pool,
			Backend:       backend.Address(),
			Protocol:      "tcp",
			ClientAddr:    clientIP,
			DurationMs:    time.Since(start).Milliseconds(),
			BytesSent:     sent,
			BytesReceived: received,
		})
	}
}

// relay copies bytes bidirectionally between client and backend, with
// correct TCP half-close handling. It returns the number of bytes
// sent upstream (client→backend) and received downstream
// (backend→client).
//
// When one direction's io.Copy returns (client or backend sent FIN, or
// an error occurred), the write half of the destination connection is
// closed to signal to the peer that no more data will be sent in that
// direction. The other direction continues until it also completes.
// Both goroutines must finish before relay returns, ensuring neither
// side leaks.
//
// If idleTimeout is positive, a deadline is set on both connections
// before each io.Copy call. The deadline resets whenever either
// direction forwards data, so this is an idle timeout — not a total
// connection lifetime cap.
func relay(client, backend net.Conn, idleTimeout time.Duration) (sent, received int64) {
	var wg sync.WaitGroup
	wg.Add(2)

	// client → backend
	go func() {
		defer wg.Done()
		if idleTimeout > 0 {
			backend.SetDeadline(time.Now().Add(idleTimeout))
		}
		n, _ := io.Copy(backend, client)
		atomic.AddInt64(&sent, n)
		if tc, ok := backend.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	// backend → client
	go func() {
		defer wg.Done()
		if idleTimeout > 0 {
			client.SetDeadline(time.Now().Add(idleTimeout))
		}
		n, _ := io.Copy(client, backend)
		atomic.AddInt64(&received, n)
		if tc, ok := client.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	wg.Wait()
	return
}
