package integration

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/discovery"
	"github.com/Hoot-Code/Hoot-Lb/internal/health"
	"github.com/Hoot-Code/Hoot-Lb/internal/metrics"
	"github.com/Hoot-Code/Hoot-Lb/internal/proxy/l4"
	"github.com/Hoot-Code/Hoot-Lb/internal/reload"
	runtimePkg "github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

func scrapeMetrics(t *testing.T, registry *metrics.Registry) string {
	t.Helper()
	msrv, err := metrics.NewMetricsServer("127.0.0.1:0", "/metrics", registry, testLogger())
	if err != nil {
		t.Fatalf("NewMetricsServer: %v", err)
	}
	go msrv.Start(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		msrv.Close(ctx)
	}()

	resp, err := http.Get(fmt.Sprintf("http://%s/metrics", msrv.Addr().String()))
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, resp.Body); err != nil {
		t.Fatalf("read metrics body: %v", err)
	}
	return buf.String()
}

func TestIntegrationCardinalityCleanupWatcher(t *testing.T) {
	echoLn, stopEcho := echoTCPServer(t)
	defer stopEcho()
	backendAddr := echoLn.Addr().String()

	dir := t.TempDir()
	listenerAddr := "127.0.0.1:0"
	cfgContent := fmt.Sprintf(`global:
  log_level: error
  shutdown_timeout: 5s
listeners:
  - name: tcp_proxy
    address: "%s"
    protocol: tcp
    pool: pool_a
pools:
  - name: pool_a
    algorithm: round_robin
    backends:
      - address: "%s"
        weight: 1
    health_check:
      type: none
`, listenerAddr, backendAddr)
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	snap, err := runtimePkg.BuildSnapshot(cfg, testLogger())
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	snapPtr := runtimePkg.NewAtomicSnapshot(snap)

	registry := metrics.NewRegistry()
	connectionsTotal := registry.NewCounterVec("lb_connections_total", "Total connections", []string{"listener", "pool", "backend", "protocol"})
	connectionsActive := registry.NewGaugeVec("lb_connections_active", "Active connections", []string{"listener", "pool", "backend", "protocol"})
	bytesTransferred := registry.NewCounterVec("lb_bytes_transferred_total", "Bytes transferred", []string{"listener", "pool", "backend", "direction"})

	metricVecs := &metrics.MetricVecs{
		CounterVecs: []*metrics.CounterVec{connectionsTotal, bytesTransferred},
		GaugeVecs:   []*metrics.GaugeVec{connectionsActive},
	}

	watcher := reload.NewWatcher(cfgPath, 0, snapPtr, snap.CertStores, testLogger(), metricVecs)

	proxyMetrics := &l4.ProxyMetrics{
		ConnectionsTotal:  connectionsTotal,
		ConnectionsActive: connectionsActive,
		BytesTransferred:  bytesTransferred,
	}
	listenerCfg := cfg.Listeners[0]
	dynamicGetter := l4.PoolStateGetter(func() (balancer.LoadBalancer, health.FailureReporter, runtimePkg.Outcome) {
		ps := snapPtr.Load().PoolStates["pool_a"]
		return ps.LB, ps.FR, nil
	})

	srv, err := l4.NewTCPServer(listenerCfg, dynamicGetter, testLogger(), proxyMetrics, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("NewTCPServer: %v", err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	conn, err := net.DialTimeout("tcp", srv.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	fmt.Fprintf(conn, "hello\n")
	scanner := bufio.NewScanner(conn)
	scanner.Scan()
	conn.Close()
	time.Sleep(100 * time.Millisecond)

	output := scrapeMetrics(t, registry)
	if !strings.Contains(output, backendAddr) {
		t.Fatalf("backend %s not present in metrics after traffic:\n%s", backendAddr, output)
	}

	newCfgContent := fmt.Sprintf(`global:
  log_level: error
  shutdown_timeout: 5s
listeners:
  - name: tcp_proxy
    address: "%s"
    protocol: tcp
    pool: pool_a
pools:
  - name: pool_a
    algorithm: round_robin
    backends:
      - address: "127.0.0.1:19999"
        weight: 1
    health_check:
      type: none
`, listenerAddr)
	if err := os.WriteFile(cfgPath, []byte(newCfgContent), 0644); err != nil {
		t.Fatalf("write new config: %v", err)
	}

	watcher.TriggerReload()

	output = scrapeMetrics(t, registry)
	if strings.Contains(output, backendAddr) {
		t.Fatalf("backend %s still present in metrics after reload:\n%s", backendAddr, output)
	}
}

type mockDiscovery struct {
	mu       sync.Mutex
	backends []discovery.Backend
}

func (m *mockDiscovery) Resolve(ctx context.Context) ([]discovery.Backend, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]discovery.Backend, len(m.backends))
	copy(out, m.backends)
	return out, nil
}

func (m *mockDiscovery) Name() string { return "mock" }

func (m *mockDiscovery) setBackends(b []discovery.Backend) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backends = b
}

