package metrics

import (
	"sync"
	"testing"
)

func TestCounterIncrement(t *testing.T) {
	c := &Counter{}
	c.Add(1)
	c.Add(5)
	if v := c.Value(); v != 6 {
		t.Fatalf("expected 6, got %d", v)
	}
}

func TestCounterMonotonic(t *testing.T) {
	c := &Counter{}
	c.Add(1)
	c.Add(0)
	if v := c.Value(); v != 1 {
		t.Fatalf("expected 1, got %d", v)
	}
}

func TestGaugeIncDec(t *testing.T) {
	g := &Gauge{}
	g.Inc()
	g.Inc()
	g.Dec()
	if v := g.Value(); v != 1 {
		t.Fatalf("expected 1, got %d", v)
	}
}

func TestGaugeNegative(t *testing.T) {
	g := &Gauge{}
	g.Dec()
	if v := g.Value(); v != -1 {
		t.Fatalf("expected -1, got %d", v)
	}
}

func TestHistogramObserve(t *testing.T) {
	h := NewHistogram(DefaultHistogramBuckets)
	h.Observe(0.003)
	h.Observe(0.05)
	h.Observe(100.0)
	if c := h.Count(); c != 3 {
		t.Fatalf("expected count 3, got %d", c)
	}
	if s := h.Sum(); s < 100.04 || s > 100.06 {
		t.Fatalf("expected sum ~100.05, got %f", s)
	}
	counts := h.BucketCounts()
	// Non-cumulative: +Inf bucket only counts observations > 10.0
	if counts[len(counts)-1] != 1 {
		t.Fatalf("+Inf non-cumulative should be 1, got %d", counts[len(counts)-1])
	}
	// Verify total via sum of non-cumulative counts
	var total uint64
	for _, c := range counts {
		total += c
	}
	if total != 3 {
		t.Fatalf("total observations should be 3, got %d", total)
	}
}

func TestCounterVecWith(t *testing.T) {
	cv := NewCounterVec("test", []string{"method"})
	cv.With("GET").Add(1)
	cv.With("POST").Add(5)
	if v := cv.With("GET").Value(); v != 1 {
		t.Fatalf("expected 1, got %d", v)
	}
	if v := cv.With("POST").Value(); v != 5 {
		t.Fatalf("expected 5, got %d", v)
	}
}

func TestGaugeVecRemove(t *testing.T) {
	gv := NewGaugeVec("test", []string{"pool"})
	gv.With("a").Set(1)
	gv.With("b").Set(2)
	if !gv.Remove("a") {
		t.Fatal("expected Remove to return true")
	}
	var count int
	gv.Each(func(labels []string, value int64) {
		count++
	})
	if count != 1 {
		t.Fatalf("expected 1 remaining, got %d", count)
	}
}

func TestCounterConcurrency(t *testing.T) {
	c := &Counter{}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				c.Add(1)
			}
		}()
	}
	wg.Wait()
	if v := c.Value(); v != 100000 {
		t.Fatalf("expected 100000, got %d", v)
	}
}

func TestRegistryEntries(t *testing.T) {
	r := NewRegistry()
	r.NewCounter("c1", "help c1")
	r.NewGauge("g1", "help g1")
	r.NewHistogram("h1", "help h1", DefaultHistogramBuckets)
	entries := r.Entries()
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
}

func BenchmarkCounterAdd(b *testing.B) {
	c := &Counter{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Add(1)
	}
}

func BenchmarkGaugeInc(b *testing.B) {
	g := &Gauge{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Inc()
	}
}

func BenchmarkHistogramObserve(b *testing.B) {
	h := NewHistogram(DefaultHistogramBuckets)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Observe(0.05)
	}
}

func BenchmarkCounterVecWith(b *testing.B) {
	cv := NewCounterVec("test", []string{"l1", "l2"})
	cv.With("a", "b")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cv.With("a", "b").Add(1)
	}
}
