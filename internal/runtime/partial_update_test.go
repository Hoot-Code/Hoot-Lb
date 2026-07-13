package runtime

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
	"unsafe"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/discovery"
	"github.com/Hoot-Code/Hoot-Lb/internal/health"
)

func TestUpdatePoolBackendsDoesNotDisturbOtherPools(t *testing.T) {
	serversA := []*balancer.Server{
		balancer.NewServer("10.0.0.1:8080", 1),
		balancer.NewServer("10.0.0.2:8080", 1),
	}
	backendsA := []balancer.Backend{serversA[0], serversA[1]}
	lbA := balancer.NewRoundRobin(backendsA)

	serversB := []*balancer.Server{
		balancer.NewServer("10.0.1.1:8080", 1),
	}
	backendsB := []balancer.Backend{serversB[0]}
	lbB := balancer.NewRoundRobin(backendsB)

	initialSnap := &Snapshot{
		PoolStates: map[string]*PoolState{
			"pool_a": {LB: lbA, Servers: serversA},
			"pool_b": {LB: lbB, Servers: serversB},
		},
		Checkers: map[string]health.HealthChecker{},
	}

	poolBBefore := initialSnap.PoolStates["pool_b"]
	lbBBefore := poolBBefore.LB
	serversBBefore := poolBBefore.Servers

	newBackends := []discovery.Backend{
		{Address: "10.0.0.10:8080", Weight: 1},
		{Address: "10.0.0.11:8080", Weight: 1},
		{Address: "10.0.0.12:8080", Weight: 1},
	}

	cfgA := config.PoolConfig{
		Name:      "pool_a",
		Algorithm: "round_robin",
	}

	newSnap, err := UpdatePoolBackends(initialSnap, "pool_a", newBackends, cfgA, testUpdateLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	poolBAfter := newSnap.PoolStates["pool_b"]
	if poolBAfter.LB != lbBBefore {
		t.Error("pool_b LB was rebuilt — expected pointer identity")
	}
	if len(poolBAfter.Servers) != len(serversBBefore) || (len(poolBAfter.Servers) > 0 && unsafe.Pointer(&poolBAfter.Servers[0]) != unsafe.Pointer(&serversBBefore[0])) {
		t.Error("pool_b Servers was rebuilt — expected pointer identity")
	}

	poolAAfter := newSnap.PoolStates["pool_a"]
	if len(poolAAfter.Servers) != 3 {
		t.Errorf("expected pool_a to have 3 servers, got %d", len(poolAAfter.Servers))
	}
}

func TestUpdatePoolBackendsHealthCheckerStartBeforeStop(t *testing.T) {
	servers := []*balancer.Server{
		balancer.NewServer("10.0.0.1:8080", 1),
	}
	backends := []balancer.Backend{servers[0]}
	lb := balancer.NewRoundRobin(backends)

	hc := health.NewChecker(config.PoolConfig{
		Name:      "pool_a",
		Algorithm: "round_robin",
		HealthCheck: &config.HealthCheckConfig{
			Type:               "tcp",
			Interval:           5 * time.Second,
			Timeout:            2 * time.Second,
			HealthyThreshold:   2,
			UnhealthyThreshold: 3,
		},
	}, servers, testUpdateLogger(), nil, nil)
	hc.Start(context.Background())

	initialSnap := &Snapshot{
		PoolStates: map[string]*PoolState{
			"pool_a": {LB: lb, Servers: servers, FR: hc},
		},
		Checkers: map[string]health.HealthChecker{
			"pool_a": hc,
		},
	}

	oldChecker := initialSnap.Checkers["pool_a"]

	newBackends := []discovery.Backend{
		{Address: "10.0.0.10:8080", Weight: 1},
	}
	cfgA := config.PoolConfig{
		Name:      "pool_a",
		Algorithm: "round_robin",
		HealthCheck: &config.HealthCheckConfig{
			Type:               "tcp",
			Interval:           5 * time.Second,
			Timeout:            2 * time.Second,
			HealthyThreshold:   2,
			UnhealthyThreshold: 3,
		},
	}

	newSnap, err := UpdatePoolBackends(initialSnap, "pool_a", newBackends, cfgA, testUpdateLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Start new checker BEFORE swap.
	newSnap.Checkers["pool_a"].Start(context.Background())

	// Swap.
	initialSnap = newSnap

	// Stop old checker AFTER swap.
	oldChecker.Stop()

	newSnap.Checkers["pool_a"].Stop()
}

func TestUpdatePoolBackendsNoHealthCheck(t *testing.T) {
	servers := []*balancer.Server{
		balancer.NewServer("10.0.0.1:8080", 1),
	}
	backends := []balancer.Backend{servers[0]}
	lb := balancer.NewRoundRobin(backends)

	initialSnap := &Snapshot{
		PoolStates: map[string]*PoolState{
			"pool_a": {LB: lb, Servers: servers},
		},
		Checkers: map[string]health.HealthChecker{},
	}

	newBackends := []discovery.Backend{
		{Address: "10.0.0.10:8080", Weight: 1},
	}
	cfgA := config.PoolConfig{
		Name:      "pool_a",
		Algorithm: "round_robin",
		HealthCheck: &config.HealthCheckConfig{
			Type: "none",
		},
	}

	newSnap, err := UpdatePoolBackends(initialSnap, "pool_a", newBackends, cfgA, testUpdateLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(newSnap.Checkers) != 0 {
		t.Errorf("expected no checkers, got %d", len(newSnap.Checkers))
	}

	poolA := newSnap.PoolStates["pool_a"]
	if len(poolA.Servers) != 1 {
		t.Errorf("expected 1 server, got %d", len(poolA.Servers))
	}
	if poolA.Servers[0].Address() != "10.0.0.10:8080" {
		t.Errorf("expected %q, got %q", "10.0.0.10:8080", poolA.Servers[0].Address())
	}
}

func testUpdateLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
