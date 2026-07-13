package l4

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/health"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
	"github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

func TestTLSPassthroughFallback(t *testing.T) {
	fallbackBackend, stopFallback := startTLSBackend(t, []string{"fallback.example.com"})
	defer stopFallback()

	logger := logging.New(slog.LevelError, os.Stdout)
	cfg := config.ListenerConfig{
		Name:     "test_fallback",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "fallback_pool",
		TLS: &config.TLSConfig{
			Mode: "passthrough",
			Routes: []config.TLSRouteConfig{
				{Host: "specific.example.com", Pool: "specific_pool"},
			},
		},
	}

	fallbackLB := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(fallbackBackend, 1)})
	specificLB := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer("127.0.0.1:1", 1)})

	poolMap := map[string]balancer.LoadBalancer{
		"fallback_pool": fallbackLB,
		"specific_pool": specificLB,
	}

	failReporters := map[string]health.FailureReporter{}

	sniRouter := func(sni string) (balancer.LoadBalancer, health.FailureReporter, runtime.Outcome) {
		if sni == "specific.example.com" {
			return poolMap["specific_pool"], failReporters["specific_pool"], nil
		}
		return poolMap["fallback_pool"], failReporters["fallback_pool"], nil
	}

	srv, err := NewTLSPassthroughServer(cfg, sniRouter, logger, nil, nil, 0, 0)
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

	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp", proxyAddr,
		&tls.Config{ServerName: "unknown.example.com", InsecureSkipVerify: true},
	)
	if err != nil {
		t.Fatalf("dial unknown SNI: %v", err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "ping\n")
	scanner := bufio.NewScanner(conn)
	scanner.Scan()
	if got := scanner.Text(); got != "backend=fallback.example.com" {
		t.Errorf("expected fallback backend, got %q", got)
	}
}

func TestTLSPassthroughNoSNI(t *testing.T) {
	fallbackBackend, stopFallback := startTLSBackend(t, []string{"fallback.example.com"})
	defer stopFallback()

	logger := logging.New(slog.LevelError, os.Stdout)
	cfg := config.ListenerConfig{
		Name:     "test_no_sni",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "fallback_pool",
		TLS: &config.TLSConfig{
			Mode: "passthrough",
		},
	}

	fallbackLB := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(fallbackBackend, 1)})

	poolMap := map[string]balancer.LoadBalancer{
		"fallback_pool": fallbackLB,
	}

	failReporters := map[string]health.FailureReporter{}

	sniRouter := func(sni string) (balancer.LoadBalancer, health.FailureReporter, runtime.Outcome) {
		_ = sni
		return poolMap["fallback_pool"], failReporters["fallback_pool"], nil
	}

	srv, err := NewTLSPassthroughServer(cfg, sniRouter, logger, nil, nil, 0, 0)
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

	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp", proxyAddr,
		&tls.Config{InsecureSkipVerify: true},
	)
	if err != nil {
		t.Fatalf("dial no SNI: %v", err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "ping\n")
	scanner := bufio.NewScanner(conn)
	scanner.Scan()
	if got := scanner.Text(); got != "backend=fallback.example.com" {
		t.Errorf("expected fallback backend for no SNI, got %q", got)
	}
}
