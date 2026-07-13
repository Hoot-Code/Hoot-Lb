package l7

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/health"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
)

// testLogger returns a quiet logger for tests.
func testLogger() *slog.Logger {
	return logging.New(slog.LevelError, os.Stdout)
}

// echoHTTPBackend starts an HTTP server that returns the request path
// in the response body. It returns the address and a stop function.
func echoHTTPBackend(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo backend listen: %v", err)
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

// startL7Proxy creates and starts an L7Server with the given routes
// and default pool. It returns the proxy address and a stop function.
func startL7Proxy(t *testing.T, routes []Route, defaultLB balancer.LoadBalancer, defaultFR ...health.FailureReporter) (string, func()) {
	t.Helper()

	var fr health.FailureReporter
	if len(defaultFR) > 0 {
		fr = defaultFR[0]
	}

	cfg := config.ListenerConfig{
		Name:     "test_http",
		Address:  "127.0.0.1:0",
		Protocol: "http",
		Pool:     "test_pool",
	}

	srv, err := NewL7Server(cfg, routes, defaultLB, fr, testLogger(), nil)
	if err != nil {
		t.Fatalf("NewL7Server: %v", err)
	}
	go srv.Serve(context.Background())
	return srv.listener.Addr().String(), func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}
}

func TestL7HostPathRouting(t *testing.T) {
	backendA, stopA := echoHTTPBackend(t)
	defer stopA()
	backendB, stopB := echoHTTPBackend(t)
	defer stopB()

	lbA := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendA, 1)})
	lbB := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendB, 1)})

	routes := []Route{
		{Host: "api.example.com", PathPrefix: "/v2", LB: lbA},
		{PathPrefix: "/static", LB: lbB},
	}

	proxyAddr, stopProxy := startL7Proxy(t, routes, lbA)
	defer stopProxy()

	client := &http.Client{Timeout: 5 * time.Second}

	// /v2/users → backend A (host matches route 0)
	resp, err := client.Get(fmt.Sprintf("http://%s/v2/users", proxyAddr))
	if err != nil {
		t.Fatalf("request /v2/users: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "/v2/users" {
		t.Errorf("/v2/users: got body %q, want path echoed from backend A", string(body))
	}

	// /static/logo.png → backend B (matches route 1)
	resp, err = client.Get(fmt.Sprintf("http://%s/static/logo.png", proxyAddr))
	if err != nil {
		t.Fatalf("request /static/logo.png: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "/static/logo.png" {
		t.Errorf("/static/logo.png: got body %q", string(body))
	}

	// /other → default (backend A, no route matches)
	resp, err = client.Get(fmt.Sprintf("http://%s/other", proxyAddr))
	if err != nil {
		t.Fatalf("request /other: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "/other" {
		t.Errorf("/other: got body %q", string(body))
	}

	// /v2/deep/nested → backend A (longest prefix /v2 wins over /static)
	resp, err = client.Get(fmt.Sprintf("http://%s/v2/deep/nested", proxyAddr))
	if err != nil {
		t.Fatalf("request /v2/deep/nested: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "/v2/deep/nested" {
		t.Errorf("/v2/deep/nested: got body %q", string(body))
	}
}

func TestL7LongestPrefixTieBreaking(t *testing.T) {
	backendA, stopA := echoHTTPBackend(t)
	defer stopA()
	backendB, stopB := echoHTTPBackend(t)
	defer stopB()

	lbA := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendA, 1)})
	lbB := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendB, 1)})

	// Both match /v2/..., but /v2/admin is more specific
	routes := []Route{
		{PathPrefix: "/v2", LB: lbA},
		{PathPrefix: "/v2/admin", LB: lbB},
	}

	proxyAddr, stopProxy := startL7Proxy(t, routes, lbA)
	defer stopProxy()

	client := &http.Client{Timeout: 5 * time.Second}

	// /v2/users → lbA (shorter prefix wins)
	resp, err := client.Get(fmt.Sprintf("http://%s/v2/users", proxyAddr))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "/v2/users" {
		t.Errorf("/v2/users: got body %q", string(body))
	}

	// /v2/admin/settings → lbB (longer prefix wins)
	resp, err = client.Get(fmt.Sprintf("http://%s/v2/admin/settings", proxyAddr))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "/v2/admin/settings" {
		t.Errorf("/v2/admin/settings: got body %q", string(body))
	}
}

