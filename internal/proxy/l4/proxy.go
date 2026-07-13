// Package l4 implements the L4 (TCP/UDP) proxy engine for the lb load
// balancer. Each configured listener is represented by a TCPServer or
// UDPServer that accepts inbound connections/datagrams, selects a
// backend via a balancer.LoadBalancer, and relays traffic bidirectionally.
//
// Both server types support graceful shutdown: calling Close stops
// accepting new work immediately and waits for in-flight connections to
// drain, bounded by a context deadline.
package l4

import (
	"net"
	"time"
)

const (
	// defaultConnectTimeout is the maximum time the TCP proxy will
	// wait to establish a connection to a backend before reporting an
	// error.
	defaultConnectTimeout = 5 * time.Second

	// acceptRetryInitialDelay is the initial backoff applied after a
	// temporary Accept() error (e.g. EMFILE/ENFILE under a burst of
	// connections, which is common right after a zero-downtime
	// restart handoff when parent and child briefly hold duplicated
	// FDs). Mirrors the backoff net/http.Server.Serve uses for the
	// same class of error.
	acceptRetryInitialDelay = 5 * time.Millisecond

	// acceptRetryMaxDelay caps the exponential backoff applied between
	// retried Accept() calls.
	acceptRetryMaxDelay = 1 * time.Second

	// defaultHandshakeTimeout is the maximum time the TLS passthrough
	// proxy waits for a ClientHello before closing the connection.
	// Prevents slow loris-style resource exhaustion. Matches the
	// ReadHeaderTimeout precedent in internal/proxy/l7.
	defaultHandshakeTimeout = 10 * time.Second

	// defaultSessionTimeout is the idle duration after which a UDP
	// session is evicted from the session table. This prevents
	// unbounded memory growth from stale sessions.
	defaultSessionTimeout = 30 * time.Second

	// udpSessionCleanupInterval controls how often the UDP eviction
	// goroutine scans the session table for idle entries.
	udpSessionCleanupInterval = 5 * time.Second

	// udpMaxDatagramSize is the maximum UDP datagram size the proxy
	// will handle. 65535 is the theoretical max; 65536 is the standard
	// read buffer size used by most UDP implementations.
	udpMaxDatagramSize = 65536
)

// acceptRetryDelay decides how an Accept() loop should respond to err.
// Temporary errors (e.g. EMFILE/ENFILE from a momentary burst of
// connections) must not be treated the same as a fatal error such as
// the listener being closed: a fatal error should stop the accept
// loop, but a temporary one should back off briefly and retry,
// exactly as net/http.Server.Serve does. prevDelay is the delay used
// for the previous retry (0 if this is the first consecutive
// temporary error); the returned delay grows exponentially up to
// acceptRetryMaxDelay. ok reports whether err was identified as
// temporary and worth retrying; when ok is false the caller should
// treat the error as fatal and stop accepting.
func acceptRetryDelay(err error, prevDelay time.Duration) (delay time.Duration, ok bool) {
	ne, isNetErr := err.(net.Error)
	if !isNetErr || !ne.Temporary() { //nolint:staticcheck // Temporary is deprecated but still the correct, stdlib-matching signal here.
		return 0, false
	}
	if prevDelay == 0 {
		delay = acceptRetryInitialDelay
	} else {
		delay = prevDelay * 2
	}
	if delay > acceptRetryMaxDelay {
		delay = acceptRetryMaxDelay
	}
	return delay, true
}
