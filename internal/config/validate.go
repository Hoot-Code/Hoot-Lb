package config

import (
	"errors"
	"fmt"
	"net"
)

// validLogLevels enumerates the log levels accepted in global.log_level.
var validLogLevels = map[string]bool{
	"debug": true,
	"info":  true,
	"warn":  true,
	"error": true,
}

// validProtocols enumerates the protocols accepted in
// listeners[].protocol.
var validProtocols = map[string]bool{
	"tcp":  true,
	"udp":  true,
	"http": true,
}

// validAlgorithms enumerates the load balancing algorithms accepted in
// pools[].algorithm.
var validAlgorithms = map[string]bool{
	"round_robin":          true,
	"weighted_round_robin": true,
	"least_conn":           true,
	"random":               true,
	"ip_hash":              true,
}

// validHealthCheckTypes enumerates the health check types accepted in
// pools[].health_check.type.
var validHealthCheckTypes = map[string]bool{
	"tcp":  true,
	"http": true,
	"none": true,
}

// validTLSModes enumerates the TLS modes accepted in listeners[].tls.mode.
var validTLSModes = map[string]bool{
	"terminate":   true,
	"passthrough": true,
}

// Validate checks a decoded Config for semantic problems that decode
// alone cannot catch: duplicate names, dangling references between
// listeners and pools, invalid addresses, and zero-weight backends.
//
// All problems found are collected and returned together (via
// errors.Join) rather than stopping at the first one, so a user correcting
// a config file can address everything in one pass instead of playing
// whack-a-mole one error at a time.
func Validate(cfg *Config) error {
	var errs []error

	if !validLogLevels[cfg.Global.LogLevel] {
		errs = append(errs, newFieldError("global.log_level", "must be one of: debug, info, warn, error (got %q)", cfg.Global.LogLevel))
	}
	if cfg.Global.ShutdownTimeout <= 0 {
		errs = append(errs, newFieldError("global.shutdown_timeout", "must be a positive duration"))
	}

	if cfg.Global.Metrics.Enabled != nil && *cfg.Global.Metrics.Enabled {
		if cfg.Global.Metrics.Address == "" {
			errs = append(errs, newFieldError("global.metrics.address", "is required when metrics is enabled"))
		} else if err := validateHostPort(cfg.Global.Metrics.Address); err != nil {
			errs = append(errs, newFieldError("global.metrics.address", "invalid address %q: %v", cfg.Global.Metrics.Address, err))
		}
		if cfg.Global.Metrics.Path == "" {
			errs = append(errs, newFieldError("global.metrics.path", "is required when metrics is enabled"))
		}
	}
	if cfg.Global.AccessLog.Enabled != nil && *cfg.Global.AccessLog.Enabled {
		if cfg.Global.AccessLog.Format != "json" {
			errs = append(errs, newFieldError("global.access_log.format", "must be \"json\" (got %q)", cfg.Global.AccessLog.Format))
		}
	}

	errs = append(errs, validateAdmin(&cfg.Global.Admin)...)

	if len(cfg.Listeners) == 0 {
		errs = append(errs, newFieldError("listeners", "at least one listener is required"))
	}
	if len(cfg.Pools) == 0 {
		errs = append(errs, newFieldError("pools", "at least one pool is required"))
	}

	poolNames := make(map[string]bool, len(cfg.Pools))
	for i, p := range cfg.Pools {
		prefix := fmt.Sprintf("pools[%d]", i)
		if poolNames[p.Name] {
			errs = append(errs, newFieldError(prefix+".name", "duplicate pool name %q", p.Name))
		}
		poolNames[p.Name] = true
		errs = append(errs, validatePool(p, prefix)...)
	}

	listenerNames := make(map[string]bool, len(cfg.Listeners))
	for i, l := range cfg.Listeners {
		prefix := fmt.Sprintf("listeners[%d]", i)
		if listenerNames[l.Name] {
			errs = append(errs, newFieldError(prefix+".name", "duplicate listener name %q", l.Name))
		}
		listenerNames[l.Name] = true
		errs = append(errs, validateListener(l, prefix, poolNames)...)
	}

	return errors.Join(errs...)
}

