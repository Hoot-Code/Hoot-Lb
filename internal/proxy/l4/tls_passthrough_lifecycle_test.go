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
	"github.com/Hoot-Code/Hoot-Lb/internal/health"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
)

func TestTLSPassthroughMalformedInput(t *testing.T) {
	logger := logging.New(slog.LevelError, os.Stdout)
	cfg := config.ListenerConfig{
		Name:     "test_malformed",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "test_pool",
		TLS: &config.TLSConfig{
			Mode: "passthrough",
		},
	}

	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer("127.0.0.1:1", 1)})
	poolMap := map[string]balancer.LoadBalancer{"test_pool": lb}

	srv, err := NewTLSPassthroughServer(cfg, testSNIRouter(poolMap, map[string]health.FailureReporter{}, "test_pool"), logger, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("NewTLSPassthroughServer: %v", err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	proxyAddr := srv.listener.Addr().String()
	baseline := runtime.NumGoroutine()

	// Test 1: garbage bytes.
	testMalformed(t, proxyAddr, []byte("not tls at all"))
	time.Sleep(200 * time.Millisecond)

	// Test 2: truncated ClientHello (header says 200 bytes, only 10 sent).
	truncated := make([]byte, 15)
	truncated[0] = 22 // handshake
	truncated[3] = 0
	truncated[4] = 200 // record claims 200 bytes
	testMalformed(t, proxyAddr, truncated)
	time.Sleep(200 * time.Millisecond)

	// Test 3: oversized record length.
	oversized := make([]byte, 5)
	oversized[0] = 22
	oversized[3] = 0xFF
	oversized[4] = 0xFF // 65535 bytes — way over maxClientHelloSize
	testMalformed(t, proxyAddr, oversized)
	time.Sleep(200 * time.Millisecond)

	// Test 4: valid record header but wrong content type.
	wrongType := make([]byte, 5)
	wrongType[0] = 21 // alert, not handshake
	wrongType[3] = 0
	wrongType[4] = 2
	testMalformed(t, proxyAddr, wrongType)
	time.Sleep(200 * time.Millisecond)

	// Verify listener still accepts connections.
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("listener not accepting after malformed input: %v", err)
	}
	conn.Close()

	// Verify no goroutine leak.
	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	time.Sleep(200 * time.Millisecond)
	current := runtime.NumGoroutine()
	if current > baseline+10 {
		t.Errorf("goroutine leak after malformed input: baseline %d, now %d", baseline, current)
	}
}

func testMalformed(t *testing.T, addr string, data []byte) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(2 * time.Second))
	conn.Write(data)

	buf := make([]byte, 1024)
	conn.Read(buf)
}

func TestTLSPassthroughGoroutineLeak(t *testing.T) {
	echoLn, stopEcho := echoTCPServer(t)
	defer stopEcho()

	logger := logging.New(slog.LevelError, os.Stdout)
	cfg := config.ListenerConfig{
		Name:     "test_leak",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "test_pool",
		TLS: &config.TLSConfig{
			Mode: "passthrough",
		},
	}

	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(echoLn.Addr().String(), 1)})
	poolMap := map[string]balancer.LoadBalancer{"test_pool": lb}

	srv, err := NewTLSPassthroughServer(cfg, testSNIRouter(poolMap, map[string]health.FailureReporter{}, "test_pool"), logger, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("NewTLSPassthroughServer: %v", err)
	}
	go srv.Serve(context.Background())

	baseline := runtime.NumGoroutine()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			conn, err := net.DialTimeout("tcp", srv.listener.Addr().String(), 2*time.Second)
			if err != nil {
				return
			}
			fmt.Fprintf(conn, "garbage-%d\n", n)
			buf := make([]byte, 1024)
			conn.Read(buf)
			conn.Close()
		}(i)
	}
	wg.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Close(ctx)

	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	time.Sleep(200 * time.Millisecond)

	current := runtime.NumGoroutine()
	if current > baseline+5 {
		t.Errorf("goroutine leak: baseline %d, now %d", baseline, current)
	}
}

func TestTLSPassthroughSilentClientTimeout(t *testing.T) {
	logger := logging.New(slog.LevelError, os.Stdout)
	handshakeTimeout := 500 * time.Millisecond
	cfg := config.ListenerConfig{
		Name:     "test_silent",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "test_pool",
		TLS: &config.TLSConfig{
			Mode:             "passthrough",
			HandshakeTimeout: handshakeTimeout,
		},
	}

	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer("127.0.0.1:1", 1)})
	poolMap := map[string]balancer.LoadBalancer{"test_pool": lb}

	srv, err := NewTLSPassthroughServer(cfg, testSNIRouter(poolMap, map[string]health.FailureReporter{}, "test_pool"), logger, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("NewTLSPassthroughServer: %v", err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	proxyAddr := srv.listener.Addr().String()

	// Connect but send zero bytes — the server should close the
	// connection after the configured handshake timeout.
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Client deadline well beyond the server timeout to prove
	// the server acts, not our own deadline.
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	start := time.Now()
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected connection to be closed by server")
	}

	// A timeout error means our client deadline fired, not the
	// server — the slowloris protection is not working.
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatalf("connection closed by client timeout (%v), not by server — slowloris protection not working", elapsed)
	}

	if elapsed > handshakeTimeout+5*time.Second {
		t.Errorf("server took %v to close, expected ~%v", elapsed, handshakeTimeout)
	}
}

func TestTLSPassthroughNilTLSConfig(t *testing.T) {
	logger := logging.New(slog.LevelError, os.Stdout)
	cfg := config.ListenerConfig{
		Name:     "test_nil_tls",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "test_pool",
	}

	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer("127.0.0.1:1", 1)})
	poolMap := map[string]balancer.LoadBalancer{"test_pool": lb}

	_, err := NewTLSPassthroughServer(cfg, testSNIRouter(poolMap, map[string]health.FailureReporter{}, "test_pool"), logger, nil, nil, 0, 0)
	if err == nil {
		t.Fatal("expected error for nil TLS config")
	}
}
