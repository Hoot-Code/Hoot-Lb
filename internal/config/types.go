// Package config defines the load balancer's configuration schema, a
// minimal YAML-subset parser used to read it (the project takes on no
// external dependencies, so a full YAML library is not available), and
// validation that turns malformed or incomplete configuration into
// clear, field-specific errors at startup.
package config

import "time"

// Config is the fully parsed and validated top-level configuration for
// the load balancer.
type Config struct {
	// Global holds settings that apply to the process as a whole,
	// rather than to any single listener or pool.
	Global GlobalConfig
	// Listeners is the set of network listeners the load balancer
	// should open. Must contain at least one entry.
	Listeners []ListenerConfig
	// Pools is the set of backend pools available for listeners to
	// route traffic into. Must contain at least one entry.
	Pools []PoolConfig
}

// ListenerConfig describes a single network listener: where it binds,
// what protocol it speaks, and which backend pool it routes traffic
// into.
type ListenerConfig struct {
	// Name uniquely identifies this listener. Used in logs and must be
	// unique across all listeners in the configuration.
	Name string
	// Address is the host:port the listener binds to, e.g.
	// "0.0.0.0:8080".
	Address string
	// Protocol is the protocol this listener speaks. Must be one of
	// "tcp", "udp", or "http".
	Protocol string
	// Pool is the name of the backend pool this listener routes
	// traffic into. Must reference a pool defined in Pools. When
	// Routes is set on an HTTP listener, Pool serves as the
	// default/fallback pool when no route matches.
	Pool string
	// Routes is an optional set of routing rules for HTTP listeners.
	// When present, incoming requests are matched against the routes
	// before falling back to the listener's default Pool. Routes are
	// only meaningful when Protocol is "http"; specifying routes on a
	// non-http listener is a validation error.
	Routes []RouteConfig
	// TLS configures TLS behavior for this listener. When present,
	// Mode must be "terminate" (valid only on HTTP listeners) or
	// "passthrough" (valid only on TCP listeners). When nil, the
	// listener operates without TLS.
	TLS *TLSConfig
	// RateLimit configures per-client rate limiting for this listener.
	// When nil, rate limiting is disabled.
	RateLimit *RateLimitConfig
	// MaxRequestBodyBytes is an optional limit on the request body
	// size for HTTP listeners (L7 only). When zero, no limit is
	// applied. Requests exceeding this limit receive a 413 response.
	MaxRequestBodyBytes int64
	// Global holds the process-wide configuration, embedded so that
	// listener-level code can access global settings such as
	// MaxConnectionsPerListener and TCPIdleTimeout.
	Global GlobalConfig
}

// PoolConfig describes a named group of backends that one or more
// listeners can route traffic into.
type PoolConfig struct {
	// Name uniquely identifies this pool. Referenced by
	// ListenerConfig.Pool and must be unique across all pools in the
	// configuration.
	Name string
	// Algorithm selects the load balancing strategy for this pool.
	// Must be one of: "round_robin", "weighted_round_robin",
	// "least_conn", "random", "ip_hash". Defaults to
	// "weighted_round_robin" when omitted.
	Algorithm string
	// Backends is the static list of upstream servers. Mutually
	// exclusive with Discovery — exactly one must be specified.
	Backends []BackendConfig
	// Discovery configures dynamic backend resolution via service
	// discovery. Mutually exclusive with Backends — exactly one must
	// be specified.
	Discovery *DiscoveryConfig
	// HealthCheck configures active and passive health checking for
	// backends in this pool. When nil, defaults to TCP health checking.
	HealthCheck *HealthCheckConfig
	// Sticky configures cookie-based sticky sessions for this pool.
	// When present, requests carrying a valid sticky cookie are routed
	// directly to the previously selected backend (if it is still in
	// the pool and healthy). When nil, sticky sessions are disabled.
	Sticky *StickyConfig
	// CircuitBreaker configures per-backend circuit breaking for this
	// pool. When nil, circuit breaking is disabled.
	CircuitBreaker *CircuitBreakerConfig
	// Retry configures retry behavior for failed requests in this
	// pool. Only applies to L7 (HTTP) listeners. When nil, retries
	// are disabled.
	Retry *RetryConfig
}

