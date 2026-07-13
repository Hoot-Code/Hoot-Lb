package l7

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
	"github.com/Hoot-Code/Hoot-Lb/internal/tlsutil"
)

// startTLSTerminationProxyWithTLS is like startTLSTerminationProxy but
// accepts a TLSConfig to control MinVersion and other TLS settings.
func startTLSTerminationProxyWithTLS(t *testing.T, certs []config.TLSCertConfig, tlsCfg *config.TLSConfig, routes []Route, defaultLB balancer.LoadBalancer) (string, func()) {
	t.Helper()

	cs, err := tlsutil.NewCertStore(certs)
	if err != nil {
		t.Fatalf("NewCertStore: %v", err)
	}

	cfg := config.ListenerConfig{
		Name:     "test_tls_version",
		Address:  "127.0.0.1:0",
		Protocol: "http",
		Pool:     "test_pool",
		TLS:      tlsCfg,
	}

	srv, err := NewL7Server(cfg, routes, defaultLB, nil, logging.New(slog.LevelError, os.Stdout), cs)
	if err != nil {
		t.Fatalf("NewL7Server: %v", err)
	}
	go srv.Serve(context.Background())
	return srv.listener.Addr().String(), func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}
}

func TestTLS11RejectedByDefault(t *testing.T) {
	cert := generateCert(t, []string{"test.example.com"})
	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer("127.0.0.1:1", 1)})

	// No TLSConfig means MinVersion defaults to tls12.
	proxyAddr, stopProxy := startTLSTerminationProxy(t, []config.TLSCertConfig{cert}, nil, lb)
	defer stopProxy()

	// Force a TLS 1.1 handshake by capping the client's max version.
	_, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp", proxyAddr,
		&tls.Config{
			ServerName:         "test.example.com",
			MaxVersion:         tls.VersionTLS11,
			InsecureSkipVerify: true,
		},
	)
	if err == nil {
		t.Fatal("expected TLS 1.1 connection to be rejected with default MinVersion (tls12)")
	}
}

func TestTLS13OnlyRejectsTLS12(t *testing.T) {
	cert := generateCert(t, []string{"test.example.com"})
	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer("127.0.0.1:1", 1)})

	tlsCfg := &config.TLSConfig{MinVersion: "tls13"}
	proxyAddr, stopProxy := startTLSTerminationProxyWithTLS(t, []config.TLSCertConfig{cert}, tlsCfg, nil, lb)
	defer stopProxy()

	// Force a TLS 1.2 handshake by capping the client's max version.
	_, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp", proxyAddr,
		&tls.Config{
			ServerName:         "test.example.com",
			MaxVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true,
		},
	)
	if err == nil {
		t.Fatal("expected TLS 1.2 connection to be rejected with MinVersion tls13")
	}
}
