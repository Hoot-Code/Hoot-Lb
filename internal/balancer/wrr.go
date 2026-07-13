package balancer

import (
	"context"
	"errors"
	"math"
	"sync"
)

// WeightedRoundRobin implements a smooth weighted round-robin
// algorithm. Instead of the naive approach (repeat each backend N
// times for weight N, producing bursts), this uses the interleaved
// algorithm: on each pick, every backend's effective weight is
// increased by its configured weight, the backend with the highest
// effective weight is selected, and that backend's effective weight
// is decreased by the total weight of all backends. This produces
// a well-distributed sequence where backends with higher weight
// receive proportionally more traffic without bursty clumping.
type WeightedRoundRobin struct {
	mu       sync.Mutex
	backends []Backend
	// currentWeights holds each backend's current effective weight
	// between picks. It is rebuilt when UpdateBackends is called.
	currentWeights []int
	totalWeight    int
}

// NewWeightedRoundRobin creates a WeightedRoundRobin balancer seeded
// with the given backends. The slice is copied.
func NewWeightedRoundRobin(backends []Backend) *WeightedRoundRobin {
	cp := make([]Backend, len(backends))
	copy(cp, backends)
	wrr := &WeightedRoundRobin{backends: cp}
	wrr.rebuildWeights()
	return wrr
}

// rebuildWeights recomputes the total weight and resets current
// weights. Must be called with mu held.
func (wrr *WeightedRoundRobin) rebuildWeights() {
	wrr.totalWeight = 0
	wrr.currentWeights = make([]int, len(wrr.backends))
	for i, b := range wrr.backends {
		wrr.totalWeight += b.Weight()
		wrr.currentWeights[i] = 0
	}
}

// Pick selects the next backend using the smooth weighted round-robin
// algorithm, skipping unhealthy backends. It returns an error if no
// healthy backends are available.
func (wrr *WeightedRoundRobin) Pick(_ context.Context) (Backend, error) {
	wrr.mu.Lock()
	defer wrr.mu.Unlock()

	n := len(wrr.backends)
	if n == 0 {
		return nil, errors.New("no backends available")
	}

	// Collect healthy indices and compute their total weight.
	type candidate struct {
		idx    int
		weight int
	}
	var healthy []candidate
	healthyTotal := 0
	for i, b := range wrr.backends {
		if b.IsHealthy() && !b.IsDraining() && !b.IsCircuitOpen() {
			healthy = append(healthy, candidate{idx: i, weight: b.Weight()})
			healthyTotal += b.Weight()
		}
	}

	if len(healthy) == 0 {
		return nil, errors.New("no healthy backends available")
	}
	if len(healthy) == 1 {
		return wrr.backends[healthy[0].idx], nil
	}

	// Smooth weighted round-robin: add each backend's configured
	// weight to its current effective weight, pick the max, then
	// subtract the total healthy weight from the winner.
	bestIdx := 0
	bestWeight := math.MinInt
	for _, c := range healthy {
		wrr.currentWeights[c.idx] += c.weight
		if wrr.currentWeights[c.idx] > bestWeight {
			bestWeight = wrr.currentWeights[c.idx]
			bestIdx = c.idx
		}
	}

	wrr.currentWeights[bestIdx] -= healthyTotal

	return wrr.backends[bestIdx], nil
}

// UpdateBackends atomically replaces the set of backends and resets
// the smooth weighted round-robin state. It is safe to call
// concurrently with Pick.
func (wrr *WeightedRoundRobin) UpdateBackends(backends []Backend) {
	wrr.mu.Lock()
	defer wrr.mu.Unlock()

	cp := make([]Backend, len(backends))
	copy(cp, backends)
	wrr.backends = cp
	wrr.rebuildWeights()
}
