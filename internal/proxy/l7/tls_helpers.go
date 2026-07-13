package l7

import (
	"crypto/tls"
	"net/http"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
)

// bodyLimitHandler wraps an http.Handler to reject request bodies that
// exceed maxBytes with a 413 status code. The limit is enforced by
// wrapping the request body with io.LimitReader.
type bodyLimitHandler struct {
	inner    http.Handler
	maxBytes int64
}

// ServeHTTP checks the Content-Length header against the body size
// limit before forwarding to the inner handler. Requests exceeding
// the limit receive a 413 response without being forwarded.
func (h *bodyLimitHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > h.maxBytes {
		http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBytes)
	h.inner.ServeHTTP(w, r)
}

// tlsVersion returns the tls.Config MinVersion value for the given
// TLSConfig. It defaults to tls.VersionTLS12 when no version is
// specified. TLS 1.0 and 1.1 are excluded because they use deprecated
// cryptographic primitives and have known weaknesses (RFC 8996).
func tlsVersion(cfg *config.TLSConfig) uint16 {
	if cfg != nil {
		switch cfg.MinVersion {
		case "tls13":
			return tls.VersionTLS13
		}
	}
	return tls.VersionTLS12
}

// tlsCipherSuites returns a recommended cipher suite list for the
// given TLSConfig. When MinVersion is "tls13", this returns nil
// because TLS 1.3 cipher suites are not configurable in Go and
// the Go runtime enforces its own secure set. For TLS 1.2, we
// restrict to AEAD cipher suites that are considered secure.
func tlsCipherSuites(cfg *config.TLSConfig) []uint16 {
	if cfg != nil && cfg.MinVersion == "tls13" {
		return nil
	}
	return []uint16{
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
		tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
	}
}
