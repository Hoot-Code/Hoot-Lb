package l7

import (
	"context"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/health"
	"github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

// backendKey is the context key for the picked backend. The Director
// stores the selected Backend here so the Transport and ErrorHandler
// can retrieve it without re-running Pick.
type backendKey struct{}

// GetBackend returns the Backend stored in the request context by the
// Director. Returns nil if no backend was stored (e.g. Pick failed).
func GetBackend(ctx context.Context) balancer.Backend {
	b, _ := ctx.Value(backendKey{}).(balancer.Backend)
	return b
}

// lbKey is the context key for the pool's LoadBalancer. The Director
// stores this so the Transport can type-assert ConnReleaser.
type lbKey struct{}

// GetLB returns the LoadBalancer stored in the request context by the
// Director.
func GetLB(ctx context.Context) balancer.LoadBalancer {
	lb, _ := ctx.Value(lbKey{}).(balancer.LoadBalancer)
	return lb
}

// frKey is the context key for the pool's FailureReporter. The
// Director stores this so the Transport can report dial failures.
type frKey struct{}

// GetFR returns the FailureReporter stored in the request context by
// the Director. Returns nil if health checking is disabled for the
// matched pool.
func GetFR(ctx context.Context) health.FailureReporter {
	fr, _ := ctx.Value(frKey{}).(health.FailureReporter)
	return fr
}

// outcomeKey is the context key for the pool's circuit breaker Outcome.
type outcomeKey struct{}

// GetOutcome returns the circuit breaker Outcome stored in the request
// context by the Director. Returns nil if circuit breaking is disabled.
func GetOutcome(ctx context.Context) runtime.Outcome {
	o, _ := ctx.Value(outcomeKey{}).(runtime.Outcome)
	return o
}

// errKey is the context key for errors from LoadBalancer.Pick. The
// Director stores the error here when Pick fails, and the Transport
// and ErrorHandler retrieve it to avoid attempting a round-trip with
// a bogus URL.
type errKey struct{}

// GetError returns the error stored in the request context by the
// Director when Pick fails. Returns nil if no error was stored.
func GetError(ctx context.Context) error {
	e, _ := ctx.Value(errKey{}).(error)
	return e
}

// SplitEntry is a resolved entry in a weighted traffic split. Each
// entry maps a weight to a LoadBalancer, FailureReporter, and Outcome
// for a specific pool.
type SplitEntry struct {
	LB          balancer.LoadBalancer
	FR          health.FailureReporter
	Outcome     runtime.Outcome
	Weight      int
	Sticky      *config.StickyConfig
	Retry       *RetryConfig
	PoolMembers PoolMembershipCheck
}

// Route is a single resolved routing rule. The pool name from config
// has been resolved to a LoadBalancer, FailureReporter, and Outcome
// at startup time, so no per-request map lookups are needed.
type Route struct {
	// Host restricts this route to requests with a matching Host
	// header. Empty matches any host.
	Host string
	// PathPrefix restricts this route to requests whose URL path
	// starts with this prefix. Empty matches any path.
	PathPrefix string
	// HeaderName restricts this route to requests with a header
	// whose name matches (case-insensitive). Empty means no header
	// condition.
	HeaderName string
	// HeaderValue is the exact value to match for HeaderName.
	HeaderValue string
	// LB is the LoadBalancer for the route's backend pool. Set for
	// non-split routes; nil for split routes.
	LB balancer.LoadBalancer
	// FR is the FailureReporter for the route's backend pool. May be
	// nil if health checking is disabled for this pool.
	FR health.FailureReporter
	// Outcome is the circuit breaker Outcome for the route's pool.
	// May be nil if circuit breaking is disabled.
	Outcome runtime.Outcome
	// Sticky is the pool's sticky session configuration. Nil when
	// sticky sessions are disabled.
	Sticky *config.StickyConfig
	// Retry is the pool's retry configuration. Nil when retries
	// are disabled.
	Retry *RetryConfig
	// PoolMembers is a live function that checks whether a given
	// address is currently a healthy member of this pool.
	PoolMembers PoolMembershipCheck
	// Split defines a weighted traffic split across multiple pools.
	// When non-nil, LB/FR/Outcome/Sticky/PoolMembers are ignored and
	// the split entries are used instead.
	Split []SplitEntry
}

// RouteTableGetter returns the current RouteTable for an HTTP listener.
// It is called per-request to support hot reload of routes.
type RouteTableGetter func() *RouteTable

// RouteTable holds the resolved routes for an HTTP listener. When a
// request arrives, routes are matched by longest path_prefix among
// those whose Host and Header conditions match (empty host matches
// any request host, absent header condition matches any request). If
// multiple routes have the same path_prefix length, the first in
// config order wins. If no route matches, the default pool is used.
type RouteTable struct {
	routes   []Route
	fallback Route
}

// NewRouteTable creates a RouteTable from the given routes and default
// pool. The default pool is used when no route matches.
func NewRouteTable(routes []Route, defaultLB balancer.LoadBalancer, defaultFR health.FailureReporter) *RouteTable {
	return &RouteTable{
		routes: routes,
		fallback: Route{
			LB: defaultLB,
			FR: defaultFR,
		},
	}
}

// NewRouteTableWithDefault creates a RouteTable with a fully specified
// default route that may include sticky configuration and split
// entries.
func NewRouteTableWithDefault(routes []Route, fallback Route) *RouteTable {
	return &RouteTable{
		routes:   routes,
		fallback: fallback,
	}
}

// Match returns the best matching route for the given request. If no
// route matches, the default route is returned. The returned pointer
// is always non-nil.
func (rt *RouteTable) Match(host, path string, headers http.Header) *Route {
	var best *Route
	bestLen := -1

	for i := range rt.routes {
		r := &rt.routes[i]
		if r.Host != "" && r.Host != host {
			continue
		}
		if r.PathPrefix != "" && !strings.HasPrefix(path, r.PathPrefix) {
			continue
		}
		if r.HeaderName != "" && headers.Get(r.HeaderName) != r.HeaderValue {
			continue
		}
		if len(r.PathPrefix) > bestLen {
			best = r
			bestLen = len(r.PathPrefix)
		}
	}

	if best != nil {
		return best
	}
	return &rt.fallback
}

// pickSplitEntry selects a SplitEntry from the given entries using
// weighted random selection. The weight is used as the probability
// proportion. Panics if entries is empty (callers must check first).
func pickSplitEntry(entries []SplitEntry) SplitEntry {
	var totalWeight int
	for _, e := range entries {
		totalWeight += e.Weight
	}

	target := rand.Intn(totalWeight)
	for _, e := range entries {
		target -= e.Weight
		if target < 0 {
			return e
		}
	}

	return entries[len(entries)-1]
}

// NewDirector returns a ReverseProxy.Director function that routes
// the request to the appropriate backend based on the route table.
// It handles three routing modes:
//
//  1. Normal routing: single pool, picks a backend via the pool's LB.
//  2. Split routing: weighted random selection among multiple pools.
//  3. Sticky sessions: if a valid sticky cookie is present, routes
//     directly to the previously selected backend.
//
// Sticky sessions take priority over the LB algorithm for that pool.
// On a fresh visit (no cookie), Pick runs normally and the result
// becomes the newly-stuck backend.
//
// ConnReleaser.Acquire and FailureReporter integration apply
// identically regardless of how the backend was chosen.
func NewDirector(table *RouteTable) func(*http.Request) {
	return newDirectorInternal(func() *RouteTable { return table }, nil, "")
}

// NewDirectorFromGetter returns a ReverseProxy.Director function that
// reads the route table from the getter on each request, supporting
// hot reload of routes.
func NewDirectorFromGetter(getter RouteTableGetter) func(*http.Request) {
	return newDirectorInternal(getter, nil, "")
}

// NewDirectorFromGetterWithMetrics returns a Director that also stores
// metrics data and pool info in the request context.
func NewDirectorFromGetterWithMetrics(getter RouteTableGetter, m *L7Metrics, poolName string) func(*http.Request) {
	return newDirectorInternal(getter, m, poolName)
}

func newDirectorInternal(getter RouteTableGetter, m *L7Metrics, poolName string) func(*http.Request) {
	return func(req *http.Request) {
		table := getter()
		route := table.Match(req.Host, req.URL.Path, req.Header)

		clientIP, _, err := net.SplitHostPort(req.RemoteAddr)
		if err != nil {
			clientIP = req.RemoteAddr
		}
		ctx := context.WithValue(req.Context(), balancer.ClientKey{}, clientIP)

		ctx = context.WithValue(ctx, requestStartKey{}, time.Now())

		var lb balancer.LoadBalancer
		var fr health.FailureReporter
		var outcome runtime.Outcome
		var sticky *config.StickyConfig
		var retryCfg *RetryConfig
		var poolMembers PoolMembershipCheck
		var effectivePool string

		if len(route.Split) > 0 {
			entry := pickSplitEntry(route.Split)
			lb = entry.LB
			fr = entry.FR
			outcome = entry.Outcome
			sticky = entry.Sticky
			retryCfg = entry.Retry
			poolMembers = entry.PoolMembers
			effectivePool = poolName
		} else {
			lb = route.LB
			fr = route.FR
			outcome = route.Outcome
			sticky = route.Sticky
			retryCfg = route.Retry
			poolMembers = route.PoolMembers
			effectivePool = poolName
		}

		var backend balancer.Backend
		var pickErr error
		usedSticky := false

		if sticky != nil {
			cookie, _ := req.Cookie(sticky.CookieName)
			if addr := validateStickyCookie(cookie, poolMembers); addr != "" {
				backend = &stickyBackendRef{address: addr}
				usedSticky = true
			}
		}

		if backend == nil {
			backend, pickErr = lb.Pick(ctx)
		}

		if pickErr != nil {
			ctx = context.WithValue(req.Context(), errKey{}, pickErr)
			req.URL.Scheme = "http"
			req.URL.Host = "localhost"
			*req = *req.WithContext(ctx)
			return
		}

		if cr, ok := lb.(balancer.ConnReleaser); ok {
			cr.Acquire(backend)
		}

		ctx = context.WithValue(ctx, backendKey{}, backend)
		ctx = context.WithValue(ctx, lbKey{}, lb)
		if fr != nil {
			ctx = context.WithValue(ctx, frKey{}, fr)
		}
		if outcome != nil {
			ctx = context.WithValue(ctx, outcomeKey{}, outcome)
		}

		if m != nil {
			ctx = context.WithValue(ctx, l7MetricsKey{}, m)
			ctx = context.WithValue(ctx, l7PoolInfoKey{}, &L7PoolInfo{Pool: effectivePool})
			m.ConnectionsTotal.With("", effectivePool, backend.Address(), "http").Add(1)
			m.ConnectionsActive.With("", effectivePool, backend.Address(), "http").Inc()
		}

		if retryCfg != nil {
			ctx = context.WithValue(ctx, retryConfigKey{}, retryCfg)
		}

		if sticky != nil && !usedSticky {
			ctx = context.WithValue(ctx, stickyInfoKey{}, &stickyInfo{
				CookieName:  sticky.CookieName,
				BackendAddr: backend.Address(),
				TTL:         int(sticky.TTL.Seconds()),
			})
		}

		*req = *req.WithContext(ctx)

		req.URL.Scheme = "http"
		req.URL.Host = backend.Address()

		InjectForwardedHeaders(req, clientIP)
		StripHopByHop(req.Header)
	}
}

// stickyBackendRef is a lightweight Backend that carries only the
// address of the sticky target. It satisfies the Backend interface so
// ConnReleaser/FailureReporter integration works identically to a
// normally-picked backend.
type stickyBackendRef struct {
	address string
}

func (s *stickyBackendRef) Address() string     { return s.address }
func (s *stickyBackendRef) Weight() int         { return 1 }
func (s *stickyBackendRef) IsHealthy() bool     { return true }
func (s *stickyBackendRef) IsDraining() bool    { return false }
func (s *stickyBackendRef) IsCircuitOpen() bool { return false }
