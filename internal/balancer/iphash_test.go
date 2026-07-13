package balancer

import (
	"context"
	"math"
	"testing"
)

func TestIPHashSameKeySameBackend(t *testing.T) {
	backends := []Backend{
		NewServer("10.0.0.1:8080", 1),
		NewServer("10.0.0.2:8080", 1),
		NewServer("10.0.0.3:8080", 1),
	}
	ih := NewIPHash(backends)

	key := "192.168.1.100"
	ctx := context.WithValue(context.Background(), ClientKey{}, key)

	first, err := ih.Pick(ctx)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}

	for i := 0; i < 100; i++ {
		b, err := ih.Pick(ctx)
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		if b.Address() != first.Address() {
			t.Errorf("pick %d: got %s, want %s (same key should map to same backend)",
				i, b.Address(), first.Address())
		}
	}
}

func TestIPHashDifferentKeysDifferentMapping(t *testing.T) {
	backends := []Backend{
		NewServer("10.0.0.1:8080", 1),
		NewServer("10.0.0.2:8080", 1),
		NewServer("10.0.0.3:8080", 1),
	}
	ih := NewIPHash(backends)

	const numKeys = 3000
	counts := make(map[string]int)
	for i := 0; i < numKeys; i++ {
		key := "10.0.0." + string(rune('0'+i%10)) + "." + string(rune('0'+i/10%10))
		ctx := context.WithValue(context.Background(), ClientKey{}, key)
		b, err := ih.Pick(ctx)
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		counts[b.Address()]++
	}

	for addr, count := range counts {
		ratio := float64(count) / float64(numKeys)
		if ratio < 0.2 || ratio > 0.5 {
			t.Errorf("key distribution for %s: %.2f%% (want ~33%%)", addr, ratio*100)
		}
	}
}

func TestIPHashWeighted(t *testing.T) {
	backends := []Backend{
		NewServer("10.0.0.1:8080", 5),
		NewServer("10.0.0.2:8080", 1),
	}
	ih := NewIPHash(backends)

	const numKeys = 10000
	counts := make(map[string]int)
	for i := 0; i < numKeys; i++ {
		key := string(rune('A'+i%26)) + string(rune('0'+i/26%10))
		ctx := context.WithValue(context.Background(), ClientKey{}, key)
		b, err := ih.Pick(ctx)
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		counts[b.Address()]++
	}

	b1Ratio := float64(counts["10.0.0.1:8080"]) / float64(numKeys)
	b2Ratio := float64(counts["10.0.0.2:8080"]) / float64(numKeys)
	if math.Abs(b1Ratio-5.0/6.0) > 0.05 {
		t.Errorf("b1 ratio: %.4f, want ~0.833", b1Ratio)
	}
	if math.Abs(b2Ratio-1.0/6.0) > 0.05 {
		t.Errorf("b2 ratio: %.4f, want ~0.167", b2Ratio)
	}
}

func TestIPHashEmptyPool(t *testing.T) {
	ih := NewIPHash(nil)
	_, err := ih.Pick(context.Background())
	if err == nil {
		t.Fatal("expected error for empty pool")
	}
}

func TestIPHashSkipUnhealthy(t *testing.T) {
	b1 := NewServer("10.0.0.1:8080", 1)
	b2 := NewServer("10.0.0.2:8080", 1)
	b3 := NewServer("10.0.0.3:8080", 1)
	ih := NewIPHash([]Backend{b1, b2, b3})

	b1.SetHealthy(false)
	b2.SetHealthy(false)

	ctx := context.WithValue(context.Background(), ClientKey{}, "192.168.1.100")
	b, err := ih.Pick(ctx)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if b.Address() != "10.0.0.3:8080" {
		t.Errorf("expected b3 (only healthy), got %s", b.Address())
	}
}

func TestIPHashUpdateBackends(t *testing.T) {
	b1 := NewServer("10.0.0.1:8080", 1)
	ih := NewIPHash([]Backend{b1})

	ctx := context.WithValue(context.Background(), ClientKey{}, "test")
	b, err := ih.Pick(ctx)
	if err != nil || b.Address() != "10.0.0.1:8080" {
		t.Fatalf("initial: %v, %v", b, err)
	}

	b2 := NewServer("10.0.0.2:8080", 1)
	ih.UpdateBackends([]Backend{b2})

	b, err = ih.Pick(ctx)
	if err != nil || b.Address() != "10.0.0.2:8080" {
		t.Fatalf("after update: %v, %v", b, err)
	}
}
