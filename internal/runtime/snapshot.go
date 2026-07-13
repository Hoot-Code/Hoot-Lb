// Package runtime manages the atomic runtime state of the load
// balancer. It provides a Snapshot type that holds all swappable state
// (per-pool balancers, health checkers, route tables, TLS stores) and
// can be swapped atomically via sync/atomic.Pointer. This is the core
// mechanism that enables zero-downtime hot reload of pools, routes,
// and certificates without restarting listeners.
package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/circuitbreaker"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/health"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
	"github.com/Hoot-Code/Hoot-Lb/internal/tlsutil"
)

// PoolState holds the resolved runtime state for a single backend
// pool: the load balancer, the concrete server slice for health
// checking, the failure reporter, optional sticky configuration,
// and per-backend circuit breakers.
type PoolState struct {
	// LB is the load balancer for this pool.
	LB balancer.LoadBalancer
	// Servers holds the concrete balancer.Server instances, used by
	// health checkers to update health status.
	Servers []*balancer.Server
	// FR is the failure reporter for passive health checking. Nil
	// when health checking is disabled for this pool.
	FR health.FailureReporter
	// Sticky holds the sticky session config, if any.
	Sticky *config.StickyConfig
	// Breakers maps backend address to its circuit breaker. Nil when
	// circuit breaking is disabled for this pool.
	Breakers map[string]CircuitBreaker
	// Retry holds the retry config for this pool, if any.
	Retry *config.RetryConfig
}

// CircuitBreaker is the interface that circuit breakers in the
// runtime must satisfy. It wraps the Outcome interface for use
// within the runtime package.
type CircuitBreaker interface {
	// Allow reports whether a request should be let through.
	Allow() bool
	// RecordSuccess records a successful request to the backend.
	RecordSuccess()
	// RecordFailure records a failed request to the backend.
	RecordFailure()
}

// Outcome defines the interface for recording success/failure outcomes
// against a backend's circuit breaker. The proxy layers call these
// methods to signal whether requests to a specific backend succeeded
// or failed.
type Outcome interface {
	RecordSuccess(balancer.Backend)
	RecordFailure(balancer.Backend)
}

// BackendOutcome routes RecordSuccess/RecordFailure calls to the
// correct backend's circuit breaker based on the backend address.
type BackendOutcome struct {
	breakers map[string]CircuitBreaker
}

// NewBackendOutcome creates a BackendOutcome from a map of backend
// addresses to circuit breakers.
func NewBackendOutcome(breakers map[string]CircuitBreaker) *BackendOutcome {
	return &BackendOutcome{breakers: breakers}
}

// RecordSuccess records a successful request to the given backend.
// The backend parameter is a balancer.Backend whose Address is used
// to look up the circuit breaker.
func (bo *BackendOutcome) RecordSuccess(b balancer.Backend) {
	if bo == nil || bo.breakers == nil {
		return
	}
	if cb, ok := bo.breakers[b.Address()]; ok {
		cb.RecordSuccess()
	}
}

// RecordFailure records a failed request to the given backend.
func (bo *BackendOutcome) RecordFailure(b balancer.Backend) {
	if bo == nil || bo.breakers == nil {
		return
	}
	if cb, ok := bo.breakers[b.Address()]; ok {
		cb.RecordFailure()
	}
}

// Snapshot holds the complete set of runtime state that can be swapped
// atomically. Every field is immutable after construction; hot reload
// creates an entirely new Snapshot and swaps it in. The old Snapshot
// (and any in-flight requests still reading from it) remains valid
// until it is garbage collected.
type Snapshot struct {
	// PoolStates maps pool name to its resolved runtime state.
	PoolStates map[string]*PoolState
	// CertStores maps TLS-terminate listener name to its atomic
	// certificate store. CertStore.Replace is called on reload to
	// swap certificates without recreating the store.
	CertStores map[string]*tlsutil.CertStore
	// Listeners records the listener configuration fingerprint (name,
	// address, protocol, TLS mode) for diff detection. Any listener
	// change causes the entire reload to be rejected.
	Listeners []ListenerFingerprint
	// Checkers maps pool name to its running health checker. New
	// checkers are started before old ones are stopped during
	// reload, ensuring no monitoring gap.
	Checkers map[string]health.HealthChecker
}

