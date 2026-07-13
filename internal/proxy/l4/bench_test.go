package l4

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
)

// BenchmarkTCPRelay measures the throughput of a single bidirectional
// TCP relay through the proxy. It uses a persistent connection and
// sends payloadSize bytes per operation, reporting bytes/op.
func BenchmarkTCPRelay(b *testing.B) {
	for _, size := range []int{1024, 64 * 1024, 1024 * 1024} {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			benchmarkTCPRelay(b, size)
		})
	}
}

func benchmarkTCPRelay(b *testing.B, payloadSize int) {
	b.Helper()

	// Start a backend that reads payloadSize bytes and echoes them
	// back, then waits for the next payload.
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer backendLn.Close()
	go func() {
		for {
			conn, err := backendLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, payloadSize)
				for {
					if _, err := io.ReadFull(c, buf); err != nil {
						return
					}
					if _, err := c.Write(buf); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	logger := testLogger()
	cfg := config.ListenerConfig{
		Name:     "bench_tcp",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "bench_pool",
	}
	backends := []balancer.Backend{balancer.NewServer(backendLn.Addr().String(), 1)}
	lb := balancer.NewRoundRobin(backends)

	srv, err := NewTCPServer(cfg, testPoolGetter(lb, nil), logger, nil, nil, nil, 0, 0)
	if err != nil {
		b.Fatal(err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	proxyAddr := srv.listener.Addr().String()
	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	// Open a single persistent connection to avoid ephemeral port
	// exhaustion under high iteration counts.
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	buf := make([]byte, payloadSize)

	b.SetBytes(int64(payloadSize))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := conn.Write(payload); err != nil {
			b.Fatal(err)
		}
		if _, err := io.ReadFull(conn, buf); err != nil {
			b.Fatal(err)
		}
	}
}
