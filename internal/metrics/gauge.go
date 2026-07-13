package metrics

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Gauge is a metric that can go up and down. It is safe for
// concurrent use. All operations are atomic — no mutex required.
type Gauge struct {
	val atomic.Int64
}

// Set sets the gauge to v.
func (g *Gauge) Set(v int64) {
	g.val.Store(v)
}

// Inc increments the gauge by 1.
func (g *Gauge) Inc() {
	g.val.Add(1)
}

// Dec decrements the gauge by 1.
func (g *Gauge) Dec() {
	g.val.Add(-1)
}

// Value returns the current gauge value.
func (g *Gauge) Value() int64 {
	return g.val.Load()
}

// GaugeVec is a Gauge keyed by an ordered label-value tuple.
type GaugeVec struct {
	name   string
	labels []string
	mu     sync.RWMutex
	items  map[string]*Gauge
}

// NewGaugeVec creates a GaugeVec with the given metric name and
// label names.
func NewGaugeVec(name string, labels []string) *GaugeVec {
	return &GaugeVec{
		name:   name,
		labels: labels,
		items:  make(map[string]*Gauge),
	}
}

// With returns the Gauge for the given label values.
func (gv *GaugeVec) With(labelValues ...string) *Gauge {
	key := gv.key(labelValues)
	gv.mu.RLock()
	if g, ok := gv.items[key]; ok {
		gv.mu.RUnlock()
		return g
	}
	gv.mu.RUnlock()
	gv.mu.Lock()
	defer gv.mu.Unlock()
	if g, ok := gv.items[key]; ok {
		return g
	}
	g := &Gauge{}
	gv.items[key] = g
	return g
}

// Remove deletes the label combination from the Vec.
func (gv *GaugeVec) Remove(labelValues ...string) bool {
	key := gv.key(labelValues)
	gv.mu.Lock()
	defer gv.mu.Unlock()
	if _, ok := gv.items[key]; ok {
		delete(gv.items, key)
		return true
	}
	return false
}

// RemoveByPoolBackend removes all label combinations where the
// given pool and backend values appear in any label position.
func (gv *GaugeVec) RemoveByPoolBackend(pool, backend string) {
	gv.mu.Lock()
	defer gv.mu.Unlock()
	for k := range gv.items {
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
			delete(gv.items, k)
		}
	}
}

// Each calls fn for every registered label combination, sorted.
func (gv *GaugeVec) Each(fn func(labels []string, value int64)) {
	gv.mu.RLock()
	defer gv.mu.RUnlock()
	keys := make([]string, 0, len(gv.items))
	for k := range gv.items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fn(strings.Split(k, "\x00"), gv.items[k].Value())
	}
}

func (gv *GaugeVec) key(labelValues []string) string {
	return strings.Join(labelValues, "\x00")
}