// validateListener checks a single listener: protocol, address syntax,
// and that its referenced pool actually exists.
func validateListener(l ListenerConfig, prefix string, poolNames map[string]bool) []error {
	var errs []error

	if !validProtocols[l.Protocol] {
		errs = append(errs, newFieldError(prefix+".protocol", "must be one of: tcp, udp, http (got %q)", l.Protocol))
	}
	if err := validateHostPort(l.Address); err != nil {
		errs = append(errs, newFieldError(prefix+".address", "invalid address %q: %v", l.Address, err))
	}
	if l.Pool == "" {
		errs = append(errs, newFieldError(prefix+".pool", "is required"))
	} else if !poolNames[l.Pool] {
		errs = append(errs, newFieldError(prefix+".pool", "references undefined pool %q", l.Pool))
	}

	if len(l.Routes) > 0 && l.Protocol != "http" {
		errs = append(errs, newFieldError(prefix+".routes", "routes are only allowed on http listeners (got %q)", l.Protocol))
	}

	for j, r := range l.Routes {
		rPrefix := fmt.Sprintf("%s.routes[%d]", prefix, j)
		errs = append(errs, validateRoute(r, rPrefix, poolNames)...)
	}

	if l.TLS != nil {
		errs = append(errs, validateTLS(l.TLS, prefix, l.Protocol, poolNames)...)
	}

	if l.RateLimit != nil {
		errs = append(errs, validateRateLimit(l.RateLimit, prefix+".rate_limit")...)
	}

	return errs
}

// validatePool checks a single pool: it must have at least one
// backend (or a discovery config), and every backend must have a valid
// address and a positive weight.
func validatePool(p PoolConfig, prefix string) []error {
	var errs []error

	if !validAlgorithms[p.Algorithm] {
		errs = append(errs, newFieldError(prefix+".algorithm", "must be one of: round_robin, weighted_round_robin, least_conn, random, ip_hash (got %q)", p.Algorithm))
	}

	hasBackends := len(p.Backends) > 0
	hasDiscovery := p.Discovery != nil

	if hasBackends && hasDiscovery {
		errs = append(errs, newFieldError(prefix, "backends and discovery are mutually exclusive — specify exactly one"))
	}

	if !hasBackends && !hasDiscovery {
		errs = append(errs, newFieldError(prefix+".backends", "is required when discovery is not specified"))
	}

	if hasBackends {
		for i, b := range p.Backends {
			bPrefix := fmt.Sprintf("%s.backends[%d]", prefix, i)
			if err := validateHostPort(b.Address); err != nil {
				errs = append(errs, newFieldError(bPrefix+".address", "invalid address %q: %v", b.Address, err))
			}
			if b.Weight <= 0 {
				errs = append(errs, newFieldError(bPrefix+".weight", "must be a positive integer (got %d)", b.Weight))
			}
		}
	}

	if hasDiscovery {
		errs = append(errs, validateDiscovery(p.Discovery, prefix+".discovery")...)
	}

	if p.HealthCheck != nil {
		errs = append(errs, validateHealthCheck(p.HealthCheck, prefix+".health_check")...)
	}

	if p.Sticky != nil {
		errs = append(errs, validateSticky(p.Sticky, prefix+".sticky")...)
	}

	if p.CircuitBreaker != nil {
		errs = append(errs, validateCircuitBreaker(p.CircuitBreaker, prefix+".circuit_breaker")...)
	}

	if p.Retry != nil {
		errs = append(errs, validateRetry(p.Retry, prefix+".retry")...)
	}

	return errs
}

// validateHealthCheck checks a single health check configuration for
// semantic correctness: valid type, positive thresholds, timeout < interval,
// and path required for HTTP checks.
func validateHealthCheck(hc *HealthCheckConfig, prefix string) []error {
	var errs []error

	if !validHealthCheckTypes[hc.Type] {
		errs = append(errs, newFieldError(prefix+".type", "must be one of: tcp, http, none (got %q)", hc.Type))
	}

	if hc.Type != "none" {
		if hc.Interval <= 0 {
			errs = append(errs, newFieldError(prefix+".interval", "must be a positive duration"))
		}
		if hc.Timeout <= 0 {
			errs = append(errs, newFieldError(prefix+".timeout", "must be a positive duration"))
		}
		if hc.Interval > 0 && hc.Timeout > 0 && hc.Timeout >= hc.Interval {
			errs = append(errs, newFieldError(prefix+".timeout", "must be less than interval (%v >= %v)", hc.Timeout, hc.Interval))
		}
		if hc.HealthyThreshold < 1 {
			errs = append(errs, newFieldError(prefix+".healthy_threshold", "must be >= 1 (got %d)", hc.HealthyThreshold))
		}
		if hc.UnhealthyThreshold < 1 {
			errs = append(errs, newFieldError(prefix+".unhealthy_threshold", "must be >= 1 (got %d)", hc.UnhealthyThreshold))
		}
	}

	if hc.Type == "http" {
		if hc.Path == "" {
			errs = append(errs, newFieldError(prefix+".path", "is required when type is http"))
		} else if hc.Path[0] != '/' {
			errs = append(errs, newFieldError(prefix+".path", "must start with / (got %q)", hc.Path))
		}
	}

	return errs
}

