package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
)

// generateSelfSignedCert creates a self-signed certificate and key for
// the given hosts, writes them to disk, and returns the paths. The
// caller should clean up the temp directory when done.
func generateSelfSignedCert(t *testing.T, hosts []string) (certPath, keyPath string) {
	t.Helper()

	dir := t.TempDir()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}

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

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	certFile, err := os.Create(certPath)
	if err != nil {
		t.Fatalf("create cert file: %v", err)
	}
	defer certFile.Close()
	pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyFile, err := os.Create(keyPath)
	if err != nil {
		t.Fatalf("create key file: %v", err)
	}
	defer keyFile.Close()
	pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPath, keyPath
}

func TestNewCertStore(t *testing.T) {
	certPath, keyPath := generateSelfSignedCert(t, []string{"example.com"})

	cs, err := NewCertStore([]config.TLSCertConfig{
		{Host: "example.com", CertFile: certPath, KeyFile: keyPath},
	})
	if err != nil {
		t.Fatalf("NewCertStore: %v", err)
	}

	certs := cs.Certs()
	if len(certs) != 1 {
		t.Fatalf("expected 1 cert, got %d", len(certs))
	}
	if _, ok := certs["example.com"]; !ok {
		t.Error("missing cert for example.com")
	}
}

func TestGetCertificateExactMatch(t *testing.T) {
	certPath, keyPath := generateSelfSignedCert(t, []string{"example.com"})

	cs, err := NewCertStore([]config.TLSCertConfig{
		{Host: "example.com", CertFile: certPath, KeyFile: keyPath},
	})
	if err != nil {
		t.Fatalf("NewCertStore: %v", err)
	}

	cert, err := cs.GetCertificate(&tls.ClientHelloInfo{ServerName: "example.com"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if cert == nil {
		t.Fatal("expected non-nil certificate")
	}
}

func TestGetCertificateDefaultFallback(t *testing.T) {
	certPath, keyPath := generateSelfSignedCert(t, []string{"fallback.example.com"})
	certPath2, keyPath2 := generateSelfSignedCert(t, []string{"specific.example.com"})

	cs, err := NewCertStore([]config.TLSCertConfig{
		{Host: "", CertFile: certPath, KeyFile: keyPath},
		{Host: "specific.example.com", CertFile: certPath2, KeyFile: keyPath2},
	})
	if err != nil {
		t.Fatalf("NewCertStore: %v", err)
	}

	// Empty ServerName should fall back to default.
	cert, err := cs.GetCertificate(&tls.ClientHelloInfo{ServerName: ""})
	if err != nil {
		t.Fatalf("GetCertificate empty SNI: %v", err)
	}
	if cert == nil {
		t.Fatal("expected non-nil certificate for empty SNI")
	}

	// Unknown SNI should also fall back to default.
	cert, err = cs.GetCertificate(&tls.ClientHelloInfo{ServerName: "unknown.example.com"})
	if err != nil {
		t.Fatalf("GetCertificate unknown SNI: %v", err)
	}
	if cert == nil {
		t.Fatal("expected non-nil certificate for unknown SNI")
	}

	// Known SNI should return the specific cert.
	cert, err = cs.GetCertificate(&tls.ClientHelloInfo{ServerName: "specific.example.com"})
	if err != nil {
		t.Fatalf("GetCertificate specific SNI: %v", err)
	}
	if cert == nil {
		t.Fatal("expected non-nil certificate for specific SNI")
	}
}

func TestGetCertificateNoMatchError(t *testing.T) {
	certPath, keyPath := generateSelfSignedCert(t, []string{"example.com"})

	cs, err := NewCertStore([]config.TLSCertConfig{
		{Host: "example.com", CertFile: certPath, KeyFile: keyPath},
	})
	if err != nil {
		t.Fatalf("NewCertStore: %v", err)
	}

	_, err = cs.GetCertificate(&tls.ClientHelloInfo{ServerName: "other.example.com"})
	if err == nil {
		t.Fatal("expected error for unmatched SNI with no default cert")
	}
}

func TestReplaceAtomicity(t *testing.T) {
	certPath1, keyPath1 := generateSelfSignedCert(t, []string{"old.example.com"})
	certPath2, keyPath2 := generateSelfSignedCert(t, []string{"new.example.com"})

	cs, err := NewCertStore([]config.TLSCertConfig{
		{Host: "old.example.com", CertFile: certPath1, KeyFile: keyPath1},
	})
	if err != nil {
		t.Fatalf("NewCertStore: %v", err)
	}

	// Start a TLS listener that uses the CertStore for certificate
	// selection, proving live handshakes are unaffected by Replace.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	tlsLn := tls.NewListener(ln, &tls.Config{
		GetCertificate: cs.GetCertificate,
		MinVersion:     tls.VersionTLS12,
	})
	go func() {
		for {
			conn, err := tlsLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 64)
				n, err := c.Read(buf)
				if err != nil {
					return
				}
				c.Write(buf[:n])
			}(conn)
		}
	}()

	// Dial with the old certificate.
	tlsConn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 2 * time.Second},
		"tcp", ln.Addr().String(),
		&tls.Config{
			ServerName:         "old.example.com",
			InsecureSkipVerify: true,
		},
	)
	if err != nil {
		t.Fatalf("TLS dial: %v", err)
	}
	defer tlsConn.Close()

	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("expected peer certificates")
	}

	// Hold connection open and atomically replace certs.
	newCerts, err := LoadCertificates([]config.TLSCertConfig{
		{Host: "new.example.com", CertFile: certPath2, KeyFile: keyPath2},
	})
	if err != nil {
		t.Fatalf("LoadCertificates: %v", err)
	}
	cs.Replace(newCerts)

	// Existing connection must still work — in-flight session is unaffected.
	tlsConn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprintf(tlsConn, "ping\n")
	buf := make([]byte, 64)
	n, err := tlsConn.Read(buf)
	if err != nil {
		t.Fatalf("existing connection broken after Replace: %v", err)
	}
	if string(buf[:n]) != "ping\n" {
		t.Errorf("expected echo, got %q", buf[:n])
	}

	// New connections should see the new certificate.
	tlsConn2, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 2 * time.Second},
		"tcp", ln.Addr().String(),
		&tls.Config{
			ServerName:         "new.example.com",
			InsecureSkipVerify: true,
		},
	)
	if err != nil {
		t.Fatalf("TLS dial with new cert: %v", err)
	}
	defer tlsConn2.Close()

	state2 := tlsConn2.ConnectionState()
	if len(state2.PeerCertificates) == 0 {
		t.Fatal("expected peer certificates for new cert")
	}

	// Old SNI should no longer work at the API level.
	_, err = cs.GetCertificate(&tls.ClientHelloInfo{ServerName: "old.example.com"})
	if err == nil {
		t.Fatal("expected error for old SNI after Replace")
	}
}

func TestReplaceConcurrentSafety(t *testing.T) {
	certPath1, keyPath1 := generateSelfSignedCert(t, []string{"a.example.com"})
	certPath2, keyPath2 := generateSelfSignedCert(t, []string{"b.example.com"})

	cs, err := NewCertStore([]config.TLSCertConfig{
		{Host: "a.example.com", CertFile: certPath1, KeyFile: keyPath1},
	})
	if err != nil {
		t.Fatalf("NewCertStore: %v", err)
	}

	newCerts, err := LoadCertificates([]config.TLSCertConfig{
		{Host: "b.example.com", CertFile: certPath2, KeyFile: keyPath2},
	})
	if err != nil {
		t.Fatalf("LoadCertificates: %v", err)
	}

	// Concurrent reads and writes must not panic or produce errors.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			cs.Replace(newCerts)
		}
	}()

	for i := 0; i < 1000; i++ {
		cs.GetCertificate(&tls.ClientHelloInfo{ServerName: "a.example.com"})
	}

	<-done
}
