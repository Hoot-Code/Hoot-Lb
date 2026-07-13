package admin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/admin/dashboard"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/metrics"
	"github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

// Server is the admin HTTP server: the REST control-plane API and the
// embedded dashboard. Both ride on the same listener and the same
// lifecycle — there is no separate dashboard listener and no way to
// run one without the other.
type Server struct {
	ln      net.Listener
	srv     *http.Server
	logger  *slog.Logger
	sem     chan struct{}
	wsConns sync.Map
	wsWg    sync.WaitGroup
	audit   *AuditLogger
}

// NewServer builds the admin HTTP server: the REST control-plane API
// plus the dashboard's static assets and WebSocket feed. cfg.Enabled
// (checked by the caller, not here) and cfg.Address are the only
// config this adds; there is no separate dashboard config block.
// connActive may be nil if metrics are disabled — the dashboard then
// reports zero active connections for every backend instead of
// erroring. restartTrigger, when non-nil, is called by POST
// /admin/restart to initiate a zero-downtime restart.
func NewServer(cfg config.AdminConfig, snap *runtime.AtomicSnapshot, fullCfg *config.Config, logger *slog.Logger, connActive *metrics.GaugeVec, restartTrigger func() error) (*Server, error) {
	h := &handler{snap: snap, cfg: fullCfg, logger: logger, restartTrigger: restartTrigger}

	restMux := buildRESTMux(h)

	semSize := cfg.MaxConcurrentRequests
	if semSize <= 0 {
		semSize = 10
	}

	s := &Server{logger: logger, sem: make(chan struct{}, semSize)}

	// Set up auth middleware: RBAC or single-token.
	authenticatedREST, token, err := buildAuthMiddleware(cfg, restMux)
	if err != nil {
		return nil, err
	}

	// Set up audit logging if configured.
	if cfg.AuditLog != nil && cfg.AuditLog.Enabled {
		s.audit = NewAuditLogger(os.Stdout)
	}
	h.audit = s.audit

	limitedREST := s.concurrencyLimiter(authenticatedREST)

	assetHandler, err := dashboard.AssetHandler()
	if err != nil {
		return nil, fmt.Errorf("loading embedded dashboard assets: %w", err)
	}

	feed := dashboard.NewFeed(snap, fullCfg, connActive)
	wsHandler := dashboard.NewWebSocketHandler(token, feed, logger, s, &s.wsWg)

	root := http.NewServeMux()
	root.Handle("/admin/ws", wsHandler)
	root.Handle("/admin/", limitedREST)
	root.Handle("/", assetHandler)

	s.srv = &http.Server{
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("admin listen on %s: %w", cfg.Address, err)
	}
	s.ln = ln

	// Wrap with mTLS if configured.
	if cfg.MTLS != nil && cfg.MTLS.Enabled {
		tlsCfg, err := buildMTLSConfig(cfg.MTLS)
		if err != nil {
			return nil, fmt.Errorf("admin mTLS: %w", err)
		}
		s.ln = tls.NewListener(ln, tlsCfg)
	}

	return s, nil
}

// NewServerFromListener builds the admin HTTP server using a
// pre-bound listener instead of calling net.Listen. Used during
// handoff reconstruction when the child process inherits file
// descriptors from the parent.
func NewServerFromListener(cfg config.AdminConfig, ln net.Listener, snap *runtime.AtomicSnapshot, fullCfg *config.Config, logger *slog.Logger, connActive *metrics.GaugeVec, restartTrigger func() error) (*Server, error) {
	h := &handler{snap: snap, cfg: fullCfg, logger: logger, restartTrigger: restartTrigger}

	restMux := buildRESTMux(h)

	semSize := cfg.MaxConcurrentRequests
	if semSize <= 0 {
		semSize = 10
	}

	s := &Server{logger: logger, ln: ln, sem: make(chan struct{}, semSize)}

	authenticatedREST, token, err := buildAuthMiddleware(cfg, restMux)
	if err != nil {
		return nil, err
	}

	if cfg.AuditLog != nil && cfg.AuditLog.Enabled {
		s.audit = NewAuditLogger(os.Stdout)
	}
	h.audit = s.audit

	limitedREST := s.concurrencyLimiter(authenticatedREST)

	assetHandler, err := dashboard.AssetHandler()
	if err != nil {
		return nil, fmt.Errorf("loading embedded dashboard assets: %w", err)
	}

	feed := dashboard.NewFeed(snap, fullCfg, connActive)
	wsHandler := dashboard.NewWebSocketHandler(token, feed, logger, s, &s.wsWg)

	root := http.NewServeMux()
	root.Handle("/admin/ws", wsHandler)
	root.Handle("/admin/", limitedREST)
	root.Handle("/", assetHandler)

	s.srv = &http.Server{
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Wrap with mTLS if configured.
	if cfg.MTLS != nil && cfg.MTLS.Enabled {
		tlsCfg, err := buildMTLSConfig(cfg.MTLS)
		if err != nil {
			return nil, fmt.Errorf("admin mTLS: %w", err)
		}
		s.ln = tls.NewListener(ln, tlsCfg)
	}

	return s, nil
}

// buildRESTMux creates the REST API mux with all endpoint handlers.
func buildRESTMux(h *handler) *http.ServeMux {
	restMux := http.NewServeMux()
	restMux.HandleFunc("/admin/pools", h.listPools)
	restMux.HandleFunc("/admin/restart", h.restart)
	restMux.HandleFunc("/admin/pools/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasSuffix(path, "/drain") {
			h.drainBackend(w, r)
		} else if strings.HasSuffix(path, "/undrain") {
			h.undrainBackend(w, r)
		} else if strings.HasSuffix(path, "/backends") && r.Method == http.MethodPost {
			h.addBackend(w, r)
		} else if strings.Contains(path, "/backends/") && r.Method == http.MethodDelete {
			h.removeBackend(w, r)
		} else {
			http.NotFound(w, r)
		}
	})
	return restMux
}

