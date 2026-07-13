package health

import (
	"context"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
)

func TestPassiveFailureExcludesFromPick(t *testing.T) {
	servers := []*balancer.Server{
		balancer.NewServer("127.0.0.1:19999", 1),
		balancer.NewServer("127.0.0.1:19998", 1),
	}
	backends := make([]balancer.Backend, len(servers))
	for i, s := range servers {
		backends[i] = s
	}
	lb := balancer.NewRoundRobin(backends)

	logger := testLogger()
	cfg := config.PoolConfig{
		Name:      "test_pool",
		Algorithm: "round_robin",
		Backends:  []config.BackendConfig{{Address: "127.0.0.1:19999", Weight: 1}, {Address: "127.0.0.1:19998", Weight: 1}},
		HealthCheck: &config.HealthCheckConfig{
			Type:               "tcp",
			Interval:           10 * time.Second,
			Timeout:            500 * time.Millisecond,
			HealthyThreshold:   2,
			UnhealthyThreshold: 3,
		},
	}

	checker := NewChecker(cfg, servers, logger, nil, nil)
	if checker == nil {
		t.Fatal("expected non-nil checker")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checker.Start(ctx)
	defer checker.Stop()

	b, err := lb.Pick(context.Background())
	if err != nil {
		t.Fatalf("initial pick: %v", err)
	}
	if !b.IsHealthy() {
		t.Fatal("first pick should be healthy")
	}

	checker.ReportFailure(servers[0])

	if servers[0].IsHealthy() {
		t.Fatal("first backend should be unhealthy after passive failure report")
	}

	for i := 0; i < 100; i++ {
		b, err := lb.Pick(context.Background())
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if b.Address() == servers[0].Address() {
			t.Fatalf("pick %d: got unhealthy backend %s", i, b.Address())
		}
	}
}

func TestPassiveFailureImmediateExclusion(t *testing.T) {
	addr, stop := echoTCPServer(t)
	defer stop()

	servers := []*balancer.Server{balancer.NewServer(addr, 1)}
	backends := make([]balancer.Backend, 1)
	backends[0] = servers[0]
	lb := balancer.NewRoundRobin(backends)

	logger := testLogger()
	cfg := config.PoolConfig{
		Name:      "test_pool",
		Algorithm: "round_robin",
		Backends:  []config.BackendConfig{{Address: addr, Weight: 1}},
		HealthCheck: &config.HealthCheckConfig{
			Type:               "tcp",
			Interval:           10 * time.Second,
			Timeout:            500 * time.Millisecond,
			HealthyThreshold:   2,
			UnhealthyThreshold: 3,
		},
	}

	checker := NewChecker(cfg, servers, logger, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checker.Start(ctx)
	defer checker.Stop()

	if !servers[0].IsHealthy() {
		t.Fatal("backend should start healthy")
	}

	b, err := lb.Pick(context.Background())
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if b.Address() != addr {
		t.Fatalf("expected %s, got %s", addr, b.Address())
	}

	checker.ReportFailure(servers[0])

	if servers[0].IsHealthy() {
		t.Fatal("backend should be immediately unhealthy after passive failure")
	}

	_, err = lb.Pick(context.Background())
	if err == nil {
		t.Fatal("expected error when all backends are unhealthy")
	}
}

func TestConcurrentReportFailureAndProbe(t *testing.T) {
	_, stop := echoTCPServer(t)
	defer stop()

	addr := "127.0.0.1:19990"
	server := balancer.NewServer(addr, 1)
	servers := []*balancer.Server{server}

	logger := testLogger()
	cfg := config.PoolConfig{
		Name:      "race_test_pool",
		Algorithm: "round_robin",
		Backends:  []config.BackendConfig{{Address: addr, Weight: 1}},
		HealthCheck: &config.HealthCheckConfig{
			Type:               "tcp",
			Interval:           3 * time.Millisecond,
			Timeout:            1 * time.Second,
			HealthyThreshold:   2,
			UnhealthyThreshold: 2,
		},
	}

	checker := NewChecker(cfg, servers, logger, nil, nil)
	if checker == nil {
		t.Fatal("expected non-nil checker")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checker.Start(ctx)
	defer checker.Stop()

	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(300 * time.Millisecond)
		for time.Now().Before(deadline) {
			checker.ReportFailure(server)
		}
	}()

	<-done
}
