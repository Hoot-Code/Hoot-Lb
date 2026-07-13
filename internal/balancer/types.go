// Package balancer defines the core abstractions shared by every load
// balancing algorithm and backend implementation in the project.
//
// This file intentionally contains *only* interfaces. Concrete
// implementations (round robin, weighted round robin, least connections,
// a real network-backed Backend, etc.) live in sibling files within this
// package. Keeping the contract stable means new algorithms can be
// implemented and swapped in without ever touching calling code.
package balancer

import "context"

// Backend represents a single upstream server that traffic can be sent
// to. Implementations are responsible for tracking their own address,
// weight, and health state; this package only depends on the interface.
type Backend interface {
	// Address returns the dial address of the backend, e.g.
	// "10.0.0.1:8080". This is the value used to open new connections
	// or make HTTP requests to the backend.
	Address() string

	// Weight returns the relative weight assigned to this backend.
	// Weighted load balancing algorithms use this value to send
	// proportionally more or less traffic to the backend; a backend
	// with weight 2 should, on average, receive twice the traffic of a
	// backend with weight 1 in the same pool.
	Weight() int

	// IsHealthy reports whether the backend is currently considered
	// able to accept traffic. The value is maintained by the health
	// checking subsystem (see the health package) and may change over
	// time as checks succeed or fail.
	IsHealthy() bool

	// IsDraining reports whether the backend is in draining state.
	// A draining backend is never selected for new connections or
	// requests, but existing in-flight traffic is left alone.
	IsDraining() bool

	// IsCircuitOpen reports whether the backend's circuit breaker is
	// currently open. A circuit-open backend is skipped during
	// selection, independent of health and draining states.
	IsCircuitOpen() bool
}

// LoadBalancer selects a Backend from a pool according to some
// algorithm (round robin, weighted round robin, least connections,
// etc.). Every algorithm must satisfy this interface so that the proxy
// engine can treat them interchangeably.
type LoadBalancer interface {
	// Pick selects the next Backend that should receive a request or
	// connection. The supplied context carries deadlines and
	// cancellation from the inbound request/connection; implementations
	// that may block (for example, waiting for a healthy backend to
	// become available) must respect ctx.Done().
	//
	// Pick returns an error if no suitable backend is available, for
	// example when the pool is empty or every backend is unhealthy.
	Pick(ctx context.Context) (Backend, error)

	// UpdateBackends atomically replaces the set of backends that Pick
	// selects from. It is called whenever the backend set changes, for
	// example after a configuration reload or a health check state
	// transition. Implementations must be safe to call concurrently
	// with Pick.
	UpdateBackends(backends []Backend)
}
