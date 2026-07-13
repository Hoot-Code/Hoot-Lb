package l4

import (
	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/health"
	"github.com/Hoot-Code/Hoot-Lb/internal/metrics"
	"github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

// ProxyMetrics holds the metric vectors shared by all L4 proxy servers.
// All fields may be nil to disable metrics.
type ProxyMetrics struct {
	ConnectionsTotal  *metrics.CounterVec
	ConnectionsActive *metrics.GaugeVec
	BytesTransferred  *metrics.CounterVec
	DialFailures      *metrics.CounterVec
}

// PoolStateGetter returns the current LoadBalancer, FailureReporter,
// and per-backend circuit breaker Outcome for a pool. It is called
// per-connection to ensure hot-reloaded pool state is applied
// immediately. The Outcome may be nil if circuit breaking is disabled.
type PoolStateGetter func() (balancer.LoadBalancer, health.FailureReporter, runtime.Outcome)

// SNIRouteGetter resolves an SNI hostname to a LoadBalancer,
// FailureReporter, and Outcome for TLS passthrough routing. It is
// called per-connection to support hot-reloaded pool assignments.
// The Outcome may be nil if circuit breaking is disabled.
type SNIRouteGetter func(sni string) (balancer.LoadBalancer, health.FailureReporter, runtime.Outcome)

// StaticPoolGetter creates a PoolStateGetter that always returns the
// same LB, FR, and Outcome. Useful for tests and non-reload scenarios.
func StaticPoolGetter(lb balancer.LoadBalancer, fr health.FailureReporter, outcome runtime.Outcome) PoolStateGetter {
	return func() (balancer.LoadBalancer, health.FailureReporter, runtime.Outcome) {
		return lb, fr, outcome
	}
}

// StaticSNIRouter creates an SNIRouteGetter from a pool map and
// failure reporters. For testing purposes.
func StaticSNIRouter(poolMap map[string]balancer.LoadBalancer, failReporters map[string]health.FailureReporter, outcomes map[string]runtime.Outcome, defaultPool string) SNIRouteGetter {
	return func(sni string) (balancer.LoadBalancer, health.FailureReporter, runtime.Outcome) {
		_ = sni
		lb := poolMap[defaultPool]
		fr := failReporters[defaultPool]
		o := outcomes[defaultPool]
		return lb, fr, o
	}
}