// validateRoute checks a single route configuration: pool/split
// mutual exclusivity, dangling pool references, split entry validity,
// and header condition correctness.
func validateRoute(r RouteConfig, prefix string, poolNames map[string]bool) []error {
	var errs []error

	hasPool := r.Pool != ""
	hasSplit := len(r.Split) > 0

	if !hasPool && !hasSplit {
		errs = append(errs, newFieldError(prefix+".pool", "is required when split is not specified"))
		return errs
	}

	if hasPool && hasSplit {
		errs = append(errs, newFieldError(prefix, "pool and split are mutually exclusive — specify exactly one"))
	}

	if hasPool && !poolNames[r.Pool] {
		errs = append(errs, newFieldError(prefix+".pool", "references undefined pool %q", r.Pool))
	}

	if hasSplit {
		if len(r.Split) < 2 {
			errs = append(errs, newFieldError(prefix+".split", "must contain at least 2 entries"))
		}
		for i, s := range r.Split {
			sPrefix := fmt.Sprintf("%s.split[%d]", prefix, i)
			if s.Weight <= 0 {
				errs = append(errs, newFieldError(sPrefix+".weight", "must be a positive integer (got %d)", s.Weight))
			}
			if !poolNames[s.Pool] {
				errs = append(errs, newFieldError(sPrefix+".pool", "references undefined pool %q", s.Pool))
			}
		}
	}

	if r.Header != nil && r.Header.Name == "" {
		errs = append(errs, newFieldError(prefix+".header.name", "is required and must be a non-empty string"))
	}

	return errs
}

// validateSticky checks a sticky session configuration for semantic
// correctness: TTL must be positive.
func validateSticky(sc *StickyConfig, prefix string) []error {
	var errs []error

	if sc.TTL <= 0 {
		errs = append(errs, newFieldError(prefix+".ttl", "must be a positive duration"))
	}

	return errs
}

// validateHostPort checks that addr is a syntactically valid
// "host:port" address. It does not attempt to resolve or dial the
// address -- only structural validity is checked here.
func validateHostPort(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if port == "" {
		return fmt.Errorf("missing port")
	}
	if host == "" {
		return nil
	}
	return nil
}

// validateTLS checks a TLS configuration for semantic correctness:
// valid mode, protocol constraints, certificate requirements, and
// pool references.
func validateTLS(t *TLSConfig, prefix string, protocol string, poolNames map[string]bool) []error {
	var errs []error
	tlsPrefix := prefix + ".tls"

	if !validTLSModes[t.Mode] {
		errs = append(errs, newFieldError(tlsPrefix+".mode", "must be one of: terminate, passthrough (got %q)", t.Mode))
	}

	switch t.Mode {
	case "terminate":
		if protocol != "http" {
			errs = append(errs, newFieldError(tlsPrefix+".mode", "terminate mode is only valid on http listeners (got %q)", protocol))
		}
		if len(t.Certificates) == 0 {
			errs = append(errs, newFieldError(tlsPrefix+".certificates", "must contain at least one certificate entry"))
		}
		var defaultCount int
		for i, c := range t.Certificates {
			cPrefix := fmt.Sprintf("%s.certificates[%d]", tlsPrefix, i)
			if c.CertFile == "" {
				errs = append(errs, newFieldError(cPrefix+".cert_file", "is required"))
			}
			if c.KeyFile == "" {
				errs = append(errs, newFieldError(cPrefix+".key_file", "is required"))
			}
			if c.Host == "" {
				defaultCount++
			}
		}
		if defaultCount > 1 {
			errs = append(errs, newFieldError(tlsPrefix+".certificates", "at most one certificate may have an empty host (default certificate)"))
		}

	case "passthrough":
		if protocol != "tcp" {
			errs = append(errs, newFieldError(tlsPrefix+".mode", "passthrough mode is only valid on tcp listeners (got %q)", protocol))
		}
		if t.HandshakeTimeout < 0 {
			errs = append(errs, newFieldError(tlsPrefix+".handshake_timeout", "must not be negative"))
		}
		for i, r := range t.Routes {
			rPrefix := fmt.Sprintf("%s.routes[%d]", tlsPrefix, i)
			if r.Pool == "" {
				errs = append(errs, newFieldError(rPrefix+".pool", "is required"))
			} else if !poolNames[r.Pool] {
				errs = append(errs, newFieldError(rPrefix+".pool", "references undefined pool %q", r.Pool))
			}
		}
	}

	return errs
}
