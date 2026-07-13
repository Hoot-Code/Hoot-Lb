// Package retry implements L7 request retry logic with exponential
// backoff and a retry budget to prevent retry storms.
package retry

import (
	"math"
	"math/rand"
	"sync"
	"time"
)

// Budget tracks a rolling window of retries vs total requests to
// enforce the retry budget ratio. It uses a sliding window counter
// that resets periodically.
type Budget struct {
	mu            sync.Mutex
	windowStart   time.Time
	windowSize    time.Duration
	budgetRatio   float64
	retriesWindow int64
	totalWindow   int64
}

// NewBudgetFromRatio creates a Budget with the given ratio and a
// 10-second rolling window.
func NewBudgetFromRatio(ratio float64) *Budget {
	return &Budget{
		windowSize:  10 * time.Second,
		budgetRatio: ratio,
		windowStart: time.Now(),
	}
}

// Allow reports whether a retry is allowed under the current budget.
func (b *Budget) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	if now.Sub(b.windowStart) > b.windowSize {
		b.retriesWindow = 0
		b.totalWindow = 0
		b.windowStart = now
	}

	b.totalWindow++
	if b.budgetRatio <= 0 {
		return false
	}
	if b.retriesWindow == 0 && b.totalWindow == 0 {
		return true
	}
	ratio := float64(b.retriesWindow) / float64(b.totalWindow)
	return ratio < b.budgetRatio
}

// RecordRetry records a retry attempt in the budget.
func (b *Budget) RecordRetry() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.retriesWindow++
}

// IsRetryable reports whether a dial error is retryable (always true)
// or a status code is in the retryable set.
func IsRetryable(statusCode int, err error, codes []int) bool {
	if err != nil {
		return true
	}
	for _, c := range codes {
		if c == statusCode {
			return true
		}
	}
	return false
}

// BackoffDuration computes the exponential backoff with jitter for
// the given attempt number (0-indexed). The formula is:
//
//	min(base * 2^attempt, max) + random jitter
func BackoffDuration(base, max time.Duration, attempt int) time.Duration {
	exp := math.Pow(2, float64(attempt))
	dur := time.Duration(float64(base) * exp)
	if dur > max {
		dur = max
	}
	// Add jitter: 0% to 50% of the computed duration.
	jitter := time.Duration(rand.Int63n(int64(dur) / 2))
	return dur + jitter
}

// IsRetryableBody reports whether the request body can be safely
// re-sent for a retry. A body is retryable if it has not been
// partially consumed and its size does not exceed maxBodyBytes.
// The bodySize parameter is the Content-Length or -1 if unknown.
func IsRetryableBody(bodySize int64, maxBodyBytes int64) bool {
	if bodySize < 0 {
		return false
	}
	if maxBodyBytes <= 0 {
		maxBodyBytes = 1 << 20 // 1MB default
	}
	return bodySize <= maxBodyBytes
}
