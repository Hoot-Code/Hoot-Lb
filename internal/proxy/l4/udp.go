package l4

import (
	"context"
	"fmt"
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

// udpSession represents a single client→backend UDP "connection". Each
// unique client address gets a dedicated outbound UDP socket to a
// selected backend. A reader goroutine runs for the lifetime of the
// session, forwarding response datagrams from the backend back to the
// client through the shared listener.
type udpSession struct {
	backendConn *net.UDPConn
	backend     balancer.Backend // original backend for ConnReleaser tracking
	lastActive  atomic.Int64     // UnixNano timestamp of last activity
}

// touch records the current time as the session's last activity.
func (s *udpSession) touch() {
	s.lastActive.Store(time.Now().UnixNano())
}

// idleDuration returns how long it has been since the last datagram
// was forwarded through this session.
func (s *udpSession) idleDuration() time.Duration {
	ts := s.lastActive.Load()
	if ts == 0 {
		return 0
	}
	return time.Since(time.Unix(0, ts))
}

// UDPServer is an L4 UDP proxy. It reads datagrams on a listening
// socket, maintains a session table keyed by client address, and
// relays return datagrams from backends to the correct client. Idle
// sessions are evicted to prevent unbounded memory growth.
type UDPServer struct {
	listener   *net.UDPConn
	poolGetter PoolStateGetter
	cfg        config.ListenerConfig
	logger     *slog.Logger
	metrics    *ProxyMetrics
	accessLog  *metrics.AccessLogger
	rateLimit  *ratelimit.Limiter

	mu       sync.Mutex
	sessions map[string]*udpSession

	// closing is set to true by Close before calling wg.Wait, so
	// that handleDatagram can check it under mu before calling
	// wg.Add(1), preventing the WaitGroup misuse panic.
	closing bool

	// wg tracks reader goroutines (one per active session) and the
	// eviction goroutine.
	wg sync.WaitGroup

	// evictStarted is closed by Serve after the eviction goroutine
	// has been registered with wg. Close waits on this before
	// calling wg.Wait to avoid racing with a delayed wg.Add.
	evictStarted chan struct{}

	// shutdown is closed by Close to signal the eviction goroutine.
	shutdown chan struct{}

	// sessionTimeout is the idle duration after which a session is
	// evicted. Exposed for testing.
	sessionTimeout time.Duration

	// cleanupInterval controls how often the eviction goroutine scans
	// for idle sessions. Exposed for testing.
	cleanupInterval time.Duration
}

// NewUDPServer creates a UDPServer bound to the address in cfg. The
// caller must call Serve to begin processing datagrams. The poolGetter
// is called per-session to get the current LoadBalancer,
// FailureReporter, and Outcome, supporting hot reload of pool state.
// m and al may be nil to disable metrics and access logging. rl may
// be nil to disable rate limiting.
func NewUDPServer(cfg config.ListenerConfig, poolGetter PoolStateGetter, logger *slog.Logger, m *ProxyMetrics, al *metrics.AccessLogger, rl *ratelimit.Limiter) (*UDPServer, error) {
	addr, err := net.ResolveUDPAddr("udp", cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("resolve UDP address %s: %w", cfg.Address, err)
	}

	ln, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("UDP listen on %s: %w", cfg.Address, err)
	}

	return &UDPServer{
		listener:        ln,
		poolGetter:      poolGetter,
		cfg:             cfg,
		logger:          logger.With(logging.ComponentKey, "udp_proxy", logging.ListenerKey, cfg.Name),
		metrics:         m,
		accessLog:       al,
		rateLimit:       rl,
		sessions:        make(map[string]*udpSession),
		shutdown:        make(chan struct{}),
		evictStarted:    make(chan struct{}),
		sessionTimeout:  defaultSessionTimeout,
		cleanupInterval: udpSessionCleanupInterval,
	}, nil
}

