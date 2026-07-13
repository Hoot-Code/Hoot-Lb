package metrics

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// DefaultHistogramBuckets are the fixed bucket boundaries for
// Histogram, in seconds.
var DefaultHistogramBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0,
}

// FineHistogramBuckets are bucket boundaries for sub-millisecond
// latency histograms where finer resolution below 5ms is needed,
// in seconds. Used for backend latency and TLS handshake metrics.
var FineHistogramBuckets = []float64{
	0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0,
}

// Histogram tracks the distribution of observations. Each bucket
// boundary has its own atomic counter (non-cumulative internally).
// The sum is stored as int64 nanoseconds and converted to seconds
// only at exposition time. All operations are atomic.
type Histogram struct {
	buckets []float64
	counts  []atomic.Uint64
	sum     atomic.Int64
	count   atomic.Uint64
}

// NewHistogram creates a Histogram with the given bucket boundaries.
// Boundaries are sorted internally.
func NewHistogram(buckets []float64) *Histogram {
	sorted := make([]float64, len(buckets))
	copy(sorted, buckets)
	sort.Float64s(sorted)
	return &Histogram{
		buckets: sorted,
		counts:  make([]atomic.Uint64, len(sorted)+1),
	}
}

// Observe records a duration in seconds.
func (h *Histogram) Observe(seconds float64) {
	h.sum.Add(int64(seconds * 1e9))
	h.count.Add(1)
	idx := sort.SearchFloat64s(h.buckets, seconds)
	h.counts[idx].Add(1)
}

// Buckets returns the bucket boundaries (excluding +Inf).
func (h *Histogram) Buckets() []float64 {
	return h.buckets
}

// Sum returns the total sum of all observations in seconds.
func (h *Histogram) Sum() float64 {
	return float64(h.sum.Load()) / 1e9
}

// Count returns the total number of observations.
func (h *Histogram) Count() uint64 {
	return h.count.Load()
}

// BucketCounts returns the non-cumulative counts for each bucket,
// including the +Inf bucket at the end.
func (h *Histogram) BucketCounts() []uint64 {
	counts := make([]uint64, len(h.counts))
	for i := range h.counts {
		counts[i] = h.counts[i].Load()
	}
	return counts
}

// HistogramVec is a Histogram keyed by an ordered label-value tuple.
type HistogramVec struct {
	name    string
	labels  []string
	buckets []float64
	mu      sync.RWMutex
	items   map[string]*Histogram
}

// NewHistogramVec creates a HistogramVec with the given metric name,
// label names, and bucket boundaries.
func NewHistogramVec(name string, labels []string, buckets []float64) *HistogramVec {
	return &HistogramVec{
		name:    name,
		labels:  labels,
		buckets: buckets,
		items:   make(map[string]*Histogram),
	}
}

// With returns the Histogram for the given label values.
func (hv *HistogramVec) With(labelValues ...string) *Histogram {
	key := hv.key(labelValues)
	hv.mu.RLock()
	if h, ok := hv.items[key]; ok {
		hv.mu.RUnlock()
		return h
	}
	hv.mu.RUnlock()
	hv.mu.Lock()
	defer hv.mu.Unlock()
	if h, ok := hv.items[key]; ok {
		return h
	}
	h := NewHistogram(hv.buckets)
	hv.items[key] = h
	return h
}

// Remove deletes the label combination from the Vec.
func (hv *HistogramVec) Remove(labelValues ...string) bool {
	key := hv.key(labelValues)
	hv.mu.Lock()
	defer hv.mu.Unlock()
	if _, ok := hv.items[key]; ok {
		delete(hv.items, key)
		return true
	}
	return false
}

// RemoveByPoolBackend removes all label combinations where the
// given pool and backend values appear in any label position.
func (hv *HistogramVec) RemoveByPoolBackend(pool, backend string) {
	hv.mu.Lock()
	defer hv.mu.Unlock()
	for k := range hv.items {
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
			delete(hv.items, k)
		}
	}
}

// Each calls fn for every registered label combination, sorted.
func (hv *HistogramVec) Each(fn func(labels []string, h *Histogram)) {
	hv.mu.RLock()
	defer hv.mu.RUnlock()
	keys := make([]string, 0, len(hv.items))
	for k := range hv.items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fn(strings.Split(k, "\x00"), hv.items[k])
	}
}

func (hv *HistogramVec) key(labelValues []string) string {
	return strings.Join(labelValues, "\x00")
}
