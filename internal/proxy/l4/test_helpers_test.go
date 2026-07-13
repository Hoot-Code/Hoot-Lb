package l4

import (
	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/health"
)

// testPoolGetter creates a PoolStateGetter for testing.
func testPoolGetter(lb balancer.LoadBalancer, fr health.FailureReporter) PoolStateGetter {
	return StaticPoolGetter(lb, fr, nil)
}

// testSNIRouter creates an SNIRouteGetter for testing.
func testSNIRouter(poolMap map[string]balancer.LoadBalancer, failReporters map[string]health.FailureReporter, defaultPool string) SNIRouteGetter {
	return StaticSNIRouter(poolMap, failReporters, nil, defaultPool)
}