// DiscoveryConfig describes a service discovery source for dynamic
// pool membership. When present, the pool's backend list is resolved
// dynamically instead of using the static Backends field. The Backends
// and Discovery fields are mutually exclusive on a pool.
type DiscoveryConfig struct {
	// Type selects the discovery backend. Must be one of "dns",
	// "consul", "k8s", or "docker". The consul, k8s, and docker
	// types are only valid when the binary is built with the
	// corresponding build tag.
	Type string
	// DNS configures DNS-based service discovery. Required when Type
	// is "dns".
	DNS *DNSDiscoveryConfig
	// Consul configures Consul-based service discovery. Required when
	// Type is "consul". Only available when built with -tags consul.
	Consul *ConsulDiscoveryConfig
	// K8s configures Kubernetes-based service discovery. Required when
	// Type is "k8s". Only available when built with -tags k8s.
	K8s *K8sDiscoveryConfig
	// Docker configures Docker-based service discovery. Required when
	// Type is "docker". Only available when built with -tags docker.
	Docker *DockerDiscoveryConfig
}

// DNSDiscoveryConfig configures DNS-based service discovery. Go's
// standard library resolver does not expose actual DNS record TTLs
// through the simple LookupHost API used here, so the operator must
// set RefreshInterval at or below their actual DNS TTL — this system
// cannot auto-detect the correct refresh interval.
type DNSDiscoveryConfig struct {
	// Host is the DNS hostname to resolve, e.g.
	// "backend.service.consul". May include a port suffix
	// (e.g. "backend.consul:8500") — if so, the port is used
	// when Port is zero.
	Host string
	// Port is the port to append to each resolved IP address. If
	// zero and Host contains a port, that port is used instead.
	Port int
	// RefreshInterval is how often to re-resolve the hostname. The
	// operator must set this at or below the actual DNS record TTL,
	// because Go's stdlib resolver does not expose TTL information.
	// Defaults to 30s when omitted.
	RefreshInterval time.Duration
}

// ConsulDiscoveryConfig configures Consul-based service discovery
// via the /v1/health/service HTTP endpoint. Only available when the
// binary is built with the "consul" build tag.
type ConsulDiscoveryConfig struct {
	// Address is the Consul agent HTTP address, e.g.
	// "http://127.0.0.1:8500".
	Address string
	// Service is the Consul service name to query.
	Service string
	// RefreshInterval is how often to re-query Consul for healthy
	// service instances. Defaults to 30s when omitted.
	RefreshInterval time.Duration
}

// K8sDiscoveryConfig configures Kubernetes-based service discovery
// via the Endpoints or EndpointSlice REST API. Only available when
// the binary is built with the "k8s" build tag.
type K8sDiscoveryConfig struct {
	// Namespace is the Kubernetes namespace of the service.
	Namespace string
	// Service is the Kubernetes service name to query.
	Service string
	// Kubeconfig is the path to a kubeconfig file. When empty and
	// running in-cluster, the in-cluster service account token is
	// used automatically.
	Kubeconfig string
	// RefreshInterval is how often to re-query the Kubernetes API.
	// Defaults to 30s when omitted.
	RefreshInterval time.Duration
}

// DockerDiscoveryConfig configures Docker-based service discovery
// via the Docker daemon's REST API over Unix socket. Only available
// when the binary is built with the "docker" build tag.
type DockerDiscoveryConfig struct {
	// Network is the Docker network name to extract container IPs from.
	Network string
	// LabelSelector is a label filter for matching containers, e.g.
	// "app=nginx".
	LabelSelector string
	// RefreshInterval is how often to re-query the Docker daemon.
	// Defaults to 30s when omitted.
	RefreshInterval time.Duration
}

