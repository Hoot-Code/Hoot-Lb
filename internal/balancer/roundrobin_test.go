package balancer

import (
	"context"
	"testing"
)

func TestRoundRobinPick(t *testing.T) {
	backends := []Backend{
		NewServer("10.0.0.1:8080", 1),
		NewServer("10.0.0.2:8080", 1),
		NewServer("10.0.0.3:8080", 1),
	}
	rr := NewRoundRobin(backends)

	seen := make(map[string]int)
	for i := 0; i < 6; i++ {
		b, err := rr.Pick(context.Background())
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		seen[b.Address()]++
	}

	if len(seen) != 3 {
		t.Fatalf("expected 3 distinct backends, got %d", len(seen))
	}
	for addr, count := range seen {
		if count != 2 {
			t.Errorf("backend %s picked %d times, want 2", addr, count)
		}
	}
}

func TestRoundRobinEmptyPool(t *testing.T) {
	rr := NewRoundRobin(nil)
	_, err := rr.Pick(context.Background())
	if err == nil {
		t.Fatal("expected error for empty pool")
	}
}

func TestRoundRobinSkipUnhealthy(t *testing.T) {
	b1 := NewServer("10.0.0.1:8080", 1)
	b2 := NewServer("10.0.0.2:8080", 1)
	rr := NewRoundRobin([]Backend{b1, b2})

	b1.SetHealthy(false)

	for i := 0; i < 10; i++ {
		b, err := rr.Pick(context.Background())
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		if b.Address() != "10.0.0.2:8080" {
			t.Errorf("pick %d: got %s, want 10.0.0.2:8080", i, b.Address())
		}
	}

	b2.SetHealthy(false)
	_, err := rr.Pick(context.Background())
	if err == nil {
		t.Fatal("expected error when all backends unhealthy")
	}
}

func TestRoundRobinUpdateBackends(t *testing.T) {
	b1 := NewServer("10.0.0.1:8080", 1)
	rr := NewRoundRobin([]Backend{b1})

	b, err := rr.Pick(context.Background())
	if err != nil || b.Address() != "10.0.0.1:8080" {
		t.Fatalf("initial pick: %v, %v", b, err)
	}

	b2 := NewServer("10.0.0.2:8080", 1)
	rr.UpdateBackends([]Backend{b2})

	b, err = rr.Pick(context.Background())
	if err != nil || b.Address() != "10.0.0.2:8080" {
		t.Fatalf("after update: %v, %v", b, err)
	}
}
