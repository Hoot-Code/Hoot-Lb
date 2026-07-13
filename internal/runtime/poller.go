package runtime

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/discovery"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
	"github.com/Hoot-Code/Hoot-Lb/internal/metrics"
)

// Poller wraps a Discovery adapter, polling it at a configured
// interval and updating the runtime snapshot when the backend set
// changes. On error, the previous backend set is retained — a
// discovery outage is invisible to traffic.
type Poller struct {
	discovery  discovery.Discovery
	snap       *AtomicSnapshot
	poolName   string
	cfg        config.PoolConfig
	interval   time.Duration
	logger     *slog.Logger
	metricVecs *metrics.MetricVecs

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu           sync.Mutex
	lastBackends []discovery.Backend
}

// NewPoller creates a Poller that polls the given Discovery adapter
// at the specified interval. On each successful resolution that
// differs from the previous set, it calls UpdatePoolBackends
// to perform a partial snapshot update. metricVecs may be nil to
// disable cardinality cleanup.
func NewPoller(
	disc discovery.Discovery,
	snap *AtomicSnapshot,
	poolName string,
	cfg config.PoolConfig,
	interval time.Duration,
	logger *slog.Logger,
	metricVecs *metrics.MetricVecs,
) *Poller {
	ctx, cancel := context.WithCancel(context.Background())

	// Seed lastBackends from the current snapshot so the first
	// poll doesn't treat an identical resolution as a change.
	var initial []discovery.Backend
	if ps := snap.Load().PoolStates[poolName]; ps != nil {
		initial = make([]discovery.Backend, len(ps.Servers))
		for i, s := range ps.Servers {
			initial[i] = discovery.Backend{
				Address: s.Address(),
				Weight:  s.Weight(),
			}
		}
	}

	return &Poller{
		discovery:    disc,
		snap:         snap,
		poolName:     poolName,
		cfg:          cfg,
		interval:     interval,
		logger:       logger,
		metricVecs:   metricVecs,
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
		lastBackends: initial,
	}
}

// Run starts the polling loop. It blocks until Stop is called.
func (p *Poller) Run() {
	defer close(p.done)

	p.logger.Info("discovery poller started",
		slog.String(logging.ComponentKey, "discovery"),
		slog.String(logging.PoolKey, p.poolName),
		slog.String("source", p.discovery.Name()),
		slog.Duration("interval", p.interval))

	p.poll()

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			p.logger.Info("discovery poller stopped",
				slog.String(logging.ComponentKey, "discovery"),
				slog.String(logging.PoolKey, p.poolName))
			return
		case <-ticker.C:
			p.poll()
		}
	}
}

// Stop halts the polling loop and waits for Run to return.
func (p *Poller) Stop() {
	p.cancel()
	<-p.done
}

func (p *Poller) poll() {
	ctx, cancel := context.WithTimeout(p.ctx, 10*time.Second)
	defer cancel()

	backends, err := p.discovery.Resolve(ctx)
	if err != nil {
		p.logger.Error("discovery resolution failed",
			slog.String(logging.ComponentKey, "discovery"),
			slog.String(logging.PoolKey, p.poolName),
			slog.String("source", p.discovery.Name()),
			slog.String("error", err.Error()))
		return
	}

	p.mu.Lock()
	changed := !backendSetsEqual(p.lastBackends, backends)
	if changed {
		p.lastBackends = backends
	}
	p.mu.Unlock()

	if !changed {
		return
	}

	p.logger.Info("discovery backend set changed",
		slog.String(logging.ComponentKey, "discovery"),
		slog.String(logging.PoolKey, p.poolName),
		slog.Int("backends", len(backends)))

	oldSnap := p.snap.Load()
	newSnap, err := UpdatePoolBackends(oldSnap, p.poolName, backends, p.cfg, p.logger)
	if err != nil {
		p.logger.Error("failed to update pool backends",
			slog.String(logging.ComponentKey, "discovery"),
			slog.String(logging.PoolKey, p.poolName),
			slog.String("error", err.Error()))
		return
	}

	// Cleanup cardinality for removed backends.
	if p.metricVecs != nil {
		metrics.CleanupRemovedBackends(oldSnap, newSnap, p.metricVecs)
	}

	if newHC, ok := newSnap.Checkers[p.poolName]; ok {
		newHC.Start(context.Background())
	}

	p.snap.Swap(newSnap)

	if oldHC, ok := oldSnap.Checkers[p.poolName]; ok {
		oldHC.Stop()
	}
}

func backendSetsEqual(a, b []discovery.Backend) bool {
	if len(a) != len(b) {
		return false
	}

	aAddrs := make(map[string]int, len(a))
	for _, be := range a {
		aAddrs[be.Address]++
	}

	for _, be := range b {
		if aAddrs[be.Address] <= 0 {
			return false
		}
		aAddrs[be.Address]--
	}

	return true
}

// LastBackends returns the last successfully resolved backend set.
// Exposed for testing.
func (p *Poller) LastBackends() []discovery.Backend {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]discovery.Backend, len(p.lastBackends))
	copy(cp, p.lastBackends)
	return cp
}
