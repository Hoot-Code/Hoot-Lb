// Package metrics provides Prometheus-compatible metrics types with
// a hand-rolled registry and text exposition format writer. All
// hot-path operations (increment, record) use atomic operations
// only — no mutexes on the fast path.
package metrics

import (
	"math"
	"strconv"
	"strings"
	"sync"
)

// MetricType identifies the type of a metric for exposition.
type MetricType string

const (
	TypeCounter   MetricType = "counter"
	TypeGauge     MetricType = "gauge"
	TypeHistogram MetricType = "histogram"
)

// MetricEntry is a registered metric in the registry.
type MetricEntry struct {
	Name         string
	Help         string
	Type         MetricType
	Counter      *Counter
	CounterVec   *CounterVec
	Gauge        *Gauge
	GaugeVec     *GaugeVec
	Histogram    *Histogram
	HistogramVec *HistogramVec
}

// Registry holds all registered metrics. It is safe for concurrent
// use.
type Registry struct {
	mu      sync.RWMutex
	entries []*MetricEntry
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// NewCounter registers a new Counter metric.
func (r *Registry) NewCounter(name, help string) *Counter {
	c := &Counter{}
	r.mu.Lock()
	r.entries = append(r.entries, &MetricEntry{
		Name:    name,
		Help:    help,
		Type:    TypeCounter,
		Counter: c,
	})
	r.mu.Unlock()
	return c
}

// NewGauge registers a new Gauge metric.
func (r *Registry) NewGauge(name, help string) *Gauge {
	g := &Gauge{}
	r.mu.Lock()
	r.entries = append(r.entries, &MetricEntry{
		Name:  name,
		Help:  help,
		Type:  TypeGauge,
		Gauge: g,
	})
	r.mu.Unlock()
	return g
}

// NewHistogram registers a new Histogram metric.
func (r *Registry) NewHistogram(name, help string, buckets []float64) *Histogram {
	h := NewHistogram(buckets)
	r.mu.Lock()
	r.entries = append(r.entries, &MetricEntry{
		Name:      name,
		Help:      help,
		Type:      TypeHistogram,
		Histogram: h,
	})
	r.mu.Unlock()
	return h
}

// NewCounterVec registers a new CounterVec metric.
func (r *Registry) NewCounterVec(name, help string, labels []string) *CounterVec {
	cv := NewCounterVec(name, labels)
	r.mu.Lock()
	r.entries = append(r.entries, &MetricEntry{
		Name:       name,
		Help:       help,
		Type:       TypeCounter,
		CounterVec: cv,
	})
	r.mu.Unlock()
	return cv
}

// NewGaugeVec registers a new GaugeVec metric.
func (r *Registry) NewGaugeVec(name, help string, labels []string) *GaugeVec {
	gv := NewGaugeVec(name, labels)
	r.mu.Lock()
	r.entries = append(r.entries, &MetricEntry{
		Name:     name,
		Help:     help,
		Type:     TypeGauge,
		GaugeVec: gv,
	})
	r.mu.Unlock()
	return gv
}

// NewHistogramVec registers a new HistogramVec metric.
func (r *Registry) NewHistogramVec(name, help string, labels []string, buckets []float64) *HistogramVec {
	hv := NewHistogramVec(name, labels, buckets)
	r.mu.Lock()
	r.entries = append(r.entries, &MetricEntry{
		Name:         name,
		Help:         help,
		Type:         TypeHistogram,
		HistogramVec: hv,
	})
	r.mu.Unlock()
	return hv
}

// Entries returns a copy of the registered metric entries.
func (r *Registry) Entries() []*MetricEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*MetricEntry, len(r.entries))
	copy(out, r.entries)
	return out
}

// escapeLabelValue escapes backslash and quote characters in a label
// value per the Prometheus text exposition format.
func escapeLabelValue(s string) string {
	s = strings.ReplaceAll(s, "\\", `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// formatLabels formats label key-value pairs for exposition.
func formatLabels(names, values []string) string {
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(n)
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(values[i]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func fmtUint64(v uint64) string {
	return strconv.FormatUint(v, 10)
}

func fmtInt64(v int64) string {
	return strconv.FormatInt(v, 10)
}

func fmtFloat(v float64) string {
	if math.IsInf(v, 1) || math.IsInf(v, -1) {
		return "+Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