// BackendConfig describes a single upstream server within a pool.
type BackendConfig struct {
	// Address is the dial address of the backend, e.g.
	// "10.0.0.1:8080".
	Address string
	// Weight is the relative weight of this backend for weighted load
	// balancing algorithms. Must be a positive integer.
	Weight int
}

// RouteConfig describes a single routing rule within an HTTP listener.
// Routes are evaluated by longest path_prefix match among those whose
// host and header conditions match (empty host matches any request
// host, absent header condition matches any request). If no route
// matches, the listener's default pool is used.
type RouteConfig struct {
	// Host restricts this route to requests with a matching Host
	// header. An empty value matches any host.
	Host string
	// PathPrefix restricts this route to requests whose URL path
	// starts with this prefix. An empty value matches any path.
	PathPrefix string
	// Header restricts this route to requests whose named header
	// matches the given value exactly (case-insensitive header name,
	// per HTTP semantics). When nil, no header condition is applied.
	Header *HeaderConfig
	// Pool is the name of the backend pool to route matching requests
	// into. Must reference a pool defined in Pools. Mutually exclusive
	// with Split — exactly one of Pool or Split must be set.
	Pool string
	// Split defines a weighted list of pools for canary-style traffic
	// splitting. Each entry specifies a pool name and a positive
	// integer weight. Traffic is distributed proportionally to weight.
	// Must contain at least 2 entries. Mutually exclusive with Pool.
	Split []SplitConfig
}

// HeaderConfig specifies an exact header match condition for a route.
// The header name is matched case-insensitively (per net/http.Header.Get
// semantics), and the value is matched exactly.
type HeaderConfig struct {
	// Name is the HTTP header name to match. Required.
	Name string
	// Value is the exact header value to match. Required.
	Value string
}

// SplitConfig describes a single entry in a weighted traffic split.
// Each entry maps a pool name to a relative weight. Traffic is
// distributed across entries proportionally to their weights.
type SplitConfig struct {
	// Pool is the name of the backend pool. Must reference a pool
	// defined in Pools.
	Pool string
	// Weight is the relative weight of this entry. Must be a positive
	// integer. Traffic is routed to this pool with probability
	// weight/total_weight.
	Weight int
}

// StickyConfig configures cookie-based sticky sessions for a pool.
// When present, the proxy sets a cookie identifying the backend chosen
// for a request. Subsequent requests carrying that cookie are routed
// directly to the same backend (if it is still in the pool and
// healthy), bypassing the pool's load balancing algorithm.
type StickyConfig struct {
	// CookieName is the name of the sticky cookie. Defaults to
	// "hoot_lb_sticky" when omitted.
	CookieName string
	// TTL is the cookie's Max-Age in seconds. Must be positive.
	// Defaults to 1h (3600s) when omitted.
	TTL time.Duration
}

// TLSConfig describes the TLS behavior for a listener. When present,
// the listener either terminates TLS (mode "terminate", valid only on
// HTTP listeners) or passes TLS connections through to backends with
// SNI-based routing (mode "passthrough", valid only on TCP listeners).
// When nil, the listener operates without TLS.
type TLSConfig struct {
	// Mode selects the TLS behavior. Must be one of "terminate" or
	// "passthrough".
	Mode string
	// Certificates lists the TLS certificates for terminate mode.
	// Each entry maps a hostname (SNI) to a cert/key pair on disk.
	// At most one entry may have an empty host, serving as the
	// default/fallback certificate when SNI is absent or unmatched.
	// Required when Mode is "terminate".
	Certificates []TLSCertConfig
	// Routes maps SNI hostnames to backend pools for passthrough
	// mode. When a TLS ClientHello arrives with a matching SNI, the
	// connection is routed to the specified pool. The listener's own
	// Pool field serves as the fallback when no route matches.
	// Required when Mode is "passthrough".
	Routes []TLSRouteConfig
	// HandshakeTimeout bounds how long the server waits for a TLS
	// ClientHello during the SNI parsing in passthrough mode.
	// Connections that send no data or send it too slowly are closed
	// after this duration, preventing resource exhaustion from slow
	// loris-style attacks. When zero, defaults to 10s. Only
	// meaningful when Mode is "passthrough".
	HandshakeTimeout time.Duration
	// MinVersion sets the minimum TLS version accepted. Valid values
	// are "tls12" and "tls13". When empty, defaults to "tls12".
	// TLS 1.0 and 1.1 are excluded because they use deprecated
	// cryptographic primitives and have known weaknesses.
	MinVersion string
}

