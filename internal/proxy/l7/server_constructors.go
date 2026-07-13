package l7

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/health"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
	"github.com/Hoot-Code/Hoot-Lb/internal/ratelimit"
	"github.com/Hoot-Code/Hoot-Lb/internal/tlsutil"
)

// NewL7Server creates an L7Server bound to the address in cfg. The
// caller must call Serve to begin accepting connections. The routes
// slice is built at startup time with each route's pool already
// resolved to a LoadBalancer and FailureReporter. If certStore is
// non-nil, the listener is wrapped with TLS termination using the
// store's GetCertificate callback.
func NewL7Server(
	cfg config.ListenerConfig,
	routes []Route,
	defaultLB balancer.LoadBalancer,
	defaultFR health.FailureReporter,
	logger *slog.Logger,
	certStore *tlsutil.CertStore,
) (*L7Server, error) {
	table := NewRouteTable(routes, defaultLB, defaultFR)
	return newL7Server(cfg, table, logger, certStore, nil, nil)
}

// NewL7ServerFromGetter creates an L7Server that reads its route table
// from the getter on each request, supporting hot reload. The getter
// is called per-request to get the current RouteTable. If certStore is
// non-nil, the listener is wrapped with TLS termination.
func NewL7ServerFromGetter(
	cfg config.ListenerConfig,
	getter RouteTableGetter,
	logger *slog.Logger,
	certStore *tlsutil.CertStore,
) (*L7Server, error) {
	return newL7Server(cfg, getter(), logger, certStore, nil, nil)
}

// NewL7ServerFromGetterWithMetrics creates an L7Server with metrics
// and rate limiting support. If m is nil, metrics are disabled. If
// rl is nil, rate limiting is disabled.
func NewL7ServerFromGetterWithMetrics(
	cfg config.ListenerConfig,
	getter RouteTableGetter,
	logger *slog.Logger,
	certStore *tlsutil.CertStore,
	m *L7Metrics,
	rl *ratelimit.Limiter,
) (*L7Server, error) {
	return newL7ServerFromGetter(cfg, getter, logger, certStore, m, rl)
}

// NewL7ServerFromGetterWithMetricsAndListener creates an L7Server
// using a pre-bound listener. If certStore is non-nil, the listener
// is wrapped with TLS. Used during handoff reconstruction.
func NewL7ServerFromGetterWithMetricsAndListener(
	cfg config.ListenerConfig,
	getter RouteTableGetter,
	logger *slog.Logger,
	certStore *tlsutil.CertStore,
	m *L7Metrics,
	rl *ratelimit.Limiter,
	ln net.Listener,
) *L7Server {
	tcpLn := ln
	if certStore != nil {
		tlsLn := tls.NewListener(ln, l7TLSConfig(cfg, certStore))
		if m != nil && m.TLSHandshakeDuration != nil {
			ln = &tlsHandshakeTimingListener{Listener: tlsLn, metrics: m.TLSHandshakeDuration, name: cfg.Name}
		} else {
			ln = tlsLn
		}
	}

	return buildL7Server(cfg, getter, logger, certStore, m, rl, tcpLn, ln)
}

// newL7Server creates an L7Server from a static RouteTable (no hot
// reload). It uses NewDirector directly instead of wrapping the table
// in a getter closure.
func newL7Server(
	cfg config.ListenerConfig,
	table *RouteTable,
	logger *slog.Logger,
	certStore *tlsutil.CertStore,
	m *L7Metrics,
	rl *ratelimit.Limiter,
) (*L7Server, error) {
	ln, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("HTTP listen on %s: %w", cfg.Address, err)
	}

	tcpLn := ln
	if certStore != nil {
		tlsLn := tls.NewListener(ln, l7TLSConfig(cfg, certStore))
		if m != nil && m.TLSHandshakeDuration != nil {
			ln = &tlsHandshakeTimingListener{Listener: tlsLn, metrics: m.TLSHandshakeDuration, name: cfg.Name}
		} else {
			ln = tlsLn
		}
	}

	return buildL7ServerStatic(cfg, table, logger, certStore, m, rl, tcpLn, ln), nil
}

func newL7ServerFromGetter(
	cfg config.ListenerConfig,
	getter RouteTableGetter,
	logger *slog.Logger,
	certStore *tlsutil.CertStore,
	m *L7Metrics,
	rl *ratelimit.Limiter,
) (*L7Server, error) {
	ln, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("HTTP listen on %s: %w", cfg.Address, err)
	}

	tcpLn := ln
	if certStore != nil {
		tlsLn := tls.NewListener(ln, l7TLSConfig(cfg, certStore))
		if m != nil && m.TLSHandshakeDuration != nil {
			ln = &tlsHandshakeTimingListener{Listener: tlsLn, metrics: m.TLSHandshakeDuration, name: cfg.Name}
		} else {
			ln = tlsLn
		}
	}

	return buildL7Server(cfg, getter, logger, certStore, m, rl, tcpLn, ln), nil
}

