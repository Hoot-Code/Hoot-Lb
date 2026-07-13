package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/discovery"
	"github.com/Hoot-Code/Hoot-Lb/internal/health"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
	"github.com/Hoot-Code/Hoot-Lb/internal/metrics"
	"github.com/Hoot-Code/Hoot-Lb/internal/proxy/l4"
	"github.com/Hoot-Code/Hoot-Lb/internal/proxy/l7"
	"github.com/Hoot-Code/Hoot-Lb/internal/ratelimit"
	"github.com/Hoot-Code/Hoot-Lb/internal/restart"
	"github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

func startTCPProxy(l config.ListenerConfig, snap *runtime.AtomicSnapshot, logger *slog.Logger, m *l4.ProxyMetrics, al *metrics.AccessLogger, rl *ratelimit.Limiter, inherited map[string]restart.ListenerDesc) (proxy, error) {
	poolGetter := l4.PoolStateGetter(func() (balancer.LoadBalancer, health.FailureReporter, runtime.Outcome) {
		ps := snap.Load().PoolStates[l.Pool]
		var outcome runtime.Outcome
		if ps.Breakers != nil {
			outcome = runtime.NewBackendOutcome(ps.Breakers)
		}
		return ps.LB, ps.FR, outcome
	})

	maxConn := l.Global.MaxConnectionsPerListener
	idleTimeout := l.Global.TCPIdleTimeout

	var srv *l4.TCPServer
	if d, ok := inherited[l.Name]; ok {
		ln, err := restart.ReconstructTCPListener(d)
		if err != nil {
			return nil, fmt.Errorf("reconstructing TCP listener %q: %w", l.Name, err)
		}
		srv = l4.NewTCPServerFromListener(l, ln, poolGetter, logger, m, al, rl, maxConn, idleTimeout)
	} else {
		var err error
		srv, err = l4.NewTCPServer(l, poolGetter, logger, m, al, rl, maxConn, idleTimeout)
		if err != nil {
			return nil, err
		}
	}
	go srv.Serve(context.Background())
	currentListeners = append(currentListeners, listenerRef{
		name: l.Name, protocol: "tcp", fileFn: srv.File,
	})
	return &proxyAdapter{closeFn: srv.Close, name: l.Name}, nil
}

func startUDPProxy(l config.ListenerConfig, snap *runtime.AtomicSnapshot, logger *slog.Logger, m *l4.ProxyMetrics, al *metrics.AccessLogger, rl *ratelimit.Limiter, inherited map[string]restart.ListenerDesc) (proxy, error) {
	poolGetter := l4.PoolStateGetter(func() (balancer.LoadBalancer, health.FailureReporter, runtime.Outcome) {
		ps := snap.Load().PoolStates[l.Pool]
		var outcome runtime.Outcome
		if ps.Breakers != nil {
			outcome = runtime.NewBackendOutcome(ps.Breakers)
		}
		return ps.LB, ps.FR, outcome
	})

	var srv *l4.UDPServer
	if d, ok := inherited[l.Name]; ok {
		conn, err := restart.ReconstructUDPConn(d)
		if err != nil {
			return nil, fmt.Errorf("reconstructing UDP listener %q: %w", l.Name, err)
		}
		srv = l4.NewUDPServerFromConn(l, conn, poolGetter, logger, m, al, rl)
	} else {
		var err error
		srv, err = l4.NewUDPServer(l, poolGetter, logger, m, al, rl)
		if err != nil {
			return nil, err
		}
	}
	go srv.Serve(context.Background())
	currentListeners = append(currentListeners, listenerRef{
		name: l.Name, protocol: "udp", fileFn: srv.File,
	})
	return &proxyAdapter{closeFn: srv.Close, name: l.Name}, nil
}