// TLSCertConfig maps a hostname to a certificate and private key file
// on disk. An empty Host indicates the default/fallback certificate,
// served when the client's SNI is absent or does not match any other
// entry.
type TLSCertConfig struct {
	// Host is the SNI hostname this certificate covers. Must match
	// exactly. An empty value means this is the default certificate.
	Host string
	// CertFile is the path to the PEM-encoded certificate file.
	CertFile string
	// KeyFile is the path to the PEM-encoded private key file.
	KeyFile string
}

// TLSRouteConfig maps an SNI hostname to a backend pool for
// passthrough mode. Connections with a matching SNI are routed to the
// specified pool.
type TLSRouteConfig struct {
	// Host is the SNI hostname this route matches. Must match
	// exactly.
	Host string
	// Pool is the name of the backend pool for matching connections.
	Pool string
}

// HealthCheckConfig describes how the health checker probes backends
// in a pool. When omitted, defaults to TCP health checking on every
// backend with the default thresholds and intervals. Setting type to
// "none" disables all health checking (active and passive) for that
// pool, keeping all backends permanently healthy.
type HealthCheckConfig struct {
	// Type selects the probe protocol. Must be one of: "tcp", "http",
	// or "none". Defaults to "tcp" when omitted.
	Type string
	// Path is the HTTP path to probe when Type is "http". Must start
	// with "/" and is required only when Type is "http".
	Path string
	// Interval is how often each backend is probed. Must be positive.
	// Defaults to 5s.
	Interval time.Duration
	// Timeout is the maximum time to wait for a single probe to
	// complete. Must be positive and less than Interval. Defaults to 2s.
	Timeout time.Duration
	// HealthyThreshold is the number of consecutive successful probes
	// required to mark an unhealthy backend as healthy. Must be >= 1.
	// Defaults to 2.
	HealthyThreshold int
	// UnhealthyThreshold is the number of consecutive failed probes
	// required to mark a healthy backend as unhealthy. Must be >= 1.
	// Defaults to 3.
	UnhealthyThreshold int
}

// RateLimitConfig describes per-client rate limiting for a listener.
// When present on a listener, incoming requests are rate-limited per
// client IP using a token bucket algorithm. When nil, rate limiting
// is disabled for the listener.
type RateLimitConfig struct {
	// RequestsPerSecond is the sustained request rate allowed per
	// client IP. Must be positive.
	RequestsPerSecond float64
	// Burst is the maximum number of requests a client can make in a
	// single burst (bucket capacity). Must be positive.
	Burst int
	// ClientIdleEviction is how long a client's bucket is kept after
	// its last request before being evicted to free memory. Must be
	// positive. Defaults to 5m when omitted.
	ClientIdleEviction time.Duration
}

// CircuitBreakerConfig describes per-backend circuit breaking for a
// pool. When present on a pool, each backend gets its own circuit
// breaker that tracks consecutive failures and transitions between
// closed, open, and half-open states. When nil, circuit breaking is
// disabled for the pool.
type CircuitBreakerConfig struct {
	// FailureThreshold is the number of consecutive failures required
	// to open the circuit. Must be positive.
	FailureThreshold int
	// OpenDuration is how long the circuit stays open before
	// transitioning to half-open. Must be positive.
	OpenDuration time.Duration
	// HalfOpenMaxProbes is the number of concurrent probes allowed
	// during half-open. Extra requests during half-open are treated
	// as if the circuit were still open. Must be positive.
	HalfOpenMaxProbes int
}
