package runtime

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/discovery"
	"github.com/Hoot-Code/Hoot-Lb/internal/health"
)

type countingDiscovery struct {
	inner    discovery.Discovery
	count    atomic.Int32
	returnFn func() ([]discovery.Backend, error)
}

func (c *countingDiscovery) Resolve(ctx context.Context) ([]discovery.Backend, error) {
	c.count.Add(1)
	if c.returnFn != nil {
		return c.returnFn()
	}
	return c.inner.Resolve(ctx)
}

func (c *countingDiscovery) Name() string { return c.inner.Name() }

func TestDiscoveryResilienceThroughRealTraffic(t *testing.T) {
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer backendLn.Close()

	backendAddr := backendLn.Addr().String()

	backendSrv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "ok")
		}),
	}
	go backendSrv.Serve(backendLn)
	defer backendSrv.Shutdown(context.Background())

	servers := []*balancer.Server{
		balancer.NewServer(backendAddr, 1),
	}
	balancerBackends := []balancer.Backend{servers[0]}
	lb := balancer.NewRoundRobin(balancerBackends)

	snap := &Snapshot{
		PoolStates: map[string]*PoolState{
			"pool_a": {LB: lb, Servers: servers},
		},
		Checkers: map[string]health.HealthChecker{},
	}
	snapPtr := NewAtomicSnapshot(snap)

	var failCount atomic.Int32
	disc := &countingDiscovery{
		inner: discovery.NewStatic("test", nil),
		returnFn: func() ([]discovery.Backend, error) {
			n := failCount.Add(1)
			if n <= 200 {
				return nil, fmt.Errorf("dns: server failure #%d", n)
			}
			return []discovery.Backend{
				{Address: backendAddr, Weight: 1},
			}, nil
		},
	}

	cfg := config.PoolConfig{
		Name:      "pool_a",
		Algorithm: "round_robin",
	}

	poller := NewPoller(disc, snapPtr, "pool_a", cfg, 20*time.Millisecond, testIntegLogger(), nil)
	go poller.Run()

	for i := 0; i < 250; i++ {
		snap := snapPtr.Load()
		ps := snap.PoolStates["pool_a"]
		if ps == nil {
			t.Fatalf("iteration %d: pool_a missing from snapshot", i)
		}
		b, err := ps.LB.Pick(context.Background())
		if err != nil {
			t.Fatalf("iteration %d: Pick failed: %v", i, err)
		}
		if b == nil {
			t.Fatalf("iteration %d: Pick returned nil backend", i)
		}
		if b.Address() != backendAddr {
			t.Fatalf("iteration %d: expected address %s, got %s", i, backendAddr, b.Address())
		}
		time.Sleep(5 * time.Millisecond)
	}

	poller.Stop()
}

