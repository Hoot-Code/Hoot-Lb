package l7

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/ratelimit"
)

// TestRequestBodyLimitExceeding verifies that a request body exceeding
// max_request_body_bytes gets a 413 response.
func TestRequestBodyLimitExceeding(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	backendHit := make(chan struct{}, 1)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			backendHit <- struct{}{}
			w.WriteHeader(200)
		}),
	}
	go srv.Serve(ln)

	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(ln.Addr().String(), 1)})
	cfg := config.ListenerConfig{
		Name:                "test_body_limit",
		Address:             "127.0.0.1:0",
		Protocol:            "http",
		Pool:                "test_pool",
		MaxRequestBodyBytes: 100,
	}

	proxy, err := NewL7ServerFromGetterWithMetrics(
		cfg,
		func() *RouteTable { return NewRouteTable(nil, lb, nil) },
		testLogger(), nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("NewL7Server: %v", err)
	}
	go proxy.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		proxy.Close(ctx)
	}()

	client := &http.Client{Timeout: 5 * time.Second}

	// Send a body that exceeds the limit.
	body := strings.Repeat("x", 200)
	resp, err := client.Post(fmt.Sprintf("http://%s/test", proxy.Addr()), "text/plain", strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", resp.StatusCode)
	}

	// Backend should never have been hit.
	select {
	case <-backendHit:
		t.Error("backend should not have been hit for oversized body")
	default:
	}
}

// TestRequestBodyLimitExact verifies that a request body at exactly
// the limit succeeds.
func TestRequestBodyLimitExact(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			fmt.Fprintf(w, "ok")
		}),
	}
	go srv.Serve(ln)

	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(ln.Addr().String(), 1)})
	cfg := config.ListenerConfig{
		Name:                "test_body_limit_exact",
		Address:             "127.0.0.1:0",
		Protocol:            "http",
		Pool:                "test_pool",
		MaxRequestBodyBytes: 100,
	}

	proxy, err := NewL7ServerFromGetterWithMetrics(
		cfg,
		func() *RouteTable { return NewRouteTable(nil, lb, nil) },
		testLogger(), nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("NewL7Server: %v", err)
	}
	go proxy.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		proxy.Close(ctx)
	}()

	client := &http.Client{Timeout: 5 * time.Second}

	// Send a body at exactly the limit.
	body := strings.Repeat("x", 100)
	resp, err := client.Post(fmt.Sprintf("http://%s/test", proxy.Addr()), "text/plain", strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for exact-limit body, got %d", resp.StatusCode)
	}
}

// TestRequestBodyLimitUnlimited verifies that when max_request_body_bytes
// is 0, any body size is accepted.
func TestRequestBodyLimitUnlimited(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			fmt.Fprintf(w, "ok")
		}),
	}
	go srv.Serve(ln)

	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(ln.Addr().String(), 1)})
	cfg := config.ListenerConfig{
		Name:     "test_body_limit_unlimited",
		Address:  "127.0.0.1:0",
		Protocol: "http",
		Pool:     "test_pool",
	}

	proxy, err := NewL7ServerFromGetterWithMetrics(
		cfg,
		func() *RouteTable { return NewRouteTable(nil, lb, nil) },
		testLogger(), nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("NewL7Server: %v", err)
	}
	go proxy.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		proxy.Close(ctx)
	}()

	client := &http.Client{Timeout: 5 * time.Second}

	body := strings.Repeat("x", 100000)
	resp, err := client.Post(fmt.Sprintf("http://%s/test", proxy.Addr()), "text/plain", strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for unlimited body, got %d", resp.StatusCode)
	}
}

