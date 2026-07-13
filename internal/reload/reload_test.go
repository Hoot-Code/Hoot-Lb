package reload

import (
	"errors"
	"log/slog"
	"os"
	goruntime "runtime"
	"sync"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
	lbruntime "github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

func testLogger() *slog.Logger {
	return logging.New(slog.LevelError, os.Stdout)
}

func TestDiffListenersIdentical(t *testing.T) {
	old := []lbruntime.ListenerFingerprint{
		{Name: "web", Address: "127.0.0.1:8080", Protocol: "http"},
		{Name: "api", Address: "127.0.0.1:8081", Protocol: "tcp"},
	}
	new := []lbruntime.ListenerFingerprint{
		{Name: "web", Address: "127.0.0.1:8080", Protocol: "http"},
		{Name: "api", Address: "127.0.0.1:8081", Protocol: "tcp"},
	}

	err := lbruntime.DiffListeners(old, new)
	if err != nil {
		t.Fatalf("unexpected error for identical listeners: %v", err)
	}
}

func TestDiffListenersAddressChanged(t *testing.T) {
	old := []lbruntime.ListenerFingerprint{
		{Name: "web", Address: "127.0.0.1:8080", Protocol: "http"},
	}
	new := []lbruntime.ListenerFingerprint{
		{Name: "web", Address: "127.0.0.1:9090", Protocol: "http"},
	}

	err := lbruntime.DiffListeners(old, new)
	if err == nil {
		t.Fatal("expected error for listener address change")
	}
	var lce *lbruntime.ListenerChangeError
	if !errors.As(err, &lce) {
		t.Fatalf("expected ListenerChangeError, got %T: %v", err, err)
	}
}

func TestDiffListenersProtocolChanged(t *testing.T) {
	old := []lbruntime.ListenerFingerprint{
		{Name: "web", Address: "127.0.0.1:8080", Protocol: "http"},
	}
	new := []lbruntime.ListenerFingerprint{
		{Name: "web", Address: "127.0.0.1:8080", Protocol: "tcp"},
	}

	err := lbruntime.DiffListeners(old, new)
	if err == nil {
		t.Fatal("expected error for listener protocol change")
	}
}

func TestDiffListenersTLSModeChanged(t *testing.T) {
	old := []lbruntime.ListenerFingerprint{
		{Name: "web", Address: "127.0.0.1:8080", Protocol: "http", TLSMode: "terminate"},
	}
	new := []lbruntime.ListenerFingerprint{
		{Name: "web", Address: "127.0.0.1:8080", Protocol: "http", TLSMode: ""},
	}

	err := lbruntime.DiffListeners(old, new)
	if err == nil {
		t.Fatal("expected error for listener TLS mode change")
	}
}

func TestDiffListenersNewAdded(t *testing.T) {
	old := []lbruntime.ListenerFingerprint{
		{Name: "web", Address: "127.0.0.1:8080", Protocol: "http"},
	}
	new := []lbruntime.ListenerFingerprint{
		{Name: "web", Address: "127.0.0.1:8080", Protocol: "http"},
		{Name: "api", Address: "127.0.0.1:8081", Protocol: "http"},
	}

	err := lbruntime.DiffListeners(old, new)
	if err == nil {
		t.Fatal("expected error for new listener added")
	}
}

func TestDiffListenersRemoved(t *testing.T) {
	old := []lbruntime.ListenerFingerprint{
		{Name: "web", Address: "127.0.0.1:8080", Protocol: "http"},
		{Name: "api", Address: "127.0.0.1:8081", Protocol: "http"},
	}
	new := []lbruntime.ListenerFingerprint{
		{Name: "web", Address: "127.0.0.1:8080", Protocol: "http"},
	}

	err := lbruntime.DiffListeners(old, new)
	if err == nil {
		t.Fatal("expected error for listener removed")
	}
}

func TestDiffListenersCountChanged(t *testing.T) {
	old := []lbruntime.ListenerFingerprint{
		{Name: "web", Address: "127.0.0.1:8080", Protocol: "http"},
	}
	new := []lbruntime.ListenerFingerprint{
		{Name: "web", Address: "127.0.0.1:8080", Protocol: "http"},
		{Name: "api", Address: "127.0.0.1:8081", Protocol: "http"},
		{Name: "admin", Address: "127.0.0.1:8082", Protocol: "http"},
	}

	err := lbruntime.DiffListeners(old, new)
	if err == nil {
		t.Fatal("expected error for listener count change")
	}
}

func TestBuildSnapshot(t *testing.T) {
	cfg := &config.Config{
		Global: config.GlobalConfig{
			LogLevel:            "error",
			ShutdownTimeout:     5 * time.Second,
			ReloadCheckInterval: 5 * time.Second,
		},
		Listeners: []config.ListenerConfig{
			{
				Name:     "web",
				Address:  "127.0.0.1:8080",
				Protocol: "http",
				Pool:     "web_pool",
			},
		},
		Pools: []config.PoolConfig{
			{
				Name:      "web_pool",
				Algorithm: "round_robin",
				Backends: []config.BackendConfig{
					{Address: "127.0.0.1:9001", Weight: 1},
					{Address: "127.0.0.1:9002", Weight: 1},
				},
				HealthCheck: &config.HealthCheckConfig{
					Type:     "none",
					Interval: 5 * time.Second,
					Timeout:  2 * time.Second,
				},
			},
		},
	}

	snap, err := lbruntime.BuildSnapshot(cfg, testLogger())
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	if len(snap.PoolStates) != 1 {
		t.Fatalf("expected 1 pool, got %d", len(snap.PoolStates))
	}

	ps, ok := snap.PoolStates["web_pool"]
	if !ok {
		t.Fatal("web_pool not found")
	}

	if len(ps.Servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(ps.Servers))
	}

	if ps.LB == nil {
		t.Fatal("LoadBalancer is nil")
	}

	if ps.FR != nil {
		t.Fatal("FailureReporter should be nil for type=none")
	}
}

