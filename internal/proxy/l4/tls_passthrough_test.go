package l4

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"log/slog"
	"math/big"
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

// startTLSBackend starts a TLS server that responds with its own cert's
// subject information, proving the client saw the backend's cert.
func startTLSBackend(t *testing.T, hosts []string) (string, func()) {
	t.Helper()

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serialNumber, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: hosts[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	template.DNSNames = append(template.DNSNames, hosts...)
	certDER, _ := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)

	tlsCert := tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	tlsLn := tls.NewListener(ln, &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS12,
	})

	go func() {
		for {
			conn, err := tlsLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				scanner := bufio.NewScanner(c)
				for scanner.Scan() {
					_ = scanner.Text()
					fmt.Fprintf(c, "backend=%s\n", template.Subject.CommonName)
				}
			}(conn)
		}
	}()

	return ln.Addr().String(), func() { tlsLn.Close() }
}

func TestTLSPassthroughProvesBackendCert(t *testing.T) {
	backendAddr, stopBackend := startTLSBackend(t, []string{"backend.example.com"})
	defer stopBackend()

	logger := logging.New(slog.LevelError, os.Stdout)
	cfg := config.ListenerConfig{
		Name:     "test_passthrough",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "test_pool",
		TLS: &config.TLSConfig{
			Mode: "passthrough",
			Routes: []config.TLSRouteConfig{
				{Host: "backend.example.com", Pool: "test_pool"},
			},
		},
	}

	backends := []balancer.Backend{balancer.NewServer(backendAddr, 1)}
	lb := balancer.NewRoundRobin(backends)

	srv, err := NewTLSPassthroughServer(cfg, testSNIRouter(map[string]balancer.LoadBalancer{"test_pool": lb}, map[string]health.FailureReporter{}, "test_pool"), logger, nil, nil, 0, 0)
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

	tlsConn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp", proxyAddr,
		&tls.Config{
			ServerName:         "backend.example.com",
			InsecureSkipVerify: true,
		},
	)
	if err != nil {
		t.Fatalf("TLS dial through proxy: %v", err)
	}
	defer tlsConn.Close()

	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("expected peer certificates from backend")
	}

	if state.PeerCertificates[0].Subject.CommonName != "backend.example.com" {
		t.Errorf("expected backend cert (backend.example.com), got CN=%q", state.PeerCertificates[0].Subject.CommonName)
	}

	fmt.Fprintf(tlsConn, "hello\n")
	scanner := bufio.NewScanner(tlsConn)
	scanner.Scan()
	got := scanner.Text()
	if got != "backend=backend.example.com" {
		t.Errorf("expected backend response, got %q", got)
	}
}

func TestTLSPassthroughSNIRouting(t *testing.T) {
	backendA, stopA := startTLSBackend(t, []string{"a.example.com"})
	defer stopA()
	backendB, stopB := startTLSBackend(t, []string{"b.example.com"})
	defer stopB()

	logger := logging.New(slog.LevelError, os.Stdout)
	cfg := config.ListenerConfig{
		Name:     "test_sni_routing",
		Address:  "127.0.0.1:0",
		Protocol: "tcp",
		Pool:     "fallback_pool",
		TLS: &config.TLSConfig{
			Mode: "passthrough",
			Routes: []config.TLSRouteConfig{
				{Host: "a.example.com", Pool: "pool_a"},
				{Host: "b.example.com", Pool: "pool_b"},
			},
		},
	}

	lbA := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendA, 1)})
	lbB := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendB, 1)})

	poolMap := map[string]balancer.LoadBalancer{
		"pool_a":        lbA,
		"pool_b":        lbB,
		"fallback_pool": balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer("127.0.0.1:1", 1)}),
	}

	failReporters := map[string]health.FailureReporter{}

	sniRouter := func(sni string) (balancer.LoadBalancer, health.FailureReporter, runtime.Outcome) {
		switch sni {
		case "a.example.com":
			return poolMap["pool_a"], failReporters["pool_a"], nil
		case "b.example.com":
			return poolMap["pool_b"], failReporters["pool_b"], nil
		default:
			return poolMap["fallback_pool"], failReporters["fallback_pool"], nil
		}
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

	connA, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp", proxyAddr,
		&tls.Config{ServerName: "a.example.com", InsecureSkipVerify: true},
	)
	if err != nil {
		t.Fatalf("dial a.example.com: %v", err)
	}
	defer connA.Close()

	fmt.Fprintf(connA, "ping\n")
	scannerA := bufio.NewScanner(connA)
	scannerA.Scan()
	if got := scannerA.Text(); got != "backend=a.example.com" {
		t.Errorf("a.example.com: expected backend A, got %q", got)
	}

	connB, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp", proxyAddr,
		&tls.Config{ServerName: "b.example.com", InsecureSkipVerify: true},
	)
	if err != nil {
		t.Fatalf("dial b.example.com: %v", err)
	}
	defer connB.Close()

	fmt.Fprintf(connB, "ping\n")
	scannerB := bufio.NewScanner(connB)
	scannerB.Scan()
	if got := scannerB.Text(); got != "backend=b.example.com" {
		t.Errorf("b.example.com: expected backend B, got %q", got)
	}
}
