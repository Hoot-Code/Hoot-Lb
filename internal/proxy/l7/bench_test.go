package l7

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
)

// echoHTTPBackendB starts an HTTP server for benchmarks.
func echoHTTPBackendB(tb testing.TB) (string, func()) {
	tb.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("echo backend listen: %v", err)
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintf(w, "%s", r.URL.Path)
		}),
	}
	go srv.Serve(ln)
	return ln.Addr().String(), func() { srv.Close() }
}

// startL7ProxyB starts an L7 proxy for benchmarks.
func startL7ProxyB(tb testing.TB, routes []Route, defaultLB balancer.LoadBalancer) (string, func()) {
	tb.Helper()
	cfg := config.ListenerConfig{
		Name:     "bench_http",
		Address:  "127.0.0.1:0",
		Protocol: "http",
		Pool:     "bench_pool",
	}
	srv, err := NewL7Server(cfg, routes, defaultLB, nil, testLogger(), nil)
	if err != nil {
		tb.Fatalf("NewL7Server: %v", err)
	}
	go srv.Serve(context.Background())
	return srv.listener.Addr().String(), func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}
}

// BenchmarkHTTPProxy measures a single round-trip through the full L7
// stack: director (route matching, backend selection, context
// propagation), transport wrapper (ConnReleaser, FailureReporter
// check), and response body streaming.
//
// Note: uses benchtime=1s to avoid macOS ephemeral port exhaustion.
func BenchmarkHTTPProxy(b *testing.B) {
	backend, stopBackend := echoHTTPBackendB(b)
	defer stopBackend()

	backends := []balancer.Backend{balancer.NewServer(backend, 1)}
	lb := balancer.NewRoundRobin(backends)

	proxyAddr, stopProxy := startL7ProxyB(b, nil, lb)
	defer stopProxy()

	// Use a low-concurrency transport to stay within ephemeral port
	// limits on macOS (which has ~16k ports with 60s TIME_WAIT).
	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
		Timeout: 5 * time.Second,
	}
	defer client.CloseIdleConnections()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := client.Get(fmt.Sprintf("http://%s/bench-%d", proxyAddr, i))
		if err != nil {
			b.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

// BenchmarkHTTPProxyParallel measures the L7 proxy throughput under
// concurrent load using http.Client with connection pooling.
//
// Note: uses benchtime=1s to avoid macOS ephemeral port exhaustion.
func BenchmarkHTTPProxyParallel(b *testing.B) {
	backend, stopBackend := echoHTTPBackendB(b)
	defer stopBackend()

	backends := []balancer.Backend{balancer.NewServer(backend, 1)}
	lb := balancer.NewRoundRobin(backends)

	proxyAddr, stopProxy := startL7ProxyB(b, nil, lb)
	defer stopProxy()

	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     90 * time.Second,
		},
		Timeout: 5 * time.Second,
	}
	defer client.CloseIdleConnections()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			resp, err := client.Get(fmt.Sprintf("http://%s/bench-%d", proxyAddr, i))
			if err != nil {
				if strings.Contains(err.Error(), "can't assign requested address") {
					continue // macOS port exhaustion, skip
				}
				b.Error(err)
				continue
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			i++
		}
	})
}
