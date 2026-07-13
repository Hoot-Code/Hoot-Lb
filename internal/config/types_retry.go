package config

import "time"

// RetryConfig describes retry behavior for failed requests in a pool.
// Only applies to L7 (HTTP) listeners. When nil, retries are disabled.
type RetryConfig struct {
	// MaxAttempts is the total number of attempts (including the
	// original request). Must be >= 2 for retries to occur. Default 3.
	MaxAttempts int
	// RetryBudgetRatio is the maximum ratio of retries to total
	// requests in a rolling window. Once exceeded, retries are
	// stopped to prevent retry storms. Default 0.2.
	RetryBudgetRatio float64
	// BackoffBase is the base duration for exponential backoff
	// between retry attempts. Default 50ms.
	BackoffBase time.Duration
	// BackoffMax caps the maximum backoff duration. Default 1s.
	BackoffMax time.Duration
	// RetryableStatusCodes lists HTTP status codes that warrant a
	// retry. Default: [502, 503, 504].
	RetryableStatusCodes []int
}
