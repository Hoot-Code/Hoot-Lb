package balancer

import (
	"context"
	"errors"
	"hash/fnv"
	"sync"
)

// IPHash is a load balancer that deterministically maps a client IP
// (or arbitrary key) to a specific backend using a consistent hash.
// This gives "sticky" behavior at the L4 layer — the same client IP
// always hits the same backend as long as the backend set doesn't
// change. When the backend set changes, only a fraction of mappings
// shift (roughly 1/n of keys remap per backend change). Weight is
// used to assign proportionally more hash ranges to heavier backends.
type IPHash struct {
	mu       sync.RWMutex
	backends []Backend
}

// NewIPHash creates an IPHash balancer seeded with the given backends.
// The slice is copied.
func NewIPHash(backends []Backend) *IPHash {
	cp := make([]Backend, len(backends))
	copy(cp, backends)
	return &IPHash{backends: cp}
}

// Pick deterministically selects a backend for the given key. The key
// is extracted from the context value using ClientKey as the context
// key; if absent or empty, "default" is used as the key. Healthy
// backends are selected in proportion to their weight. Returns an
// error if no healthy backends are available.
func (ih *IPHash) Pick(ctx context.Context) (Backend, error) {
	ih.mu.RLock()
	defer ih.mu.RUnlock()

	var candidates []Backend
	var totalWeight int
	for _, b := range ih.backends {
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

	key, _ := ctx.Value(ClientKey{}).(string)
	if key == "" {
		key = "default"
	}

	h := fnv.New32a()
	h.Write([]byte(key))
	hash := h.Sum32()

	target := int(hash % uint32(totalWeight))
	for _, b := range candidates {
		target -= b.Weight()
		if target < 0 {
			return b, nil
		}
	}

	return candidates[len(candidates)-1], nil
}

// UpdateBackends atomically replaces the set of backends. It is safe
// to call concurrently with Pick.
func (ih *IPHash) UpdateBackends(backends []Backend) {
	ih.mu.Lock()
	defer ih.mu.Unlock()

	cp := make([]Backend, len(backends))
	copy(cp, backends)
	ih.backends = cp
}

// ClientKey is the context key type for passing a client IP or
// identifier to IPHash.Pick. Usage:
//
//	ctx = context.WithValue(ctx, balancer.ClientKey{}, "1.2.3.4")
//	backend, err := lb.Pick(ctx)
type ClientKey struct{}
