package l7

import (
	"bytes"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/retry"
)

const (
	defaultDialTimeout = 5 * time.Second

	defaultTLSHandshakeTimeout = 5 * time.Second

	defaultResponseHeaderTimeout = 30 * time.Second
)

// wrappedTransport is an http.RoundTripper that wraps http.Transport
// with connection tracking (ConnReleaser) and failure reporting
// (FailureReporter) integration. It reads the picked backend, pool
// LoadBalancer, and FailureReporter from the request context, which
// are stored by the Director function.
//
// Acquire is called in the Director (before Pick returns) to ensure
// the connection count is incremented before the next Pick. Release
// and ReportFailure are called here in the Transport on error, or
// when the response body is closed on success.
type wrappedTransport struct {
	base *http.Transport
}

// RoundTrip executes a single HTTP transaction. On dial failure, it
// calls FailureReporter.ReportFailure and Release. On success, it
// wraps the response body so that Release is called exactly once when
// the body is closed. When retry config is present in the context,
// retryable failures trigger a retry with a new backend.
func (wt *wrappedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := GetError(req.Context()); err != nil {
		return nil, err
	}

	retryCfg := GetRetryConfig(req.Context())
	if retryCfg != nil {
		return wt.roundTripWithRetry(req, retryCfg)
	}
	return wt.roundTripSingle(req)
}

// roundTripSingle executes a single HTTP transaction without retry.
func (wt *wrappedTransport) roundTripSingle(req *http.Request) (*http.Response, error) {
	backend := GetBackend(req.Context())
	if backend == nil {
		return wt.base.RoundTrip(req)
	}

	fr := GetFR(req.Context())
	outcome := GetOutcome(req.Context())

	dialStart := time.Now()
	resp, err := wt.base.RoundTrip(req)
	if err != nil {
		if fr != nil {
			fr.ReportFailure(backend)
		}
		if outcome != nil {
			outcome.RecordFailure(backend)
		}
		lb := GetLB(req.Context())
		if cr, ok := lb.(balancer.ConnReleaser); ok {
			cr.Release(backend)
		}
		if m := GetL7Metrics(req.Context()); m != nil {
			poolInfo := GetL7PoolInfo(req.Context())
			if poolInfo != nil {
				m.ConnectionsActive.With("", poolInfo.Pool, backend.Address(), "http").Dec()
				m.DialFailures.With("", poolInfo.Pool, backend.Address()).Add(1)
			}
		}
		return nil, err
	}

	// Record backend latency (dial to first response byte).
	if m := GetL7Metrics(req.Context()); m != nil && m.BackendLatency != nil {
		poolInfo := GetL7PoolInfo(req.Context())
		if poolInfo != nil {
			m.BackendLatency.With(poolInfo.Pool, backend.Address()).Observe(time.Since(dialStart).Seconds())
		}
	}

	if outcome != nil {
		if resp.StatusCode >= 500 {
			outcome.RecordFailure(backend)
		} else {
			outcome.RecordSuccess(backend)
		}
	}

	lb := GetLB(req.Context())
	if cr, ok := lb.(balancer.ConnReleaser); ok {
		resp.Body = &releaseBody{
			ReadCloser: resp.Body,
			once:       sync.Once{},
			release: func() {
				cr.Release(backend)
				if m := GetL7Metrics(req.Context()); m != nil {
					poolInfo := GetL7PoolInfo(req.Context())
					if poolInfo != nil {
						m.ConnectionsActive.With("", poolInfo.Pool, backend.Address(), "http").Dec()
						start := GetRequestStart(req.Context())
						if !start.IsZero() {
							m.RequestDuration.With("", poolInfo.Pool, backend.Address()).Observe(time.Since(start).Seconds())
						}
					}
				}
			},
		}
	} else {
		if m := GetL7Metrics(req.Context()); m != nil {
			poolInfo := GetL7PoolInfo(req.Context())
			if poolInfo != nil {
				resp.Body = &metricsBody{
					ReadCloser: resp.Body,
					once:       sync.Once{},
					close: func() {
						m.ConnectionsActive.With("", poolInfo.Pool, backend.Address(), "http").Dec()
						start := GetRequestStart(req.Context())
						if !start.IsZero() {
							m.RequestDuration.With("", poolInfo.Pool, backend.Address()).Observe(time.Since(start).Seconds())
						}
					},
				}
			}
		}
	}

	return resp, nil
}

