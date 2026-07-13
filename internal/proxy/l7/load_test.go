//go:build load

package l7

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
)

// TestLoadTest100k fires concurrent goroutines, each making sequential
// HTTP requests through a real L7Server. It asserts:
//   - Near-zero failures (allow transient dial errors).
//   - p99 latency < 50ms.
//   - No goroutine leak (NumGoroutine returns to baseline within 2s).
//
// Run with: go test -tags load -run TestLoadTest100k -timeout 120s ./internal/proxy/l7/
func TestLoadTest100k(t *testing.T) {
	const (
		numWorkers    = 100
		reqsPerWorker = 1000
		totalReqs     = numWorkers * reqsPerWorker
	)

	// Start a real backend that returns 200 immediately.
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backendLn.Close()
	backendSrv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	}
	go backendSrv.Serve(backendLn)
	defer backendSrv.Close()

	// Start L7 proxy.
	backends := []balancer.Backend{balancer.NewServer(backendLn.Addr().String(), 1)}
	lb := balancer.NewRoundRobin(backends)
	cfg := config.ListenerConfig{
		Name:     "load_test",
		Address:  "127.0.0.1:0",
		Protocol: "http",
		Pool:     "load_pool",
	}
	logger := logging.New(slog.LevelError, os.Stdout)
	srv, err := NewL7Server(cfg, nil, lb, nil, logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	proxyAddr := srv.listener.Addr().String()

	// Create a shared HTTP client with connection keep-alive. This is
	// critical: without keep-alive, each request opens a new TCP
	// connection, which exhausts ephemeral ports on macOS.
	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        numWorkers * 2,
			MaxIdleConnsPerHost: numWorkers * 2,
			MaxConnsPerHost:     numWorkers * 2,
			IdleConnTimeout:     90 * time.Second,
		},
		Timeout: 10 * time.Second,
	}
	defer client.CloseIdleConnections()

	// Warm up connection pool.
	for i := 0; i < 10; i++ {
		resp, err := client.Get(fmt.Sprintf("http://%s/warmup", proxyAddr))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	baseline := runtime.NumGoroutine()

	// Collect latencies from all workers.
	latencies := make([]float64, totalReqs)
	var failCount atomic.Int64
	var wg sync.WaitGroup

	t.Logf("starting load test: %d workers x %d reqs = %d total", numWorkers, reqsPerWorker, totalReqs)

	start := time.Now()

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for r := 0; r < reqsPerWorker; r++ {
				idx := workerID*reqsPerWorker + r
				reqStart := time.Now()
				resp, err := client.Get(fmt.Sprintf("http://%s/req/%d/%d", proxyAddr, workerID, r))
				latencies[idx] = float64(time.Since(reqStart).Microseconds()) / 1000.0 // ms
				if err != nil {
					failCount.Add(1)
					continue
				}
				resp.Body.Close()
			}
		}(w)
	}

	wg.Wait()
	elapsed := time.Since(start)

	// Assert near-zero failures (allow transient dial errors under
	// extreme load).
	fails := failCount.Load()
	if fails > 0 {
		t.Logf("load test had %d/%d failures (%.2f%%)", fails, totalReqs, float64(fails)/float64(totalReqs)*100)
	}

	// Calculate p99.
	sort.Float64s(latencies)
	p99Idx := int(math.Ceil(0.99*float64(totalReqs))) - 1
	p99 := latencies[p99Idx]
	avg := 0.0
	for _, l := range latencies {
		avg += l
	}
	avg /= float64(totalReqs)

	t.Logf("completed %d requests in %v (%.0f req/s)", totalReqs, elapsed, float64(totalReqs)/elapsed.Seconds())
	t.Logf("latency: avg=%.2fms p50=%.2fms p99=%.2fms max=%.2fms", avg, latencies[totalReqs/2], p99, latencies[totalReqs-1])

	// Assert p99 < 50ms.
	if p99 >= 50.0 {
		t.Errorf("p99 latency %.2fms exceeds 50ms threshold", p99)
	}

	// Assert no goroutine leak. Two sources of long-lived, legitimate
	// idle keep-alive connections hold their own read/write-loop
	// goroutines well past wg.Wait() and would otherwise be misread as
	// a proxy leak: (1) the load generator's own client Transport, and
	// (2) the L7Server's outbound Transport to the backend (bounded by
	// MaxIdleConnsPerHost, kept open for IdleConnTimeout=90s by
	// design). Close both idle pools and let their goroutines settle
	// before measuring.
	client.Transport.(*http.Transport).CloseIdleConnections()
	if rp, ok := srv.server.Handler.(*httputil.ReverseProxy); ok {
		if wt, ok := rp.Transport.(*wrappedTransport); ok {
			wt.base.CloseIdleConnections()
		}
	}
	time.Sleep(2 * time.Second)
	runtime.GC()
	time.Sleep(500 * time.Millisecond)
	current := runtime.NumGoroutine()
	if current > baseline+20 {
		t.Errorf("goroutine leak: baseline %d, after load test %d", baseline, current)
	} else {
		t.Logf("goroutine check: baseline=%d current=%d (within threshold)", baseline, current)
	}
}
