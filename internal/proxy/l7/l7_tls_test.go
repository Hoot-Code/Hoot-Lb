package l7

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
	"github.com/Hoot-Code/Hoot-Lb/internal/tlsutil"
)

// generateCert creates a self-signed cert/key pair for the given hosts,
// writes them to disk, and returns a TLSCertConfig.
func generateCert(t *testing.T, hosts []string) config.TLSCertConfig {
	t.Helper()
	dir := t.TempDir()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	serialNumber, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, h)
		}
	}

	certDER, _ := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)

	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	cf, _ := os.Create(certPath)
	pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	cf.Close()

	keyDER, _ := x509.MarshalECPrivateKey(key)
	kf, _ := os.Create(keyPath)
	pem.Encode(kf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	kf.Close()

	return config.TLSCertConfig{Host: hosts[0], CertFile: certPath, KeyFile: keyPath}
}

// startTLSTerminationProxy starts an L7 server with TLS termination
// using the provided certificate configs. It returns the proxy address
// and a stop function.
func startTLSTerminationProxy(t *testing.T, certs []config.TLSCertConfig, routes []Route, defaultLB balancer.LoadBalancer) (string, func()) {
	t.Helper()

	cs, err := tlsutil.NewCertStore(certs)
	if err != nil {
		t.Fatalf("NewCertStore: %v", err)
	}

	cfg := config.ListenerConfig{
		Name:     "test_tls",
		Address:  "127.0.0.1:0",
		Protocol: "http",
		Pool:     "test_pool",
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

func TestTLSTerminationExactSNI(t *testing.T) {
	certA := generateCert(t, []string{"a.example.com"})
	certB := generateCert(t, []string{"b.example.com"})

	lbA := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer("127.0.0.1:1", 1)})
	lbB := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer("127.0.0.1:1", 1)})

	routes := []Route{
		{Host: "a.example.com", LB: lbA},
		{Host: "b.example.com", LB: lbB},
	}

	proxyAddr, stopProxy := startTLSTerminationProxy(t, []config.TLSCertConfig{certA, certB}, routes, lbA)
	defer stopProxy()

	// Connect with SNI a.example.com.
	tlsConn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp", proxyAddr,
		&tls.Config{
			ServerName: "a.example.com",
			// Skip verification since we use self-signed certs.
			InsecureSkipVerify: true,
		},
	)
	if err != nil {
		t.Fatalf("dial with SNI a.example.com: %v", err)
	}
	defer tlsConn.Close()

	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("expected peer certificates")
	}
	if state.PeerCertificates[0].Subject.CommonName != "test" {
		t.Errorf("unexpected common name: %s", state.PeerCertificates[0].Subject.CommonName)
	}
	if len(state.PeerCertificates[0].DNSNames) > 0 && state.PeerCertificates[0].DNSNames[0] != "a.example.com" {
		t.Errorf("expected DNS name a.example.com, got %v", state.PeerCertificates[0].DNSNames)
	}

	// Connect with SNI b.example.com.
	tlsConn2, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp", proxyAddr,
		&tls.Config{
			ServerName:         "b.example.com",
			InsecureSkipVerify: true,
		},
	)
	if err != nil {
		t.Fatalf("dial with SNI b.example.com: %v", err)
	}
	defer tlsConn2.Close()

	state2 := tlsConn2.ConnectionState()
	if len(state2.PeerCertificates) == 0 {
		t.Fatal("expected peer certificates for b.example.com")
	}
	if len(state2.PeerCertificates[0].DNSNames) > 0 && state2.PeerCertificates[0].DNSNames[0] != "b.example.com" {
		t.Errorf("expected DNS name b.example.com, got %v", state2.PeerCertificates[0].DNSNames)
	}
}

