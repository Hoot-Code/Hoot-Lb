// Package health defines the contract for backend health checking.
//
// This file contains only the HealthChecker interface and the
// FailureReporter interface. Concrete checkers (TCP connect probes,
// HTTP probes, etc.) live in checker.go. Defining the interface here
// lets the balancer and proxy engines be written against a stable
// contract.
package health

import (
	"context"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
)

// HealthChecker periodically probes a set of backends to determine
// whether each one is able to serve traffic. The result of those probes
// is reflected back through Backend.IsHealthy (see the balancer
// package).
//
// Implementations should run their probing loop in the background
// after Start is called and stop cleanly when either the context passed
// to Start is cancelled, or Stop is called explicitly -- whichever
// happens first.
type HealthChecker interface {
	// Start begins the health checking loop in the background. It must
	// return promptly; the actual probing happens asynchronously and
	// continues until ctx is cancelled or Stop is called.
	Start(ctx context.Context)

	// Stop halts the health checking loop and releases any resources
	// (timers, connections, goroutines) it holds. Stop must be safe to
	// call even if Start was never called, and safe to call more than
	// once.
	Stop()
}

// FailureReporter is an optional interface that a HealthChecker can
// implement to receive immediate notification of real client dial
// failures from the proxy layer. Unlike active-check failures, a
// single real dial failure marks the backend unhealthy immediately
// without applying hysteresis thresholds — this is a confirmed failure
// on real traffic, not a noisy synthetic probe.
//
// The proxy layer (internal/proxy/l4) type-asserts the health checker
// against FailureReporter after a dial failure. If the assertion
// succeeds and the pool has health checking enabled (type != "none"),
// ReportFailure is called with the failed backend. If the pool has
// health checking disabled, the assertion is never attempted.
type FailureReporter interface {
	// ReportFailure notifies the health checker that a real client
	// dial to the given backend has failed. The backend should be
	// marked unhealthy immediately without waiting for the next active
	// probe cycle.
	ReportFailure(b balancer.Backend)
}
