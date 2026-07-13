package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"
)

// MetricsServer serves the Prometheus text exposition format on a
// dedicated HTTP listener, separate from proxied traffic.
type MetricsServer struct {
	ln     net.Listener
	srv    *http.Server
	logger *slog.Logger
}

// NewMetricsServer creates a MetricsServer that will serve metrics
// from the given registry at the configured address and path.
func NewMetricsServer(address, path string, registry *Registry, logger *slog.Logger) (*MetricsServer, error) {
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("metrics listen on %s: %w", address, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if err := WriteExposition(w, registry); err != nil {
			logger.Error("failed to write metrics exposition",
				slog.String("error", err.Error()))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	return &MetricsServer{
		ln:     ln,
		srv:    srv,
		logger: logger,
	}, nil
}

// Start begins serving metrics. It blocks until ctx is cancelled.
func (ms *MetricsServer) Start(ctx context.Context) {
	ms.logger.Info("metrics server started",
		slog.String("address", ms.ln.Addr().String()))
	if err := ms.srv.Serve(ms.ln); err != nil && err != http.ErrServerClosed {
		ms.logger.Error("metrics server error",
			slog.String("error", err.Error()))
	}
}

// Addr returns the listener's address.
func (ms *MetricsServer) Addr() net.Addr {
	return ms.ln.Addr()
}

// Close gracefully shuts down the metrics server.
func (ms *MetricsServer) Close(ctx context.Context) error {
	return ms.srv.Shutdown(ctx)
}

// File returns a duplicated file descriptor for the metrics listener.
// The returned *os.File is suitable for passing to a child process
// via exec.Cmd.ExtraFiles.
func (ms *MetricsServer) File() (*os.File, error) {
	tcpLn, ok := ms.ln.(*net.TCPListener)
	if !ok {
		return nil, fmt.Errorf("metrics listener is %T", ms.ln)
	}
	return tcpLn.File()
}

// Listener returns the underlying net.Listener.
func (ms *MetricsServer) Listener() net.Listener {
	return ms.ln
}

// NewMetricsServerFromListener creates a MetricsServer using a
// pre-bound listener. Used during handoff reconstruction.
func NewMetricsServerFromListener(ln net.Listener, path string, registry *Registry, logger *slog.Logger) *MetricsServer {
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if err := WriteExposition(w, registry); err != nil {
			logger.Error("failed to write metrics exposition",
				slog.String("error", err.Error()))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	return &MetricsServer{
		ln:     ln,
		srv:    srv,
		logger: logger,
	}
}