func TestL7Streaming(t *testing.T) {
	const bodySize = 5 * 1024 * 1024

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(bodySize))
			w.WriteHeader(200)
			buf := make([]byte, 32*1024)
			for written := 0; written < bodySize; {
				n := len(buf)
				if written+n > bodySize {
					n = bodySize - written
				}
				w.Write(buf[:n])
				written += n
			}
		}),
	}
	go srv.Serve(ln)

	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(ln.Addr().String(), 1)})
	proxyAddr, stopProxy := startL7Proxy(t, nil, lb)
	defer stopProxy()

	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/big", proxyAddr))
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	written, err := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if written != bodySize {
		t.Errorf("body size: got %d, want %d", written, bodySize)
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	memIncrease := after.TotalAlloc - before.TotalAlloc
	if memIncrease > uint64(bodySize)*2 {
		t.Errorf("memory increase %d exceeds %d (body not streamed)", memIncrease, bodySize*2)
	}
}

func TestL7ClientAbort(t *testing.T) {
	ready := make(chan struct{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			close(ready)
			<-r.Context().Done()
		}),
	}
	go srv.Serve(ln)

	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(ln.Addr().String(), 1)})
	proxyAddr, stopProxy := startL7Proxy(t, nil, lb)
	defer stopProxy()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://%s/slow", proxyAddr), nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, _ := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if resp != nil {
			resp.Body.Close()
		}
	}()

	<-ready
	time.Sleep(50 * time.Millisecond)

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not complete after cancellation")
	}
}

func TestL7HopByHopAndForwardedFor(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	receivedHeaders := make(chan http.Header, 10)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Clone()
			receivedHeaders <- h
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintf(w, "OK")
		}),
	}
	go srv.Serve(ln)

	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(ln.Addr().String(), 1)})
	proxyAddr, stopProxy := startL7Proxy(t, nil, lb)
	defer stopProxy()

	client := &http.Client{Timeout: 5 * time.Second}

	// Test: X-Forwarded-For and X-Real-IP are present
	resp, err := client.Get(fmt.Sprintf("http://%s/test", proxyAddr))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	h := <-receivedHeaders
	if h.Get("X-Forwarded-For") == "" {
		t.Error("missing X-Forwarded-For header")
	}
	if h.Get("X-Real-IP") == "" {
		t.Error("missing X-Real-IP header")
	}

	// Test: hop-by-hop headers are stripped
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://%s/test", proxyAddr), nil)
	req.Header.Set("Connection", "keep-alive, X-Custom")
	req.Header.Set("Keep-Alive", "timeout=5")
	req.Header.Set("X-Custom", "test-value")

	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	h = <-receivedHeaders
	if h.Get("Keep-Alive") != "" {
		t.Error("hop-by-hop header Keep-Alive not stripped")
	}
	if h.Get("X-Custom") != "" {
		t.Error("Connection-listed header X-Custom not stripped")
	}
	if h.Get("Connection") != "" {
		t.Error("Connection header not stripped")
	}

	// Test: X-Forwarded-For append (not overwrite)
	req2, _ := http.NewRequest("GET", fmt.Sprintf("http://%s/test", proxyAddr), nil)
	req2.Header.Set("X-Forwarded-For", "1.2.3.4")

	resp, err = client.Do(req2)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	h = <-receivedHeaders
	xff := h.Get("X-Forwarded-For")
	if !strings.Contains(xff, "1.2.3.4,") {
		t.Errorf("X-Forwarded-For not appended correctly: %s", xff)
	}
}

// TestL7ServerCloseGoroutineLeak verifies that closing an L7Server
// does not leak goroutines.
func TestL7ServerCloseGoroutineLeak(t *testing.T) {
	backend, stopBackend := echoHTTPBackend(t)
	defer stopBackend()

	backends := []balancer.Backend{balancer.NewServer(backend, 1)}
	lb := balancer.NewRoundRobin(backends)

	baseline := runtime.NumGoroutine()

	for i := 0; i < 5; i++ {
		proxyAddr, stopProxy := startL7Proxy(t, nil, lb)

		client := &http.Client{Timeout: 2 * time.Second}
		for j := 0; j < 10; j++ {
			resp, err := client.Get(fmt.Sprintf("http://%s/leak-%d/%d", proxyAddr, i, j))
			if err == nil {
				resp.Body.Close()
			}
		}
		client.CloseIdleConnections()
		stopProxy()
	}

	// Allow HTTP transport idle connection goroutines to drain.
	time.Sleep(2 * time.Second)
	runtime.GC()
	time.Sleep(500 * time.Millisecond)

	current := runtime.NumGoroutine()
	if current > baseline+20 {
		t.Errorf("goroutine leak: baseline %d, now %d", baseline, current)
	}
}
