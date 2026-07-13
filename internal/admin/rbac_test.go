package admin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// generateTestCA creates a self-signed CA certificate and key for testing.
func generateTestCA(t *testing.T, dir string) (caCert *x509.Certificate, caKey *ecdsa.PrivateKey, caFile string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating CA key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating CA cert: %v", err)
	}

	certFile := filepath.Join(dir, "ca.pem")
	keyFile := filepath.Join(dir, "ca-key.pem")

	certOut, _ := os.Create(certFile)
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	certOut.Close()

	keyBytes, _ := x509.MarshalECPrivateKey(key)
	keyOut, _ := os.Create(keyFile)
	pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	keyOut.Close()

	cert, _ := x509.ParseCertificate(certDER)
	return cert, key, certFile
}

// generateTestClientCert creates a client certificate signed by the CA.
func generateTestClientCert(t *testing.T, dir string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, cn string) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating client key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("creating client cert: %v", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
	}
}

func TestRBACReadOnlyRejectedOnMutatingEndpoint(t *testing.T) {
	// Set up tokens.
	os.Setenv("HOOT_LB_TEST_VIEWER", "viewer-token-1234")
	defer os.Unsetenv("HOOT_LB_TEST_VIEWER")

	roles := []role{
		{
			token:       "viewer-token-1234",
			permissions: map[string]bool{PermRead: true},
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/admin/pools", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/admin/pools/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	handler := rbacMiddleware(roles, mux)

	// Read-only token should succeed on GET.
	req := httptest.NewRequest(http.MethodGet, "/admin/pools", nil)
	req.Header.Set("Authorization", "Bearer viewer-token-1234")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for read with viewer token, got %d", w.Code)
	}

	// Read-only token should be rejected on drain (403, not 401).
	req = httptest.NewRequest(http.MethodPost, "/admin/pools/backend1/drain", nil)
	req.Header.Set("Authorization", "Bearer viewer-token-1234")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for drain with viewer token, got %d", w.Code)
	}
}

func TestRBACFullAccessSucceedsOnEverything(t *testing.T) {
	os.Setenv("HOOT_LB_TEST_ADMIN", "admin-token-5678")
	defer os.Unsetenv("HOOT_LB_TEST_ADMIN")

	roles := []role{
		{
			token: "admin-token-5678",
			permissions: map[string]bool{
				PermRead:     true,
				PermDrain:    true,
				PermRestart:  true,
				PermBackends: true,
				PermConfig:   true,
			},
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/admin/pools", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/admin/pools/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/admin/restart", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	handler := rbacMiddleware(roles, mux)

	// Admin token should succeed on everything.
	tests := []struct {
		method string
		path   string
		code   int
	}{
		{http.MethodGet, "/admin/pools", http.StatusOK},
		{http.MethodPost, "/admin/pools/b/drain", http.StatusNoContent},
		{http.MethodPost, "/admin/pools/b/undrain", http.StatusNoContent},
		{http.MethodPost, "/admin/restart", http.StatusAccepted},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		req.Header.Set("Authorization", "Bearer admin-token-5678")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tt.code {
			t.Errorf("%s %s: expected %d, got %d", tt.method, tt.path, tt.code, w.Code)
		}
	}
}

// generateTestServerCert creates a server certificate signed by the CA
// with IP SANs for localhost testing.
func generateTestServerCert(t *testing.T, dir string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating server key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("creating server cert: %v", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
	}
}

func TestMTLSRejectsClientWithoutCert(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey, caFile := generateTestCA(t, dir)

	// Generate a server cert signed by the CA.
	serverCert := generateTestServerCert(t, dir, caCert, caKey)

	// Create server with mTLS.
	tlsCfg := &tls.Config{
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    x509.NewCertPool(),
		Certificates: []tls.Certificate{serverCert},
	}
	tlsCfg.ClientCAs.AddCert(caCert)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := &http.Server{
		Handler:   handler,
		TLSConfig: tlsCfg,
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}
	defer ln.Close()
	go server.Serve(ln)

	// Client without cert should be rejected at TLS layer.
	caCertPEM, _ := os.ReadFile(caFile)
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caCertPEM)

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: caPool,
			},
		},
	}

	_, err = client.Get("https://" + ln.Addr().String() + "/")
	if err == nil {
		t.Error("expected TLS rejection for client without cert, got nil error")
	}
}

func TestMTLSAcceptsClientWithValidCert(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey, _ := generateTestCA(t, dir)

	// Generate server and client certs.
	serverCert := generateTestServerCert(t, dir, caCert, caKey)
	clientCert := generateTestClientCert(t, dir, caCert, caKey, "test-client")

	// Create server with mTLS.
	tlsCfg := &tls.Config{
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    x509.NewCertPool(),
		Certificates: []tls.Certificate{serverCert},
	}
	tlsCfg.ClientCAs.AddCert(caCert)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := &http.Server{
		Handler:   handler,
		TLSConfig: tlsCfg,
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}
	defer ln.Close()
	go server.Serve(ln)

	// Client with valid cert should succeed.
	caCertDER, _ := x509.ParseCertificate(caCert.Raw)
	caPool := x509.NewCertPool()
	caPool.AddCert(caCertDER)

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      caPool,
				Certificates: []tls.Certificate{clientCert},
			},
		},
	}

	resp, err := client.Get("https://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("expected success with valid client cert, got: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}
