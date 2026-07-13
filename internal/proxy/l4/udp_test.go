package l4

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
)

// echoUDPServer starts a simple UDP echo server. It returns the
// connection and a stop function.
func echoUDPServer(t *testing.T) (*net.UDPConn, func()) {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		buf := make([]byte, 65536)
		for {
			n, remote, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			conn.WriteToUDP(buf[:n], remote)
		}
	}()
	return conn, func() { conn.Close() }
}

func TestUDPProxyEcho(t *testing.T) {
	echoConn, stopEcho := echoUDPServer(t)
	defer stopEcho()

	logger := logging.New(slog.LevelError, os.Stdout)
	cfg := config.ListenerConfig{
		Name:     "test_udp",
		Address:  "127.0.0.1:0",
		Protocol: "udp",
		Pool:     "test_pool",
	}
	backends := []balancer.Backend{balancer.NewServer(echoConn.LocalAddr().String(), 1)}
	lb := balancer.NewRoundRobin(backends)

	srv, err := NewUDPServer(cfg, testPoolGetter(lb, nil), logger, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewUDPServer: %v", err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	// Connect to the proxy and exchange datagrams.
	clientAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	clientConn, err := net.ListenUDP("udp", clientAddr)
	if err != nil {
		t.Fatalf("client listen: %v", err)
	}
	defer clientConn.Close()

	proxyAddr, _ := net.ResolveUDPAddr("udp", srv.listener.LocalAddr().String())
	clientConn.SetDeadline(time.Now().Add(3 * time.Second))

	testMessages := []string{"hello", "world", "udp proxy test", "datagram round-trip"}
	for _, msg := range testMessages {
		_, err := clientConn.WriteToUDP([]byte(msg), proxyAddr)
		if err != nil {
			t.Fatalf("write to proxy: %v", err)
		}

		buf := make([]byte, 65536)
		n, _, err := clientConn.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("read from proxy for %q: %v", msg, err)
		}
		got := string(buf[:n])
		if got != msg {
			t.Errorf("echo mismatch: sent %q, got %q", msg, got)
		}
	}
}

func TestUDPProxyManyDatagrams(t *testing.T) {
	echoConn, stopEcho := echoUDPServer(t)
	defer stopEcho()

	logger := logging.New(slog.LevelError, os.Stdout)
	cfg := config.ListenerConfig{
		Name:     "test_udp_many",
		Address:  "127.0.0.1:0",
		Protocol: "udp",
		Pool:     "test_pool",
	}
	backends := []balancer.Backend{balancer.NewServer(echoConn.LocalAddr().String(), 1)}
	lb := balancer.NewRoundRobin(backends)

	srv, err := NewUDPServer(cfg, testPoolGetter(lb, nil), logger, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewUDPServer: %v", err)
	}
	// Use short timeouts for test speed.
	srv.sessionTimeout = 100 * time.Millisecond
	srv.cleanupInterval = 50 * time.Millisecond
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	baseline := runtime.NumGoroutine()

	const numSessions = 30
	var wg sync.WaitGroup
	proxyAddr, _ := net.ResolveUDPAddr("udp", srv.listener.LocalAddr().String())

	for i := 0; i < numSessions; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			clientAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
			clientConn, err := net.ListenUDP("udp", clientAddr)
			if err != nil {
				t.Errorf("session %d: listen: %v", n, err)
				return
			}
			defer clientConn.Close()
			clientConn.SetDeadline(time.Now().Add(3 * time.Second))

			msg := fmt.Sprintf("msg-%d", n)
			if _, err := clientConn.WriteToUDP([]byte(msg), proxyAddr); err != nil {
				t.Errorf("session %d: write: %v", n, err)
				return
			}
			buf := make([]byte, 65536)
			nread, _, err := clientConn.ReadFromUDP(buf)
			if err != nil {
				t.Errorf("session %d: read: %v", n, err)
				return
			}
			if string(buf[:nread]) != msg {
				t.Errorf("session %d: got %q, want %q", n, string(buf[:nread]), msg)
			}
		}(i)
	}
	wg.Wait()

	// Wait for sessions to be evicted.
	time.Sleep(300 * time.Millisecond)
	runtime.GC()
	time.Sleep(200 * time.Millisecond)

	current := runtime.NumGoroutine()
	if current > baseline+10 {
		t.Errorf("goroutine leak after %d UDP sessions: baseline %d, now %d", numSessions, baseline, current)
	}
}

func TestUDPSessionEviction(t *testing.T) {
	echoConn, stopEcho := echoUDPServer(t)
	defer stopEcho()

	logger := logging.New(slog.LevelError, os.Stdout)
	cfg := config.ListenerConfig{
		Name:     "test_udp_evict",
		Address:  "127.0.0.1:0",
		Protocol: "udp",
		Pool:     "test_pool",
	}
	backends := []balancer.Backend{balancer.NewServer(echoConn.LocalAddr().String(), 1)}
	lb := balancer.NewRoundRobin(backends)

	srv, err := NewUDPServer(cfg, testPoolGetter(lb, nil), logger, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewUDPServer: %v", err)
	}
	// Use a very short session timeout and cleanup interval for testing.
	srv.sessionTimeout = 100 * time.Millisecond
	srv.cleanupInterval = 50 * time.Millisecond
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	// Create a session by sending a datagram.
	clientAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	clientConn, err := net.ListenUDP("udp", clientAddr)
	if err != nil {
		t.Fatalf("client listen: %v", err)
	}
	defer clientConn.Close()

	proxyAddr, _ := net.ResolveUDPAddr("udp", srv.listener.LocalAddr().String())
	clientConn.SetDeadline(time.Now().Add(3 * time.Second))

	if _, err := clientConn.WriteToUDP([]byte("ping"), proxyAddr); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 65536)
	if _, _, err := clientConn.ReadFromUDP(buf); err != nil {
		t.Fatalf("read: %v", err)
	}

	// Confirm session exists.
	srv.mu.Lock()
	count := len(srv.sessions)
	srv.mu.Unlock()
	if count != 1 {
		t.Fatalf("expected 1 session, got %d", count)
	}

	// Wait for eviction (200ms covers session timeout + cleanup interval).
	time.Sleep(300 * time.Millisecond)

	srv.mu.Lock()
	count = len(srv.sessions)
	srv.mu.Unlock()
	if count != 0 {
		t.Errorf("expected 0 sessions after eviction, got %d", count)
	}
}
