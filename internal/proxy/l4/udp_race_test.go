package l4

import (
	"context"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
)

// TestUDPCloseConcurrentWithDatagrams verifies that calling Close()
// while datagrams are being processed does not trigger a WaitGroup
// misuse panic, and that the eviction goroutine is fully exited by
// the time Close() returns.
func TestUDPCloseConcurrentWithDatagrams(t *testing.T) {
	echoConn, stopEcho := echoUDPServer(t)
	defer stopEcho()

	logger := logging.New(slog.LevelError, os.Stdout)
	cfg := config.ListenerConfig{
		Name:     "test_udp_race",
		Address:  "127.0.0.1:0",
		Protocol: "udp",
		Pool:     "test_pool",
	}
	backends := []balancer.Backend{balancer.NewServer(echoConn.LocalAddr().String(), 1)}
	lb := balancer.NewRoundRobin(backends)

	const iterations = 50
	for i := 0; i < iterations; i++ {
		srv, err := NewUDPServer(cfg, testPoolGetter(lb, nil), logger, nil, nil, nil)
		if err != nil {
			t.Fatalf("NewUDPServer: %v", err)
		}
		srv.sessionTimeout = 100 * time.Millisecond
		srv.cleanupInterval = 50 * time.Millisecond

		ctx, cancel := context.WithCancel(context.Background())
		go srv.Serve(ctx)

		proxyAddr, _ := net.ResolveUDPAddr("udp", srv.listener.LocalAddr().String())

		var wg sync.WaitGroup
		for j := 0; j < 10; j++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				clientAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
				clientConn, err := net.ListenUDP("udp", clientAddr)
				if err != nil {
					return
				}
				defer clientConn.Close()
				clientConn.SetDeadline(time.Now().Add(2 * time.Second))
				clientConn.WriteToUDP([]byte("ping"), proxyAddr)
				buf := make([]byte, 65536)
				clientConn.ReadFromUDP(buf)
			}(j)
		}

		wg.Wait()

		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		srv.Close(closeCtx)
		closeCancel()
		cancel()
	}
}

// TestUDPEvictionGoroutineTracked verifies that the eviction goroutine
// started in Serve() is tracked by the WaitGroup, so Close() returns
// only after the eviction goroutine has fully exited.
func TestUDPEvictionGoroutineTracked(t *testing.T) {
	echoConn, stopEcho := echoUDPServer(t)
	defer stopEcho()

	logger := logging.New(slog.LevelError, os.Stdout)
	cfg := config.ListenerConfig{
		Name:     "test_udp_evict_track",
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
	srv.sessionTimeout = 50 * time.Millisecond
	srv.cleanupInterval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)

	proxyAddr, _ := net.ResolveUDPAddr("udp", srv.listener.LocalAddr().String())
	clientAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	clientConn, err := net.ListenUDP("udp", clientAddr)
	if err != nil {
		t.Fatalf("client listen: %v", err)
	}
	defer clientConn.Close()
	clientConn.WriteToUDP([]byte("ping"), proxyAddr)
	buf := make([]byte, 65536)
	clientConn.SetDeadline(time.Now().Add(2 * time.Second))
	clientConn.ReadFromUDP(buf)

	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeCancel()
	err = srv.Close(closeCtx)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	cancel()
}

// TestUDPProxyCloseConcurrentRacyShutdown exercises the race between
// Close() and an incoming datagram creating a new session. This
// specifically targets the WaitGroup misuse: Add called concurrently
// with Wait.
func TestUDPProxyCloseConcurrentRacyShutdown(t *testing.T) {
	echoConn, stopEcho := echoUDPServer(t)
	defer stopEcho()

	logger := logging.New(slog.LevelError, os.Stdout)
	cfg := config.ListenerConfig{
		Name:     "test_udp_racy",
		Address:  "127.0.0.1:0",
		Protocol: "udp",
		Pool:     "test_pool",
	}
	backends := []balancer.Backend{balancer.NewServer(echoConn.LocalAddr().String(), 1)}
	lb := balancer.NewRoundRobin(backends)

	const iterations = 5
	for i := 0; i < iterations; i++ {
		srv, err := NewUDPServer(cfg, testPoolGetter(lb, nil), logger, nil, nil, nil)
		if err != nil {
			t.Fatalf("NewUDPServer: %v", err)
		}
		srv.sessionTimeout = 100 * time.Millisecond
		srv.cleanupInterval = 50 * time.Millisecond

		proxyAddr, _ := net.ResolveUDPAddr("udp", srv.listener.LocalAddr().String())

		ctx, cancel := context.WithCancel(context.Background())
		go srv.Serve(ctx)

		var wg sync.WaitGroup
		for j := 0; j < 20; j++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				clientAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
				clientConn, err := net.ListenUDP("udp", clientAddr)
				if err != nil {
					return
				}
				defer clientConn.Close()
				clientConn.SetDeadline(time.Now().Add(1 * time.Second))
				clientConn.WriteToUDP([]byte("ping"), proxyAddr)
				buf := make([]byte, 65536)
				clientConn.ReadFromUDP(buf)
			}(j)
		}

		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		srv.Close(closeCtx)
		closeCancel()
		cancel()

		wg.Wait()
	}
}