// ListenerFingerprint captures the immutable identity of a listener
// for change detection. If any field differs between old and new
// config, the reload is rejected because listener-level changes
// require a process restart.
type ListenerFingerprint struct {
	Name     string
	Address  string
	Protocol string
	TLSMode  string
}

// AtomicSnapshot is a thread-safe holder for the current Snapshot.
// Proxy servers read from it on every new connection or request,
// ensuring hot-reloaded state is applied immediately.
type AtomicSnapshot struct {
	ptr atomic.Pointer[Snapshot]
}

// NewAtomicSnapshot creates an AtomicSnapshot with the given initial
// snapshot.
func NewAtomicSnapshot(snap *Snapshot) *AtomicSnapshot {
	a := &AtomicSnapshot{}
	a.ptr.Store(snap)
	return a
}

// Load returns the current Snapshot. The returned pointer is safe to
// use concurrently; it will never be nil after construction.
func (a *AtomicSnapshot) Load() *Snapshot {
	return a.ptr.Load()
}

// Swap atomically replaces the current Snapshot and returns the
// previous one.
func (a *AtomicSnapshot) Swap(new *Snapshot) *Snapshot {
	return a.ptr.Swap(new)
}

// BuildSnapshot constructs a new Snapshot from the given config. It
// creates fresh LoadBalancers, Server slices, health checkers
// (started but not yet swapped in), CertStores, and per-listener
// route tables. The caller is responsible for starting the returned
// health checkers, swapping the snapshot, and stopping old checkers.
//
// BuildSnapshot is a pure function with no side effects on existing
// state — both startup and reload call this exact same function.
func BuildSnapshot(cfg *config.Config, logger *slog.Logger) (*Snapshot, error) {
	poolStates := make(map[string]*PoolState, len(cfg.Pools))
	checkers := make(map[string]health.HealthChecker)

	for _, p := range cfg.Pools {
		var backends []balancer.Backend
		var servers []*balancer.Server

		if p.Discovery != nil {
			disc, err := newDiscoveryAdapter(p, logger)
			if err != nil {
				return nil, fmt.Errorf("pool %q: %w", p.Name, err)
			}
			resolved, err := disc.Resolve(context.Background())
			if err != nil {
				logger.Warn("initial discovery resolution failed, starting with empty pool",
					slog.String(logging.ComponentKey, "runtime"),
					slog.String(logging.PoolKey, p.Name),
					slog.String("error", err.Error()))
				resolved = nil
			}
			servers = make([]*balancer.Server, len(resolved))
			backends = make([]balancer.Backend, len(resolved))
			for i, b := range resolved {
				servers[i] = balancer.NewServer(b.Address, b.Weight)
				backends[i] = servers[i]
			}
		} else {
			servers = make([]*balancer.Server, len(p.Backends))
			backends = make([]balancer.Backend, len(p.Backends))
			for i, b := range p.Backends {
				servers[i] = balancer.NewServer(b.Address, b.Weight)
				backends[i] = servers[i]
			}
		}

		lb := newBalancer(p.Algorithm, backends)

		var fr health.FailureReporter
		hc := health.NewChecker(p, servers, logger, nil, nil)
		if hc != nil {
			fr = hc
			checkers[p.Name] = hc
		}

		var breakers map[string]CircuitBreaker
		if p.CircuitBreaker != nil {
			breakers = make(map[string]CircuitBreaker, len(servers))
			for _, srv := range servers {
				breakers[srv.Address()] = circuitbreaker.NewBreaker(
					p.CircuitBreaker.FailureThreshold,
					p.CircuitBreaker.OpenDuration,
					p.CircuitBreaker.HalfOpenMaxProbes,
					srv.CircuitOpenAtomic(),
				)
			}
		}

		poolStates[p.Name] = &PoolState{
			LB:       lb,
			Servers:  servers,
			FR:       fr,
			Sticky:   p.Sticky,
			Breakers: breakers,
			Retry:    p.Retry,
		}
	}

	// Build per-listener cert stores (TLS-terminate only).
	certStores := make(map[string]*tlsutil.CertStore)
	for _, l := range cfg.Listeners {
		if l.TLS != nil && l.TLS.Mode == "terminate" {
			cs, err := tlsutil.NewCertStore(l.TLS.Certificates)
			if err != nil {
				return nil, err
			}
			certStores[l.Name] = cs
		}
	}

	// Build listener fingerprints for diff detection.
	fingerprints := make([]ListenerFingerprint, len(cfg.Listeners))
	for i, l := range cfg.Listeners {
		var tlsMode string
		if l.TLS != nil {
			tlsMode = l.TLS.Mode
		}
		fingerprints[i] = ListenerFingerprint{
			Name:     l.Name,
			Address:  l.Address,
			Protocol: l.Protocol,
			TLSMode:  tlsMode,
		}
	}

	return &Snapshot{
		PoolStates: poolStates,
		CertStores: certStores,
		Listeners:  fingerprints,
		Checkers:   checkers,
	}, nil
}

