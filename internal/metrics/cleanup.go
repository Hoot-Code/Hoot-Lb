package metrics

// MetricVecs holds all metric vectors needed for cardinality cleanup
// during reload and discovery updates.
type MetricVecs struct {
	GaugeVecs     []*GaugeVec
	CounterVecs   []*CounterVec
	HistogramVecs []*HistogramVec
}

// PoolBackendExtractor extracts pool name to backend address mappings
// from a snapshot. This is an interface to avoid circular imports
// between metrics and runtime packages.
type PoolBackendExtractor interface {
	// ExtractPoolBackends returns a map of pool name to backend addresses.
	ExtractPoolBackends() map[string][]string
}

// CleanupRemovedBackends removes metric label entries for backends
// that are no longer in any pool. It accepts two extractors (old and
// new snapshots) and removes label entries for backends present in
// the old snapshot but absent in the new one.
func CleanupRemovedBackends(
	oldSnap, newSnap PoolBackendExtractor,
	mv *MetricVecs,
) {
	if mv == nil {
		return
	}

	oldPoolBackends := oldSnap.ExtractPoolBackends()
	newPoolBackends := newSnap.ExtractPoolBackends()

	for pool, oldBackends := range oldPoolBackends {
		newAddrs := newPoolBackends[pool]
		newSet := make(map[string]bool, len(newAddrs))
		for _, addr := range newAddrs {
			newSet[addr] = true
		}
		for _, addr := range oldBackends {
			if !newSet[addr] {
				for _, gv := range mv.GaugeVecs {
					gv.RemoveByPoolBackend(pool, addr)
				}
				for _, cv := range mv.CounterVecs {
					cv.RemoveByPoolBackend(pool, addr)
				}
				for _, hv := range mv.HistogramVecs {
					hv.RemoveByPoolBackend(pool, addr)
				}
			}
		}
	}
}
