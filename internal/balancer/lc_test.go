package balancer

import (
	"context"
	"testing"
)

func TestLeastConnectionsFavorsLeastLoaded(t *testing.T) {
	b1 := NewServer("10.0.0.1:8080", 1)
	b2 := NewServer("10.0.0.2:8080", 1)
	b3 := NewServer("10.0.0.3:8080", 1)
	lc := NewLeastConnections([]Backend{b1, b2, b3})

	lc.Acquire(b1)
	lc.Acquire(b1)
	lc.Acquire(b1)
	lc.Acquire(b1)
	lc.Acquire(b1)
	lc.Acquire(b2)
	lc.Acquire(b2)
	lc.Acquire(b2)

	picked, err := lc.Pick(context.Background())
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if picked.Address() != "10.0.0.3:8080" {
		t.Errorf("expected b3 (least loaded, 0 conns), got %s", picked.Address())
	}
	lc.Acquire(picked)

	picked, err = lc.Pick(context.Background())
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if picked.Address() != "10.0.0.3:8080" {
		t.Errorf("expected b3 (least loaded, 1 conn), got %s", picked.Address())
	}
	lc.Acquire(picked)

	picked, err = lc.Pick(context.Background())
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if picked.Address() != "10.0.0.3:8080" {
		t.Errorf("expected b3 (least loaded, 2 conns), got %s", picked.Address())
	}
	lc.Acquire(picked)

	picked, err = lc.Pick(context.Background())
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if picked.Address() != "10.0.0.2:8080" {
		t.Errorf("expected b2 (tied with b3 but lower address order), got %s", picked.Address())
	}

	for i := 0; i < 3; i++ {
		lc.Release(b1)
	}
	picked, err = lc.Pick(context.Background())
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if picked.Address() != "10.0.0.1:8080" {
		t.Errorf("expected b1 (least loaded after release, 2 conns), got %s", picked.Address())
	}
}

func TestLeastConnectionsWeightTiebreak(t *testing.T) {
	b1 := NewServer("10.0.0.1:8080", 1)
	b2 := NewServer("10.0.0.2:8080", 3)
	lc := NewLeastConnections([]Backend{b1, b2})

	picked, err := lc.Pick(context.Background())
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if picked.Address() != "10.0.0.2:8080" {
		t.Errorf("expected b2 (higher weight tiebreak), got %s", picked.Address())
	}
}

func TestLeastConnectionsEmptyPool(t *testing.T) {
	lc := NewLeastConnections(nil)
	_, err := lc.Pick(context.Background())
	if err == nil {
		t.Fatal("expected error for empty pool")
	}
}

func TestLeastConnectionsSkipUnhealthy(t *testing.T) {
	b1 := NewServer("10.0.0.1:8080", 1)
	b2 := NewServer("10.0.0.2:8080", 1)
	lc := NewLeastConnections([]Backend{b1, b2})

	lc.Acquire(b2)
	b2.SetHealthy(false)

	picked, err := lc.Pick(context.Background())
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if picked.Address() != "10.0.0.1:8080" {
		t.Errorf("expected b1 (only healthy), got %s", picked.Address())
	}
}

func TestLeastConnectionsRelease(t *testing.T) {
	b1 := NewServer("10.0.0.1:8080", 1)
	lc := NewLeastConnections([]Backend{b1})

	lc.Acquire(b1)
	lc.Release(b1)

	lc.Release(b1)

	picked, err := lc.Pick(context.Background())
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if picked.Address() != "10.0.0.1:8080" {
		t.Errorf("expected b1, got %s", picked.Address())
	}
}