func startHTTPProxy(l config.ListenerConfig, snap *runtime.AtomicSnapshot, logger *slog.Logger, m *l7.L7Metrics, al *metrics.AccessLogger, rl *ratelimit.Limiter, inherited map[string]restart.ListenerDesc) (proxy, error) {
	getter := l7.RouteTableGetter(func() *l7.RouteTable {
		s := snap.Load()
		return buildRouteTable(l, s)
	})
	certStore := snap.Load().CertStores[l.Name]

	var srv *l7.L7Server
	if d, ok := inherited[l.Name]; ok {
		ln, err := restart.ReconstructTCPListener(d)
		if err != nil {
			return nil, fmt.Errorf("reconstructing HTTP listener %q: %w", l.Name, err)
		}
		srv = l7.NewL7ServerFromGetterWithMetricsAndListener(l, getter, logger, certStore, m, rl, ln)
	} else {
		var err error
		srv, err = l7.NewL7ServerFromGetterWithMetrics(l, getter, logger, certStore, m, rl)
		if err != nil {
			return nil, err
		}
	}
	go srv.Serve(context.Background())
	currentListeners = append(currentListeners, listenerRef{
		name: l.Name, protocol: "http", fileFn: srv.File,
	})
	return &proxyAdapter{closeFn: srv.Close, name: l.Name}, nil
}

func startTLSPassthroughProxy(l config.ListenerConfig, snap *runtime.AtomicSnapshot, logger *slog.Logger, m *l4.ProxyMetrics, al *metrics.AccessLogger, inherited map[string]restart.ListenerDesc) (proxy, error) {
	sniRouter := l4.SNIRouteGetter(func(sni string) (balancer.LoadBalancer, health.FailureReporter, runtime.Outcome) {
		s := snap.Load()
		if l.TLS != nil {
			for _, r := range l.TLS.Routes {
				if r.Host == sni {
					ps := s.PoolStates[r.Pool]
					if ps != nil {
						var outcome runtime.Outcome
						if ps.Breakers != nil {
							outcome = runtime.NewBackendOutcome(ps.Breakers)
						}
						return ps.LB, ps.FR, outcome
					}
				}
			}
		}
		ps := s.PoolStates[l.Pool]
		var outcome runtime.Outcome
		if ps.Breakers != nil {
			outcome = runtime.NewBackendOutcome(ps.Breakers)
		}
		return ps.LB, ps.FR, outcome
	})

	maxConn := l.Global.MaxConnectionsPerListener
	idleTimeout := l.Global.TCPIdleTimeout

	var srv *l4.TLSPassthroughServer
	if d, ok := inherited[l.Name]; ok {
		ln, err := restart.ReconstructTCPListener(d)
		if err != nil {
			return nil, fmt.Errorf("reconstructing TLS passthrough listener %q: %w", l.Name, err)
		}
		srv = l4.NewTLSPassthroughServerFromListener(l, ln, sniRouter, logger, m, al, maxConn, idleTimeout)
	} else {
		var err error
		srv, err = l4.NewTLSPassthroughServer(l, sniRouter, logger, m, al, maxConn, idleTimeout)
		if err != nil {
			return nil, err
		}
	}
	go srv.Serve(context.Background())
	currentListeners = append(currentListeners, listenerRef{
		name: l.Name, protocol: "tcp", fileFn: srv.File,
	})
	return &proxyAdapter{closeFn: srv.Close, name: l.Name}, nil
}

