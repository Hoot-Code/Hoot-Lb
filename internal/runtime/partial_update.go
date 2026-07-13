package runtime

import (
	"log/slog"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/circuitbreaker"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/discovery"
	"github.com/Hoot-Code/Hoot-Lb/internal/health"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
)

// UpdatePoolBackends builds a new Snapshot that replaces only the
// specified pool's backend set, load balancer, and health checker.
// All other pools are copied by reference — their LoadBalancer,
// Servers, HealthChecker, and FailureReporter pointers are unchanged.
// The function returns a new Snapshot ready to be swapped in by the
// caller; it does NOT swap the snapshot itself.
//
// The caller must:
//  1. Start the new pool's health checker
//  2. Atomically swap the snapshot pointer
//  3. Stop the old pool's health checker
//
// This ordering ensures no monitoring gap — new backends are
// watched before old watchers are torn down.
func UpdatePoolBackends(
	current *Snapshot,
	poolName string,
	newBackends []discovery.Backend,
	cfg config.PoolConfig,
	logger *slog.Logger,
) (*Snapshot, error) {
	servers := make([]*balancer.Server, len(newBackends))
	balancerBackends := make([]balancer.Backend, len(newBackends))
	for i, b := range newBackends {
		servers[i] = balancer.NewServer(b.Address, b.Weight)
		balancerBackends[i] = servers[i]
	}

	lb := newBalancer(cfg.Algorithm, balancerBackends)

	var fr health.FailureReporter
	hc := health.NewChecker(cfg, servers, logger, nil, nil)
	if hc != nil {
		fr = hc
	}

	var breakers map[string]CircuitBreaker
	if cfg.CircuitBreaker != nil {
		breakers = make(map[string]CircuitBreaker, len(servers))
		for _, srv := range servers {
			breakers[srv.Address()] = circuitbreaker.NewBreaker(
				cfg.CircuitBreaker.FailureThreshold,
				cfg.CircuitBreaker.OpenDuration,
				cfg.CircuitBreaker.HalfOpenMaxProbes,
				srv.CircuitOpenAtomic(),
			)
		}
	}

	newPoolStates := make(map[string]*PoolState, len(current.PoolStates))
	for name, ps := range current.PoolStates {
		newPoolStates[name] = ps
	}
	newPoolStates[poolName] = &PoolState{
		LB:       lb,
		Servers:  servers,
		FR:       fr,
		Sticky:   cfg.Sticky,
		Breakers: breakers,
	}

	newCheckers := make(map[string]health.HealthChecker, len(current.Checkers))
	for name, c := range current.Checkers {
		newCheckers[name] = c
	}
	if hc != nil {
		newCheckers[poolName] = hc
	}

	logger.Info("pool backends updated",
		slog.String(logging.ComponentKey, "discovery"),
		slog.String(logging.PoolKey, poolName),
		slog.Int("backends", len(newBackends)))

	return &Snapshot{
		PoolStates: newPoolStates,
		CertStores: current.CertStores,
		Listeners:  current.Listeners,
		Checkers:   newCheckers,
	}, nil
}
