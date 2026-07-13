package l7

import (
	"context"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/metrics"
)

// L7Metrics holds metric vectors for an HTTP proxy server.
type L7Metrics struct {
	ConnectionsTotal     *metrics.CounterVec
	ConnectionsActive    *metrics.GaugeVec
	RequestDuration      *metrics.HistogramVec
	DialFailures         *metrics.CounterVec
	BackendLatency       *metrics.HistogramVec
	TLSHandshakeDuration *metrics.HistogramVec
}

// l7MetricsKey is the context key for L7 metrics data.
type l7MetricsKey struct{}

// GetL7Metrics returns the L7 metrics data stored in the request
// context by the Director.
func GetL7Metrics(ctx context.Context) *L7Metrics {
	m, _ := ctx.Value(l7MetricsKey{}).(*L7Metrics)
	return m
}

// l7PoolInfoKey is the context key for pool info used by metrics.
type l7PoolInfoKey struct{}

// L7PoolInfo holds pool metadata for metrics labeling.
type L7PoolInfo struct {
	Pool string
}

// GetL7PoolInfo returns the pool info stored in the request context.
func GetL7PoolInfo(ctx context.Context) *L7PoolInfo {
	p, _ := ctx.Value(l7PoolInfoKey{}).(*L7PoolInfo)
	return p
}

// requestStartKey is the context key for request start time.
type requestStartKey struct{}

// GetRequestStart returns the request start time stored in context.
func GetRequestStart(ctx context.Context) time.Time {
	t, _ := ctx.Value(requestStartKey{}).(time.Time)
	return t
}

// stickyInfoKey is the context key for sticky cookie information.
// The Director stores this after choosing a backend so the
// ModifyResponse hook can set the Set-Cookie header.
type stickyInfoKey struct{}

// stickyInfo holds the data needed to set a sticky cookie on the
// response after the backend is chosen.
type stickyInfo struct {
	CookieName  string
	BackendAddr string
	TTL         int // Max-Age in seconds
}

// GetStickyInfo returns the sticky cookie info stored in the request
// context by the Director. Returns nil if no sticky cookie should be
// set.
func GetStickyInfo(ctx context.Context) *stickyInfo {
	s, _ := ctx.Value(stickyInfoKey{}).(*stickyInfo)
	return s
}

// retryConfigKey is the context key for retry configuration.
type retryConfigKey struct{}

// GetRetryConfig returns the retry config stored in the request context.
func GetRetryConfig(ctx context.Context) *RetryConfig {
	r, _ := ctx.Value(retryConfigKey{}).(*RetryConfig)
	return r
}

// RetryConfig holds retry configuration attached to a request context
// by the Director when the pool has retry enabled.
type RetryConfig struct {
	MaxAttempts          int
	RetryBudgetRatio     float64
	BackoffBase          time.Duration
	BackoffMax           time.Duration
	RetryableStatusCodes []int
	MaxBodyBytes         int64
}

// RetryConfigFromPool creates a retry config from pool config values.
// Exported for use by the wiring layer.
func RetryConfigFromPool(cfg *config.RetryConfig, maxBodyBytes int64) *RetryConfig {
	if cfg == nil {
		return nil
	}
	if maxBodyBytes <= 0 {
		maxBodyBytes = 1 << 20
	}
	return &RetryConfig{
		MaxAttempts:          cfg.MaxAttempts,
		RetryBudgetRatio:     cfg.RetryBudgetRatio,
		BackoffBase:          cfg.BackoffBase,
		BackoffMax:           cfg.BackoffMax,
		RetryableStatusCodes: cfg.RetryableStatusCodes,
		MaxBodyBytes:         maxBodyBytes,
	}
}
