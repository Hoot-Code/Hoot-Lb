package admin

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

func TestConcurrencyCap(t *testing.T) {
	t.Setenv("HOOT_LB_TEST_ADMIN_TOKEN", testToken)

	cfg := &config.Config{
		Pools: []config.PoolConfig{
			{
				Name:      "static-pool",
				Algorithm: "round_robin",
				Backends: []config.BackendConfig{
					{Address: "10.0.0.1:8080", Weight: 1},
				},
				HealthCheck: &config.HealthCheckConfig{Type: "none"},
			},
		},
	}

	adminCfg := config.AdminConfig{
		Enabled:               boolPtr(true),
		Address:               ":0",
		TokenEnv:              "HOOT_LB_TEST_ADMIN_TOKEN",
		MaxConcurrentRequests: 2,
	}

	logger := testLogger()

	snap, err := runtime.BuildSnapshot(cfg, logger)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	atomicSnap := runtime.NewAtomicSnapshot(snap)

	adminSrv, err := NewServer(adminCfg, atomicSnap, cfg, logger, nil, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	gate := make(chan struct{})

	slowHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-gate
		w.WriteHeader(http.StatusOK)
	})

	limited := adminSrv.concurrencyLimiter(slowHandler)

	ts := httptest.NewServer(limited)
	defer ts.Close()

	var wg sync.WaitGroup
	var okCount, busyCount atomic.Int32
	const totalRequests = 10

	for i := 0; i < totalRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(ts.URL + "/test")
			if err != nil {
				return
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				okCount.Add(1)
			} else if resp.StatusCode == http.StatusServiceUnavailable {
				busyCount.Add(1)
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()

	if okCount.Load() != 2 {
		t.Fatalf("expected exactly 2 OK, got %d", okCount.Load())
	}
	if busyCount.Load() != 8 {
		t.Fatalf("expected exactly 8 busy, got %d", busyCount.Load())
	}
	if okCount.Load()+busyCount.Load() != totalRequests {
		t.Fatalf("total responses %d != %d requests", okCount.Load()+busyCount.Load(), totalRequests)
	}
}

func TestAdminEndpoint_Simple(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	resp := doReq(t, http.MethodGet, baseURL+"/admin/pools", authHeader(), nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}