// TestRateLimitRetryAfterHeader verifies that every 429 response from
// the rate limiter includes a Retry-After header.
func TestRateLimitRetryAfterHeader(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		}),
	}
	go srv.Serve(ln)

	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(ln.Addr().String(), 1)})

	// Configure a very restrictive rate limiter: 1 req/s with burst 1.
	rl := ratelimit.NewLimiter(1, 1, 0)
	defer rl.Stop()

	cfg := config.ListenerConfig{
		Name:     "test_retry_after",
		Address:  "127.0.0.1:0",
		Protocol: "http",
		Pool:     "test_pool",
	}

	proxy, err := NewL7ServerFromGetterWithMetrics(
		cfg,
		func() *RouteTable { return NewRouteTable(nil, lb, nil) },
		testLogger(), nil, nil, rl,
	)
	if err != nil {
		t.Fatalf("NewL7Server: %v", err)
	}
	go proxy.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		proxy.Close(ctx)
	}()

	client := &http.Client{Timeout: 5 * time.Second}

	// First request should succeed (uses the burst token).
	resp, err := client.Get(fmt.Sprintf("http://%s/test", proxy.Addr()))
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	resp.Body.Close()

	// Second immediate request should be rate limited (429).
	for i := 0; i < 3; i++ {
		resp, err = client.Get(fmt.Sprintf("http://%s/test", proxy.Addr()))
		if err != nil {
			t.Fatalf("rate-limited request %d: %v", i, err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusTooManyRequests {
			t.Errorf("request %d: expected 429, got %d", i, resp.StatusCode)
			continue
		}

		retryAfter := resp.Header.Get("Retry-After")
		if retryAfter == "" {
			t.Errorf("request %d: missing Retry-After header on 429", i)
		} else if retryAfter != "1" {
			t.Errorf("request %d: Retry-After=%q, want %q", i, retryAfter, "1")
		}
	}
}

// TestHeaderInjectionCRLF verifies that \r\n in X-Forwarded-For
// values are stripped before forwarding to the backend.
func TestHeaderInjectionCRLF(t *testing.T) {
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
			w.WriteHeader(200)
		}),
	}
	go srv.Serve(ln)

	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(ln.Addr().String(), 1)})
	proxyAddr, stopProxy := startL7Proxy(t, nil, lb)
	defer stopProxy()

	// Send a request with \r\n in X-Forwarded-For via raw connection
	// to bypass http.Client sanitization.
	rawConn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("raw dial: %v", err)
	}
	defer rawConn.Close()

	rawConn.SetDeadline(time.Now().Add(5 * time.Second))

	// Craft a request with malicious X-Forwarded-For containing \r\n.
	request := "GET /test HTTP/1.1\r\n" +
		"Host: localhost\r\n" +
		"X-Forwarded-For: 1.2.3.4\r\nEvil-Header: injected\r\n\r\n"

	_, err = fmt.Fprint(rawConn, request)
	if err != nil {
		t.Fatalf("write request: %v", err)
	}

	// Read response.
	buf := make([]byte, 4096)
	n, _ := rawConn.Read(buf)
	_ = string(buf[:n])

	// Check the backend received headers.
	select {
	case h := <-receivedHeaders:
		xff := h.Get("X-Forwarded-For")
		// The \r\n should be stripped, so no "Evil-Header" should appear.
		if strings.Contains(xff, "Evil-Header") {
			t.Errorf("header injection succeeded: X-Forwarded-For = %q", xff)
		}
		if strings.ContainsAny(xff, "\r\n") {
			t.Errorf("X-Forwarded-For contains CR/LF: %q", xff)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("backend did not receive request within timeout")
	}
}

// TestSanitiseXFF verifies the sanitiseXFF helper directly.
func TestSanitiseXFF(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no injection", "1.2.3.4, 5.6.7.8", "1.2.3.4, 5.6.7.8"},
		{"LF injection", "1.2.3.4\nEvil: header", "1.2.3.4Evil: header"},
		{"CR injection", "1.2.3.4\rEvil: header", "1.2.3.4Evil: header"},
		{"CRLF injection", "1.2.3.4\r\nEvil: header", "1.2.3.4Evil: header"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitiseXFF(tt.in)
			if got != tt.want {
				t.Errorf("sanitiseXFF(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
