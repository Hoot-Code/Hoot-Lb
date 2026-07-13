// Package circuitbreaker implements a per-backend circuit breaker for
// the load balancer. It tracks consecutive failures and transitions
// between closed, open, and half-open states. In the closed state,
// normal traffic flows. After failure_threshold consecutive failures,
// the circuit opens and the backend is skipped. After open_duration
// elapses, the circuit enters half-open and allows a limited number of
// probe requests through. A successful probe closes the circuit; a
// failed probe reopens it.
package circuitbreaker

import (
	"sync"
	"sync/atomic"
	"time"
)

// State represents the circuit breaker's current state.
type State int

const (
	// StateClosed is the normal operating state. Traffic flows freely.
	StateClosed State = iota
	// StateOpen rejects all traffic. The backend is skipped.
	StateOpen
	// StateHalfOpen allows a limited number of probe requests.
	StateHalfOpen
)

// Backend is the minimal interface a backend must satisfy for the
// circuit breaker to operate. The load balancer's Server type
// satisfies this via SetCircuitOpen.
type Backend interface {
	SetCircuitOpen(open bool)
}

// Breaker tracks failure counts and circuit state for a single backend.
// It is safe for concurrent use.
type Breaker struct {
	mu                sync.Mutex
	state             State
	consecutiveFail   int
	failureThreshold  int
	openDuration      time.Duration
	halfOpenMaxProbes int
	openedAt          time.Time
	halfOpenProbes    int

	circuitOpen *atomic.Bool
}

// NewBreaker creates a Breaker with the given configuration. The
// circuitOpen atomic is the caller's atomic.Bool that is set/cleared
// via SetCircuitOpen to control whether the balancer skips this
// backend.
func NewBreaker(failureThreshold int, openDuration time.Duration, halfOpenMaxProbes int, circuitOpen *atomic.Bool) *Breaker {
	return &Breaker{
		state:             StateClosed,
		failureThreshold:  failureThreshold,
		openDuration:      openDuration,
		halfOpenMaxProbes: halfOpenMaxProbes,
		circuitOpen:       circuitOpen,
	}
}

// RecordSuccess records a successful request to the backend. In
// half-open state, a successful probe closes the circuit and resets
// the failure count. In closed state, the failure counter is reset.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateHalfOpen:
		b.state = StateClosed
		b.consecutiveFail = 0
		b.halfOpenProbes = 0
		b.circuitOpen.Store(false)
	case StateClosed:
		b.consecutiveFail = 0
	}
}

// RecordFailure records a failed request to the backend. In closed
// state, consecutive failures are counted; reaching the threshold
// opens the circuit. In half-open state, a failed probe reopens the
// circuit.
func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		b.consecutiveFail++
		if b.consecutiveFail >= b.failureThreshold {
			b.openCircuit()
		}
	case StateHalfOpen:
		b.openCircuit()
	}
}

// Allow reports whether a request should be let through. In closed
// state, all requests are allowed. In open state, no requests are
// allowed (after checking if open_duration has elapsed to transition
// to half-open). In half-open state, up to half_open_max_probes
// concurrent probes are allowed; additional requests are rejected.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		return true
	case StateOpen:
		if time.Since(b.openedAt) >= b.openDuration {
			b.state = StateHalfOpen
			b.halfOpenProbes = 1
			b.circuitOpen.Store(false)
			return true
		}
		return false
	case StateHalfOpen:
		if b.halfOpenProbes < b.halfOpenMaxProbes {
			b.halfOpenProbes++
			return true
		}
		return false
	}
	return false
}

// openCircuit transitions to the open state. Must be called with mu held.
func (b *Breaker) openCircuit() {
	b.state = StateOpen
	b.consecutiveFail = 0
	b.halfOpenProbes = 0
	b.openedAt = time.Now()
	b.circuitOpen.Store(true)
}

// State returns the current state of the circuit breaker.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == StateOpen && time.Since(b.openedAt) >= b.openDuration {
		b.state = StateHalfOpen
		b.halfOpenProbes = 0
		b.circuitOpen.Store(false)
		return b.state
	}
	return b.state
}