// buildAuthMiddleware creates the appropriate auth middleware based on
// the admin config. Returns the middleware, the primary token (for
// dashboard WS auth), and any error.
func buildAuthMiddleware(cfg config.AdminConfig, restMux http.Handler) (http.Handler, string, error) {
	hasRoles := len(cfg.Roles) > 0

	if hasRoles {
		roles := make([]role, len(cfg.Roles))
		var firstToken string
		for i, rc := range cfg.Roles {
			token := os.Getenv(rc.TokenEnv)
			if token == "" {
				return nil, "", fmt.Errorf("admin role token env var %q is not set or empty", rc.TokenEnv)
			}
			if i == 0 {
				firstToken = token
			}
			perms := make(map[string]bool, len(rc.Permissions))
			for _, p := range rc.Permissions {
				perms[p] = true
			}
			roles[i] = role{token: token, permissions: perms}
		}
		return rbacMiddleware(roles, restMux), firstToken, nil
	}

	// Single-token mode (legacy).
	token := os.Getenv(cfg.TokenEnv)
	if token == "" {
		return nil, "", fmt.Errorf("admin token env var %q is not set or empty", cfg.TokenEnv)
	}
	return tokenMiddleware(token, restMux), token, nil
}

// buildMTLSConfig creates a tls.Config that requires client certificates
// signed by the configured CA. This is defense in depth — it runs in
// addition to bearer token auth, not instead of it.
func buildMTLSConfig(mcfg *config.AdminMTLSConfig) (*tls.Config, error) {
	caCert, err := os.ReadFile(mcfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("reading CA file %q: %w", mcfg.CAFile, err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA certificate from %q", mcfg.CAFile)
	}

	return &tls.Config{
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  pool,
		MinVersion: tls.VersionTLS12,
	}, nil
}

func (s *Server) concurrencyLimiter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
			next.ServeHTTP(w, r)
		default:
			http.Error(w, `{"error":"server busy"}`, http.StatusServiceUnavailable)
		}
	})
}

func (s *Server) Track(conn net.Conn) {
	s.wsConns.Store(conn, struct{}{})
}

func (s *Server) Untrack(conn net.Conn) {
	s.wsConns.Delete(conn)
}

func (s *Server) Start() {
	s.logger.Info("admin server started", slog.String("address", s.ln.Addr().String()))
	if err := s.srv.Serve(s.ln); err != nil && err != http.ErrServerClosed {
		s.logger.Error("admin server error", slog.String("error", err.Error()))
	}
}

func (s *Server) Close(ctx context.Context) error {
	err := s.srv.Shutdown(ctx)

	s.wsConns.Range(func(key, _ any) bool {
		if conn, ok := key.(net.Conn); ok {
			conn.Close()
		}
		return true
	})

	s.wsWg.Wait()

	return err
}

func (s *Server) File() (*os.File, error) {
	tcpLn, ok := s.ln.(*net.TCPListener)
	if !ok {
		return nil, fmt.Errorf("admin listener is %T", s.ln)
	}
	return tcpLn.File()
}

func (s *Server) Listener() net.Listener {
	return s.ln
}