func TestSnapshotAtomicSwap(t *testing.T) {
	cfg := &config.Config{
		Global: config.GlobalConfig{
			LogLevel:            "error",
			ShutdownTimeout:     5 * time.Second,
			ReloadCheckInterval: 5 * time.Second,
		},
		Listeners: []config.ListenerConfig{
			{
				Name:     "web",
				Address:  "127.0.0.1:8080",
				Protocol: "http",
				Pool:     "web_pool",
			},
		},
		Pools: []config.PoolConfig{
			{
				Name:      "web_pool",
				Algorithm: "round_robin",
				Backends: []config.BackendConfig{
					{Address: "127.0.0.1:9001", Weight: 1},
				},
				HealthCheck: &config.HealthCheckConfig{
					Type:     "none",
					Interval: 5 * time.Second,
					Timeout:  2 * time.Second,
				},
			},
		},
	}

	snap1, err := lbruntime.BuildSnapshot(cfg, testLogger())
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	snap2, err := lbruntime.BuildSnapshot(cfg, testLogger())
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	atomicSnap := lbruntime.NewAtomicSnapshot(snap1)

	if atomicSnap.Load() != snap1 {
		t.Fatal("expected snap1")
	}

	old := atomicSnap.Swap(snap2)
	if old != snap1 {
		t.Fatal("expected snap1 from Swap")
	}

	if atomicSnap.Load() != snap2 {
		t.Fatal("expected snap2 after Swap")
	}
}

func TestDiffListenersConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			old := []lbruntime.ListenerFingerprint{
				{Name: "web", Address: "127.0.0.1:8080", Protocol: "http"},
			}
			new := []lbruntime.ListenerFingerprint{
				{Name: "web", Address: "127.0.0.1:8080", Protocol: "http"},
			}
			lbruntime.DiffListeners(old, new)
		}()
	}
	wg.Wait()
}

func TestWatcherGoroutineLeak(t *testing.T) {
	dir := t.TempDir()
	cfgContent := `
global:
  log_level: error
  shutdown_timeout: 5s
listeners:
  - name: tcp_proxy
    address: "127.0.0.1:0"
    protocol: tcp
    pool: pool_a
pools:
  - name: pool_a
    algorithm: round_robin
    backends:
      - address: "127.0.0.1:1"
        weight: 1
    health_check:
      type: none
`
	cfgPath := writeConfig(t, dir, cfgContent)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	snap, err := lbruntime.BuildSnapshot(cfg, testLogger())
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	snapPtr := lbruntime.NewAtomicSnapshot(snap)

	baseline := goruntime.NumGoroutine()

	watcher := NewWatcher(cfgPath, 10*time.Millisecond, snapPtr, snap.CertStores, testLogger(), nil)

	go watcher.Run()

	time.Sleep(100 * time.Millisecond)

	watcher.Stop()

	time.Sleep(200 * time.Millisecond)
	goruntime.GC()
	time.Sleep(200 * time.Millisecond)
	goruntime.GC()
	time.Sleep(200 * time.Millisecond)

	current := goruntime.NumGoroutine()
	if current > baseline+5 {
		t.Errorf("goroutine leak after watcher.Stop(): baseline %d, now %d", baseline, current)
	}
}

func TestReloadInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	validConfig := `
global:
  log_level: error
  shutdown_timeout: 5s
listeners:
  - name: tcp_proxy
    address: "127.0.0.1:0"
    protocol: tcp
    pool: pool_a
pools:
  - name: pool_a
    algorithm: round_robin
    backends:
      - address: "127.0.0.1:1"
        weight: 1
    health_check:
      type: none
`
	cfgPath := writeConfig(t, dir, validConfig)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	snap, err := lbruntime.BuildSnapshot(cfg, testLogger())
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	snapPtr := lbruntime.NewAtomicSnapshot(snap)

	watcher := NewWatcher(cfgPath, 10*time.Millisecond, snapPtr, snap.CertStores, testLogger(), nil)

	go watcher.Run()

	// Write invalid config.
	invalidConfig := `
global:
  log_level: error
  shutdown_timeout: 5s
listeners: []
pools:
  - name: pool_a
    algorithm: round_robin
    backends:
      - address: "127.0.0.1:1"
        weight: 1
`
	writeConfig(t, dir, invalidConfig)

	// Wait for reload attempt.
	time.Sleep(100 * time.Millisecond)

	// Verify snapshot is still valid.
	if snapPtr.Load() != snap {
		t.Fatal("snapshot should not have changed after invalid config")
	}

	watcher.Stop()
}

func writeConfig(t *testing.T, dir string, content string) string {
	t.Helper()
	path := dir + "/config.yaml"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