// DiffListeners compares two sets of listener fingerprints and returns
// an error if any listener has changed (added, removed, or modified).
// Listener-level changes require a process restart.
func DiffListeners(old []ListenerFingerprint, new []ListenerFingerprint) error {
	if len(old) != len(new) {
		return &ListenerChangeError{
			Detail: fmt.Sprintf("listener count changed from %d to %d", len(old), len(new)),
		}
	}

	oldByName := make(map[string]ListenerFingerprint, len(old))
	for _, lf := range old {
		oldByName[lf.Name] = lf
	}

	seen := make(map[string]bool, len(new))
	for _, nf := range new {
		of, ok := oldByName[nf.Name]
		if !ok {
			return &ListenerChangeError{
				Detail: "new listener added: " + nf.Name,
			}
		}
		seen[nf.Name] = true
		if of.Address != nf.Address {
			return &ListenerChangeError{
				Detail: "listener " + nf.Name + " address changed from " + of.Address + " to " + nf.Address,
			}
		}
		if of.Protocol != nf.Protocol {
			return &ListenerChangeError{
				Detail: "listener " + nf.Name + " protocol changed from " + of.Protocol + " to " + nf.Protocol,
			}
		}
		if of.TLSMode != nf.TLSMode {
			return &ListenerChangeError{
				Detail: "listener " + nf.Name + " TLS mode changed from " + of.TLSMode + " to " + nf.TLSMode,
			}
		}
	}

	for _, of := range old {
		if !seen[of.Name] {
			return &ListenerChangeError{
				Detail: "listener removed: " + of.Name,
			}
		}
	}

	return nil
}

// ListenerChangeError is returned when a reload attempt changes
// listener-level configuration, which requires a process restart.
type ListenerChangeError struct {
	Detail string
}

func (e *ListenerChangeError) Error() string {
	return "listener-level change detected (requires restart): " + e.Detail
}

// newBalancer creates a new LoadBalancer for the given algorithm and
// backend list.
func newBalancer(algorithm string, backends []balancer.Backend) balancer.LoadBalancer {
	switch algorithm {
	case "round_robin":
		return balancer.NewRoundRobin(backends)
	case "weighted_round_robin":
		return balancer.NewWeightedRoundRobin(backends)
	case "least_conn":
		return balancer.NewLeastConnections(backends)
	case "random":
		return balancer.NewRandom(backends)
	case "ip_hash":
		return balancer.NewIPHash(backends)
	default:
		return balancer.NewWeightedRoundRobin(backends)
	}
}

// ExtractPoolBackends returns a map of pool name to backend addresses,
// implementing the metrics.PoolBackendExtractor interface for
// cardinality cleanup.
func (s *Snapshot) ExtractPoolBackends() map[string][]string {
	result := make(map[string][]string, len(s.PoolStates))
	for poolName, ps := range s.PoolStates {
		addrs := make([]string, len(ps.Servers))
		for i, srv := range ps.Servers {
			addrs[i] = srv.Address()
		}
		result[poolName] = addrs
	}
	return result
}
