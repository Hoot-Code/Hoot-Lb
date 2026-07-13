package balancer

import (
	"context"
	"math"
	"testing"
)

func TestWeightedRoundRobinDistribution(t *testing.T) {
	backends := []Backend{
		NewServer("10.0.0.1:8080", 5),
		NewServer("10.0.0.2:8080", 3),
		NewServer("10.0.0.3:8080", 1),
	}
	wrr := NewWeightedRoundRobin(backends)

	const picks = 10000
	counts := make(map[string]int)
	for i := 0; i < picks; i++ {
		b, err := wrr.Pick(context.Background())
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		counts[b.Address()]++
	}

	weights := []struct {
		addr   string
		weight int
	}{
		{"10.0.0.1:8080", 5},
		{"10.0.0.2:8080", 3},
		{"10.0.0.3:8080", 1},
	}
	totalWeight := 9

	tolerance := 0.03
	for _, w := range weights {
		observed := float64(counts[w.addr]) / float64(picks)
		expected := float64(w.weight) / float64(totalWeight)
		diff := math.Abs(observed - expected)
		if diff > tolerance {
			t.Errorf("%s: observed %.4f, expected %.4f, diff %.4f > tolerance %.4f",
				w.addr, observed, expected, diff, tolerance)
		}
	}
}

func TestWeightedRoundRobinEqualWeights(t *testing.T) {
	backends := []Backend{
		NewServer("10.0.0.1:8080", 1),
		NewServer("10.0.0.2:8080", 1),
		NewServer("10.0.0.3:8080", 1),
	}
	wrr := NewWeightedRoundRobin(backends)

	seen := make(map[string]int)
	for i := 0; i < 9; i++ {
		b, err := wrr.Pick(context.Background())
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		seen[b.Address()]++
	}
	for addr, count := range seen {
		if count != 3 {
			t.Errorf("equal-weight backend %s picked %d times, want 3", addr, count)
		}
	}
}

func TestWeightedRoundRobinEmptyPool(t *testing.T) {
	wrr := NewWeightedRoundRobin(nil)
	_, err := wrr.Pick(context.Background())
	if err == nil {
		t.Fatal("expected error for empty pool")
	}
}

func TestWeightedRoundRobinSkipUnhealthy(t *testing.T) {
	b1 := NewServer("10.0.0.1:8080", 5)
	b2 := NewServer("10.0.0.2:8080", 3)
	b3 := NewServer("10.0.0.3:8080", 1)
	wrr := NewWeightedRoundRobin([]Backend{b1, b2, b3})

	b3.SetHealthy(false)

	const picks = 8000
	counts := make(map[string]int)
	for i := 0; i < picks; i++ {
		b, err := wrr.Pick(context.Background())
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		counts[b.Address()]++
	}

	tolerance := 0.03
	if d := math.Abs(float64(counts["10.0.0.1:8080"])/float64(picks) - 5.0/8.0); d > tolerance {
		t.Errorf("10.0.0.1:8080: observed %.4f, expected 0.625, diff %.4f", float64(counts["10.0.0.1:8080"])/float64(picks), d)
	}
	if d := math.Abs(float64(counts["10.0.0.2:8080"])/float64(picks) - 3.0/8.0); d > tolerance {
		t.Errorf("10.0.0.2:8080: observed %.4f, expected 0.375, diff %.4f", float64(counts["10.0.0.2:8080"])/float64(picks), d)
	}
}
