package integration

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/metrics"
	"github.com/Hoot-Code/Hoot-Lb/internal/proxy/l4"
	"github.com/Hoot-Code/Hoot-Lb/internal/proxy/l7"
)

func echoUDPServer(t *testing.T) (*net.UDPConn, func()) {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve UDP addr: %v", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("listen UDP: %v", err)
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

func TestIntegrationUDPTrafficMetrics(t *testing.T) {
	echoLn, stopEcho := echoUDPServer(t)
	defer stopEcho()

	registry := metrics.NewRegistry()
	connectionsTotal := registry.NewCounterVec("lb_connections_total", "Total connections", []string{"listener", "pool", "backend", "protocol"})
	connectionsActive := registry.NewGaugeVec("lb_connections_active", "Active connections", []string{"listener", "pool", "backend", "protocol"})
	bytesTransferred := registry.NewCounterVec("lb_bytes_transferred_total", "Bytes transferred", []string{"listener", "pool", "backend", "direction"})
	dialFailures := registry.NewCounterVec("lb_dial_failures_total", "Dial failures", []string{"listener", "pool", "backend"})

	proxyMetrics := &l4.ProxyMetrics{
		ConnectionsTotal:  connectionsTotal,
		ConnectionsActive: connectionsActive,
		BytesTransferred:  bytesTransferred,
		DialFailures:      dialFailures,
	}

	logger := testLogger()
	cfg := config.ListenerConfig{
		Name:     "test_udp",
		Address:  "127.0.0.1:0",
		Protocol: "udp",
		Pool:     "test_pool",
	}
	backends := []balancer.Backend{balancer.NewServer(echoLn.LocalAddr().String(), 1)}
	lb := balancer.NewRoundRobin(backends)

	srv, err := l4.NewUDPServer(cfg, l4.StaticPoolGetter(lb, nil, nil), logger, proxyMetrics, nil, nil)
	if err != nil {
		t.Fatalf("NewUDPServer: %v", err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	udpAddr, err := net.ResolveUDPAddr("udp", srv.Addr().String())
	if err != nil {
		t.Fatalf("resolve proxy addr: %v", err)
	}

	for i := 0; i < 3; i++ {
		conn, err := net.DialUDP("udp", nil, udpAddr)
		if err != nil {
			t.Fatalf("dial UDP proxy: %v", err)
		}
		msg := fmt.Sprintf("ping-%d\n", i)
		conn.Write([]byte(msg))
		buf := make([]byte, 65536)
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		conn.Read(buf)
		conn.Close()
	}

	time.Sleep(200 * time.Millisecond)

	output := scrapeMetrics(t, registry)
	if !strings.Contains(output, "lb_connections_total{") {
		t.Fatalf("connections_total not present:\n%s", output)
	}
	if !strings.Contains(output, "lb_connections_active{") {
		t.Fatalf("connections_active not present:\n%s", output)
	}
	if !strings.Contains(output, `protocol="udp"`) {
		t.Fatalf("udp protocol label not present:\n%s", output)
	}
	if !strings.Contains(output, `pool="test_pool"`) {
		t.Fatalf("pool label not present:\n%s", output)
	}
	if !strings.Contains(output, `backend="`) {
		t.Fatalf("backend label not present:\n%s", output)
	}
}

func TestIntegrationL7TrafficMetrics(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen backend: %v", err)
	}
	backendAddr := echoLn.Addr().String()
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintf(w, "ok")
		}),
	}
	go srv.Serve(echoLn)
	defer srv.Close()

	registry := metrics.NewRegistry()
	connectionsTotal := registry.NewCounterVec("lb_connections_total", "Total connections", []string{"listener", "pool", "backend", "protocol"})
	connectionsActive := registry.NewGaugeVec("lb_connections_active", "Active connections", []string{"listener", "pool", "backend", "protocol"})
	requestDuration := registry.NewHistogramVec("lb_request_duration", "Request duration", []string{"listener", "pool", "backend"}, metrics.DefaultHistogramBuckets)
	dialFailures := registry.NewCounterVec("lb_dial_failures_total", "Dial failures", []string{"listener", "pool", "backend"})

	l7Metrics := &l7.L7Metrics{
		ConnectionsTotal:  connectionsTotal,
		ConnectionsActive: connectionsActive,
		RequestDuration:   requestDuration,
		DialFailures:      dialFailures,
	}

	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendAddr, 1)})
	table := l7.NewRouteTable(nil, lb, nil)
	getter := l7.RouteTableGetter(func() *l7.RouteTable { return table })

	l7Cfg := config.ListenerConfig{
		Name:     "test_http",
		Address:  "127.0.0.1:0",
		Protocol: "http",
		Pool:     "test_pool",
	}

	l7Srv, err := l7.NewL7ServerFromGetterWithMetrics(l7Cfg, getter, testLogger(), nil, l7Metrics, nil)
	if err != nil {
		t.Fatalf("NewL7ServerFromGetterWithMetrics: %v", err)
	}
	go l7Srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		l7Srv.Close(ctx)
	}()

	proxyAddr := l7Srv.Addr().String()
	client := &http.Client{Timeout: 5 * time.Second}

	const numRequests = 5
	for i := 0; i < numRequests; i++ {
		resp, err := client.Get(fmt.Sprintf("http://%s/request-%d", proxyAddr, i))
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	time.Sleep(200 * time.Millisecond)

	output := scrapeMetrics(t, registry)
	if !strings.Contains(output, "lb_connections_total{") {
		t.Fatalf("connections_total not present:\n%s", output)
	}

	lines := strings.Split(output, "\n")
	countChecked := false
	subSecondBucketFound := false
	for _, line := range lines {
		if strings.HasPrefix(line, "lb_request_duration_count{") {
			parts := strings.SplitN(line, "}", 2)
			if len(parts) == 2 {
				val := strings.TrimSpace(parts[1])
				count, err := strconv.Atoi(val)
				if err != nil {
					t.Fatalf("failed to parse request_duration_count %q: %v", val, err)
				}
				if count != numRequests {
					t.Fatalf("request_duration_count = %d, want %d:\n%s", count, numRequests, output)
				}
				countChecked = true
			}
		}
		if strings.HasPrefix(line, "lb_request_duration_bucket{") {
			leStart := strings.Index(line, `le="`)
			if leStart == -1 {
				continue
			}
			leStart += 4
			leEnd := strings.Index(line[leStart:], `"`)
			if leEnd == -1 {
				continue
			}
			leVal := line[leStart : leStart+leEnd]
			if leVal == "+Inf" {
				continue
			}
			le, err := strconv.ParseFloat(leVal, 64)
			if err != nil || le >= 1.0 {
				continue
			}
			parts := strings.SplitN(line, "}", 2)
			if len(parts) == 2 {
				bucketCount, err := strconv.Atoi(strings.TrimSpace(parts[1]))
				if err == nil && bucketCount > 0 {
					subSecondBucketFound = true
				}
			}
		}
	}
	if !countChecked {
		t.Fatalf("lb_request_duration_count not found in metrics:\n%s", output)
	}
	if !subSecondBucketFound {
		t.Fatalf("no bucket below 1s has a non-zero count (fast loopback requests should land there):\n%s", output)
	}
}
