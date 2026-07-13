package metrics

import (
	"testing"
)

func TestFineHistogramBuckets(t *testing.T) {
	if len(FineHistogramBuckets) == 0 {
		t.Fatal("FineHistogramBuckets should not be empty")
	}
	// Verify sorted
	for i := 1; i < len(FineHistogramBuckets); i++ {
		if FineHistogramBuckets[i] <= FineHistogramBuckets[i-1] {
			t.Errorf("FineHistogramBuckets not sorted at index %d: %f <= %f",
				i, FineHistogramBuckets[i], FineHistogramBuckets[i-1])
		}
	}
	// Verify sub-ms resolution
	if FineHistogramBuckets[0] >= 0.001 {
		t.Errorf("first bucket should be sub-millisecond, got %f", FineHistogramBuckets[0])
	}
}

func TestFineHistogramObservation(t *testing.T) {
	h := NewHistogram(FineHistogramBuckets)

	// Observe a sub-millisecond value
	h.Observe(0.0003) // 300 microseconds
	if h.Count() != 1 {
		t.Errorf("expected count 1, got %d", h.Count())
	}

	// Observe a millisecond value
	h.Observe(0.005) // 5ms
	if h.Count() != 2 {
		t.Errorf("expected count 2, got %d", h.Count())
	}

	// Verify bucket counts
	counts := h.BucketCounts()
	// 0.0003 should fall in the 0.0005 bucket (index 1)
	if counts[1] != 1 {
		t.Errorf("expected 1 in 0.0005 bucket, got %d", counts[1])
	}
}

func TestHistogramVecWithFineBuckets(t *testing.T) {
	hv := NewHistogramVec("test_fine", []string{"pool", "backend"}, FineHistogramBuckets)

	h := hv.With("pool_a", "backend_1")
	h.Observe(0.0001)

	h2 := hv.With("pool_a", "backend_2")
	h2.Observe(0.5)

	if h.Count() != 1 {
		t.Errorf("expected count 1, got %d", h.Count())
	}
	if h2.Count() != 1 {
		t.Errorf("expected count 1, got %d", h2.Count())
	}
}
