package l4

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
)

// TestTCPCloseConcurrentWithAccept verifies that calling Close() while
// connections are being accepted does not trigger a WaitGroup misuse
// panic (the "Add called concurrently with Wait" race).
func TestTCPCloseConcurrentWithAccept(t *testing.T) {
	echoLn, stopEcho := echoTCPServer(t)
	defer stopEcho()

	logger := testLogger()
	cfg := config.ListenerConfig{
		Name:     "test_tcp_race",
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

	proxyAddr := srv.listener.Addr().String()

	const iterations = 50
	for i := 0; i < iterations; i++ {
		// Spawn several concurrent connections.
		var wg sync.WaitGroup
		for j := 0; j < 10; j++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
				if err != nil {
					return
				}
				fmt.Fprintf(conn, "ping\n")
				scanner := bufio.NewScanner(conn)
				scanner.Scan()
				conn.Close()
			}(j)
		}

		// Close concurrently with incoming connections.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		srv.Close(ctx)
		cancel()

		wg.Wait()

		// Recreate for the next iteration.
		srv, err = NewTCPServer(cfg, testPoolGetter(lb, nil), logger, nil, nil, nil, 0, 0)
		if err != nil {
			t.Fatalf("NewTCPServer (recreate): %v", err)
		}
		go srv.Serve(context.Background())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Close(ctx)
}
