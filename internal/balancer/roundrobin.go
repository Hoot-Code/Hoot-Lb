// Package balancer provides load balancing algorithms and backend
// management for the Hoot-Lb load balancer.
//
// RoundRobin distributes connections across backends in simple
// round-robin order, ignoring backend weight. It serves as the
// unweighted baseline algorithm and is selected when a pool's
// algorithm is set to "round_robin".
package balancer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// RoundRobin is a load balancer that selects backends in simple
// round-robin order using an atomic counter. Unhealthy backends are
// skipped — this is the unweighted baseline; use WeightedRoundRobin
// for weight-aware selection.
type RoundRobin struct {
	mu       sync.RWMutex
	backends []Backend
	next     atomic.Uint64
}

// NewRoundRobin creates a RoundRobin balancer seeded with the given
// backends. The slice is copied, so the caller may safely modify it
// after construction. Call UpdateBackends to swap the set at runtime.
func NewRoundRobin(backends []Backend) *RoundRobin {
	cp := make([]Backend, len(backends))
	copy(cp, backends)
	return &RoundRobin{backends: cp}
}

// Pick selects the next healthy backend in round-robin order. It
// returns an error if no healthy backends are available. ctx is
// accepted to satisfy the LoadBalancer interface; this implementation
// does not block.
func (r *RoundRobin) Pick(_ context.Context) (Backend, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	n := len(r.backends)
	if n == 0 {
		return nil, errors.New("no backends available")
	}

	// Skip unhealthy backends by scanning at most n positions.
	start := r.next.Add(1) - 1
	for i := uint64(0); i < uint64(n); i++ {
		idx := (start + i) % uint64(n)
		if r.backends[idx].IsHealthy() && !r.backends[idx].IsDraining() && !r.backends[idx].IsCircuitOpen() {
			return r.backends[idx], nil
		}
	}
	return nil, errors.New("no healthy backends available")
}

// UpdateBackends atomically replaces the set of backends. It is safe
// to call concurrently with Pick.
func (r *RoundRobin) UpdateBackends(backends []Backend) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cp := make([]Backend, len(backends))
	copy(cp, backends)
	r.backends = cp
}
