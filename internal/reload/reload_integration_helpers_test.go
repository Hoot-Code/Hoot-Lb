package reload

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/proxy/l7"
	lbruntime "github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

func tcpIdentBackend(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					fmt.Fprintf(c, "%s\n", addr)
				}
			}(conn)
		}
	}()
	return addr, func() { ln.Close() }
}

func tcpSlowBackend(t *testing.T) (string, func(), chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	unblock := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					<-unblock
					fmt.Fprintf(c, "%s\n", addr)
				}
			}(conn)
		}
	}()
	return addr, func() { ln.Close() }, unblock
}

func httpIdentBackend(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintf(w, "%s", addr)
		}),
	}
	go srv.Serve(ln)
	return addr, func() { srv.Close() }
}

func genCertFiles(t *testing.T, hosts []string) config.TLSCertConfig {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hosts[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	certDER, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
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

func writeConfigContent(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func buildTestRouteTable(l config.ListenerConfig, s *lbruntime.Snapshot) *l7.RouteTable {
	var routes []l7.Route
	for _, r := range l.Routes {
		route := l7.Route{Host: r.Host, PathPrefix: r.PathPrefix}
		if r.Header != nil {
			route.HeaderName = r.Header.Name
			route.HeaderValue = r.Header.Value
		}
		if len(r.Split) > 0 {
			split := make([]l7.SplitEntry, len(r.Split))
			for i, sc := range r.Split {
				ps := s.PoolStates[sc.Pool]
				split[i] = l7.SplitEntry{LB: ps.LB, FR: ps.FR, Weight: sc.Weight, Sticky: ps.Sticky}
			}
			route.Split = split
		} else {
			ps := s.PoolStates[r.Pool]
			route.LB, route.FR, route.Sticky = ps.LB, ps.FR, ps.Sticky
		}
		routes = append(routes, route)
	}
	dp := s.PoolStates[l.Pool]
	return l7.NewRouteTable(routes, dp.LB, dp.FR)
}

const reloadCfgTemplate = `global:
  log_level: error
  shutdown_timeout: 5s
listeners:
  - name: %s
    address: "%s"
    protocol: %s
    pool: %s
pools:
  - name: pool_a
    algorithm: round_robin
    backends:
      - address: "%s"
        weight: 1
    health_check:
      type: none
  - name: pool_b
    algorithm: round_robin
    backends:
      - address: "%s"
        weight: 1
    health_check:
      type: none
`

const httpReloadCfgTemplate = `global:
  log_level: error
  shutdown_timeout: 5s
listeners:
  - name: http_proxy
    address: "127.0.0.1:0"
    protocol: http
    pool: pool_a
pools:
  - name: pool_a
    algorithm: round_robin
    backends:
      - address: "%s"
        weight: 1
    health_check:
      type: none
  - name: pool_b
    algorithm: round_robin
    backends:
      - address: "%s"
        weight: 1
    health_check:
      type: none
`
