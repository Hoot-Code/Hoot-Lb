// Package tlsutil provides a thread-safe certificate store for TLS
// termination and a loader that reads certificates from the config's
// on-disk paths. The store is designed for safe hot rotation: the
// entire certificate map is swapped atomically via sync/atomic, so
// in-flight TLS connections are never affected by a rotation.
package tlsutil

import (
	"crypto/tls"
	"fmt"
	"sync/atomic"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
)

// CertStore holds a map of SNI hostname → *tls.Certificate, swapable
// atomically. It satisfies tls.Config.GetCertificate so the TLS
// handshake always sees a consistent snapshot.
type CertStore struct {
	certs atomic.Pointer[map[string]*tls.Certificate]
}

// NewCertStore creates a CertStore and loads the certificates from the
// given config entries. The empty-string key holds the default/fallback
// certificate (if any). Returns an error if any certificate file cannot
// be loaded.
func NewCertStore(entries []config.TLSCertConfig) (*CertStore, error) {
	m, err := LoadCertificates(entries)
	if err != nil {
		return nil, err
	}
	cs := &CertStore{}
	cs.certs.Store(m)
	return cs, nil
}

// LoadCertificates reads certificate and key files from disk for each
// config entry and returns a map keyed by SNI hostname. The empty
// string key holds the default/fallback certificate.
func LoadCertificates(entries []config.TLSCertConfig) (*map[string]*tls.Certificate, error) {
	m := make(map[string]*tls.Certificate, len(entries))
	for _, e := range entries {
		cert, err := tls.LoadX509KeyPair(e.CertFile, e.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("loading certificate for host %q: %w", e.Host, err)
		}
		m[e.Host] = &cert
	}
	return &m, nil
}

// GetCertificate selects the certificate for the given ClientHello.
// It matches hello.ServerName against the stored hostnames; if no
// exact match is found (or ServerName is empty), the default ("")
// entry is returned. Returns an error only if no certificate matches
// at all. This method signature matches tls.Config.GetCertificate.
func (cs *CertStore) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	m := *cs.certs.Load()

	if hello.ServerName != "" {
		if cert, ok := m[hello.ServerName]; ok {
			return cert, nil
		}
	}

	if cert, ok := m[""]; ok {
		return cert, nil
	}

	return nil, fmt.Errorf("no certificate for SNI %q and no default certificate configured", hello.ServerName)
}

// Replace atomically swaps the entire certificate map. The old map is
// left for any in-flight TLS handshakes to finish with; new handshakes
// will see the new certificates. This method is safe to call
// concurrently with GetCertificate and with itself.
func (cs *CertStore) Replace(certs *map[string]*tls.Certificate) {
	cs.certs.Store(certs)
}

// Certs returns a shallow copy of the current certificate map for
// inspection (e.g. tests, diagnostics). The returned map is safe to
// read without synchronization. The certificate pointers are shared
// with the live store and must not be mutated.
func (cs *CertStore) Certs() map[string]*tls.Certificate {
	src := *cs.certs.Load()
	dst := make(map[string]*tls.Certificate, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
