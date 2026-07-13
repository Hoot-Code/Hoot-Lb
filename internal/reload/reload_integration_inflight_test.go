package reload

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/health"
	"github.com/Hoot-Code/Hoot-Lb/internal/proxy/l4"
	lbruntime "github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

func TestInflightSurvivesReload(t *testing.T) {
	addrA, stopA := tcpIdentBackend(t)
	defer stopA()
	addrB, stopB := tcpIdentBackend(t)
	defer stopB()

	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, fmt.Sprintf(reloadCfgTemplate,
		"tcp_proxy", "127.0.0.1:0", "tcp", "pool_a", addrA, addrB))

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	snap, err := lbruntime.BuildSnapshot(cfg, testLogger())
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	snapPtr := lbruntime.NewAtomicSnapshot(snap)

	listenerCfg := cfg.Listeners[0]
	getter := l4.PoolStateGetter(func() (balancer.LoadBalancer, health.FailureReporter, lbruntime.Outcome) {
		ps := snapPtr.Load().PoolStates[listenerCfg.Pool]
		return ps.LB, ps.FR, nil
	})
	srv, err := l4.NewTCPServer(listenerCfg, getter, testLogger(), nil, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("NewTCPServer: %v", err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	watcher := NewWatcher(cfgPath, 0, snapPtr, snap.CertStores, testLogger(), nil)

	proxyAddr := srv.Addr().String()

	// Connection 1: hits pool_a → addrA
	c1, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	c1.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(c1, "hello\n")
	sc1 := bufio.NewScanner(c1)
	sc1.Scan()
	got1 := strings.TrimSpace(sc1.Text())
	if got1 != addrA {
		t.Fatalf("conn1: want %s, got %s", addrA, got1)
	}

	// Reload: pool_a now points to addrB
	writeConfig(t, dir, fmt.Sprintf(reloadCfgTemplate,
		"tcp_proxy", "127.0.0.1:0", "tcp", "pool_a", addrB, addrA))
	watcher.TriggerReload()

	// Connection 2: hits pool_a → addrB (new backend)
	c2, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	c2.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(c2, "hello\n")
	sc2 := bufio.NewScanner(c2)
	sc2.Scan()
	got2 := strings.TrimSpace(sc2.Text())
	if got2 != addrB {
		t.Fatalf("conn2: want %s, got %s", addrB, got2)
	}

	c1.Close()
	c2.Close()
}

func TestInflightSlowBackendSurvivesReload(t *testing.T) {
	addrA, stopA, unblock := tcpSlowBackend(t)
	defer stopA()
	addrB, stopB := tcpIdentBackend(t)
	defer stopB()

	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, fmt.Sprintf(reloadCfgTemplate,
		"tcp_proxy", "127.0.0.1:0", "tcp", "pool_a", addrA, addrB))

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	snap, err := lbruntime.BuildSnapshot(cfg, testLogger())
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	snapPtr := lbruntime.NewAtomicSnapshot(snap)

	listenerCfg := cfg.Listeners[0]
	getter := l4.PoolStateGetter(func() (balancer.LoadBalancer, health.FailureReporter, lbruntime.Outcome) {
		ps := snapPtr.Load().PoolStates[listenerCfg.Pool]
		return ps.LB, ps.FR, nil
	})
	srv, err := l4.NewTCPServer(listenerCfg, getter, testLogger(), nil, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("NewTCPServer: %v", err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	watcher := NewWatcher(cfgPath, 0, snapPtr, snap.CertStores, testLogger(), nil)
	proxyAddr := srv.Addr().String()

	// Send through slow backend — response delayed.
	c1, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c1.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(c1, "slow\n")

	// Let the connection land on backend A before reloading.
	time.Sleep(100 * time.Millisecond)

	// Reload: pool_a now points to addrB.
	writeConfig(t, dir, fmt.Sprintf(reloadCfgTemplate,
		"tcp_proxy", "127.0.0.1:0", "tcp", "pool_a", addrB, addrA))
	watcher.TriggerReload()

	// Unblock the backend so the in-flight request completes.
	close(unblock)

	// In-flight conn1 still completes against addrA.
	sc := bufio.NewScanner(c1)
	sc.Scan()
	got := strings.TrimSpace(sc.Text())
	if got != addrA {
		t.Errorf("in-flight conn: want %s, got %s", addrA, got)
	}
	c1.Close()

	// New connection goes to addrB.
	c2, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	c2.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(c2, "new\n")
	sc2 := bufio.NewScanner(c2)
	sc2.Scan()
	got2 := strings.TrimSpace(sc2.Text())
	if got2 != addrB {
		t.Errorf("new conn: want %s, got %s", addrB, got2)
	}
	c2.Close()
}