func buildRouteTable(l config.ListenerConfig, s *runtime.Snapshot) *l7.RouteTable {
	var routes []l7.Route
	for _, r := range l.Routes {
		route := l7.Route{
			Host:       r.Host,
			PathPrefix: r.PathPrefix,
		}
		if r.Header != nil {
			route.HeaderName = r.Header.Name
			route.HeaderValue = r.Header.Value
		}
		if len(r.Split) > 0 {
			split := make([]l7.SplitEntry, len(r.Split))
			for i, sc := range r.Split {
				ps := s.PoolStates[sc.Pool]
				var outcome runtime.Outcome
				if ps.Breakers != nil {
					outcome = runtime.NewBackendOutcome(ps.Breakers)
				}
				split[i] = l7.SplitEntry{
					LB:          ps.LB,
					FR:          ps.FR,
					Outcome:     outcome,
					Weight:      sc.Weight,
					Sticky:      ps.Sticky,
					Retry:       convertRetryConfig(ps.Retry),
					PoolMembers: makePoolMembershipCheck(ps.Servers),
				}
			}
			route.Split = split
		} else {
			ps := s.PoolStates[r.Pool]
			route.LB = ps.LB
			route.FR = ps.FR
			route.Sticky = ps.Sticky
			route.Retry = convertRetryConfig(ps.Retry)
			route.PoolMembers = makePoolMembershipCheck(ps.Servers)
			if ps.Breakers != nil {
				route.Outcome = runtime.NewBackendOutcome(ps.Breakers)
			}
		}
		routes = append(routes, route)
	}

	defaultPS := s.PoolStates[l.Pool]
	fallback := l7.Route{
		LB:          defaultPS.LB,
		FR:          defaultPS.FR,
		Sticky:      defaultPS.Sticky,
		Retry:       convertRetryConfig(defaultPS.Retry),
		PoolMembers: makePoolMembershipCheck(defaultPS.Servers),
	}
	if defaultPS.Breakers != nil {
		fallback.Outcome = runtime.NewBackendOutcome(defaultPS.Breakers)
	}
	return l7.NewRouteTableWithDefault(routes, fallback)
}

func makePoolMembershipCheck(servers []*balancer.Server) l7.PoolMembershipCheck {
	return func(addr string) bool {
		for _, s := range servers {
			if s.Address() == addr && s.IsHealthy() && !s.IsDraining() && !s.IsCircuitOpen() {
				return true
			}
		}
		return false
	}
}

func convertRetryConfig(rc *config.RetryConfig) *l7.RetryConfig {
	if rc == nil {
		return nil
	}
	return l7.RetryConfigFromPool(rc, 0)
}

func printStartupBanner(logger *slog.Logger, cfg *config.Config) {
	logger.Info("configuration loaded",
		slog.String(logging.ComponentKey, "main"),
		slog.Int("listeners", len(cfg.Listeners)),
		slog.Int("pools", len(cfg.Pools)))

	backendsByPool := make(map[string]int, len(cfg.Pools))
	algorithmByPool := make(map[string]string, len(cfg.Pools))
	for _, p := range cfg.Pools {
		backendsByPool[p.Name] = len(p.Backends)
		algorithmByPool[p.Name] = p.Algorithm
	}

	for _, l := range cfg.Listeners {
		logger.Info("listener configured",
			slog.String(logging.ComponentKey, "main"),
			slog.String(logging.ListenerKey, l.Name),
			slog.String("address", l.Address),
			slog.String("protocol", l.Protocol),
			slog.String(logging.PoolKey, l.Pool),
			slog.Int("pool_backends", backendsByPool[l.Pool]),
			slog.String("algorithm", algorithmByPool[l.Pool]))
	}
}

func newDiscoveryAdapterForPool(p config.PoolConfig, logger *slog.Logger) (discovery.Discovery, error) {
	if p.Discovery == nil {
		return nil, fmt.Errorf("pool %q has no discovery config", p.Name)
	}

	switch p.Discovery.Type {
	case "dns":
		if p.Discovery.DNS == nil {
			return nil, fmt.Errorf("pool %q: dns config missing", p.Name)
		}
		return discovery.NewDNS(
			p.Name,
			net.DefaultResolver,
			p.Discovery.DNS.Host,
			p.Discovery.DNS.Port,
			logger,
		), nil
	case "consul":
		return nil, fmt.Errorf("consul discovery requires building with -tags consul")
	case "k8s":
		return nil, fmt.Errorf("k8s discovery requires building with -tags k8s")
	case "docker":
		return nil, fmt.Errorf("docker discovery requires building with -tags docker")
	default:
		return nil, fmt.Errorf("unknown discovery type %q for pool %q", p.Discovery.Type, p.Name)
	}
}
