package l4

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
	"github.com/Hoot-Code/Hoot-Lb/internal/metrics"
)

// TLSPassthroughServer accepts raw TCP connections, inspects the TLS
// ClientHello to extract the SNI hostname, routes the connection to a
// backend pool based on SNI, and relays the original encrypted bytes
// unmodified. It never performs a TLS handshake and never holds a
// private key.
type TLSPassthroughServer struct {
	listener         net.Listener
	sniRouter        SNIRouteGetter
	cfg              config.ListenerConfig
	logger           *slog.Logger
	handshakeTimeout time.Duration
	metrics          *ProxyMetrics
	accessLog        *metrics.AccessLogger

	// connSem limits the number of concurrent connections. When nil,
	// the limit is unlimited.
	connSem chan struct{}

	// idleTimeout is the maximum idle duration for TLS passthrough
	// relays. When zero, no idle timeout is applied.
	idleTimeout time.Duration

	mu      sync.Mutex
	closing bool
	wg      sync.WaitGroup

	shutdown chan struct{}

	active sync.WaitGroup
}

// NewTLSPassthroughServer creates a TLSPassthroughServer. The sniRouter
// is called per-connection to resolve the SNI hostname to a
// LoadBalancer and FailureReporter, supporting hot reload. m and al
// may be nil to disable metrics and access logging. maxConn limits
// concurrent connections per-listener (0 = unlimited). idleTimeout
// sets the relay idle timeout (0 = disabled).
func NewTLSPassthroughServer(
	cfg config.ListenerConfig,
	sniRouter SNIRouteGetter,
	logger *slog.Logger,
	m *ProxyMetrics,
	al *metrics.AccessLogger,
	maxConn int,
	idleTimeout time.Duration,
) (*TLSPassthroughServer, error) {
	if cfg.TLS == nil {
		return nil, fmt.Errorf("TLS passthrough server requires TLS configuration")
	}

	ln, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("TLS passthrough listen on %s: %w", cfg.Address, err)
	}

	handshakeTimeout := defaultHandshakeTimeout
	if cfg.TLS.HandshakeTimeout > 0 {
		handshakeTimeout = cfg.TLS.HandshakeTimeout
	}

	var connSem chan struct{}
	if maxConn > 0 {
		connSem = make(chan struct{}, maxConn)
	}

	return &TLSPassthroughServer{
		listener:         ln,
		sniRouter:        sniRouter,
		cfg:              cfg,
		logger:           logger.With(logging.ComponentKey, "tls_passthrough", logging.ListenerKey, cfg.Name),
		handshakeTimeout: handshakeTimeout,
		metrics:          m,
		accessLog:        al,
		connSem:          connSem,
		idleTimeout:      idleTimeout,
		shutdown:         make(chan struct{}),
	}, nil
}

// Serve runs the accept loop. For each connection it reads the
// ClientHello, extracts SNI, selects a backend pool, and relays
// traffic. It returns when the listener is closed via Close or when
// ctx is cancelled.
func (s *TLSPassthroughServer) Serve(ctx context.Context) {
	s.logger.Info("TLS passthrough proxy started",
		slog.String("address", s.listener.Addr().String()))

	var retryDelay time.Duration
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.shutdown:
				s.logger.Info("TLS passthrough proxy stopped accepting connections")
				return
			case <-ctx.Done():
				s.logger.Info("TLS passthrough proxy context cancelled")
				return
			default:
			}
			if delay, ok := acceptRetryDelay(err, retryDelay); ok {
				retryDelay = delay
				s.logger.Error("TLS passthrough proxy temporary accept error, retrying",
					slog.String("error", err.Error()),
					slog.Duration("retry_in", retryDelay))
				time.Sleep(retryDelay)
				continue
			}
			s.logger.Error("TLS passthrough proxy accept error",
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

// File returns a duplicated file descriptor for the underlying
// listener. The returned *os.File is suitable for passing to a child
// process via exec.Cmd.ExtraFiles.
func (s *TLSPassthroughServer) File() (*os.File, error) {
	tcpLn, ok := s.listener.(*net.TCPListener)
	if !ok {
		return nil, fmt.Errorf("TLS passthrough listener is %T", s.listener)
	}
	return tcpLn.File()
}

// Listener returns the underlying net.Listener.
func (s *TLSPassthroughServer) Listener() net.Listener {
	return s.listener
}

// NewTLSPassthroughServerFromListener creates a TLSPassthroughServer
// using a pre-bound listener. Used during handoff reconstruction.
func NewTLSPassthroughServerFromListener(cfg config.ListenerConfig, ln net.Listener, sniRouter SNIRouteGetter, logger *slog.Logger, m *ProxyMetrics, al *metrics.AccessLogger, maxConn int, idleTimeout time.Duration) *TLSPassthroughServer {
	handshakeTimeout := defaultHandshakeTimeout
	if cfg.TLS != nil && cfg.TLS.HandshakeTimeout > 0 {
		handshakeTimeout = cfg.TLS.HandshakeTimeout
	}

	var connSem chan struct{}
	if maxConn > 0 {
		connSem = make(chan struct{}, maxConn)
	}

	return &TLSPassthroughServer{
		listener:         ln,
		sniRouter:        sniRouter,
		cfg:              cfg,
		logger:           logger.With(logging.ComponentKey, "tls_passthrough", logging.ListenerKey, cfg.Name),
		handshakeTimeout: handshakeTimeout,
		metrics:          m,
		accessLog:        al,
		connSem:          connSem,
		idleTimeout:      idleTimeout,
		shutdown:         make(chan struct{}),
	}
}

// Close stops accepting new connections and waits for in-flight
// connections to drain.
func (s *TLSPassthroughServer) Close(ctx context.Context) error {
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
		s.logger.Info("TLS passthrough proxy drained all connections")
		return nil
	case <-ctx.Done():
		s.logger.Warn("TLS passthrough proxy shutdown timed out")
		return ctx.Err()
	}
}

