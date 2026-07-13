// Package reload implements hot configuration reload for the load
// balancer. It watches the config file for changes via polling,
// validates the new config, detects listener-level changes (which
// require a restart), builds a new runtime snapshot, and atomically
// swaps it in. It also handles SIGHUP for immediate reload.
package reload

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
	"github.com/Hoot-Code/Hoot-Lb/internal/metrics"
	"github.com/Hoot-Code/Hoot-Lb/internal/runtime"
	"github.com/Hoot-Code/Hoot-Lb/internal/tlsutil"
)

// Watcher polls the config file at a configurable interval and
// triggers a hot reload when the file changes. It also handles SIGHUP
// for immediate reload. The reload process validates the new config,
// rejects listener-level changes, builds a new Snapshot, starts new
// health checkers, swaps the snapshot atomically, and stops old
// checkers — in that exact order to avoid monitoring gaps.
type Watcher struct {
	configPath string
	interval   time.Duration
	snap       *runtime.AtomicSnapshot
	certStores map[string]*tlsutil.CertStore
	metricVecs *metrics.MetricVecs
	logger     *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// NewWatcher creates a Watcher that polls configPath at the given
// interval. If interval is zero, polling is disabled (only SIGHUP
// triggers reload). certStores maps TLS-terminate listener names to
// their CertStore instances, which are updated on reload via Replace.
// metricVecs may be nil to disable cardinality cleanup.
func NewWatcher(
	configPath string,
	interval time.Duration,
	snap *runtime.AtomicSnapshot,
	certStores map[string]*tlsutil.CertStore,
	logger *slog.Logger,
	metricVecs *metrics.MetricVecs,
) *Watcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &Watcher{
		configPath: configPath,
		interval:   interval,
		snap:       snap,
		certStores: certStores,
		metricVecs: metricVecs,
		logger:     logger,
		ctx:        ctx,
		cancel:     cancel,
		done:       make(chan struct{}),
	}
}

// Run starts the watcher loop. It blocks until Stop is called.
// The watcher polls the config file at the configured interval and
// listens for SIGHUP signals.
func (w *Watcher) Run() {
	defer close(w.done)

	// Set up SIGHUP handler.
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	defer signal.Stop(sighup)

	var tickerC <-chan time.Time
	if w.interval > 0 {
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		tickerC = ticker.C
	}

	w.logger.Info("config watcher started",
		slog.String(logging.ComponentKey, "reload"),
		slog.String("config_path", w.configPath),
		slog.Duration("interval", w.interval))

	for {
		select {
		case <-w.ctx.Done():
			w.logger.Info("config watcher stopped",
				slog.String(logging.ComponentKey, "reload"))
			return
		case <-tickerC:
			w.checkAndReload()
		case <-sighup:
			w.logger.Info("SIGHUP received, triggering reload",
				slog.String(logging.ComponentKey, "reload"))
			w.checkAndReload()
		}
	}
}

// Stop halts the watcher and waits for Run to return.
func (w *Watcher) Stop() {
	w.cancel()
	<-w.done
}

// checkAndReload reads the config file, validates it, diffs
// listeners, builds a new snapshot, and atomically swaps it in. On
// any error, the old snapshot remains untouched and the error is
// logged.
func (w *Watcher) checkAndReload() {
	data, err := os.ReadFile(w.configPath)
	if err != nil {
		w.logger.Error("failed to read config file for reload",
			slog.String(logging.ComponentKey, "reload"),
			slog.String("error", err.Error()))
		return
	}

	newCfg, err := config.Parse(data)
	if err != nil {
		w.logger.Error("failed to parse config for reload",
			slog.String(logging.ComponentKey, "reload"),
			slog.String("error", err.Error()))
		return
	}

	if err := config.Validate(newCfg); err != nil {
		w.logger.Error("config validation failed, keeping current config",
			slog.String(logging.ComponentKey, "reload"),
			slog.String("error", err.Error()))
		return
	}

	// Diff listener set against the running snapshot.
	oldSnap := w.snap.Load()
	if err := runtime.DiffListeners(oldSnap.Listeners, buildFingerprints(newCfg)); err != nil {
		var lce *runtime.ListenerChangeError
		if errors.As(err, &lce) {
			w.logger.Error("reload rejected",
				slog.String(logging.ComponentKey, "reload"),
				slog.String("reason", lce.Detail),
				slog.String("action", "restart required"))
		} else {
			w.logger.Error("reload rejected",
				slog.String(logging.ComponentKey, "reload"),
				slog.String("error", err.Error()))
		}
		return
	}

	// Reload TLS certificates for existing TLS-terminate listeners.
	for _, l := range newCfg.Listeners {
		if l.TLS != nil && l.TLS.Mode == "terminate" {
			if cs, ok := w.certStores[l.Name]; ok {
				newCerts, err := tlsutil.LoadCertificates(l.TLS.Certificates)
				if err != nil {
					w.logger.Error("failed to reload TLS certificates",
						slog.String(logging.ComponentKey, "reload"),
						slog.String(logging.ListenerKey, l.Name),
						slog.String("error", err.Error()))
					return
				}
				cs.Replace(newCerts)
				w.logger.Info("TLS certificates reloaded",
					slog.String(logging.ComponentKey, "reload"),
					slog.String(logging.ListenerKey, l.Name),
					slog.Int("certificates", len(l.TLS.Certificates)))
			}
		}
	}

	// Build new snapshot.
	newSnap, err := runtime.BuildSnapshot(newCfg, w.logger)
	if err != nil {
		w.logger.Error("failed to build new snapshot",
			slog.String(logging.ComponentKey, "reload"),
			slog.String("error", err.Error()))
		return
	}

	// Cleanup cardinality for removed backends.
	if w.metricVecs != nil {
		metrics.CleanupRemovedBackends(oldSnap, newSnap, w.metricVecs)
	}

	// Start new health checkers BEFORE swapping snapshot.
	for _, hc := range newSnap.Checkers {
		hc.Start(context.Background())
	}

	// Atomically swap snapshot.
	w.snap.Swap(newSnap)

	// Stop old health checkers AFTER swap.
	for _, hc := range oldSnap.Checkers {
		hc.Stop()
	}

	w.logger.Info("configuration reloaded successfully",
		slog.String(logging.ComponentKey, "reload"),
		slog.Int("pools", len(newSnap.PoolStates)),
		slog.Int("listeners", len(newSnap.Listeners)))
}

// buildFingerprints creates listener fingerprints from a config for
// diff comparison.
func buildFingerprints(cfg *config.Config) []runtime.ListenerFingerprint {
	fp := make([]runtime.ListenerFingerprint, len(cfg.Listeners))
	for i, l := range cfg.Listeners {
		var tlsMode string
		if l.TLS != nil {
			tlsMode = l.TLS.Mode
		}
		fp[i] = runtime.ListenerFingerprint{
			Name:     l.Name,
			Address:  l.Address,
			Protocol: l.Protocol,
			TLSMode:  tlsMode,
		}
	}
	return fp
}

// TriggerReload performs an immediate reload check. This is exposed
// for testing purposes — production code uses Run with polling and
// SIGHUP.
func (w *Watcher) TriggerReload() {
	w.checkAndReload()
}
