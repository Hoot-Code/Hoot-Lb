package balancer

import "sync/atomic"

// Server is a concrete, in-memory implementation of the Backend
// interface. It holds the address, weight, and health state for a
// single upstream server. Health, draining, and circuit-open states
// are maintained atomically so they can be updated by independent
// subsystems (health checker, admin API, circuit breaker) without
// coordinating with the load balancer's Pick path.
type Server struct {
	address     string
	weight      int
	healthy     atomic.Bool
	draining    atomic.Bool
	circuitOpen atomic.Bool
}

// NewServer creates a Server with the given address and weight,
// starting in a healthy state.
func NewServer(address string, weight int) *Server {
	s := &Server{address: address, weight: weight}
	s.healthy.Store(true)
	return s
}

// Address returns the dial address of the server (e.g.
// "10.0.0.1:8080").
func (s *Server) Address() string { return s.address }

// Weight returns the relative weight of this server.
func (s *Server) Weight() int { return s.weight }

// IsHealthy reports whether the server is currently considered able
// to accept traffic.
func (s *Server) IsHealthy() bool { return s.healthy.Load() }

// SetHealthy updates the health state of the server. Intended for use
// by the health checker; exposed here for testing.
func (s *Server) SetHealthy(healthy bool) { s.healthy.Store(healthy) }

// IsDraining reports whether the server is currently in draining
// state. A draining server is excluded from new traffic selection
// but existing connections are left untouched.
func (s *Server) IsDraining() bool { return s.draining.Load() }

// SetDraining updates the draining state of the server. Intended
// for use by the admin API; exposed here for testing.
func (s *Server) SetDraining(draining bool) { s.draining.Store(draining) }

// IsCircuitOpen reports whether the server's circuit breaker is
// currently open. A circuit-open server is excluded from new traffic
// selection, independent of health and draining states.
func (s *Server) IsCircuitOpen() bool { return s.circuitOpen.Load() }

// SetCircuitOpen updates the circuit breaker state of the server.
// Intended for use by the circuit breaker subsystem; exposed here
// for testing.
func (s *Server) SetCircuitOpen(open bool) { s.circuitOpen.Store(open) }

// CircuitOpenAtomic returns the underlying atomic.Bool for the
// circuit breaker state. This allows the circuit breaker subsystem
// to manage the atomic directly for concurrency-safe probe gating.
func (s *Server) CircuitOpenAtomic() *atomic.Bool { return &s.circuitOpen }
