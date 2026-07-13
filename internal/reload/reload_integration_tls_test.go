package reload

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/proxy/l7"
	lbruntime "github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

func TestTLSCertReloadViaWatcher(t *testing.T) {
	backendAddr, stopBackend := httpIdentBackend(t)
	defer stopBackend()

	certV1 := genCertFiles(t, []string{"v1.example.com"})
	certV2 := genCertFiles(t, []string{"v2.example.com"})

	dir := t.TempDir()
	initialCfg := fmt.Sprintf(`global:
  log_level: error
  shutdown_timeout: 5s
listeners:
  - name: https_proxy
    address: "127.0.0.1:0"
    protocol: http
    pool: web_pool
    tls:
      mode: terminate
      certificates:
        - host: v1.example.com
          cert_file: "%s"
          key_file: "%s"
pools:
  - name: web_pool
    algorithm: round_robin
    backends:
      - address: "%s"
        weight: 1
    health_check:
      type: none
`, certV1.CertFile, certV1.KeyFile, backendAddr)
	cfgPath := writeConfig(t, dir, initialCfg)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	snap, err := lbruntime.BuildSnapshot(cfg, testLogger())
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	snapPtr := lbruntime.NewAtomicSnapshot(snap)
	certStores := snap.CertStores

	listenerCfg := cfg.Listeners[0]
	getter := l7.RouteTableGetter(func() *l7.RouteTable {
		return buildTestRouteTable(listenerCfg, snapPtr.Load())
	})
	srv, err := l7.NewL7ServerFromGetter(listenerCfg, getter, testLogger(), certStores["https_proxy"])
	if err != nil {
		t.Fatalf("NewL7Server: %v", err)
	}
	go srv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Close(ctx)
	}()

	watcher := NewWatcher(cfgPath, 0, snapPtr, certStores, testLogger(), nil)
	proxyAddr := srv.Addr().String()

	// TLS connect → should present v1 cert.
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
	state1 := tlsConn1.ConnectionState()
	if len(state1.PeerCertificates) == 0 {
		t.Fatal("no peer certs on v1 connection")
	}
	if state1.PeerCertificates[0].Subject.CommonName != "v1.example.com" {
		t.Errorf("v1 cert CN: got %q, want v1.example.com", state1.PeerCertificates[0].Subject.CommonName)
	}
	tlsConn1.Close()

	// Reload: change cert to v2 via the full Watcher pipeline.
	reloadCfg := fmt.Sprintf(`global:
  log_level: error
  shutdown_timeout: 5s
listeners:
  - name: https_proxy
    address: "127.0.0.1:0"
    protocol: http
    pool: web_pool
    tls:
      mode: terminate
      certificates:
        - host: v2.example.com
          cert_file: "%s"
          key_file: "%s"
pools:
  - name: web_pool
    algorithm: round_robin
    backends:
      - address: "%s"
        weight: 1
    health_check:
      type: none
`, certV2.CertFile, certV2.KeyFile, backendAddr)
	writeConfig(t, dir, reloadCfg)
	watcher.TriggerReload()

	// New TLS connection → should present v2 cert.
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
	state2 := tlsConn2.ConnectionState()
	if len(state2.PeerCertificates) == 0 {
		t.Fatal("no peer certs on v2 connection")
	}
	dnsNames := state2.PeerCertificates[0].DNSNames
	if len(dnsNames) == 0 || dnsNames[0] != "v2.example.com" {
		t.Errorf("v2 cert DNS: got %v, want [v2.example.com]", dnsNames)
	}
}