// handleConn reads the ClientHello, routes by SNI, and relays to the
// selected backend.
func (s *TLSPassthroughServer) handleConn(ctx context.Context, clientConn net.Conn) {
	defer clientConn.Close()

	clientConn.SetReadDeadline(time.Now().Add(s.handshakeTimeout))

	raw, err := readClientHello(clientConn)
	if err != nil {
		s.logger.Warn("malformed TLS ClientHello",
			slog.String("client", clientConn.RemoteAddr().String()),
			slog.String("error", err.Error()))
		return
	}

	sni, err := ParseSNI(raw)
	if err != nil {
		s.logger.Warn("failed to parse SNI from ClientHello",
			slog.String("client", clientConn.RemoteAddr().String()),
			slog.String("error", err.Error()))
		return
	}

	clientConn.SetReadDeadline(time.Time{})

	lb, fr, outcome := s.sniRouter(sni)

	clientIP := extractClientIP(clientConn.RemoteAddr())
	ctx = context.WithValue(ctx, balancer.ClientKey{}, clientIP)

	backend, err := lb.Pick(ctx)
	if err != nil {
		s.logger.Error("failed to pick backend for TLS passthrough",
			slog.String("sni", sni),
			slog.String("error", err.Error()))
		return
	}

	start := time.Now()

	if cr, ok := lb.(balancer.ConnReleaser); ok {
		cr.Acquire(backend)
		defer cr.Release(backend)
	}

	if s.metrics != nil {
		s.metrics.ConnectionsTotal.With(s.cfg.Name, s.cfg.Pool, backend.Address(), "tcp").Add(1)
		s.metrics.ConnectionsActive.With(s.cfg.Name, s.cfg.Pool, backend.Address(), "tcp").Inc()
		defer s.metrics.ConnectionsActive.With(s.cfg.Name, s.cfg.Pool, backend.Address(), "tcp").Dec()
	}

	s.logger.Debug("TLS passthrough connecting",
		slog.String("client", clientConn.RemoteAddr().String()),
		slog.String("sni", sni),
		slog.String(logging.BackendKey, backend.Address()))

	dialer := net.Dialer{Timeout: defaultConnectTimeout}
	backendConn, err := dialer.DialContext(ctx, "tcp", backend.Address())
	if err != nil {
		s.logger.Error("failed to dial backend for TLS passthrough",
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

	// Wrap the client connection so the first Read replays the
	// buffered ClientHello bytes before reading fresh data.
	wrapped := &bufferedConn{
		Conn:      clientConn,
		remaining: raw,
	}

	sent, received := relay(wrapped, backendConn, s.idleTimeout)

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

// extractClientIP extracts the IP address from a net.Addr.
func extractClientIP(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

// bufferedConn wraps a net.Conn so that the first Read call replays
// the buffered bytes before reading fresh data from the underlying
// connection. This allows us to "unread" the ClientHello bytes we
// already consumed during SNI parsing.
type bufferedConn struct {
	net.Conn
	remaining []byte
}

// Read reads from the buffered bytes first, then from the underlying
// connection. It never returns both buffered and fresh data in a
// single call — the buffer is fully drained first.
func (bc *bufferedConn) Read(p []byte) (int, error) {
	if len(bc.remaining) > 0 {
		n := copy(p, bc.remaining)
		bc.remaining = bc.remaining[n:]
		return n, nil
	}
	return bc.Conn.Read(p)
}