func TestPollerNoOpOnIdenticalResolution(t *testing.T) {
	fixed := []discovery.Backend{
		{Address: "10.0.0.1:8080", Weight: 1},
		{Address: "10.0.0.2:8080", Weight: 1},
	}
	disc := discovery.NewStatic("test", fixed)
	cd := &countingDiscovery{inner: disc}

	servers := []*balancer.Server{
		balancer.NewServer("10.0.0.1:8080", 1),
		balancer.NewServer("10.0.0.2:8080", 1),
	}
	balancerBackends := []balancer.Backend{servers[0], servers[1]}
	lb := balancer.NewRoundRobin(balancerBackends)

	snap := &Snapshot{
		PoolStates: map[string]*PoolState{
			"pool_a": {LB: lb, Servers: servers},
		},
		Checkers: map[string]health.HealthChecker{},
	}
	snapPtr := NewAtomicSnapshot(snap)
	originalSnapPtr := snapPtr.Load()

	cfg := config.PoolConfig{
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

	poller := NewPoller(cd, snapPtr, "pool_a", cfg, 20*time.Millisecond, testIntegLogger(), nil)
	go poller.Run()

	time.Sleep(200 * time.Millisecond)

	if snapPtr.Load() != originalSnapPtr {
		t.Error("snapshot was swapped on identical resolution — expected pointer identity")
	}

	if cd.count.Load() < 3 {
		t.Errorf("expected at least 3 resolve calls, got %d", cd.count.Load())
	}

	poller.Stop()
}

func TestPollerErrorResilience(t *testing.T) {
	callCount := 0
	disc := &countingDiscovery{
		inner: discovery.NewStatic("test", nil),
		returnFn: func() ([]discovery.Backend, error) {
			callCount++
			if callCount <= 100 {
				return nil, fmt.Errorf("dns: server failure")
			}
			return []discovery.Backend{
				{Address: "10.0.0.1:8080", Weight: 1},
			}, nil
		},
	}

	servers := []*balancer.Server{
		balancer.NewServer("10.0.0.1:8080", 1),
		balancer.NewServer("10.0.0.2:8080", 1),
	}
	balancerBackends := []balancer.Backend{servers[0], servers[1]}
	lb := balancer.NewRoundRobin(balancerBackends)

	snap := &Snapshot{
		PoolStates: map[string]*PoolState{
			"pool_a": {LB: lb, Servers: servers},
		},
		Checkers: map[string]health.HealthChecker{},
	}
	snapPtr := NewAtomicSnapshot(snap)
	originalSnapPtr := snapPtr.Load()

	cfg := config.PoolConfig{
		Name:      "pool_a",
		Algorithm: "round_robin",
	}

	poller := NewPoller(disc, snapPtr, "pool_a", cfg, 20*time.Millisecond, testIntegLogger(), nil)
	go poller.Run()

	time.Sleep(300 * time.Millisecond)

	if snapPtr.Load() != originalSnapPtr {
		t.Error("snapshot was swapped during error — expected pointer identity")
	}

	poolA := snapPtr.Load().PoolStates["pool_a"]
	if len(poolA.Servers) != 2 {
		t.Errorf("expected 2 servers (last-known-good), got %d", len(poolA.Servers))
	}

	poller.Stop()
}

func TestPollerDoesNotDisturbOtherPools(t *testing.T) {
	disc := discovery.NewStatic("test", []discovery.Backend{
		{Address: "10.0.0.10:8080", Weight: 1},
		{Address: "10.0.0.11:8080", Weight: 1},
	})

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

	snap := &Snapshot{
		PoolStates: map[string]*PoolState{
			"pool_a": {LB: lbA, Servers: serversA},
			"pool_b": {LB: lbB, Servers: serversB},
		},
		Checkers: map[string]health.HealthChecker{},
	}
	snapPtr := NewAtomicSnapshot(snap)

	poolBBefore := snapPtr.Load().PoolStates["pool_b"]
	lbBBefore := poolBBefore.LB
	serversBBefore := poolBBefore.Servers

	cfg := config.PoolConfig{
		Name:      "pool_a",
		Algorithm: "round_robin",
	}

	poller := NewPoller(disc, snapPtr, "pool_a", cfg, 20*time.Millisecond, testIntegLogger(), nil)
	go poller.Run()

	time.Sleep(200 * time.Millisecond)

	poolBAfter := snapPtr.Load().PoolStates["pool_b"]
	if poolBAfter.LB != lbBBefore {
		t.Error("pool_b LB was rebuilt — expected pointer identity")
	}
	if len(poolBAfter.Servers) != len(serversBBefore) || (len(poolBAfter.Servers) > 0 && poolBAfter.Servers[0] != serversBBefore[0]) {
		t.Error("pool_b Servers was rebuilt — expected pointer identity")
	}

	poller.Stop()
}

func TestBackendSetsEqual(t *testing.T) {
	tests := []struct {
		name     string
		a, b     []discovery.Backend
		expected bool
	}{
		{
			name:     "identical",
			a:        []discovery.Backend{{Address: "a:1", Weight: 1}, {Address: "b:1", Weight: 1}},
			b:        []discovery.Backend{{Address: "a:1", Weight: 1}, {Address: "b:1", Weight: 1}},
			expected: true,
		},
		{
			name:     "same set different order",
			a:        []discovery.Backend{{Address: "a:1", Weight: 1}, {Address: "b:1", Weight: 1}},
			b:        []discovery.Backend{{Address: "b:1", Weight: 1}, {Address: "a:1", Weight: 1}},
			expected: true,
		},
		{
			name:     "different addresses",
			a:        []discovery.Backend{{Address: "a:1", Weight: 1}},
			b:        []discovery.Backend{{Address: "b:1", Weight: 1}},
			expected: false,
		},
		{
			name:     "different lengths",
			a:        []discovery.Backend{{Address: "a:1", Weight: 1}},
			b:        []discovery.Backend{{Address: "a:1", Weight: 1}, {Address: "b:1", Weight: 1}},
			expected: false,
		},
		{
			name:     "duplicates",
			a:        []discovery.Backend{{Address: "a:1", Weight: 1}, {Address: "a:1", Weight: 1}},
			b:        []discovery.Backend{{Address: "a:1", Weight: 1}},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := backendSetsEqual(tt.a, tt.b); got != tt.expected {
				t.Errorf("backendSetsEqual() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func testIntegLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestPollerStopGoroutineLeak verifies that stopping multiple
// discovery pollers does not leak goroutines.
func TestPollerStopGoroutineLeak(t *testing.T) {
	baseline := runtime.NumGoroutine()

	snap := &Snapshot{
		PoolStates: map[string]*PoolState{
			"pool1": {Servers: []*balancer.Server{}},
			"pool2": {Servers: []*balancer.Server{}},
		},
	}
	snapPtr := NewAtomicSnapshot(snap)
	logger := testIntegLogger()

	var pollers []*Poller
	for i := 0; i < 5; i++ {
		disc := discovery.NewStatic(fmt.Sprintf("disc%d", i), []discovery.Backend{
			{Address: "127.0.0.1:9999", Weight: 1},
		})
		cfg := config.PoolConfig{
			Name: fmt.Sprintf("pool%d", (i%2)+1),
		}
		p := NewPoller(disc, snapPtr, cfg.Name, cfg, 100*time.Millisecond, logger, nil)
		pollers = append(pollers, p)
		go p.Run()
	}

	time.Sleep(500 * time.Millisecond)

	for _, p := range pollers {
		p.Stop()
	}

	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	time.Sleep(200 * time.Millisecond)

	current := runtime.NumGoroutine()
	if current > baseline+5 {
		t.Errorf("goroutine leak: baseline %d, now %d", baseline, current)
	}
}
