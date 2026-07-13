package l7

import (
	"crypto/tls"
	"net"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/metrics"
	"github.com/Hoot-Code/Hoot-Lb/internal/tlsutil"
)

// l7TLSConfig builds the tls.Config used to terminate TLS on an L7
// listener, sourcing certificates from certStore.
func l7TLSConfig(cfg config.ListenerConfig, certStore *tlsutil.CertStore) *tls.Config {
	return &tls.Config{
		GetCertificate: certStore.GetCertificate,
		MinVersion:     tlsVersion(cfg.TLS),
		CipherSuites:   tlsCipherSuites(cfg.TLS),
	}
}

// tlsHandshakeTimingListener wraps a net.Listener to measure TLS
// handshake duration. It records the time between Accept returning
// a connection and the TLS handshake completing (which happens
// during the first Read/Write on the tls.Conn).
type tlsHandshakeTimingListener struct {
	net.Listener
	metrics *metrics.HistogramVec
	name    string
}

func (l *tlsHandshakeTimingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if l.metrics == nil {
		return conn, nil
	}
	return &tlsTimingConn{Conn: conn, metrics: l.metrics, name: l.name, start: time.Now()}, nil
}

// tlsTimingConn wraps a net.Conn to record TLS handshake duration
// when the first read or write occurs (after the handshake completes).
type tlsTimingConn struct {
	net.Conn
	metrics  *metrics.HistogramVec
	name     string
	start    time.Time
	observed bool
}

func (c *tlsTimingConn) Read(b []byte) (int, error) {
	if !c.observed {
		c.observed = true
		c.metrics.With(c.name).Observe(time.Since(c.start).Seconds())
	}
	return c.Conn.Read(b)
}

func (c *tlsTimingConn) Write(b []byte) (int, error) {
	if !c.observed {
		c.observed = true
		c.metrics.With(c.name).Observe(time.Since(c.start).Seconds())
	}
	return c.Conn.Write(b)
}