// buildL7Server assembles an L7Server from an already-bound tcpLn
// (used for File()/handoff) and its possibly-TLS-wrapped ln (used for
// Serve()/Accept()). It is shared by the fresh-listen and
// pre-bound-listener (handoff) construction paths so the transport,
// handler chain, and connection-limit semaphore are built identically
// in both places.
func buildL7Server(
	cfg config.ListenerConfig,
	getter RouteTableGetter,
	logger *slog.Logger,
	certStore *tlsutil.CertStore,
	m *L7Metrics,
	rl *ratelimit.Limiter,
	tcpLn net.Listener,
	ln net.Listener,
) *L7Server {
	transport := &wrappedTransport{
		base: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   defaultDialTimeout,
				DualStack: true,
			}).DialContext,
			TLSHandshakeTimeout:   defaultTLSHandshakeTimeout,
			ResponseHeaderTimeout: defaultResponseHeaderTimeout,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
		},
	}

	rp := &httputil.ReverseProxy{
		Director:      NewDirectorFromGetterWithMetrics(getter, m, cfg.Pool),
		Transport:     transport,
		FlushInterval: 100 * time.Millisecond,
		ModifyResponse: func(resp *http.Response) error {
			if si := GetStickyInfo(resp.Request.Context()); si != nil {
				setStickyCookie(resp.Header, si.CookieName, si.BackendAddr, time.Duration(si.TTL)*time.Second, certStore != nil)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if pickErr := GetError(r.Context()); pickErr != nil {
				err = pickErr
			}
			logger.Error("HTTP proxy error",
				slog.String("error", err.Error()),
				slog.String("host", r.Host),
				slog.String("path", r.URL.Path))
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		},
	}

	var handler http.Handler = rp
	if cfg.MaxRequestBodyBytes > 0 {
		handler = &bodyLimitHandler{
			inner:    handler,
			maxBytes: cfg.MaxRequestBodyBytes,
		}
	}
	if rl != nil {
		handler = &rateLimitHandler{
			inner:     handler,
			rateLimit: rl,
			logger:    logger,
		}
	}

	var connSem chan struct{}
	if cfg.Global.MaxConnectionsPerListener > 0 {
		connSem = make(chan struct{}, cfg.Global.MaxConnectionsPerListener)
	}

	return &L7Server{
		listener:      ln,
		tcpListener:   tcpLn,
		tlsTerminated: certStore != nil,
		server: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       90 * time.Second,
		},
		logger:              logger.With(logging.ComponentKey, "http_proxy", logging.ListenerKey, cfg.Name),
		rateLimit:           rl,
		connSem:             connSem,
		maxRequestBodyBytes: cfg.MaxRequestBodyBytes,
	}
}

// buildL7ServerStatic is like buildL7Server but uses NewDirector
// directly for a static RouteTable, avoiding the getter closure.
func buildL7ServerStatic(
	cfg config.ListenerConfig,
	table *RouteTable,
	logger *slog.Logger,
	certStore *tlsutil.CertStore,
	m *L7Metrics,
	rl *ratelimit.Limiter,
	tcpLn net.Listener,
	ln net.Listener,
) *L7Server {
	transport := &wrappedTransport{
		base: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   defaultDialTimeout,
				DualStack: true,
			}).DialContext,
			TLSHandshakeTimeout:   defaultTLSHandshakeTimeout,
			ResponseHeaderTimeout: defaultResponseHeaderTimeout,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
		},
	}

	rp := &httputil.ReverseProxy{
		Director:      NewDirector(table),
		Transport:     transport,
		FlushInterval: 100 * time.Millisecond,
		ModifyResponse: func(resp *http.Response) error {
			if si := GetStickyInfo(resp.Request.Context()); si != nil {
				setStickyCookie(resp.Header, si.CookieName, si.BackendAddr, time.Duration(si.TTL)*time.Second, certStore != nil)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if pickErr := GetError(r.Context()); pickErr != nil {
				err = pickErr
			}
			logger.Error("HTTP proxy error",
				slog.String("error", err.Error()),
				slog.String("host", r.Host),
				slog.String("path", r.URL.Path))
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		},
	}

	var handler http.Handler = rp
	if cfg.MaxRequestBodyBytes > 0 {
		handler = &bodyLimitHandler{
			inner:    handler,
			maxBytes: cfg.MaxRequestBodyBytes,
		}
	}
	if rl != nil {
		handler = &rateLimitHandler{
			inner:     handler,
			rateLimit: rl,
			logger:    logger,
		}
	}

	var connSem chan struct{}
	if cfg.Global.MaxConnectionsPerListener > 0 {
		connSem = make(chan struct{}, cfg.Global.MaxConnectionsPerListener)
	}

	return &L7Server{
		listener:      ln,
		tcpListener:   tcpLn,
		tlsTerminated: certStore != nil,
		server: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       90 * time.Second,
		},
		logger:              logger.With(logging.ComponentKey, "http_proxy", logging.ListenerKey, cfg.Name),
		rateLimit:           rl,
		connSem:             connSem,
		maxRequestBodyBytes: cfg.MaxRequestBodyBytes,
	}
}
