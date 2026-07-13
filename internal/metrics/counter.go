package metrics

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Counter is a monotonically increasing metric. It is safe for
// concurrent use. All operations are atomic — no mutex required.
type Counter struct {
	val atomic.Uint64
}

// Add increments the counter by v.
func (c *Counter) Add(v uint64) {
	c.val.Add(v)
}

// Value returns the current counter value.
func (c *Counter) Value() uint64 {
	return c.val.Load()
}

// CounterVec is a Counter keyed by an ordered label-value tuple.
// Registering a new label combination is done under a mutex (rare).
// Incrementing an existing combination is a single atomic op.
type CounterVec struct {
	name   string
	labels []string
	mu     sync.RWMutex
	items  map[string]*Counter
}

// NewCounterVec creates a CounterVec with the given metric name and
// label names.
func NewCounterVec(name string, labels []string) *CounterVec {
	return &CounterVec{
		name:   name,
		labels: labels,
		items:  make(map[string]*Counter),
	}
}

// With returns the Counter for the given label values.
func (cv *CounterVec) With(labelValues ...string) *Counter {
	key := cv.key(labelValues)
	cv.mu.RLock()
	if c, ok := cv.items[key]; ok {
		cv.mu.RUnlock()
		return c
	}
	cv.mu.RUnlock()
	cv.mu.Lock()
	defer cv.mu.Unlock()
	if c, ok := cv.items[key]; ok {
		return c
	}
	c := &Counter{}
	cv.items[key] = c
	return c
}

// Remove deletes the label combination from the Vec.
func (cv *CounterVec) Remove(labelValues ...string) bool {
	key := cv.key(labelValues)
	cv.mu.Lock()
	defer cv.mu.Unlock()
	if _, ok := cv.items[key]; ok {
		delete(cv.items, key)
		return true
	}
	return false
}

// RemoveByPoolBackend removes all label combinations where the
// given pool and backend values appear in any label position.
func (cv *CounterVec) RemoveByPoolBackend(pool, backend string) {
	cv.mu.Lock()
	defer cv.mu.Unlock()
	for k := range cv.items {
		parts := strings.Split(k, "\x00")
		hasPool := false
		hasBackend := false
		for _, p := range parts {
			if p == pool {
				hasPool = true
			}
			if p == backend {
				hasBackend = true
			}
		}
		if hasPool && hasBackend {
			delete(cv.items, k)
		}
	}
}

// Each calls fn for every registered label combination, sorted.
func (cv *CounterVec) Each(fn func(labels []string, value uint64)) {
	cv.mu.RLock()
	defer cv.mu.RUnlock()
	keys := make([]string, 0, len(cv.items))
	for k := range cv.items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fn(strings.Split(k, "\x00"), cv.items[k].Value())
	}
}

func (cv *CounterVec) key(labelValues []string) string {
	return strings.Join(labelValues, "\x00")
}
