package l4

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
)

// testLogger returns a quiet logger for tests.
func testLogger() *slog.Logger {
	return logging.New(slog.LevelError, os.Stdout)
}

// echoTCPServer starts a simple TCP echo server that reads lines and
// writes them back. It returns the listener and a stop function.
func echoTCPServer(t *testing.T) (net.Listener, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo server listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				scanner := bufio.NewScanner(c)
				for scanner.Scan() {
					fmt.Fprintf(c, "%s\n", scanner.Text())
				}
			}(conn)
		}
	}()
	return ln, func() { ln.Close() }
}

func TestTCPProxyEcho(t *testing.T) {
	echoLn, stopEcho := echoTCPServer(t)
	defer stopEcho()

	logger := testLogger()
	cfg := config.ListenerConfig{
		Name:     "test_tcp",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "test_pool",
	}
	backends := []balancer.Backend{balancer.NewServer(echoLn.Addr().String(), 1)}
	lb := balancer.NewRoundRobin(backends)

	srv, err := NewTCPServer(cfg, testPoolGetter(lb, nil), logger, nil, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("NewTCPServer: %v", err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	// Connect to the proxy and exchange data.
	conn, err := net.DialTimeout("tcp", srv.listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(3 * time.Second))

	testLines := []string{"hello", "world", "foo bar", "lb proxy test"}
	for _, line := range testLines {
		fmt.Fprintf(conn, "%s\n", line)
		scanner := bufio.NewScanner(conn)
		if !scanner.Scan() {
			t.Fatalf("expected response for %q, got EOF", line)
		}
		got := strings.TrimSpace(scanner.Text())
		if got != line {
			t.Errorf("echo mismatch: sent %q, got %q", line, got)
		}
	}
}

func TestTCPProxyClientClose(t *testing.T) {
	echoLn, stopEcho := echoTCPServer(t)
	defer stopEcho()

	logger := testLogger()
	cfg := config.ListenerConfig{
		Name:     "test_tcp_close",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "test_pool",
	}
	backends := []balancer.Backend{balancer.NewServer(echoLn.Addr().String(), 1)}
	lb := balancer.NewRoundRobin(backends)

	srv, err := NewTCPServer(cfg, testPoolGetter(lb, nil), logger, nil, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("NewTCPServer: %v", err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	baseline := runtime.NumGoroutine()

	// Open a connection, send a line, read it back, then close.
	conn, err := net.DialTimeout("tcp", srv.listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	fmt.Fprintf(conn, "ping\n")
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		t.Fatal("expected echo response")
	}
	conn.Close()

	// Wait for goroutines to settle.
	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	time.Sleep(200 * time.Millisecond)

	current := runtime.NumGoroutine()
	if current > baseline+5 {
		t.Errorf("goroutine leak: baseline %d, now %d", baseline, current)
	}
}

// TestIPHashStickinessThroughProxy sends multiple connections from the
// same loopback IP (but different source ports) through a TCPServer
// backed by an IPHash balancer. It verifies that every connection
// lands on the same backend, proving the proxy layer correctly
// populates the balancer.ClientKey context value.
func TestIPHashStickinessThroughProxy(t *testing.T) {
	// Start two echo backends that identify themselves. When a client
	// connects, the server reads a line and replies with its own
	// listener address so the client knows which backend was selected.
	startIdentBackend := func(t *testing.T) (string, func()) {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("ident backend listen: %v", err)
		}
		addr := ln.Addr().String()
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go func(c net.Conn) {
					defer c.Close()
					scanner := bufio.NewScanner(c)
					for scanner.Scan() {
						fmt.Fprintf(c, "%s\n", addr)
					}
				}(conn)
			}
		}()
		return addr, func() { ln.Close() }
	}

	addrA, stopA := startIdentBackend(t)
	defer stopA()
	addrB, stopB := startIdentBackend(t)
	defer stopB()

	logger := testLogger()
	cfg := config.ListenerConfig{
		Name:     "test_iphash_tcp",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "test_pool",
	}
	backends := []balancer.Backend{
		balancer.NewServer(addrA, 1),
		balancer.NewServer(addrB, 1),
	}
	lb := balancer.NewIPHash(backends)

	srv, err := NewTCPServer(cfg, testPoolGetter(lb, nil), logger, nil, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("NewTCPServer: %v", err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	proxyAddr := srv.listener.Addr().String()

	// Send several connections from the same loopback IP on different
	// source ports. Each connection sends a line and reads back the
	// backend's self-identified address.
	const numConns = 10
	seenBackends := make(map[string]int) // backend addr → connection count
	for i := 0; i < numConns; i++ {
		conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
		if err != nil {
			t.Fatalf("conn %d: dial: %v", i, err)
		}
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		fmt.Fprintf(conn, "ping-%d\n", i)
		scanner := bufio.NewScanner(conn)
		if !scanner.Scan() {
			conn.Close()
			t.Fatalf("conn %d: no response", i)
		}
		got := strings.TrimSpace(scanner.Text())
		seenBackends[got]++
		conn.Close()
	}

	// All connections from the same client IP must have landed on the
	// same backend — exactly one backend should appear in the results.
	if len(seenBackends) != 1 {
		t.Fatalf("IPHash stickiness broken: connections hit %d distinct backends (want 1): %v",
			len(seenBackends), seenBackends)
	}
	for backend, count := range seenBackends {
		if count != numConns {
			t.Fatalf("backend %s got %d connections, want %d", backend, count, numConns)
		}
		t.Logf("all %d connections from 127.0.0.1 landed on %s (IPHash stickiness verified)", numConns, backend)
	}
}

func TestTCPProxyManyConnections(t *testing.T) {
	echoLn, stopEcho := echoTCPServer(t)
	defer stopEcho()

	logger := testLogger()
	cfg := config.ListenerConfig{
		Name:     "test_tcp_many",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "test_pool",
	}
	backends := []balancer.Backend{balancer.NewServer(echoLn.Addr().String(), 1)}
	lb := balancer.NewRoundRobin(backends)

	srv, err := NewTCPServer(cfg, testPoolGetter(lb, nil), logger, nil, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("NewTCPServer: %v", err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	baseline := runtime.NumGoroutine()

	const numConns = 50
	var wg sync.WaitGroup
	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			conn, err := net.DialTimeout("tcp", srv.listener.Addr().String(), 2*time.Second)
			if err != nil {
				t.Errorf("conn %d: dial: %v", n, err)
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			fmt.Fprintf(conn, "msg-%d\n", n)
			scanner := bufio.NewScanner(conn)
			if !scanner.Scan() {
				t.Errorf("conn %d: no response", n)
				return
			}
			got := strings.TrimSpace(scanner.Text())
			if got != fmt.Sprintf("msg-%d", n) {
				t.Errorf("conn %d: got %q", n, got)
			}
		}(i)
	}
	wg.Wait()

	// Wait for goroutines to settle.
	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	time.Sleep(200 * time.Millisecond)

	current := runtime.NumGoroutine()
	// Allow some headroom for the Go runtime's internal goroutines.
	if current > baseline+10 {
		t.Errorf("goroutine leak after %d connections: baseline %d, now %d", numConns, baseline, current)
	}
}
