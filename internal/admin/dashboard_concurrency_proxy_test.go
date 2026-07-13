package admin

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/proxy/l4"
)

// TestDashboardConcurrencyDoesNotAffectProxyLatency runs 50
// concurrent dashboard WebSocket viewers against a real admin server
// while real proxy traffic flows through a real L4 TCP proxy sharing
// the same process, and compares request latency against a baseline
// with zero dashboard viewers. There's no hard threshold enforced —
// the goal is to show the comparison numbers,
// not gate the build on a flaky timing assertion — but it does fail
// on a large, unambiguous regression as a smoke check.
func TestDashboardConcurrencyDoesNotAffectProxyLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping latency comparison under -short")
	}

	// A trivial TCP echo backend: read whatever's sent, write it
	// straight back. Good enough to measure round-trip latency
	// through the real L4 proxy without needing an HTTP backend.
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen: %v", err)
	}
	defer backendLn.Close()
	go func() {
		for {
			c, err := backendLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(c)
		}
	}()

	backend := balancer.NewServer(backendLn.Addr().String(), 1)
	lb := balancer.NewRoundRobin([]balancer.Backend{backend})

	proxyCfg := config.ListenerConfig{
		Name:     "test-tcp-listener",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "test-pool",
	}
	proxy, err := l4.NewTCPServer(proxyCfg, l4.StaticPoolGetter(lb, nil, nil), testLogger(), nil, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("NewTCPServer: %v", err)
	}
	proxyCtx, proxyCancel := context.WithCancel(context.Background())
	defer proxyCancel()
	go proxy.Serve(proxyCtx)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		proxy.Close(ctx)
	}()
	time.Sleep(50 * time.Millisecond)

	measureLatency := func(n int) time.Duration {
		start := time.Now()
		buf := make([]byte, 5)
		for i := 0; i < n; i++ {
			conn, err := net.DialTimeout("tcp", proxy.Addr().String(), 2*time.Second)
			if err != nil {
				t.Fatalf("dial proxy: %v", err)
			}
			conn.SetDeadline(time.Now().Add(2 * time.Second))
			if _, err := conn.Write([]byte("hello")); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := io.ReadFull(conn, buf); err != nil {
				t.Fatalf("read: %v", err)
			}
			conn.Close()
		}
		return time.Since(start) / time.Duration(n)
	}

	const requestsPerMeasurement = 300
	baseline := measureLatency(requestsPerMeasurement)

	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	const viewers = 50
	conns := make([]*wsTestConn, 0, viewers)
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for i := 0; i < viewers; i++ {
		conn, _, err := dialDashboardWS(baseURL, testToken)
		if err != nil {
			t.Fatalf("dialDashboardWS viewer #%d: %v", i, err)
		}
		conns = append(conns, conn)
	}
	// Drain each viewer's initial push so none of them are sitting on
	// an un-drained socket buffer skewing the comparison.
	for _, c := range conns {
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		readWSTextFrame(c)
	}

	withViewers := measureLatency(requestsPerMeasurement)

	t.Logf("proxy-path average latency: baseline (0 dashboard viewers) = %s, with %d dashboard viewers = %s (%.1fx)",
		baseline, viewers, withViewers, float64(withViewers)/float64(baseline))

	// Smoke check only: a 10x regression would indicate the dashboard
	// is genuinely contending with the proxy path (e.g. a shared lock
	// it shouldn't be touching), not just measurement noise.
	if withViewers > baseline*10 {
		t.Fatalf("proxy latency with %d dashboard viewers (%s) is more than 10x the baseline (%s) — possible contention with the proxy path",
			viewers, withViewers, baseline)
	}

}
