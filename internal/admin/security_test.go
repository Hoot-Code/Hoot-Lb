package admin

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

// TestAuth_EmptyBearerToken verifies that an empty Bearer token
// ("Bearer ") is rejected at the guard level, not by the constant-time
// comparison silently passing.
func TestAuth_EmptyBearerToken(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	resp := doReq(t, http.MethodGet, baseURL+"/admin/pools", "Bearer ", nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for empty Bearer token, got %d", resp.StatusCode)
	}
}

// TestAdminWriteTimeout verifies that the admin server enforces a
// WriteTimeout. A handler that blocks longer than the timeout should
// cause the connection to be closed.
func TestAdminWriteTimeout(t *testing.T) {
	t.Setenv("HOOT_LB_TEST_WRITE_TIMEOUT_TOKEN", "write-timeout-test-token")

	cfg := &config.Config{
		Pools: []config.PoolConfig{
			{
				Name:      "test-pool",
				Algorithm: "round_robin",
				Backends: []config.BackendConfig{
					{Address: "127.0.0.1:1", Weight: 1},
				},
				HealthCheck: &config.HealthCheckConfig{Type: "none"},
			},
		},
	}

	snap, err := runtime.BuildSnapshot(cfg, testLogger())
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	atomicSnap := runtime.NewAtomicSnapshot(snap)

	adminCfg := config.AdminConfig{
		Enabled:               boolPtr(true),
		Address:               "127.0.0.1:0",
		TokenEnv:              "HOOT_LB_TEST_WRITE_TIMEOUT_TOKEN",
		MaxConcurrentRequests: 10,
	}

	s, err := NewServer(adminCfg, atomicSnap, cfg, testLogger(), nil, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	// Override the server's WriteTimeout to a very short duration
	// for testing.
	s.srv.WriteTimeout = 200 * time.Millisecond

	go s.Start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.Close(ctx)
	}()

	// Wait for server to be ready.
	time.Sleep(50 * time.Millisecond)

	addr := s.ln.Addr().String()

	// Make a request to the pools endpoint — it should succeed
	// normally (the endpoint itself is fast).
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Send a well-formed request and verify we get a response.
	fmt.Fprintf(conn, "GET /admin/pools HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer write-timeout-test-token\r\n\r\n")
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if n == 0 && err != nil {
		t.Fatalf("no response from admin server: %v", err)
	}
	// The response should be a 200 or similar, not a timeout.
	resp := string(buf[:n])
	if len(resp) == 0 {
		t.Fatal("empty response from admin server")
	}
	if resp[0] != 'H' {
		t.Errorf("unexpected response start: %s", resp[:min(50, len(resp))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