// Serve reads inbound datagrams and proxies them to backends. It
// returns when the listener is closed (via Close) or ctx is cancelled.
func (s *UDPServer) Serve(ctx context.Context) {
	s.logger.Info("UDP proxy started",
		slog.String("address", s.listener.LocalAddr().String()))

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.evictIdleSessions(ctx)
	}()
	close(s.evictStarted)

	buf := make([]byte, udpMaxDatagramSize)
	for {
		n, clientAddr, err := s.listener.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.shutdown:
				s.logger.Info("UDP proxy stopped accepting datagrams")
			case <-ctx.Done():
				s.logger.Info("UDP proxy context cancelled")
			default:
				s.logger.Error("UDP proxy read error",
					slog.String("error", err.Error()))
			}
			return
		}

		datagram := make([]byte, n)
		copy(datagram, buf[:n])
		s.handleDatagram(ctx, clientAddr, datagram)
	}
}

// Addr returns the listener's network address. This is primarily
// useful for tests that need the dynamically assigned port.
func (s *UDPServer) Addr() net.Addr {
	return s.listener.LocalAddr()
}

// File returns a duplicated file descriptor for the underlying UDP connection.
func (s *UDPServer) File() (*os.File, error) { return s.listener.File() }

// Listener returns the underlying *net.UDPConn.
func (s *UDPServer) Listener() *net.UDPConn { return s.listener }

// NewUDPServerFromConn creates a UDPServer using a pre-bound UDP connection.
func NewUDPServerFromConn(cfg config.ListenerConfig, conn *net.UDPConn, poolGetter PoolStateGetter, logger *slog.Logger, m *ProxyMetrics, al *metrics.AccessLogger, rl *ratelimit.Limiter) *UDPServer {
	return &UDPServer{
		listener: conn, poolGetter: poolGetter, cfg: cfg,
		logger:  logger.With(logging.ComponentKey, "udp_proxy", logging.ListenerKey, cfg.Name),
		metrics: m, accessLog: al, rateLimit: rl,
		sessions: make(map[string]*udpSession), shutdown: make(chan struct{}),
		evictStarted: make(chan struct{}), sessionTimeout: defaultSessionTimeout,
		cleanupInterval: udpSessionCleanupInterval,
	}
}

// Close stops accepting new datagrams, closes all active sessions, and
// waits for reader goroutines to finish. It returns nil if everything
// drains before ctx expires, or ctx.Err() on timeout.
func (s *UDPServer) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()

	close(s.shutdown)
	s.listener.Close()

	s.mu.Lock()
	lb, _, _ := s.poolGetter()
	for key, sess := range s.sessions {
		sess.backendConn.Close()
		if cr, ok := lb.(balancer.ConnReleaser); ok {
			cr.Release(sess.backend)
		}
		if s.metrics != nil {
			s.metrics.ConnectionsActive.With(s.cfg.Name, s.cfg.Pool, sess.backend.Address(), "udp").Dec()
		}
		delete(s.sessions, key)
	}
	s.mu.Unlock()

	// Wait for the eviction goroutine to be registered with wg
	// before calling wg.Wait, to avoid racing with a delayed Add.
	<-s.evictStarted

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.logger.Info("UDP proxy closed all sessions")
		return nil
	case <-ctx.Done():
		s.logger.Warn("UDP proxy shutdown timed out, some sessions may be abandoned")
		return ctx.Err()
	}
}

