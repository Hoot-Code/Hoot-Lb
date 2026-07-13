package admin

import (
	"context"
	"net/http"
	stdruntime "runtime"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

func TestCircuitBreakerChurn(t *testing.T) {
	t.Setenv("HOOT_LB_TEST_ADMIN_TOKEN", testToken)

	cbCfg := &config.CircuitBreakerConfig{
		FailureThreshold:  3,
		OpenDuration:      1 * time.Second,
		HalfOpenMaxProbes: 1,
	}

	cfg := &config.Config{
		Pools: []config.PoolConfig{
			{
				Name:      "cb-pool",
				Algorithm: "round_robin",
				Backends: []config.BackendConfig{
					{Address: "10.0.0.1:8080", Weight: 1},
					{Address: "10.0.0.2:8080", Weight: 1},
				},
				HealthCheck:    &config.HealthCheckConfig{Type: "none"},
				CircuitBreaker: cbCfg,
			},
		},
		Listeners: []config.ListenerConfig{
			{
				Name:     "http",
				Address:  "0.0.0.0:8080",
				Protocol: "http",
				Pool:     "cb-pool",
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

	snap, err := runtime.BuildSnapshot(cfg, logger)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	atomicSnap := runtime.NewAtomicSnapshot(snap)

	adminSrv, err := NewServer(adminCfg, atomicSnap, cfg, logger, nil, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	go adminSrv.Start()
	time.Sleep(50 * time.Millisecond)

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		adminSrv.Close(ctx)
	}()

	baseURL := "http://" + adminSrv.ln.Addr().String()

	// Step 1: Add a third backend via admin API, confirm circuit breaker exists.
	addReq := AddBackendRequest{Address: "10.0.0.3:8080", Weight: 1}
	resp := doReq(t, http.MethodPost, baseURL+"/admin/pools/cb-pool/backends", authHeader(), addReq)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add backend: expected 201, got %d", resp.StatusCode)
	}

	snapAfterAdd := atomicSnap.Load()
	ps := snapAfterAdd.PoolStates["cb-pool"]
	if ps == nil {
		t.Fatal("cb-pool not found in snapshot after add")
	}
	if len(ps.Servers) != 3 {
		t.Fatalf("expected 3 servers after add, got %d", len(ps.Servers))
	}
	if ps.Breakers == nil {
		t.Fatal("expected circuit breakers to be non-nil")
	}
	cb, ok := ps.Breakers["10.0.0.3:8080"]
	if !ok {
		t.Fatal("expected circuit breaker for newly added backend 10.0.0.3:8080")
	}

	// Step 2: Drive the new backend's breaker to open via repeated failures.
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}

	if cb.Allow() {
		t.Fatal("breaker should be in open state after threshold failures")
	}

	// Confirm Pick skips the circuit-open backend.
	seenAddrs := make(map[string]bool)
	for i := 0; i < 200; i++ {
		b, err := ps.LB.Pick(context.Background())
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		seenAddrs[b.Address()] = true
	}
	if seenAddrs["10.0.0.3:8080"] {
		t.Fatal("circuit-open backend 10.0.0.3:8080 should not be picked")
	}
	if !seenAddrs["10.0.0.1:8080"] || !seenAddrs["10.0.0.2:8080"] {
		t.Fatalf("expected both healthy backends to be picked, got: %v", seenAddrs)
	}

	// Step 3: Remove a different backend (10.0.0.1:8080) and confirm no panic/leak.
	goroutinesBefore := stdruntime.NumGoroutine()

	resp = doReq(t, http.MethodDelete, baseURL+"/admin/pools/cb-pool/backends/10.0.0.1:8080", authHeader(), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove backend: expected 204, got %d", resp.StatusCode)
	}

	snapAfterRemove := atomicSnap.Load()
	ps2 := snapAfterRemove.PoolStates["cb-pool"]
	if ps2 == nil {
		t.Fatal("cb-pool not found in snapshot after remove")
	}
	if len(ps2.Servers) != 2 {
		t.Fatalf("expected 2 servers after remove, got %d", len(ps2.Servers))
	}

	// Confirm only the remaining backends are picked.
	seenAddrs2 := make(map[string]bool)
	for i := 0; i < 200; i++ {
		b, err := ps2.LB.Pick(context.Background())
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		seenAddrs2[b.Address()] = true
	}
	if seenAddrs2["10.0.0.1:8080"] {
		t.Fatal("removed backend 10.0.0.1:8080 should not be picked")
	}

	// Give goroutines a moment to settle, then check for leaks.
	time.Sleep(200 * time.Millisecond)
	stdruntime.GC()
	time.Sleep(100 * time.Millisecond)
	goroutinesAfter := stdruntime.NumGoroutine()

	// Allow small fluctuations (background GC, test infrastructure)
	// but flag large leaks (> 5 goroutines above pre-removal baseline).
	if goroutinesAfter > goroutinesBefore+5 {
		t.Fatalf("goroutine leak detected: before=%d after=%d (delta=%d)",
			goroutinesBefore, goroutinesAfter, goroutinesAfter-goroutinesBefore)
	}
}
