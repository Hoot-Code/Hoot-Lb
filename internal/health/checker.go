// Package health implements backend health checking for the load
// balancer. It provides active health checking via TCP and HTTP probes,
// and passive health checking via a FailureReporter interface that the
// proxy layer can use to report real dial failures.
//
// Active checking runs one goroutine per backend, using a ticker-driven
// probe loop with configurable intervals, timeouts, and hysteresis
// thresholds. Passive checking allows the proxy layer to immediately
// mark a backend unhealthy when a real client dial fails, without
// waiting for the next active probe cycle.
package health

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
	"github.com/Hoot-Code/Hoot-Lb/internal/metrics"
)

// BackendHealth tracks the health state of a single backend, including
// consecutive success/failure counters for hysteresis. It is safe for
// concurrent use.
type BackendHealth struct {
	server             *balancer.Server
	consecutiveSuccess int
	consecutiveFailure int
	healthy            bool
	mu                 sync.Mutex
}

// NewChecker creates a HealthChecker for the given pool configuration
// and servers. The servers slice must contain the same *Server pointers
// that were passed to the LoadBalancer so that health updates reach
// Pick(). If hc.Type is "none", NewChecker returns nil (no health
// checking needed). healthGauge may be nil to disable health metrics.
// healthCheckDuration may be nil to disable health check duration metrics.
func NewChecker(poolCfg config.PoolConfig, servers []*balancer.Server, logger *slog.Logger, healthGauge *metrics.GaugeVec, healthCheckDuration *metrics.HistogramVec) *Checker {
	hc := poolCfg.HealthCheck
	if hc == nil || hc.Type == "none" {
		return nil
	}

	backends := make([]*BackendHealth, len(servers))
	for i, s := range servers {
		backends[i] = &BackendHealth{
			server:  s,
			healthy: true,
		}
	}

	return &Checker{
		poolName:            poolCfg.Name,
		cfg:                 hc,
		backends:            backends,
		logger:              logger.With(logging.ComponentKey, "healthcheck", logging.PoolKey, poolCfg.Name),
		healthGauge:         healthGauge,
		healthCheckDuration: healthCheckDuration,
	}
}

// Checker implements HealthChecker for a single backend pool. It runs
// one goroutine per backend, each probing on the configured interval
// with staggered initial delays.
type Checker struct {
	poolName            string
	cfg                 *config.HealthCheckConfig
	backends            []*BackendHealth
	logger              *slog.Logger
	healthGauge         *metrics.GaugeVec
	healthCheckDuration *metrics.HistogramVec

	cancel context.CancelFunc
	wg     sync.WaitGroup
	done   chan struct{}
}

// Start begins the health checking loop in the background. It returns
// promptly; the actual probing happens asynchronously and continues
// until ctx is cancelled or Stop is called.
func (c *Checker) Start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)
	c.done = make(chan struct{})

	for i, bh := range c.backends {
		c.wg.Add(1)
		go c.probe(ctx, bh, i)
	}

	go func() {
		c.wg.Wait()
		close(c.done)
	}()
}

// Stop halts the health checking loop and releases all resources. It
// is safe to call even if Start was never called, and safe to call
// more than once.
func (c *Checker) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	if c.done != nil {
		<-c.done
	}
}

// ReportFailure marks a backend as unhealthy immediately, without
// waiting for the next active probe. This is called by the proxy layer
// when a real client dial fails. Only backends currently marked healthy
// are affected.
func (c *Checker) ReportFailure(b balancer.Backend) {
	for _, bh := range c.backends {
		if bh.server.Address() == b.Address() {
			bh.mu.Lock()
			if bh.server.IsHealthy() {
				bh.server.SetHealthy(false)
				bh.consecutiveSuccess = 0
				if c.healthGauge != nil {
					c.healthGauge.With(c.poolName, b.Address()).Set(0)
				}
				c.logger.Warn("backend marked unhealthy via passive failure",
					slog.String(logging.BackendKey, b.Address()))
			}
			bh.mu.Unlock()
			return
		}
	}
}

// probe runs the health check loop for a single backend. It uses a
// ticker-driven loop with staggered initial delays to avoid synchronized
// probe bursts across backends.
func (c *Checker) probe(ctx context.Context, bh *BackendHealth, index int) {
	defer c.wg.Done()

	interval := c.cfg.Interval

	// Stagger the initial tick based on index to avoid a synchronized
	// burst of probes when the pool is large. Each backend starts at
	// a fraction of the interval proportional to its index.
	delay := time.Duration(float64(interval) * float64(index) / float64(len(c.backends)))
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}

	// Run the first check immediately.
	c.runCheck(ctx, bh)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.runCheck(ctx, bh)
		}
	}
}

// runCheck executes a single health probe against the backend and
// applies the hysteresis state machine.
func (c *Checker) runCheck(ctx context.Context, bh *BackendHealth) {
	start := time.Now()
	var success bool
	switch c.cfg.Type {
	case "tcp":
		success = c.checkTCP(ctx, bh.server.Address())
	case "http":
		success = c.checkHTTP(ctx, bh.server.Address(), c.cfg.Path)
	default:
		return
	}

	// Record health check duration.
	if c.healthCheckDuration != nil {
		c.healthCheckDuration.With(c.poolName, bh.server.Address()).Observe(time.Since(start).Seconds())
	}

	bh.mu.Lock()
	if success {
		bh.consecutiveFailure = 0
		bh.consecutiveSuccess++

		if !bh.server.IsHealthy() && bh.consecutiveSuccess >= c.cfg.HealthyThreshold {
			bh.server.SetHealthy(true)
			bh.consecutiveSuccess = 0
			if c.healthGauge != nil {
				c.healthGauge.With(c.poolName, bh.server.Address()).Set(1)
			}
			c.logger.Info("backend marked healthy",
				slog.String(logging.BackendKey, bh.server.Address()))
		}
	} else {
		bh.consecutiveSuccess = 0
		bh.consecutiveFailure++

		if bh.server.IsHealthy() && bh.consecutiveFailure >= c.cfg.UnhealthyThreshold {
			bh.server.SetHealthy(false)
			bh.consecutiveFailure = 0
			if c.healthGauge != nil {
				c.healthGauge.With(c.poolName, bh.server.Address()).Set(0)
			}
			c.logger.Warn("backend marked unhealthy",
				slog.String(logging.BackendKey, bh.server.Address()))
		}
	}
	bh.mu.Unlock()
}

// checkTCP performs a TCP connect probe. Success is defined as the
// connection being established within the timeout. The connection is
// closed immediately after a successful dial — this is a liveness
// probe, not a data check.
func (c *Checker) checkTCP(ctx context.Context, addr string) bool {
	dialer := net.Dialer{Timeout: c.cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// checkHTTP performs an HTTP GET probe. Success is defined as receiving
// any 2xx status code within the timeout. A dedicated http.Client with
// a per-request timeout is used — never http.DefaultClient.
func (c *Checker) checkHTTP(ctx context.Context, addr, path string) bool {
	url := fmt.Sprintf("http://%s%s", addr, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}

	client := &http.Client{Timeout: c.cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}
