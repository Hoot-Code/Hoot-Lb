package balancer

import (
	"context"
	"errors"
	"math/rand"
	"sync"
)

// Random is a load balancer that selects backends with probability
// proportional to their weight. Backends with higher weight receive
// proportionally more traffic. Unhealthy backends are skipped.
type Random struct {
	mu       sync.RWMutex
	backends []Backend
	rng      *rand.Rand
}

// NewRandom creates a Random balancer seeded with the given backends.
// The slice is copied. Uses a new source of randomness seeded with 0
// (which is fine for load balancing — deterministic seeding only
// matters for reproducibility, not fairness).
func NewRandom(backends []Backend) *Random {
	cp := make([]Backend, len(backends))
	copy(cp, backends)
	return &Random{
		backends: cp,
		rng:      rand.New(rand.NewSource(0)),
	}
}

// Pick selects a random backend with probability proportional to
// weight, skipping unhealthy backends. It returns an error if no
// healthy backends are available.
func (rb *Random) Pick(_ context.Context) (Backend, error) {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	var candidates []Backend
	var totalWeight int
	for _, b := range rb.backends {
		if b.IsHealthy() && !b.IsDraining() && !b.IsCircuitOpen() {
			candidates = append(candidates, b)
			totalWeight += b.Weight()
		}
	}

	if len(candidates) == 0 {
		return nil, errors.New("no healthy backends available")
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}

	target := rb.rng.Intn(totalWeight)
	for _, b := range candidates {
		target -= b.Weight()
		if target < 0 {
			return b, nil
		}
	}

	// Should not reach here, but handle gracefully.
	return candidates[len(candidates)-1], nil
}

// UpdateBackends atomically replaces the set of backends. It is safe
// to call concurrently with Pick.
func (rb *Random) UpdateBackends(backends []Backend) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	cp := make([]Backend, len(backends))
	copy(cp, backends)
	rb.backends = cp
}
