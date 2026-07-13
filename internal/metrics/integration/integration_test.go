package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
	"github.com/Hoot-Code/Hoot-Lb/internal/metrics"
	"github.com/Hoot-Code/Hoot-Lb/internal/proxy/l4"
)

func testLogger() *slog.Logger {
	return logging.New(slog.LevelError, os.Stdout)
}

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

func TestIntegrationTCPTrafficMetrics(t *testing.T) {
	echoLn, stopEcho := echoTCPServer(t)
	defer stopEcho()

	registry := metrics.NewRegistry()
	connectionsTotal := registry.NewCounterVec("lb_connections_total", "Total connections", []string{"listener", "pool", "backend", "protocol"})
	connectionsActive := registry.NewGaugeVec("lb_connections_active", "Active connections", []string{"listener", "pool", "backend", "protocol"})
	bytesTransferred := registry.NewCounterVec("lb_bytes_transferred_total", "Bytes transferred", []string{"listener", "pool", "backend", "direction"})

	proxyMetrics := &l4.ProxyMetrics{
		ConnectionsTotal:  connectionsTotal,
		ConnectionsActive: connectionsActive,
		BytesTransferred:  bytesTransferred,
	}

	logger := testLogger()
	cfg := config.ListenerConfig{
		Name:     "test_tcp",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "test_pool",
	}
	backends := []balancer.Backend{balancer.NewServer(echoLn.Addr().String(), 1)}
	lb := balancer.NewRoundRobin(backends)

	srv, err := l4.NewTCPServer(cfg, l4.StaticPoolGetter(lb, nil, nil), logger, proxyMetrics, nil, nil, 0, 0)
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
		t.Fatalf("dial proxy: %v", err)
	}
	fmt.Fprintf(conn, "hello\n")
	scanner := bufio.NewScanner(conn)
	scanner.Scan()
	conn.Close()

	time.Sleep(100 * time.Millisecond)

	var buf bytes.Buffer
	metrics.WriteExposition(&buf, registry)
	output := buf.String()

	if !strings.Contains(output, `lb_connections_total{`) {
		t.Fatalf("connections_total not present:\n%s", output)
	}
	if !strings.Contains(output, `lb_bytes_transferred_total{`) {
		t.Fatalf("bytes_transferred not present:\n%s", output)
	}
}

func TestIntegrationAccessLog(t *testing.T) {
	echoLn, stopEcho := echoTCPServer(t)
	defer stopEcho()

	var logBuf bytes.Buffer
	accessLogger := metrics.NewAccessLogger(&logBuf)

	logger := testLogger()
	cfg := config.ListenerConfig{
		Name:     "test_tcp_log",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "test_pool",
	}
	backends := []balancer.Backend{balancer.NewServer(echoLn.Addr().String(), 1)}
	lb := balancer.NewRoundRobin(backends)

	srv, err := l4.NewTCPServer(cfg, l4.StaticPoolGetter(lb, nil, nil), logger, nil, accessLogger, nil, 0, 0)
	if err != nil {
		t.Fatalf("NewTCPServer: %v", err)
	}
	go srv.Serve(context.Background())

	conn, err := net.DialTimeout("tcp", srv.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	fmt.Fprintf(conn, "hello\n")
	scanner := bufio.NewScanner(conn)
	scanner.Scan()
	conn.Close()

	// Close server and wait for all handlers to finish before reading log buffer.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Close(ctx)

	lines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
	if len(lines) < 1 {
		t.Fatalf("expected at least 1 access log line, got %d", len(lines))
	}

	var entry metrics.AccessLogEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if entry.Listener != "test_tcp_log" {
		t.Fatalf("expected listener 'test_tcp_log', got %q", entry.Listener)
	}
	if entry.Protocol != "tcp" {
		t.Fatalf("expected protocol 'tcp', got %q", entry.Protocol)
	}
}

func TestIntegrationConcurrentMetrics(t *testing.T) {
	echoLn, stopEcho := echoTCPServer(t)
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
		Name:     "test_concurrent",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "test_pool",
	}
	backends := []balancer.Backend{balancer.NewServer(echoLn.Addr().String(), 1)}
	lb := balancer.NewRoundRobin(backends)

	srv, err := l4.NewTCPServer(cfg, l4.StaticPoolGetter(lb, nil, nil), logger, proxyMetrics, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("NewTCPServer: %v", err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				conn, err := net.DialTimeout("tcp", srv.Addr().String(), 2*time.Second)
				if err != nil {
					return
				}
				fmt.Fprintf(conn, "test\n")
				scanner := bufio.NewScanner(conn)
				scanner.Scan()
				conn.Close()
			}
		}()
	}

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var buf bytes.Buffer
			metrics.WriteExposition(&buf, registry)
		}()
	}

	wg.Wait()
	time.Sleep(100 * time.Millisecond)

	var buf bytes.Buffer
	if err := metrics.WriteExposition(&buf, registry); err != nil {
		t.Fatalf("final scrape failed: %v", err)
	}
}

func TestMetricsServerEndpoint(t *testing.T) {
	registry := metrics.NewRegistry()
	c := registry.NewCounter("test_total", "test")
	c.Add(1)

	msrv, err := metrics.NewMetricsServer("127.0.0.1:0", "/metrics", registry, testLogger())
	if err != nil {
		t.Fatalf("NewMetricsServer: %v", err)
	}
	go msrv.Start(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		msrv.Close(ctx)
	}()

	resp, err := http.Get(fmt.Sprintf("http://%s/metrics", msrv.Addr().String()))
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("expected text/plain content type, got %q", ct)
	}
}
