package health

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
)

// serveOKOnce accepts connections on the given listener and writes
// "ok\n" to each until the listener is closed. It takes the listener
// as a parameter (rather than closing over a variable the caller may
// later reassign) so each call's goroutine only ever reads its own
// immutable local reference — reusing and reassigning a single shared
// listener variable across multiple listen/close/relisten cycles from
// several goroutine closures is a data race (the closing/reassigning
// goroutine writes the variable while an old accept-loop goroutine may
// still be reading it), even though the intent is for each old
// goroutine to have already exited via its Accept error by the time
// the variable is reassigned. There's no synchronization guaranteeing
// that ordering, so the race is real, not just theoretical.
func serveOKOnce(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		fmt.Fprintf(conn, "ok\n")
		conn.Close()
	}
}

func TestHysteresisAlternatingNeverUnhealthy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	actualAddr := ln.Addr().String()

	go serveOKOnce(ln)

	servers := []*balancer.Server{balancer.NewServer(actualAddr, 1)}
	logger := testLogger()
	cfg := config.PoolConfig{
		Name:      "test_pool",
		Algorithm: "round_robin",
		Backends:  []config.BackendConfig{{Address: actualAddr, Weight: 1}},
		HealthCheck: &config.HealthCheckConfig{
			Type:               "tcp",
			Interval:           200 * time.Millisecond,
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

	time.Sleep(800 * time.Millisecond)
	if !servers[0].IsHealthy() {
		t.Fatal("backend should start healthy")
	}

	ln.Close()
	time.Sleep(300 * time.Millisecond)
	ln2, err := net.Listen("tcp", actualAddr)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	go serveOKOnce(ln2)
	time.Sleep(300 * time.Millisecond)

	ln2.Close()
	time.Sleep(300 * time.Millisecond)
	ln3, err := net.Listen("tcp", actualAddr)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	go serveOKOnce(ln3)
	time.Sleep(300 * time.Millisecond)

	ln3.Close()
	time.Sleep(300 * time.Millisecond)
	ln4, err := net.Listen("tcp", actualAddr)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	go serveOKOnce(ln4)
	time.Sleep(300 * time.Millisecond)

	if !servers[0].IsHealthy() {
		t.Error("backend should remain healthy with alternating success/failure pattern")
	}
}

func TestHysteresisRecoveryShortOfThreshold(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	go serveOKOnce(ln)

	addr := ln.Addr().String()
	servers := []*balancer.Server{balancer.NewServer(addr, 1)}
	logger := testLogger()
	cfg := config.PoolConfig{
		Name:      "test_pool",
		Algorithm: "round_robin",
		Backends:  []config.BackendConfig{{Address: addr, Weight: 1}},
		HealthCheck: &config.HealthCheckConfig{
			Type:               "tcp",
			Interval:           100 * time.Millisecond,
			Timeout:            500 * time.Millisecond,
			HealthyThreshold:   2,
			UnhealthyThreshold: 2,
		},
	}

	checker := NewChecker(cfg, servers, logger, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checker.Start(ctx)
	defer checker.Stop()

	time.Sleep(600 * time.Millisecond)
	if !servers[0].IsHealthy() {
		t.Fatal("backend should start healthy")
	}

	ln.Close()
	time.Sleep(500 * time.Millisecond)

	if servers[0].IsHealthy() {
		t.Fatal("backend should be unhealthy after failures")
	}

	ln2, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("reopen listener: %v", err)
	}
	go serveOKOnce(ln2)

	if servers[0].IsHealthy() {
		t.Fatal("backend should still be unhealthy immediately after reopening")
	}

	time.Sleep(400 * time.Millisecond)

	if !servers[0].IsHealthy() {
		t.Error("backend should be healthy after enough consecutive successes")
	}
}
