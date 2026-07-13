package admin

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

// TestDashboardWebSocketNeverLogsToken connects (and attempts to
// connect with a bad token) against an admin server whose logger
// writes to an in-memory buffer, then asserts the token never
// appears anywhere in the captured log output — covering both the
// "rejected" and "accepted" code paths in WebSocketHandler.ServeHTTP.
//
// This is a runtime check of the same property the self-verification
// checklist asks to confirm by inspection: every log statement in
// dashboard/websocket.go logs r.URL.Path only, never r.URL.String()
// or r.URL.RawQuery (see ServeHTTP and its doc comment in
// internal/admin/dashboard/websocket.go).
func TestDashboardWebSocketNeverLogsToken(t *testing.T) {
	t.Setenv("HOOT_LB_TEST_ADMIN_TOKEN", testToken)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	cfg := &config.Config{
		Pools: []config.PoolConfig{{
			Name:        "static-pool",
			Algorithm:   "round_robin",
			Backends:    []config.BackendConfig{{Address: "10.0.0.1:8080", Weight: 1}},
			HealthCheck: &config.HealthCheckConfig{Type: "none"},
		}},
	}
	adminCfg := config.AdminConfig{
		Enabled:               boolPtr(true),
		Address:               ":0",
		TokenEnv:              "HOOT_LB_TEST_ADMIN_TOKEN",
		MaxConcurrentRequests: 10,
	}

	snap, err := runtime.BuildSnapshot(cfg, logger)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	atomicSnap := runtime.NewAtomicSnapshot(snap)

	srv, err := NewServer(adminCfg, atomicSnap, cfg, logger, nil, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go srv.Start()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	defer srv.Close(closeCtx)

	baseURL := fmt.Sprintf("http://%s", srv.ln.Addr().String())

	// Accepted connection.
	conn, _, err := dialDashboardWS(baseURL, testToken)
	if err != nil {
		t.Fatalf("dialDashboardWS (valid token): %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := readWSTextFrame(conn); err != nil {
		t.Fatalf("readWSTextFrame: %v", err)
	}
	conn.Close()

	// Rejected connection — this is the path most likely to log the
	// raw URL "for debugging", which is exactly what must not happen.
	if _, resp, err := dialDashboardWS(baseURL, "totally-wrong-token"); err == nil {
		t.Fatal("expected rejection")
	} else if resp == nil || resp.StatusCode != 401 {
		t.Fatalf("expected 401, got resp=%v err=%v", resp, err)
	}

	// Close synchronously to ensure all WebSocket handler goroutines
	// have exited and finished writing to logBuf before we read it.
	// Server.Close calls wsWg.Wait() internally, so no sleep needed.
	srv.Close(closeCtx)

	logs := logBuf.String()
	if strings.Contains(logs, testToken) {
		t.Fatalf("admin token leaked into logs:\n%s", logs)
	}
	if strings.Contains(logs, "totally-wrong-token") {
		t.Fatalf("rejected token leaked into logs:\n%s", logs)
	}
	if strings.Contains(logs, "token=") {
		t.Fatalf("raw query string (containing token=...) leaked into logs:\n%s", logs)
	}

	t.Logf("captured admin log output (token-free):\n%s", logs)
}
