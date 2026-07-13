package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/health"
	"github.com/Hoot-Code/Hoot-Lb/internal/runtime"
	"github.com/Hoot-Code/Hoot-Lb/internal/tlsutil"
)

const testToken = "test-admin-token-12345"

func boolPtr(b bool) *bool { return &b }

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func startTestServer(t *testing.T) (string, *runtime.AtomicSnapshot, *config.Config, func()) {
	t.Helper()
	t.Setenv("HOOT_LB_TEST_ADMIN_TOKEN", testToken)

	cfg := &config.Config{
		Pools: []config.PoolConfig{
			{
				Name:      "static-pool",
				Algorithm: "round_robin",
				Backends: []config.BackendConfig{
					{Address: "10.0.0.1:8080", Weight: 1},
					{Address: "10.0.0.2:8080", Weight: 1},
				},
				HealthCheck: &config.HealthCheckConfig{Type: "none"},
			},
			{
				Name:      "discovery-pool",
				Algorithm: "round_robin",
				Discovery: &config.DiscoveryConfig{
					Type: "dns",
					DNS: &config.DNSDiscoveryConfig{
						Host: "nonexistent.test.local",
					},
				},
				HealthCheck: &config.HealthCheckConfig{Type: "none"},
			},
		},
	}

	adminCfg := config.AdminConfig{
		Enabled:               boolPtr(true),
		Address:               ":0",
		TokenEnv:              "HOOT_LB_TEST_ADMIN_TOKEN",
		MaxConcurrentRequests: 10,
	}

	logger := testLogger()

	// Build snapshot manually to avoid triggering real DNS resolution
	// for the discovery pool (which hangs on CI and macOS).
	staticBackends := []balancer.Backend{
		balancer.NewServer("10.0.0.1:8080", 1),
		balancer.NewServer("10.0.0.2:8080", 1),
	}
	staticServers := make([]*balancer.Server, len(staticBackends))
	for i, b := range staticBackends {
		staticServers[i] = b.(*balancer.Server)
	}
	snap := &runtime.Snapshot{
		PoolStates: map[string]*runtime.PoolState{
			"static-pool": {
				LB:      balancer.NewRoundRobin(staticBackends),
				Servers: staticServers,
			},
			"discovery-pool": {
				LB:      balancer.NewRoundRobin(nil),
				Servers: nil,
			},
		},
		CertStores: make(map[string]*tlsutil.CertStore),
		Listeners:  nil,
		Checkers:   make(map[string]health.HealthChecker),
	}

	atomicSnap := runtime.NewAtomicSnapshot(snap)

	adminSrv, err := NewServer(adminCfg, atomicSnap, cfg, logger, nil, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	go adminSrv.Start()

	time.Sleep(50 * time.Millisecond)

	baseURL := fmt.Sprintf("http://%s", adminSrv.ln.Addr().String())

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		adminSrv.Close(ctx)
	}

	return baseURL, atomicSnap, cfg, cleanup
}

func authHeader() string {
	return "Bearer " + testToken
}

func doReq(t *testing.T, method, url, bearerToken string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", bearerToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}
