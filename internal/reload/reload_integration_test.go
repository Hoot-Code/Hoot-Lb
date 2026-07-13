package reload

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/health"
	"github.com/Hoot-Code/Hoot-Lb/internal/proxy/l4"
	lbruntime "github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

func TestInvalidReloadConfigWithTraffic(t *testing.T) {
	addrA, stopA := tcpIdentBackend(t)
	defer stopA()

	dir := t.TempDir()
	validCfg := fmt.Sprintf(reloadCfgTemplate,
		"tcp_proxy", "127.0.0.1:0", "tcp", "pool_a", addrA, addrA)
	cfgPath := writeConfig(t, dir, validCfg)

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

	// Pre-reload request succeeds.
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial pre: %v", err)
	}
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(conn, "before\n")
	sc := bufio.NewScanner(conn)
	sc.Scan()
	if got := strings.TrimSpace(sc.Text()); got != addrA {
		t.Fatalf("pre-reload: want %s, got %s", addrA, got)
	}
	conn.Close()

	// Write broken YAML.
	os.WriteFile(cfgPath, []byte("{{{not valid yaml"), 0644)
	watcher.TriggerReload()

	// Snapshot must be unchanged.
	if snapPtr.Load() != snap {
		t.Fatal("snapshot changed after invalid config")
	}

	// Post-reload request still succeeds.
	conn2, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial post: %v", err)
	}
	conn2.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(conn2, "after\n")
	sc2 := bufio.NewScanner(conn2)
	sc2.Scan()
	if got := strings.TrimSpace(sc2.Text()); got != addrA {
		t.Fatalf("post-reload: want %s, got %s", addrA, got)
	}
	conn2.Close()
}

func TestListenerChangeRejectedWithTraffic(t *testing.T) {
	addrA, stopA := tcpIdentBackend(t)
	defer stopA()

	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, fmt.Sprintf(reloadCfgTemplate,
		"tcp_proxy", "127.0.0.1:0", "tcp", "pool_a", addrA, addrA))

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

	// Pre-reload: request works.
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial pre: %v", err)
	}
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(conn, "ping\n")
	sc := bufio.NewScanner(conn)
	sc.Scan()
	if got := strings.TrimSpace(sc.Text()); got != addrA {
		t.Fatalf("pre: want %s, got %s", addrA, got)
	}
	conn.Close()

	// Write config with changed listener address → must be rejected.
	badCfg := fmt.Sprintf(reloadCfgTemplate,
		"tcp_proxy", "127.0.0.1:19876", "tcp", "pool_a", addrA, addrA)
	writeConfig(t, dir, badCfg)
	watcher.TriggerReload()

	// Snapshot unchanged.
	if snapPtr.Load() != snap {
		t.Fatal("snapshot changed after listener-level change")
	}

	// Original listener still works.
	conn2, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial post: %v", err)
	}
	conn2.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(conn2, "still_ok\n")
	sc2 := bufio.NewScanner(conn2)
	sc2.Scan()
	if got := strings.TrimSpace(sc2.Text()); got != addrA {
		t.Fatalf("post: want %s, got %s", addrA, got)
	}
	conn2.Close()
}
