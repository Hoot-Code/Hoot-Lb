package circuitbreaker

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClosedToOpen(t *testing.T) {
	var co atomic.Bool
	b := NewBreaker(3, 50*time.Millisecond, 1, &co)

	for i := 0; i < 2; i++ {
		b.RecordFailure()
		if b.State() != StateClosed {
			t.Fatalf("expected closed after %d failures, got %d", i+1, b.State())
		}
	}

	b.RecordFailure()
	if b.State() != StateOpen {
		t.Fatalf("expected open after 3 failures, got %d", b.State())
	}
	if !co.Load() {
		t.Fatal("circuitOpen should be true")
	}
}

func TestOpenRejects(t *testing.T) {
	var co atomic.Bool
	b := NewBreaker(2, 1*time.Second, 1, &co)

	b.RecordFailure()
	b.RecordFailure()

	if b.Allow() {
		t.Fatal("should not allow in open state")
	}
}

func TestHalfOpenAfterDuration(t *testing.T) {
	var co atomic.Bool
	b := NewBreaker(2, 30*time.Millisecond, 1, &co)

	b.RecordFailure()
	b.RecordFailure()

	time.Sleep(40 * time.Millisecond)

	if !b.Allow() {
		t.Fatal("should allow in half-open state")
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("expected half-open, got %d", b.State())
	}
}

func TestHalfOpenProbeLimit(t *testing.T) {
	var co atomic.Bool
	b := NewBreaker(2, 30*time.Millisecond, 1, &co)

	b.RecordFailure()
	b.RecordFailure()

	time.Sleep(40 * time.Millisecond)

	if !b.Allow() {
		t.Fatal("first half-open probe should be allowed")
	}

	if b.Allow() {
		t.Fatal("second half-open probe should be rejected (max_probes=1)")
	}
}

func TestHalfOpenSuccessCloses(t *testing.T) {
	var co atomic.Bool
	b := NewBreaker(2, 30*time.Millisecond, 1, &co)

	b.RecordFailure()
	b.RecordFailure()

	time.Sleep(40 * time.Millisecond)

	b.Allow()
	b.RecordSuccess()

	if b.State() != StateClosed {
		t.Fatalf("expected closed after successful probe, got %d", b.State())
	}
	if co.Load() {
		t.Fatal("circuitOpen should be false after success")
	}

	if !b.Allow() {
		t.Fatal("should allow in closed state")
	}
}

func TestHalfOpenFailureReopens(t *testing.T) {
	var co atomic.Bool
	b := NewBreaker(2, 30*time.Millisecond, 1, &co)

	b.RecordFailure()
	b.RecordFailure()

	time.Sleep(40 * time.Millisecond)

	b.Allow()
	b.RecordFailure()

	if b.State() != StateOpen {
		t.Fatalf("expected open after failed probe, got %d", b.State())
	}
	if !co.Load() {
		t.Fatal("circuitOpen should be true after failed probe")
	}
}

func TestRecoveryAndReopen(t *testing.T) {
	var co atomic.Bool
	b := NewBreaker(2, 30*time.Millisecond, 1, &co)

	b.RecordFailure()
	b.RecordFailure()

	time.Sleep(40 * time.Millisecond)
	b.Allow()
	b.RecordSuccess()

	if b.State() != StateClosed {
		t.Fatal("should be closed after successful probe")
	}

	b.RecordFailure()
	b.RecordFailure()

	if b.State() != StateOpen {
		t.Fatal("should be open after 2 more failures")
	}

	time.Sleep(10 * time.Millisecond)
	if b.Allow() {
		t.Fatal("should not allow before open_duration elapses")
	}

	time.Sleep(30 * time.Millisecond)
	if !b.Allow() {
		t.Fatal("should allow after open_duration in half-open")
	}
}

func TestSuccessResetsCounter(t *testing.T) {
	var co atomic.Bool
	b := NewBreaker(3, 1*time.Second, 1, &co)

	b.RecordFailure()
	b.RecordFailure()
	b.RecordSuccess()

	b.RecordFailure()
	if b.State() != StateClosed {
		t.Fatal("should still be closed - success reset counter")
	}
}

func TestConcurrentHalfOpen(t *testing.T) {
	var co atomic.Bool
	b := NewBreaker(2, 30*time.Millisecond, 1, &co)

	b.RecordFailure()
	b.RecordFailure()

	time.Sleep(40 * time.Millisecond)

	var wg sync.WaitGroup
	allowed := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := b.Allow()
			allowed <- a
		}()
	}
	wg.Wait()
	close(allowed)

	var count int
	for a := range allowed {
		if a {
			count++
		}
	}

	if count != 1 {
		t.Fatalf("expected exactly 1 probe allowed, got %d", count)
	}
}

func TestNoOpSuccessInClosed(t *testing.T) {
	var co atomic.Bool
	b := NewBreaker(3, 1*time.Second, 1, &co)

	b.RecordSuccess()

	if b.State() != StateClosed {
		t.Fatal("should remain closed")
	}
}
