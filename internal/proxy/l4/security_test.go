package l4

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
)

// TestConnectionLimitRejectsExcess verifies that when max_connections
// is set, the (max+1)th connection is rejected immediately while the
// first max connections remain open and functional.
func TestConnectionLimitRejectsExcess(t *testing.T) {
	echoLn, stopEcho := echoTCPServer(t)
	defer stopEcho()

	logger := testLogger()
	cfg := config.ListenerConfig{
		Name:     "test_conn_limit",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "test_pool",
	}
	backends := []balancer.Backend{balancer.NewServer(echoLn.Addr().String(), 1)}
	lb := balancer.NewRoundRobin(backends)

	const maxConns = 3
	srv, err := NewTCPServer(cfg, testPoolGetter(lb, nil), logger, nil, nil, nil, maxConns, 0)
	if err != nil {
		t.Fatalf("NewTCPServer: %v", err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	// Open exactly maxConns connections — all should succeed.
	var conns []net.Conn
	for i := 0; i < maxConns; i++ {
		conn, err := net.DialTimeout("tcp", srv.Addr().String(), 2*time.Second)
		if err != nil {
			t.Fatalf("conn %d: dial: %v", i, err)
		}
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		fmt.Fprintf(conn, "hello-%d\n", i)
		scanner := bufio.NewScanner(conn)
		if !scanner.Scan() {
			conn.Close()
			t.Fatalf("conn %d: no response", i)
		}
		got := strings.TrimSpace(scanner.Text())
		if got != fmt.Sprintf("hello-%d", i) {
			t.Errorf("conn %d: got %q", i, got)
		}
		conns = append(conns, conn)
	}

	// The (max+1)th connection should be rejected immediately.
	conn, err := net.DialTimeout("tcp", srv.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("excess conn: dial: %v", err)
	}
	defer conn.Close()

	// The server should close the excess connection immediately.
	// Read should return 0 bytes or error.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	n, err := conn.Read(buf)
	if n != 0 {
		t.Errorf("excess conn: expected 0 bytes, got %d", n)
	}
	if err == nil {
		t.Errorf("excess conn: expected error (EOF or closed), got nil")
	}

	// Clean up existing connections.
	for _, c := range conns {
		c.Close()
	}
}

// TestTCPIdleTimeoutTearsDownIdleConnection verifies that a TCP relay
// with tcp_idle_timeout set tears down the connection when both sides
// go silent. The deadline resets on data, so the timeout applies to
// idle periods.
func TestTCPIdleTimeoutTearsDownIdleConnection(t *testing.T) {
	// Create a backend that sends one response then waits.
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen: %v", err)
	}
	defer backendLn.Close()

	ready := make(chan struct{})
	go func() {
		conn, err := backendLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		if scanner.Scan() {
			fmt.Fprintf(conn, "response\n")
		}
		close(ready)
		// Keep the connection open; the relay idle timeout should
		// tear it down.
		select {}
	}()

	logger := testLogger()
	cfg := config.ListenerConfig{
		Name:     "test_idle_timeout",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "test_pool",
	}
	backends := []balancer.Backend{balancer.NewServer(backendLn.Addr().String(), 1)}
	lb := balancer.NewRoundRobin(backends)

	const idleTimeout = 100 * time.Millisecond
	srv, err := NewTCPServer(cfg, testPoolGetter(lb, nil), logger, nil, nil, nil, 0, idleTimeout)
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
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Send a request and receive a response.
	fmt.Fprintf(conn, "ping\n")
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		t.Fatal("no response from backend")
	}
	got := strings.TrimSpace(scanner.Text())
	if got != "response" {
		t.Fatalf("got %q, want %q", got, "response")
	}

	// Now go silent. The idle timeout should tear down the relay
	// within ~2x the timeout.
	<-ready // ensure backend is ready
	start := time.Now()
	scanOK := scanner.Scan() // should return false (connection closed)
	elapsed := time.Since(start)

	if scanOK {
		t.Errorf("expected connection to be closed, but got data: %q", scanner.Text())
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("idle timeout took too long: %v (want < 500ms)", elapsed)
	}
	if elapsed < 50*time.Millisecond {
		t.Errorf("idle timeout fired too quickly: %v (want >= ~50ms)", elapsed)
	}
}

// TestTCPNoIdleTimeoutStaysOpen verifies that when tcp_idle_timeout
// is 0, a silent connection stays open (no timeout applied).
func TestTCPNoIdleTimeoutStaysOpen(t *testing.T) {
	echoLn, stopEcho := echoTCPServer(t)
	defer stopEcho()

	logger := testLogger()
	cfg := config.ListenerConfig{
		Name:     "test_no_idle",
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

	conn, err := net.DialTimeout("tcp", srv.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "ping\n")
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		t.Fatal("no response")
	}

	// Wait longer than any reasonable timeout.
	time.Sleep(300 * time.Millisecond)

	// Connection should still be usable.
	fmt.Fprintf(conn, "ping2\n")
	if !scanner.Scan() {
		t.Fatal("connection died after idle period (should have stayed open)")
	}
}
