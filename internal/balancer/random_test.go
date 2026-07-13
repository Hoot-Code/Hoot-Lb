package balancer

import (
	"context"
	"math"
	"testing"
)

func TestRandomDistribution(t *testing.T) {
	backends := []Backend{
		NewServer("10.0.0.1:8080", 5),
		NewServer("10.0.0.2:8080", 3),
		NewServer("10.0.0.3:8080", 1),
	}
	rb := NewRandom(backends)

	const picks = 50000
	counts := make(map[string]int)
	for i := 0; i < picks; i++ {
		b, err := rb.Pick(context.Background())
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

func TestRandomEmptyPool(t *testing.T) {
	rb := NewRandom(nil)
	_, err := rb.Pick(context.Background())
	if err == nil {
		t.Fatal("expected error for empty pool")
	}
}

func TestRandomSkipUnhealthy(t *testing.T) {
	b1 := NewServer("10.0.0.1:8080", 1)
	b2 := NewServer("10.0.0.2:8080", 1)
	rb := NewRandom([]Backend{b1, b2})

	b1.SetHealthy(false)

	for i := 0; i < 100; i++ {
		b, err := rb.Pick(context.Background())
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		if b.Address() != "10.0.0.2:8080" {
			t.Errorf("pick %d: got %s, want 10.0.0.2:8080", i, b.Address())
		}
	}
}
