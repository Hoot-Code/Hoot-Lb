package balancer

import (
	"context"
	"errors"
	"sync"
)

// LeastConnections is a load balancer that selects the backend with
// the fewest active (in-flight) connections. It implements the
// ConnReleaser interface: callers must call Release when a connection
// or session ends so the connection count is decremented.
//
// Weight is used as a tiebreaker among backends with equal connection
// counts — the backend with the higher weight is preferred, since a
// higher weight typically indicates greater capacity.
type LeastConnections struct {
	mu       sync.RWMutex
	backends []Backend
	conns    map[string]int // backend address → active connection count
}

// NewLeastConnections creates a LeastConnections balancer seeded with
// the given backends. The slice is copied.
func NewLeastConnections(backends []Backend) *LeastConnections {
	cp := make([]Backend, len(backends))
	copy(cp, backends)
	lc := &LeastConnections{
		backends: cp,
		conns:    make(map[string]int),
	}
	return lc
}

// Pick selects the healthy backend with the fewest active connections.
// In case of a tie, the backend with the higher weight is preferred.
// Returns an error if no healthy backends are available.
func (lc *LeastConnections) Pick(_ context.Context) (Backend, error) {
	lc.mu.RLock()
	defer lc.mu.RUnlock()

	var best Backend
	var bestConns int
	var bestWeight int
	first := true

	for _, b := range lc.backends {
		if !b.IsHealthy() || b.IsDraining() || b.IsCircuitOpen() {
			continue
		}
		c := lc.conns[b.Address()]
		if first || c < bestConns || (c == bestConns && b.Weight() > bestWeight) {
			best = b
			bestConns = c
			bestWeight = b.Weight()
			first = false
		}
	}

	if first {
		return nil, errors.New("no healthy backends available")
	}
	return best, nil
}

// Acquire increments the connection count for the given backend. It
// must be called when a connection is established to the backend
// (typically by the proxy layer after a successful Pick).
func (lc *LeastConnections) Acquire(b Backend) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.conns[b.Address()]++
}

// Release decrements the connection count for the given backend. It
// must be called when a connection or session ends. The count is
// clamped at zero to guard against unbalanced calls.
func (lc *LeastConnections) Release(b Backend) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	addr := b.Address()
	if lc.conns[addr] > 0 {
		lc.conns[addr]--
	}
}

// UpdateBackends atomically replaces the set of backends. Existing
// connection counts for removed backends are discarded. It is safe to
// call concurrently with Pick.
func (lc *LeastConnections) UpdateBackends(backends []Backend) {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	cp := make([]Backend, len(backends))
	copy(cp, backends)
	lc.backends = cp
}
