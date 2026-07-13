package balancer

// ConnReleaser is an optional interface that a LoadBalancer can
// implement to track the number of in-flight connections per backend.
// Algorithms that don't care about connection counts (round robin,
// random, etc.) do not implement this interface.
//
// The proxy layer (internal/proxy/l4) type-asserts the configured
// LoadBalancer against ConnReleaser after Pick. If the assertion
// succeeds, Acquire is called after a successful connection to the
// backend, and Release is called when the connection or session ends.
// For algorithms that don't implement ConnReleaser, these calls are
// simply never made — no-ops by omission.
//
// The typical usage contract:
//
//	Pick a backend → if lb is ConnReleaser, call lb.Acquire(backend)
//	... use the connection ...
//	connection ends → if lb is ConnReleaser, call lb.Release(backend)
type ConnReleaser interface {
	Acquire(b Backend)
	Release(b Backend)
}