func TestTLSTerminationDefaultCert(t *testing.T) {
	// defaultCert has Host="" which makes it the fallback cert.
	defaultCert := generateCert(t, []string{"default.example.com"})
	defaultCert.Host = "" // override to make it the default
	specificCert := generateCert(t, []string{"specific.example.com"})

	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer("127.0.0.1:1", 1)})

	proxyAddr, stopProxy := startTLSTerminationProxy(t, []config.TLSCertConfig{defaultCert, specificCert}, nil, lb)
	defer stopProxy()

	// Connect with unknown SNI — should get default cert.
	tlsConn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp", proxyAddr,
		&tls.Config{
			ServerName:         "unknown.example.com",
			InsecureSkipVerify: true,
		},
	)
	if err != nil {
		t.Fatalf("dial with unknown SNI: %v", err)
	}
	defer tlsConn.Close()

	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("expected peer certificates")
	}
	// The default cert should have DNS name default.example.com.
	if len(state.PeerCertificates[0].DNSNames) > 0 && state.PeerCertificates[0].DNSNames[0] != "default.example.com" {
		t.Errorf("expected default cert, got DNS names: %v", state.PeerCertificates[0].DNSNames)
	}

	// Connect with empty SNI (InsecureSkipVerify + no ServerName) — also gets default.
	tlsConn2, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp", proxyAddr,
		&tls.Config{
			InsecureSkipVerify: true,
		},
	)
	if err != nil {
		t.Fatalf("dial with empty SNI: %v", err)
	}
	defer tlsConn2.Close()

	state2 := tlsConn2.ConnectionState()
	if len(state2.PeerCertificates) == 0 {
		t.Fatal("expected peer certificates for empty SNI")
	}
	if len(state2.PeerCertificates[0].DNSNames) > 0 && state2.PeerCertificates[0].DNSNames[0] != "default.example.com" {
		t.Errorf("expected default cert for empty SNI, got DNS names: %v", state2.PeerCertificates[0].DNSNames)
	}
}

func TestTLSTerminationHTTPRouting(t *testing.T) {
	backendA, stopA := echoHTTPBackend(t)
	defer stopA()
	backendB, stopB := echoHTTPBackend(t)
	defer stopB()

	certA := generateCert(t, []string{"a.example.com"})
	certB := generateCert(t, []string{"b.example.com"})

	lbA := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendA, 1)})
	lbB := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendB, 1)})

	routes := []Route{
		{Host: "a.example.com", LB: lbA},
		{Host: "b.example.com", LB: lbB},
	}

	proxyAddr, stopProxy := startTLSTerminationProxy(t, []config.TLSCertConfig{certA, certB}, routes, lbA)
	defer stopProxy()

	// Connect with SNI a.example.com, send HTTP request.
	tlsConn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp", proxyAddr,
		&tls.Config{
			ServerName:         "a.example.com",
			InsecureSkipVerify: true,
		},
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer tlsConn.Close()

	fmt.Fprintf(tlsConn, "GET /test HTTP/1.1\r\nHost: a.example.com\r\n\r\n")
	buf := make([]byte, 4096)
	n, _ := tlsConn.Read(buf)
	resp := string(buf[:n])
	if len(resp) == 0 {
		t.Fatal("expected HTTP response")
	}
}

func TestTLSTerminationCertRotation(t *testing.T) {
	certV1 := generateCert(t, []string{"v1.example.com"})
	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer("127.0.0.1:1", 1)})

	cs, err := tlsutil.NewCertStore([]config.TLSCertConfig{certV1})
	if err != nil {
		t.Fatalf("NewCertStore: %v", err)
	}

	cfg := config.ListenerConfig{
		Name:     "test_rotation",
		Address:  "127.0.0.1:0",
		Protocol: "http",
		Pool:     "test_pool",
	}

	srv, err := NewL7Server(cfg, nil, lb, nil, logging.New(slog.LevelError, os.Stdout), cs)
	if err != nil {
		t.Fatalf("NewL7Server: %v", err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	proxyAddr := srv.listener.Addr().String()

	// Connect with v1 SNI — should succeed.
	tlsConn1, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp", proxyAddr,
		&tls.Config{
			ServerName:         "v1.example.com",
			InsecureSkipVerify: true,
		},
	)
	if err != nil {
		t.Fatalf("dial v1: %v", err)
	}
	tlsConn1.Close()

	// Load v2 cert and replace atomically.
	certV2 := generateCert(t, []string{"v2.example.com"})
	newCerts, err := tlsutil.LoadCertificates([]config.TLSCertConfig{certV2})
	if err != nil {
		t.Fatalf("LoadCertificates: %v", err)
	}
	cs.Replace(newCerts)

	// New connection with v2 SNI should get v2 cert.
	tlsConn2, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp", proxyAddr,
		&tls.Config{
			ServerName:         "v2.example.com",
			InsecureSkipVerify: true,
		},
	)
	if err != nil {
		t.Fatalf("dial v2: %v", err)
	}
	defer tlsConn2.Close()

	state := tlsConn2.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("expected peer certificates")
	}
	if len(state.PeerCertificates[0].DNSNames) > 0 && state.PeerCertificates[0].DNSNames[0] != "v2.example.com" {
		t.Errorf("expected v2 cert, got DNS names: %v", state.PeerCertificates[0].DNSNames)
	}

	// Old v1 SNI should no longer match.
	_, err = tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp", proxyAddr,
		&tls.Config{
			ServerName:         "v1.example.com",
			InsecureSkipVerify: true,
		},
	)
	if err == nil {
		t.Error("expected error for v1 SNI after rotation")
	}
}