// roundTripWithRetry executes the request with retry logic. It buffers
// the request body and retries on dial failures or retryable status
// codes, picking a new backend via the pool's LoadBalancer each time.
func (wt *wrappedTransport) roundTripWithRetry(req *http.Request, rcfg *RetryConfig) (*http.Response, error) {
	lb := GetLB(req.Context())
	fr := GetFR(req.Context())
	outcome := GetOutcome(req.Context())

	maxAttempts := rcfg.MaxAttempts
	if maxAttempts < 2 {
		return wt.roundTripSingle(req)
	}

	// Buffer request body for retries.
	var bodyBytes []byte
	var bodySize int64
	if req.Body != nil {
		var buf bytes.Buffer
		n, err := io.Copy(&buf, req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		bodySize = n
		bodyBytes = buf.Bytes()
	}

	bodyRetryable := retry.IsRetryableBody(bodySize, rcfg.MaxBodyBytes)
	budget := retry.NewBudgetFromRatio(rcfg.RetryBudgetRatio)

	var lastErr error
	dialStart := time.Now()

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			if !bodyRetryable || !budget.Allow() {
				break
			}
			dur := retry.BackoffDuration(rcfg.BackoffBase, rcfg.BackoffMax, attempt-1)
			timer := time.NewTimer(dur)
			<-timer.C

			backend, pickErr := lb.Pick(req.Context())
			if pickErr != nil {
				return nil, pickErr
			}

			if cr, ok := lb.(balancer.ConnReleaser); ok {
				cr.Acquire(backend)
			}

			req = req.Clone(req.Context())
			req.URL.Host = backend.Address()
		}

		if bodyBytes != nil {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		} else {
			req.Body = nil
		}

		backend := GetBackend(req.Context())
		if backend == nil && attempt > 0 {
			// Build a minimal backend ref for the retry attempt.
			backend = &retryBackend{addr: req.URL.Host}
		}

		resp, err := wt.base.RoundTrip(req)

		if err != nil {
			lastErr = err
			dialStart = time.Now()
			if backend != nil {
				if cr, ok := lb.(balancer.ConnReleaser); ok {
					cr.Release(backend)
				}
				if fr != nil {
					fr.ReportFailure(backend)
				}
				if outcome != nil {
					outcome.RecordFailure(backend)
				}
				if m := GetL7Metrics(req.Context()); m != nil {
					poolInfo := GetL7PoolInfo(req.Context())
					if poolInfo != nil {
						m.ConnectionsActive.With("", poolInfo.Pool, backend.Address(), "http").Dec()
						m.DialFailures.With("", poolInfo.Pool, backend.Address()).Add(1)
					}
				}
			}
			budget.RecordRetry()
			continue
		}

		if retry.IsRetryable(resp.StatusCode, nil, rcfg.RetryableStatusCodes) {
			resp.Body.Close()
			lastErr = errRetryableStatus(resp.StatusCode)
			dialStart = time.Now()
			if backend != nil {
				if outcome != nil {
					outcome.RecordFailure(backend)
				}
				if m := GetL7Metrics(req.Context()); m != nil {
					poolInfo := GetL7PoolInfo(req.Context())
					if poolInfo != nil {
						m.ConnectionsActive.With("", poolInfo.Pool, backend.Address(), "http").Dec()
					}
				}
			}
			budget.RecordRetry()
			continue
		}

		if backend != nil && outcome != nil {
			outcome.RecordSuccess(backend)
		}

		// Record backend latency for retry success.
		if m := GetL7Metrics(req.Context()); m != nil && m.BackendLatency != nil && backend != nil {
			poolInfo := GetL7PoolInfo(req.Context())
			if poolInfo != nil {
				m.BackendLatency.With(poolInfo.Pool, backend.Address()).Observe(time.Since(dialStart).Seconds())
			}
		}

		if backend != nil {
			lb2 := GetLB(req.Context())
			if cr, ok := lb2.(balancer.ConnReleaser); ok {
				resp.Body = &releaseBody{
					ReadCloser: resp.Body,
					once:       sync.Once{},
					release: func() {
						cr.Release(backend)
						if m := GetL7Metrics(req.Context()); m != nil {
							poolInfo := GetL7PoolInfo(req.Context())
							if poolInfo != nil {
								m.ConnectionsActive.With("", poolInfo.Pool, backend.Address(), "http").Dec()
								start := GetRequestStart(req.Context())
								if !start.IsZero() {
									m.RequestDuration.With("", poolInfo.Pool, backend.Address()).Observe(time.Since(start).Seconds())
								}
							}
						}
					},
				}
			} else {
				if m := GetL7Metrics(req.Context()); m != nil {
					poolInfo := GetL7PoolInfo(req.Context())
					if poolInfo != nil {
						resp.Body = &metricsBody{
							ReadCloser: resp.Body,
							once:       sync.Once{},
							close: func() {
								m.ConnectionsActive.With("", poolInfo.Pool, backend.Address(), "http").Dec()
								start := GetRequestStart(req.Context())
								if !start.IsZero() {
									m.RequestDuration.With("", poolInfo.Pool, backend.Address()).Observe(time.Since(start).Seconds())
								}
							},
						}
					}
				}
			}
		}

		return resp, nil
	}

	return nil, lastErr
}

// errRetryableStatus is an error for a retryable HTTP status code.
type errRetryableStatus int

func (e errRetryableStatus) Error() string {
	return "retryable status"
}

// retryBackend is a minimal backend ref for retry attempts.
type retryBackend struct {
	addr string
}

func (b *retryBackend) Address() string     { return b.addr }
func (b *retryBackend) Weight() int         { return 1 }
func (b *retryBackend) IsHealthy() bool     { return true }
func (b *retryBackend) IsDraining() bool    { return false }
func (b *retryBackend) IsCircuitOpen() bool { return false }

// releaseBody wraps an io.ReadCloser so that Close() calls the
// release function exactly once, via sync.Once. This ensures the
// backend counts as "in flight" for the full duration the client is
// reading the response body, not just until headers arrive.
type releaseBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

// Close closes the underlying body and calls the release function
// exactly once.
func (rb *releaseBody) Close() error {
	var err error
	rb.once.Do(func() {
		err = rb.ReadCloser.Close()
		rb.release()
	})
	return err
}

// metricsBody wraps an io.ReadCloser to call a close function when
// the body is closed, used for metrics when ConnReleaser is not
// available.
type metricsBody struct {
	io.ReadCloser
	once  sync.Once
	close func()
}

// Close closes the underlying body and calls the close function
// exactly once.
func (mb *metricsBody) Close() error {
	var err error
	mb.once.Do(func() {
		err = mb.ReadCloser.Close()
		mb.close()
	})
	return err
}
