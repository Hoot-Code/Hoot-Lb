package l7

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
)

func TestL7IPHashStickiness(t *testing.T) {
	startIdentBackend := func(t *testing.T) (string, func()) {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("ident backend listen: %v", err)
		}
		addr := ln.Addr().String()
		srv := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				fmt.Fprintf(w, "%s", addr)
			}),
		}
		go srv.Serve(ln)
		return addr, func() { srv.Close() }
	}

	addrA, stopA := startIdentBackend(t)
	defer stopA()
	addrB, stopB := startIdentBackend(t)
	defer stopB()

	lb := balancer.NewIPHash([]balancer.Backend{
		balancer.NewServer(addrA, 1),
		balancer.NewServer(addrB, 1),
	})

	proxyAddr, stopProxy := startL7Proxy(t, nil, lb)
	defer stopProxy()

	client := &http.Client{Timeout: 5 * time.Second}

	const numRequests = 10
	seenBackends := make(map[string]int)
	for i := 0; i < numRequests; i++ {
		resp, err := client.Get(fmt.Sprintf("http://%s/ping-%d", proxyAddr, i))
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		seenBackends[string(body)]++
	}

	if len(seenBackends) != 1 {
		t.Fatalf("IPHash stickiness broken: hit %d backends (want 1): %v", len(seenBackends), seenBackends)
	}
	for backend, count := range seenBackends {
		if count != numRequests {
			t.Fatalf("backend %s got %d requests, want %d", backend, count, numRequests)
		}
		t.Logf("all %d requests from 127.0.0.1 landed on %s", numRequests, backend)
	}
}

func TestL7ConnReleaser(t *testing.T) {
	var hitA, hitB atomic.Int32

	blockA := make(chan struct{})
	blockB := make(chan struct{})

	startBlockBackend := func(t *testing.T, hit *atomic.Int32, unblock chan struct{}, ready chan struct{}) (string, func()) {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("block backend listen: %v", err)
		}
		addr := ln.Addr().String()
		srv := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hit.Add(1)
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				ready <- struct{}{}
				<-unblock
			}),
		}
		go srv.Serve(ln)
		return addr, func() { srv.Close() }
	}

	readyA := make(chan struct{})
	readyB := make(chan struct{})

	addrA, stopA := startBlockBackend(t, &hitA, blockA, readyA)
	defer stopA()
	addrB, stopB := startBlockBackend(t, &hitB, blockB, readyB)
	defer stopB()

	lb := balancer.NewLeastConnections([]balancer.Backend{
		balancer.NewServer(addrA, 1),
		balancer.NewServer(addrB, 1),
	})

	proxyAddr, stopProxy := startL7Proxy(t, nil, lb)
	defer stopProxy()

	client := &http.Client{Timeout: 10 * time.Second}

	// First request → should go to backend A (both at 0 connections)
	resp1Ch := make(chan *http.Response, 1)
	go func() {
		resp, _ := client.Get(fmt.Sprintf("http://%s/first", proxyAddr))
		resp1Ch <- resp
	}()

	select {
	case <-readyA:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not reach backend A")
	}

	// Second request → should go to backend B (A has 1, B has 0)
	resp2Ch := make(chan *http.Response, 1)
	go func() {
		resp, _ := client.Get(fmt.Sprintf("http://%s/second", proxyAddr))
		resp2Ch <- resp
	}()

	select {
	case <-readyB:
	case <-time.After(5 * time.Second):
		t.Fatal("second request did not reach backend B")
	}

	if hitA.Load() != 1 || hitB.Load() != 1 {
		t.Errorf("expected each backend hit once, got A=%d B=%d", hitA.Load(), hitB.Load())
	}

	// Unblock both backends
	close(blockA)
	close(blockB)

	// Clean up
	resp1 := <-resp1Ch
	if resp1 != nil {
		resp1.Body.Close()
	}
	resp2 := <-resp2Ch
	if resp2 != nil {
		resp2.Body.Close()
	}
}

// stubHealthChecker is a test-only FailureReporter that records calls.
type stubHealthChecker struct {
	failed []string
}

func (s *stubHealthChecker) ReportFailure(b balancer.Backend) {
	s.failed = append(s.failed, b.Address())
}

func TestL7FailureReporter(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	realAddr := ln.Addr().String()
	fakeAddr := "127.0.0.1:1"

	fr := &stubHealthChecker{}

	routes := []Route{
		{PathPrefix: "/fail", LB: balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(fakeAddr, 1)}), FR: fr},
	}

	defaultLB := balancer.NewRoundRobin([]balancer.Backend{
		balancer.NewServer(realAddr, 1),
	})

	proxyAddr, stopProxy := startL7Proxy(t, routes, defaultLB, fr)
	defer stopProxy()

	client := &http.Client{Timeout: 2 * time.Second}

	_, err = client.Get(fmt.Sprintf("http://%s/fail", proxyAddr))
	_ = err

	time.Sleep(200 * time.Millisecond)

	if len(fr.failed) == 0 {
		t.Error("ReportFailure was not called after dial failure")
	}

	found := false
	for _, addr := range fr.failed {
		if addr == fakeAddr {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ReportFailure called with wrong addr, got %v, want %s", fr.failed, fakeAddr)
	}
}
