package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/admin"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
	"github.com/Hoot-Code/Hoot-Lb/internal/metrics"
	"github.com/Hoot-Code/Hoot-Lb/internal/proxy/l4"
	"github.com/Hoot-Code/Hoot-Lb/internal/proxy/l7"
	"github.com/Hoot-Code/Hoot-Lb/internal/ratelimit"
	"github.com/Hoot-Code/Hoot-Lb/internal/reload"
	"github.com/Hoot-Code/Hoot-Lb/internal/restart"
	"github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lb:", err)
		os.Exit(1)
	}
}

func run() error {
	version := flag.Bool("version", false, "print version and exit")
	configPath := flag.String("config", "", "path to the YAML configuration file (required)")
	flag.Parse()

	if *version {
		fmt.Println("hoot-lb dev")
		return nil
	}

	if *configPath == "" {
		flag.Usage()
		return fmt.Errorf("missing required -config flag")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Detect inherited listeners from a parent process handoff.
	inheritedDescs, readyW := restart.ReconstructListeners()
	inheritedMap := make(map[string]restart.ListenerDesc, len(inheritedDescs))
	for _, d := range inheritedDescs {
		inheritedMap[d.Name] = d
	}

	level, err := logging.ParseLevel(cfg.Global.LogLevel)
	if err != nil {
		return fmt.Errorf("configuring logger: %w", err)
	}
	logger := logging.New(level, os.Stdout)
	slog.SetDefault(logger)

	printStartupBanner(logger, cfg)

	// Create metrics registry and access logger.
	var registry *metrics.Registry
	var accessLogger *metrics.AccessLogger
	metricsEnabled := cfg.Global.Metrics.Enabled == nil || *cfg.Global.Metrics.Enabled
	accessLogEnabled := cfg.Global.AccessLog.Enabled == nil || *cfg.Global.AccessLog.Enabled

	if metricsEnabled {
		registry = metrics.NewRegistry()
	}
	if accessLogEnabled {
		accessLogger = metrics.NewAccessLogger(os.Stdout)
	}

	// Register all metrics.
	var connectionsTotal *metrics.CounterVec
	var connectionsActive *metrics.GaugeVec
	var requestDuration *metrics.HistogramVec
	var bytesTransferred *metrics.CounterVec
	var dialFailures *metrics.CounterVec
	var backendHealthy *metrics.GaugeVec
	var backendLatency *metrics.HistogramVec
	var tlsHandshakeDuration *metrics.HistogramVec
	var healthCheckDuration *metrics.HistogramVec
	var connectionDuration *metrics.HistogramVec

	if registry != nil {
		connectionsTotal = registry.NewCounterVec("lb_connections_total", "Total connections established", []string{"listener", "pool", "backend", "protocol"})
		connectionsActive = registry.NewGaugeVec("lb_connections_active", "Currently active connections", []string{"listener", "pool", "backend", "protocol"})
		requestDuration = registry.NewHistogramVec("lb_request_duration_seconds", "Request duration in seconds", []string{"listener", "pool", "backend"}, metrics.DefaultHistogramBuckets)
		bytesTransferred = registry.NewCounterVec("lb_bytes_transferred_total", "Total bytes transferred", []string{"listener", "pool", "backend", "direction"})
		dialFailures = registry.NewCounterVec("lb_dial_failures_total", "Total dial failures", []string{"listener", "pool", "backend"})
		backendHealthy = registry.NewGaugeVec("lb_backend_healthy", "Backend health status (1=healthy, 0=unhealthy)", []string{"pool", "backend"})
		backendLatency = registry.NewHistogramVec("lb_backend_latency_seconds", "Time from dial start to first response byte", []string{"pool", "backend"}, metrics.FineHistogramBuckets)
		tlsHandshakeDuration = registry.NewHistogramVec("lb_tls_handshake_duration_seconds", "TLS handshake duration", []string{"listener"}, metrics.FineHistogramBuckets)
		healthCheckDuration = registry.NewHistogramVec("lb_health_check_duration_seconds", "Health check probe duration", []string{"pool", "backend"}, metrics.FineHistogramBuckets)
		connectionDuration = registry.NewHistogramVec("lb_connection_duration_seconds", "Full connection lifetime", []string{"listener", "pool", "backend", "protocol"}, metrics.DefaultHistogramBuckets)
	}

	// Build metric vectors for cardinality cleanup.
	var metricVecs *metrics.MetricVecs
	if registry != nil {
		metricVecs = &metrics.MetricVecs{
			GaugeVecs:     []*metrics.GaugeVec{connectionsActive, backendHealthy},
			CounterVecs:   []*metrics.CounterVec{connectionsTotal, bytesTransferred, dialFailures},
			HistogramVecs: []*metrics.HistogramVec{requestDuration, backendLatency, tlsHandshakeDuration, healthCheckDuration, connectionDuration},
		}
	}

	// Start metrics server.
	if registry != nil {
		var msrv *metrics.MetricsServer
		if d, ok := inheritedMap["_metrics"]; ok {
			var ln net.Listener
			ln, err = restart.ReconstructTCPListener(d)
			if err != nil {
				return fmt.Errorf("reconstructing metrics listener: %w", err)
			}
			msrv = metrics.NewMetricsServerFromListener(ln, cfg.Global.Metrics.Path, registry, logger)
		} else {
			msrv, err = metrics.NewMetricsServer(cfg.Global.Metrics.Address, cfg.Global.Metrics.Path, registry, logger)
			if err != nil {
				return fmt.Errorf("starting metrics server: %w", err)
			}
		}
		go msrv.Start(context.Background())
		currentListeners = append(currentListeners, listenerRef{name: "_metrics", protocol: "http", fileFn: msrv.File})
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			msrv.Close(ctx)
		}()
	}

	// Build proxy metrics.
	var proxyMetrics *l4.ProxyMetrics
	var l7Metrics *l7.L7Metrics
	if registry != nil {
		proxyMetrics = &l4.ProxyMetrics{
			ConnectionsTotal:  connectionsTotal,
			ConnectionsActive: connectionsActive,
			BytesTransferred:  bytesTransferred,
			DialFailures:      dialFailures,
		}
		l7Metrics = &l7.L7Metrics{ConnectionsTotal: connectionsTotal, ConnectionsActive: connectionsActive, RequestDuration: requestDuration, DialFailures: dialFailures, BackendLatency: backendLatency, TLSHandshakeDuration: tlsHandshakeDuration}
	}

	// Build initial snapshot using the shared BuildSnapshot function.
	snap, err := runtime.BuildSnapshot(cfg, logger)
	if err != nil {
		return fmt.Errorf("building initial snapshot: %w", err)
	}
	snapPtr := runtime.NewAtomicSnapshot(snap)

	// restartCh is signaled when a handoff succeeds so the parent
	// can begin its graceful drain and exit.
	restartCh := make(chan struct{}, 1)

	// Start admin server if enabled.
	var adminSrv *admin.Server
	adminEnabled := cfg.Global.Admin.Enabled != nil && *cfg.Global.Admin.Enabled
	if adminEnabled {
		restartFn := func() error {
			if err := triggerRestart(*configPath, logger); err != nil {
				return err
			}
			select {
			case restartCh <- struct{}{}:
			default:
			}
			return nil
		}
		if d, ok := inheritedMap["_admin"]; ok {
			var ln net.Listener
			ln, err = restart.ReconstructTCPListener(d)
			if err != nil {
				return fmt.Errorf("reconstructing admin listener: %w", err)
			}
			adminSrv, err = admin.NewServerFromListener(cfg.Global.Admin, ln, snapPtr, cfg, logger, connectionsActive, restartFn)
		} else {
			adminSrv, err = admin.NewServer(cfg.Global.Admin, snapPtr, cfg, logger, connectionsActive, restartFn)
		}
		if err != nil {
			return fmt.Errorf("starting admin server: %w", err)
		}
		go adminSrv.Start()
		currentListeners = append(currentListeners, listenerRef{name: "_admin", protocol: "http", fileFn: adminSrv.File})
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			adminSrv.Close(ctx)
		}()
	}

	// Start initial health checkers.
	for _, hc := range snap.Checkers {
		hc.Start(context.Background())
	}

	// Start discovery pollers for pools using discovery.
	var pollers []*runtime.Poller
	for _, p := range cfg.Pools {
		if p.Discovery != nil {
			disc, err := newDiscoveryAdapterForPool(p, logger)
			if err != nil {
				return fmt.Errorf("creating discovery adapter for pool %q: %w", p.Name, err)
			}
			interval := p.Discovery.DNS.RefreshInterval
			if p.Discovery.Type == "consul" && p.Discovery.Consul != nil {
				interval = p.Discovery.Consul.RefreshInterval
			}
			if p.Discovery.Type == "k8s" && p.Discovery.K8s != nil {
				interval = p.Discovery.K8s.RefreshInterval
			}
			if p.Discovery.Type == "docker" && p.Discovery.Docker != nil {
				interval = p.Discovery.Docker.RefreshInterval
			}
			poller := runtime.NewPoller(disc, snapPtr, p.Name, p, interval, logger, metricVecs)
			pollers = append(pollers, poller)
			go poller.Run()
		}
	}

	var proxies []proxy
	var rateLimiters []*ratelimit.Limiter
	for _, l := range cfg.Listeners {
		var rl *ratelimit.Limiter
		if l.RateLimit != nil {
			rl = ratelimit.NewLimiter(
				l.RateLimit.RequestsPerSecond,
				l.RateLimit.Burst,
				l.RateLimit.ClientIdleEviction,
			)
			rateLimiters = append(rateLimiters, rl)
		}
		switch l.Protocol {
		case "tcp":
			if l.TLS != nil && l.TLS.Mode == "passthrough" {
				p, err := startTLSPassthroughProxy(l, snapPtr, logger, proxyMetrics, accessLogger, inheritedMap)
				if err != nil {
					return fmt.Errorf("starting TLS passthrough proxy %q: %w", l.Name, err)
				}
				proxies = append(proxies, p)
			} else {
				p, err := startTCPProxy(l, snapPtr, logger, proxyMetrics, accessLogger, rl, inheritedMap)
				if err != nil {
					return fmt.Errorf("starting TCP proxy %q: %w", l.Name, err)
				}
				proxies = append(proxies, p)
			}
		case "udp":
			p, err := startUDPProxy(l, snapPtr, logger, proxyMetrics, accessLogger, rl, inheritedMap)
			if err != nil {
				return fmt.Errorf("starting UDP proxy %q: %w", l.Name, err)
			}
			proxies = append(proxies, p)
		case "http":
			p, err := startHTTPProxy(l, snapPtr, logger, l7Metrics, accessLogger, rl, inheritedMap)
			if err != nil {
				return fmt.Errorf("starting HTTP proxy %q: %w", l.Name, err)
			}
			proxies = append(proxies, p)
		default:
			logger.Warn("unknown protocol, skipping",
				slog.String(logging.ListenerKey, l.Name),
				slog.String("protocol", l.Protocol))
		}
	}

	// Start config watcher for hot reload.
	watcher := reload.NewWatcher(
		*configPath,
		cfg.Global.ReloadCheckInterval,
		snapPtr,
		snap.CertStores,
		logger,
		metricVecs,
	)

	go watcher.Run()

	// Signal readiness to parent if this is a handoff child.
	if readyW != nil {
		restart.SignalReady(readyW)
		logger.Info("child process ready, parent will exit",
			slog.String(logging.ComponentKey, "main"))
	}

	// Wire up SIGUSR2 for zero-downtime restart (parent only).
	if !restart.IsHandoff() {
		stopSIGUSR2 := restart.ListenSIGUSR2(func() {
			if err := triggerRestart(*configPath, logger); err != nil {
				logger.Error("restart trigger failed",
					slog.String(logging.ComponentKey, "main"),
					slog.String("error", err.Error()))
				return
			}
			select {
			case restartCh <- struct{}{}:
			default:
			}
		}, logger)
		defer stopSIGUSR2()
	}

	logger.Info("load balancer running, waiting for shutdown signal",
		slog.String(logging.ComponentKey, "main"),
		slog.Int("active_proxies", len(proxies)))

	waitForSignal(restartCh)

	logger.Info("draining connections",
		slog.String(logging.ComponentKey, "main"),
		slog.Duration("shutdown_timeout", cfg.Global.ShutdownTimeout))

	// Stop watcher before shutting down proxies.
	watcher.Stop()

	// Stop admin server.
	if adminSrv != nil {
		adminCtx, adminCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer adminCancel()
		adminSrv.Close(adminCtx)
	}

	// Stop discovery pollers.
	for _, poller := range pollers {
		poller.Stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Global.ShutdownTimeout)
	defer cancel()

	// Stop health checkers from the current snapshot.
	for _, hc := range snapPtr.Load().Checkers {
		hc.Stop()
	}

	// Stop rate limiter eviction goroutines.
	for _, rl := range rateLimiters {
		rl.Stop()
	}

	var shutdownErr error
	for _, p := range proxies {
		if err := p.Close(shutdownCtx); err != nil {
			logger.Error("proxy close error",
				slog.String(logging.ListenerKey, p.Name()),
				slog.String("error", err.Error()))
			shutdownErr = err
		}
	}

	if shutdownErr != nil {
		return fmt.Errorf("shutdown: %w", shutdownErr)
	}

	logger.Info("load balancer shut down cleanly",
		slog.String(logging.ComponentKey, "main"))
	return nil
}
