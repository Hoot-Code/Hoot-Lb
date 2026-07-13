package ratelimit

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestAllowUnderLimit(t *testing.T) {
	l := NewLimiter(10, 10, 0)
	defer l.Stop()

	for i := 0; i < 10; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("request %d should be allowed", i)
		}
	}
}

func TestRejectOverBurst(t *testing.T) {
	l := NewLimiter(1, 5, 0)
	defer l.Stop()

	for i := 0; i < 5; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("request %d should be allowed", i)
		}
	}

	if l.Allow("1.2.3.4") {
		t.Fatal("request past burst should be rejected")
	}
}

func TestRefillOverTime(t *testing.T) {
	l := NewLimiter(100, 1, 0)
	defer l.Stop()

	if !l.Allow("1.2.3.4") {
		t.Fatal("first request should be allowed")
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("second request should be rejected immediately")
	}

	time.Sleep(20 * time.Millisecond)

	if !l.Allow("1.2.3.4") {
		t.Fatal("request after refill should be allowed")
	}
}

func TestPerClientIsolation(t *testing.T) {
	l := NewLimiter(2, 2, 0)
	defer l.Stop()

	if !l.Allow("client-a") {
		t.Fatal("client-a first should be allowed")
	}
	if !l.Allow("client-a") {
		t.Fatal("client-a second should be allowed")
	}
	if l.Allow("client-a") {
		t.Fatal("client-a third should be rejected")
	}

	if !l.Allow("client-b") {
		t.Fatal("client-b should not be affected by client-a")
	}
}

func TestEviction(t *testing.T) {
	l := NewLimiter(100, 100, 50*time.Millisecond)
	defer l.Stop()

	count := 500
	for i := 0; i < count; i++ {
		l.Allow(fmt.Sprintf("client-%d", i))
	}

	if l.Len() != count {
		t.Fatalf("expected %d buckets, got %d", count, l.Len())
	}

	time.Sleep(120 * time.Millisecond)

	if l.Len() != 0 {
		t.Fatalf("expected 0 buckets after eviction, got %d", l.Len())
	}
}

func TestEvictionOnlyIdle(t *testing.T) {
	l := NewLimiter(1000, 1000, 50*time.Millisecond)
	defer l.Stop()

	for i := 0; i < 100; i++ {
		l.Allow(fmt.Sprintf("idle-%d", i))
	}

	l.Allow("active-1")

	time.Sleep(30 * time.Millisecond)

	l.Allow("active-1")

	time.Sleep(60 * time.Millisecond)

	if l.Len() != 1 {
		t.Fatalf("expected 1 active bucket, got %d", l.Len())
	}

	if !l.Allow("active-1") {
		t.Fatal("active client should still be allowed")
	}
}

func TestConcurrentAccess(t *testing.T) {
	l := NewLimiter(10000, 10000, 0)
	defer l.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ip := fmt.Sprintf("client-%d", n%10)
			for j := 0; j < 100; j++ {
				l.Allow(ip)
			}
		}(i)
	}
	wg.Wait()
}

func TestNoEvictionWhenZeroTTL(t *testing.T) {
	l := NewLimiter(100, 100, 0)
	defer l.Stop()

	for i := 0; i < 100; i++ {
		l.Allow(fmt.Sprintf("client-%d", i))
	}

	time.Sleep(100 * time.Millisecond)

	if l.Len() != 100 {
		t.Fatalf("expected 100 buckets (no eviction), got %d", l.Len())
	}
}

// TestStopGoroutineLeak verifies that Stop reclaims the eviction
// goroutine and leaves no leaked goroutines.
func TestStopGoroutineLeak(t *testing.T) {
	baseline := runtime.NumGoroutine()

	for i := 0; i < 10; i++ {
		l := NewLimiter(100, 100, 50*time.Millisecond)
		for j := 0; j < 50; j++ {
			l.Allow(fmt.Sprintf("client-%d-%d", i, j))
		}
		l.Stop()
	}

	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	time.Sleep(200 * time.Millisecond)

	current := runtime.NumGoroutine()
	if current > baseline+5 {
		t.Errorf("goroutine leak after 10 limiter create/stop cycles: baseline %d, now %d", baseline, current)
	}
}
