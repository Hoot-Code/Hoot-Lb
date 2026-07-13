package config

import "time"

// GlobalConfig holds process-wide settings that are not specific to any
// single listener or backend pool.
type GlobalConfig struct {
	// LogLevel controls the verbosity of the structured logger. Must be
	// one of "debug", "info", "warn", or "error".
	LogLevel string
	// ShutdownTimeout bounds how long the process waits for in-flight
	// connections to drain during a graceful shutdown before forcing
	// an exit.
	ShutdownTimeout time.Duration
	// ReloadCheckInterval controls how often the config file is polled
	// for changes. When zero or omitted, config polling is disabled
	// (only SIGHUP triggers reload). Accepts any value parseable by
	// Go's time.ParseDuration, e.g. "5s", "30s".
	ReloadCheckInterval time.Duration
	// MaxConnectionsPerListener caps the number of concurrent
	// connections accepted per TCP/HTTP listener. When zero, the
	// limit is unlimited (existing behavior). UDP listeners are
	// excluded because connection-oriented counting does not map
	// cleanly to datagram protocols.
	MaxConnectionsPerListener int
	// TCPIdleTimeout is the maximum time a TCP or TLS passthrough
	// relay may remain idle (no data flowing in either direction)
	// before the connection is closed. When zero, idle connections
	// are never timed out (existing behavior). This is not a total
	// connection lifetime cap — the deadline resets whenever data is
	// forwarded in either direction.
	TCPIdleTimeout time.Duration
	// Metrics configures the Prometheus-compatible metrics endpoint.
	Metrics MetricsConfig
	// AccessLog configures structured access logging.
	AccessLog AccessLogConfig
	// Admin configures the admin REST API control plane.
	Admin AdminConfig
}

// MetricsConfig configures the Prometheus-compatible metrics endpoint.
type MetricsConfig struct {
	// Enabled controls whether the metrics HTTP listener is started.
	// Default true.
	Enabled *bool
	// Address is the host:port the metrics endpoint binds to.
	// Default "127.0.0.1:9090" (loopback-only).
	Address string
	// Path is the HTTP path where metrics are exposed.
	// Default "/metrics".
	Path string
}

// AccessLogConfig configures structured access logging for completed
// connections and requests.
type AccessLogConfig struct {
	// Enabled controls whether access log lines are emitted.
	// Default true.
	Enabled *bool
	// Format is the output format. Only "json" is supported.
	Format string
}

// AdminConfig configures the admin REST API, which provides runtime
// control-plane endpoints for backend management. The admin API runs
// on its own dedicated listener, separate from proxied traffic and
// the metrics endpoint.
type AdminConfig struct {
	// Enabled controls whether the admin API listener is started.
	// When nil, defaults to false (disabled).
	Enabled *bool
	// Address is the host:port the admin API binds to.
	// Default "127.0.0.1:9091" (loopback-only).
	Address string
	// TokenEnv is the name of an environment variable that holds
	// the bearer token for authentication. The token is read from
	// this env var at startup; never put the token literally in the
	// config file.
	TokenEnv string
	// MaxConcurrentRequests is the maximum number of admin API
	// requests processed concurrently. Requests beyond this limit
	// receive an immediate 503 response. Default 10.
	MaxConcurrentRequests int
	// MTLS configures mutual TLS for the admin API. When enabled,
	// client certificates signed by the configured CA are required
	// in addition to bearer token authentication.
	MTLS *AdminMTLSConfig
	// Roles configures role-based access control for the admin API.
	// When present, replaces the single TokenEnv auth model.
	Roles []AdminRoleConfig
	// AuditLog configures the audit log for admin API mutations.
	AuditLog *AdminAuditLogConfig
}

// AdminMTLSConfig configures mutual TLS for the admin API.
type AdminMTLSConfig struct {
	// Enabled controls whether client certificates are required.
	Enabled bool
	// CAFile is the path to the PEM-encoded CA certificate used to
	// verify client certificates.
	CAFile string
}

// AdminRoleConfig defines a role with a specific set of permissions.
type AdminRoleConfig struct {
	// TokenEnv is the name of an environment variable that holds
	// the bearer token for this role.
	TokenEnv string
	// Permissions is the set of permissions granted to this role.
	// Valid values: read, drain, restart, backends, config.
	Permissions []string
}

// AdminAuditLogConfig configures the audit log for admin API mutations.
type AdminAuditLogConfig struct {
	// Enabled controls whether audit logging is active.
	Enabled bool
}
