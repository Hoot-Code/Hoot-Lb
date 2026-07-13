# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [v0.1.0] - 2025-01-01

### Added

- L4 TCP proxy with bidirectional relay
- L4 UDP proxy with session management
- L7 HTTP reverse proxy with route matching
- Five load balancing algorithms: round robin, weighted round robin,
  least connections, random, IP hash
- TLS termination (HTTP listeners)
- TLS SNI passthrough (TCP listeners)
- HTTP host/path/header-based routing
- Canary traffic splitting with weighted pool splits
- Cookie-based sticky sessions
- Active TCP and HTTP health checking with passive failure reporting
- Per-client token bucket rate limiting
- Per-backend circuit breaker (closed/open/half-open)
- Hot configuration reload via file polling and SIGHUP
- Zero-downtime binary restart via SIGUSR2 or admin API
- DNS-based service discovery
- Consul service discovery (consul build tag)
- Prometheus-compatible metrics (hand-rolled, no external deps)
- Structured JSON access logging
- Admin REST API for runtime control-plane operations
- Embedded web dashboard with live WebSocket updates
- Graceful shutdown for all listener types
- Maximum connection limits per listener
- TCP idle timeout
- Request body size limits (L7)
- Atomic certificate store for hot TLS rotation
- Zero external dependencies (Go standard library only)
