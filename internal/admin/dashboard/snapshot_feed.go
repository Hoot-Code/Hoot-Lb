// Package dashboard implements Hoot-Lb's embedded web dashboard: a
// small static HTML/CSS/JS page (served via go:embed) plus a
// hand-rolled RFC 6455 WebSocket endpoint that streams periodic JSON
// snapshots of pool/backend state to connected browsers.
//
// This package adds no listener of its own — internal/admin mounts
// AssetHandler and WebSocketHandler onto its existing HTTP server, so
// the dashboard is gated by the same admin.enabled config as the REST
// API and shares its lifecycle exactly. The WebSocket implementation
// is kept isolated here, separate from the REST handlers in
// internal/admin, keeping concerns cleanly separated.
package dashboard

import (
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/metrics"
	"github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

// Snapshot is the JSON payload pushed to dashboard viewers over the
// WebSocket connection. It mirrors the admin REST API's pool/backend
// view (see internal/admin.ListPoolsResponse) but adds a live
// connection count sourced from the metrics registry, since that's
// the one piece of state the REST API doesn't already expose per
// backend.
type Snapshot struct {
	// GeneratedAt is the time this snapshot was built, for display
	// and staleness detection in the browser.
	GeneratedAt time.Time `json:"generated_at"`
	// Pools holds one entry per configured pool, in config order.
	Pools []PoolSnapshot `json:"pools"`
}

// PoolSnapshot describes one backend pool's current state for the
// dashboard.
type PoolSnapshot struct {
	// Name is the pool's configured name.
	Name string `json:"name"`
	// Algorithm is the pool's configured load balancing algorithm.
	Algorithm string `json:"algorithm"`
	// Backends holds one entry per backend currently in the pool, in
	// the order returned by the runtime snapshot.
	Backends []BackendSnapshot `json:"backends"`
}

// BackendSnapshot describes one backend's current state for the
// dashboard, including state the REST API already exposes (address,
// weight, healthy, draining) plus a live connection count.
type BackendSnapshot struct {
	// Address is the backend's dial address.
	Address string `json:"address"`
	// Weight is the backend's relative weight.
	Weight int `json:"weight"`
	// Healthy reports whether the backend currently passes health
	// checks.
	Healthy bool `json:"healthy"`
	// Draining reports whether the backend has been administratively
	// excluded from new traffic selection.
	Draining bool `json:"draining"`
	// ActiveConnections is the current number of active connections
	// to this backend, summed across all listeners and protocols
	// that route to it. Zero when metrics are disabled.
	ActiveConnections int64 `json:"active_connections"`
}

// Feed produces Snapshot values on demand by reading the current
// runtime snapshot and, if available, the live connection-count
// gauge vector from the metrics registry. Build is called once per
// push tick per connected viewer, so it must stay cheap and
// lock-light — it reuses the exact same AtomicSnapshot.Load() read
// every other read path in this project already uses, plus a single
// pass over the connections-active GaugeVec using the same Each
// access pattern the metrics exposition writer already uses. Neither
// is a new contended path.
type Feed struct {
	snap       *runtime.AtomicSnapshot
	cfg        *config.Config
	connActive *metrics.GaugeVec // nil when metrics are disabled
}

// NewFeed creates a Feed backed by the given runtime snapshot holder,
// static pool configuration, and connection-count gauge vector.
// connActive may be nil — in that case Build reports zero active
// connections for every backend instead of erroring.
func NewFeed(snap *runtime.AtomicSnapshot, cfg *config.Config, connActive *metrics.GaugeVec) *Feed {
	return &Feed{snap: snap, cfg: cfg, connActive: connActive}
}

// Build constructs a fresh Snapshot from the current runtime state.
func (f *Feed) Build() Snapshot {
	rs := f.snap.Load()

	var connCounts map[string]int64
	if f.connActive != nil {
		connCounts = make(map[string]int64)
		f.connActive.Each(func(labels []string, value int64) {
			// Labels are [listener, pool, backend, protocol]. Sum
			// across listener and protocol, keyed by pool+backend,
			// since the dashboard shows one row per backend, not
			// one row per listener/protocol combination.
			if len(labels) < 3 {
				return
			}
			connCounts[labels[1]+"\x00"+labels[2]] += value
		})
	}

	pools := make([]PoolSnapshot, 0, len(f.cfg.Pools))
	for _, p := range f.cfg.Pools {
		ps := PoolSnapshot{Name: p.Name, Algorithm: p.Algorithm}

		if state, ok := rs.PoolStates[p.Name]; ok {
			ps.Backends = make([]BackendSnapshot, len(state.Servers))
			for i, srv := range state.Servers {
				var active int64
				if connCounts != nil {
					active = connCounts[p.Name+"\x00"+srv.Address()]
				}
				ps.Backends[i] = BackendSnapshot{
					Address:           srv.Address(),
					Weight:            srv.Weight(),
					Healthy:           srv.IsHealthy(),
					Draining:          srv.IsDraining(),
					ActiveConnections: active,
				}
			}
		}

		pools = append(pools, ps)
	}

	return Snapshot{GeneratedAt: time.Now(), Pools: pools}
}
