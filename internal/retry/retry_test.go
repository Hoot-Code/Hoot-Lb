package retry

import (
	"testing"
	"time"
)

func TestBackoffDuration(t *testing.T) {
	base := 50 * time.Millisecond
	max := 1 * time.Second

	// First attempt: base * 2^0 = 50ms + jitter
	d0 := BackoffDuration(base, max, 0)
	if d0 < base || d0 > base+base/2 {
		t.Errorf("attempt 0: expected [50ms, 75ms], got %v", d0)
	}

	// Second attempt: base * 2^1 = 100ms + jitter
	d1 := BackoffDuration(base, max, 1)
	if d1 < 100*time.Millisecond || d1 > 150*time.Millisecond {
		t.Errorf("attempt 1: expected [100ms, 150ms], got %v", d1)
	}

	// Third attempt: base * 2^2 = 200ms + jitter
	d2 := BackoffDuration(base, max, 2)
	if d2 < 200*time.Millisecond || d2 > 300*time.Millisecond {
		t.Errorf("attempt 2: expected [200ms, 300ms], got %v", d2)
	}

	// Capped at max
	d10 := BackoffDuration(base, max, 10)
	if d10 > max+max/2 {
		t.Errorf("attempt 10: expected capped at ~1s, got %v", d10)
	}
}

func TestIsRetryableBody(t *testing.T) {
	if !IsRetryableBody(100, 1000) {
		t.Error("small body should be retryable")
	}
	if IsRetryableBody(2000, 1000) {
		t.Error("large body should not be retryable")
	}
	if IsRetryableBody(-1, 1000) {
		t.Error("unknown size body should not be retryable")
	}
	if !IsRetryableBody(100, 0) {
		t.Error("zero max should use default 1MB")
	}
}

func TestIsRetryable(t *testing.T) {
	codes := []int{502, 503, 504}

	if !IsRetryable(502, nil, codes) {
		t.Error("502 should be retryable")
	}
	if !IsRetryable(503, nil, codes) {
		t.Error("503 should be retryable")
	}
	if IsRetryable(500, nil, codes) {
		t.Error("500 should not be retryable")
	}
	if IsRetryable(200, nil, codes) {
		t.Error("200 should not be retryable")
	}
	// Dial errors are always retryable
	if !IsRetryable(0, dialError{}, codes) {
		t.Error("dial errors should be retryable")
	}
}

type dialError struct{}

func (d dialError) Error() string { return "dial" }

func TestBudget(t *testing.T) {
	b := NewBudgetFromRatio(0.2)

	// Initially allowed
	if !b.Allow() {
		t.Error("budget should allow initially")
	}

	// Record retries and check budget caps
	for i := 0; i < 100; i++ {
		b.Allow()
		b.RecordRetry()
	}

	// After 100 retries / 100 total, ratio is 1.0 > 0.2
	if b.Allow() {
		t.Error("budget should be exhausted after 100 retries")
	}
}

func TestBudgetWindowReset(t *testing.T) {
	b := &Budget{
		windowSize:  1 * time.Millisecond,
		budgetRatio: 0.2,
		windowStart: time.Now().Add(-1 * time.Second), // expired window
	}

	// Window is expired, should allow
	if !b.Allow() {
		t.Error("budget should allow after window reset")
	}
}