func TestIntegrationCardinalityCleanupPoller(t *testing.T) {
	echoLn, stopEcho := echoTCPServer(t)
	defer stopEcho()
	backendAddr := echoLn.Addr().String()

	md := &mockDiscovery{
		backends: []discovery.Backend{{Address: backendAddr, Weight: 1}},
	}

	snap, err := runtimePkg.BuildSnapshot(&config.Config{
		Global: config.GlobalConfig{
			LogLevel:        "error",
			ShutdownTimeout: 5 * time.Second,
		},
		Listeners: []config.ListenerConfig{
			{Name: "tcp_proxy", Address: "127.0.0.1:0", Protocol: "tcp", Pool: "pool_a"},
		},
		Pools: []config.PoolConfig{
			{Name: "pool_a", Algorithm: "round_robin", Backends: []config.BackendConfig{{Address: backendAddr, Weight: 1}}, HealthCheck: &config.HealthCheckConfig{Type: "none"}},
		},
	}, testLogger())
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	snapPtr := runtimePkg.NewAtomicSnapshot(snap)

	registry := metrics.NewRegistry()
	connectionsTotal := registry.NewCounterVec("lb_connections_total", "Total connections", []string{"listener", "pool", "backend", "protocol"})
	connectionsActive := registry.NewGaugeVec("lb_connections_active", "Active connections", []string{"listener", "pool", "backend", "protocol"})
	bytesTransferred := registry.NewCounterVec("lb_bytes_transferred_total", "Bytes transferred", []string{"listener", "pool", "backend", "direction"})

	metricVecs := &metrics.MetricVecs{
		CounterVecs: []*metrics.CounterVec{connectionsTotal, bytesTransferred},
		GaugeVecs:   []*metrics.GaugeVec{connectionsActive},
	}

	poller := runtimePkg.NewPoller(md, snapPtr, "pool_a", config.PoolConfig{
		Name:        "pool_a",
		Algorithm:   "round_robin",
		HealthCheck: &config.HealthCheckConfig{Type: "none"},
	}, 50*time.Millisecond, testLogger(), metricVecs)

	go poller.Run()
	defer poller.Stop()

	proxyMetrics := &l4.ProxyMetrics{
		ConnectionsTotal:  connectionsTotal,
		ConnectionsActive: connectionsActive,
		BytesTransferred:  bytesTransferred,
	}
	listenerCfg := config.ListenerConfig{Name: "tcp_proxy", Address: "127.0.0.1:0", Protocol: "tcp", Pool: "pool_a"}
	dynamicGetter := l4.PoolStateGetter(func() (balancer.LoadBalancer, health.FailureReporter, runtimePkg.Outcome) {
		ps := snapPtr.Load().PoolStates["pool_a"]
		return ps.LB, ps.FR, nil
	})

	srv, err := l4.NewTCPServer(listenerCfg, dynamicGetter, testLogger(), proxyMetrics, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("NewTCPServer: %v", err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	conn, err := net.DialTimeout("tcp", srv.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	fmt.Fprintf(conn, "hello\n")
	scanner := bufio.NewScanner(conn)
	scanner.Scan()
	conn.Close()
	time.Sleep(100 * time.Millisecond)

	output := scrapeMetrics(t, registry)
	if !strings.Contains(output, backendAddr) {
		t.Fatalf("backend %s not present in metrics after traffic:\n%s", backendAddr, output)
	}

	md.setBackends([]discovery.Backend{{Address: "127.0.0.1:19998", Weight: 1}})
	time.Sleep(200 * time.Millisecond)

	output = scrapeMetrics(t, registry)
	if strings.Contains(output, backendAddr) {
		t.Fatalf("backend %s still present in metrics after poller update:\n%s", backendAddr, output)
	}
}
