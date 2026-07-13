package health

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
)

func TestEndToEndHealthCheck(t *testing.T) {
	addr1, stop1 := echoTCPServer(t)
	defer stop1()

	addr2, stop2 := echoTCPServer(t)

	servers := []*balancer.Server{
		balancer.NewServer(addr1, 1),
		balancer.NewServer(addr2, 1),
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
		Backends: []config.BackendConfig{
			{Address: addr1, Weight: 1},
			{Address: addr2, Weight: 1},
		},
		HealthCheck: &config.HealthCheckConfig{
			Type:               "tcp",
			Interval:           200 * time.Millisecond,
			Timeout:            500 * time.Millisecond,
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

	time.Sleep(800 * time.Millisecond)

	if !servers[0].IsHealthy() || !servers[1].IsHealthy() {
		t.Fatal("both backends should be healthy initially")
	}

	stop2()

	time.Sleep(800 * time.Millisecond)

	if servers[1].IsHealthy() {
		t.Fatal("backend 2 should be unhealthy after being stopped")
	}

	for i := 0; i < 20; i++ {
		b, err := lb.Pick(context.Background())
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if b.Address() == servers[1].Address() {
			t.Fatalf("pick %d: got unhealthy backend %s", i, b.Address())
		}
	}

	ln2, err := net.Listen("tcp", addr2)
	if err != nil {
		t.Fatalf("restart backend 2: %v", err)
	}
	defer ln2.Close()
	go func() {
		for {
			conn, err := ln2.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				scanner := bufio.NewScanner(c)
				for scanner.Scan() {
					fmt.Fprintf(c, "%s\n", scanner.Text())
				}
			}(conn)
		}
	}()

	time.Sleep(800 * time.Millisecond)

	if !servers[1].IsHealthy() {
		t.Fatal("backend 2 should be healthy after restart and recovery checks")
	}

	seen := make(map[string]bool)
	for i := 0; i < 20; i++ {
		b, err := lb.Pick(context.Background())
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		seen[b.Address()] = true
	}
	if !seen[addr1] {
		t.Error("backend 1 should appear in picks")
	}
	if !seen[addr2] {
		t.Error("backend 2 should appear in picks after recovery")
	}
}

func TestGoroutineLeakCheck(t *testing.T) {
	const poolSize = 20

	addrs := make([]string, poolSize)
	stops := make([]func(), poolSize)
	for i := 0; i < poolSize; i++ {
		addr, stop := echoTCPServer(t)
		addrs[i] = addr
		stops[i] = stop
	}
	defer func() {
		for _, stop := range stops {
			stop()
		}
	}()

	servers := make([]*balancer.Server, poolSize)
	backends := make([]balancer.Backend, poolSize)
	cfgBackends := make([]config.BackendConfig, poolSize)
	for i := 0; i < poolSize; i++ {
		servers[i] = balancer.NewServer(addrs[i], 1)
		backends[i] = servers[i]
		cfgBackends[i] = config.BackendConfig{Address: addrs[i], Weight: 1}
	}
	_ = balancer.NewRoundRobin(backends)

	logger := testLogger()
	cfg := config.PoolConfig{
		Name:      "leak_test_pool",
		Algorithm: "round_robin",
		Backends:  cfgBackends,
		HealthCheck: &config.HealthCheckConfig{
			Type:               "tcp",
			Interval:           50 * time.Millisecond,
			Timeout:            500 * time.Millisecond,
			HealthyThreshold:   2,
			UnhealthyThreshold: 3,
		},
	}

	checker := NewChecker(cfg, servers, logger, nil, nil)
	if checker == nil {
		t.Fatal("expected non-nil checker")
	}

	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	checker.Start(ctx)

	time.Sleep(300 * time.Millisecond)

	checker.Stop()
	cancel()

	time.Sleep(300 * time.Millisecond)
	runtime.GC()
	time.Sleep(200 * time.Millisecond)

	current := runtime.NumGoroutine()
	if current > baseline+5 {
		t.Errorf("goroutine leak: baseline %d, now %d (expected at most %d)",
			baseline, current, baseline+5)
	}
}