// handleDatagram forwards a single datagram from a client to the
// appropriate backend, creating a new session if one doesn't exist for
// this client address.
func (s *UDPServer) handleDatagram(ctx context.Context, clientAddr *net.UDPAddr, datagram []byte) {
	key := clientAddr.String()
	clientIP := clientAddr.IP.String()
	ctx = context.WithValue(ctx, balancer.ClientKey{}, clientIP)

	if s.rateLimit != nil && !s.rateLimit.Allow(clientIP) {
		s.logger.Debug("rate limited UDP client",
			slog.String("client", clientIP))
		return
	}

	lb, fr, outcome := s.poolGetter()

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	sess, exists := s.sessions[key]
	if !exists {
		backend, err := lb.Pick(ctx)
		if err != nil {
			s.mu.Unlock()
			s.logger.Error("failed to pick backend for UDP session",
				slog.String("client", key),
				slog.String("error", err.Error()))
			return
		}

		backendAddr, err := net.ResolveUDPAddr("udp", backend.Address())
		if err != nil {
			s.mu.Unlock()
			s.logger.Error("failed to resolve backend address",
				slog.String(logging.BackendKey, backend.Address()),
				slog.String("error", err.Error()))
			return
		}

		backendConn, err := net.DialUDP("udp", nil, backendAddr)
		if err != nil {
			s.mu.Unlock()
			s.logger.Error("failed to dial UDP backend",
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

		if outcome != nil {
			outcome.RecordSuccess(backend)
		}

		sess = &udpSession{backendConn: backendConn, backend: backend}
		sess.touch()
		s.sessions[key] = sess

		// Track the connection on the backend if the balancer
		// supports it (e.g. LeastConnections).
		if cr, ok := lb.(balancer.ConnReleaser); ok {
			cr.Acquire(backend)
		}

		if s.metrics != nil {
			s.metrics.ConnectionsTotal.With(s.cfg.Name, s.cfg.Pool, backend.Address(), "udp").Add(1)
			s.metrics.ConnectionsActive.With(s.cfg.Name, s.cfg.Pool, backend.Address(), "udp").Inc()
		}

		s.wg.Add(1)
		go s.readSession(ctx, clientAddr, sess)

		s.logger.Debug("new UDP session",
			slog.String("client", key),
			slog.String(logging.BackendKey, backend.Address()))
	}
	s.mu.Unlock()

	sess.touch()

	if _, err := sess.backendConn.Write(datagram); err != nil {
		s.logger.Error("failed to forward UDP datagram to backend",
			slog.String("client", key),
			slog.String("error", err.Error()))
	}
}

// readSession runs for the lifetime of a UDP session, reading response
// datagrams from the backend and forwarding them to the client through
// the shared listener. When the backend connection is closed (either
// by eviction or shutdown), the goroutine exits and decrements wg.
func (s *UDPServer) readSession(ctx context.Context, clientAddr *net.UDPAddr, sess *udpSession) {
	defer s.wg.Done()
	defer sess.backendConn.Close()

	buf := make([]byte, udpMaxDatagramSize)
	for {
		n, _, err := sess.backendConn.ReadFromUDP(buf)
		if err != nil {
			// Connection closed by eviction or shutdown — not an error.
			return
		}

		sess.touch()

		if _, err := s.listener.WriteToUDP(buf[:n], clientAddr); err != nil {
			s.logger.Debug("failed to return UDP datagram to client",
				slog.String("client", clientAddr.String()),
				slog.String("error", err.Error()))
		}
	}
}

// evictIdleSessions periodically scans the session table and closes
// any session that has been idle longer than sessionTimeout. It runs
// until the shutdown channel is closed.
func (s *UDPServer) evictIdleSessions(ctx context.Context) {
	ticker := time.NewTicker(s.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.shutdown:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.evictExpired()
		}
	}
}

// evictExpired closes and removes all sessions whose idle duration
// exceeds the timeout threshold.
func (s *UDPServer) evictExpired() {
	var evicted int

	lb, _, _ := s.poolGetter()
	s.mu.Lock()
	for key, sess := range s.sessions {
		if sess.idleDuration() > s.sessionTimeout {
			sess.backendConn.Close()
			delete(s.sessions, key)
			if cr, ok := lb.(balancer.ConnReleaser); ok {
				cr.Release(sess.backend)
			}
			if s.metrics != nil {
				s.metrics.ConnectionsActive.With(s.cfg.Name, s.cfg.Pool, sess.backend.Address(), "udp").Dec()
			}
			evicted++
		}
	}
	s.mu.Unlock()

	if evicted > 0 {
		s.logger.Debug("evicted idle UDP sessions",
			slog.Int("count", evicted))
	}
}
