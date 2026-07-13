# Hoot-Lb

A lightweight, dependency-free load balancer in Go — a leaner
alternative to NGINX/HAProxy/Traefik, supporting both L4 (TCP/UDP) and
L7 (HTTP) load balancing.

## Comparison with other load balancers

| Feature | Hoot-Lb | NGINX | HAProxy | Traefik |
|---------|---------|-------|---------|---------|
| Binary size | ~8 MB | ~2 MB | ~2 MB | ~30 MB |
| External dependencies | 0 (stdlib only) | Many | Many | Many |
| Config format | YAML (1 file) | nginx.conf | haproxy.cfg | YAML/TOML/labels |
| L4 TCP/UDP | Yes | Yes | Yes | TCP only |
| L7 HTTP | Yes | Yes | Yes | Yes |
| Hot reload | Yes (SIGHUP/polling) | Yes (signal) | Yes (socket) | Yes (dynamic) |
| Zero-downtime restart | Yes (FD handoff) | No | Yes (runtime API) | Yes |
| Service discovery | DNS, Consul | DNS, Consul (commercial) | DNS, Consul, etcd | Docker, K8s, Consul, etcd |
| Dashboard | Built-in | Third-party | Stats socket | Built-in |
| Rate limiting | Per-client token bucket | Limit_req zone | Stick-table | InFlightReq |
| Circuit breaking | Built-in | No | No | Yes |
| Admin API | REST + WebSocket | nginx plus ($) | Runtime API | API |

Hoot-Lb trades raw throughput for simplicity: zero configuration files,
zero package managers, zero build-time dependencies. The entire binary
is built from Go's standard library alone.

## Layout

```
cmd/lb/              entrypoint: flag parsing, config load, startup banner, signal-based shutdown
internal/config/     YAML config schema, parser, and validation
  global.go          GlobalConfig, MetricsConfig, AccessLogConfig, AdminConfig types
  types.go           Config, ListenerConfig, PoolConfig, TLSConfig, and related types
internal/balancer/   core Backend / LoadBalancer interfaces + algorithm implementations
  roundrobin.go      unweighted round-robin
  wrr.go             smooth weighted round-robin
  lc.go              least connections (with ConnReleaser)
  random.go          weight-aware random selection
  iphash.go          deterministic hash-based sticky routing
  connreleaser.go    optional ConnReleaser interface
  backend.go         concrete Backend (Server) implementation
  types.go           Backend and LoadBalancer interfaces
internal/health/     HealthChecker interface, FailureReporter interface, TCP/HTTP active checkers
internal/logging/    structured logging (log/slog) setup and shared field-name constants
internal/tlsutil/    atomic certificate store for hot rotation
  certstore.go       CertStore with GetCertificate and Replace
internal/proxy/l4/   L4 TCP and UDP proxy engines (with passive failure reporting)
  tcp.go             TCP proxy server and relay
  udp.go             UDP proxy server with session management
  clienthello.go     TLS ClientHello parser (SNI extraction, no crypto)
  tls_passthrough.go TLS SNI passthrough server
  types.go           PoolStateGetter and SNIRouteGetter for hot reload
internal/proxy/l7/   L7 HTTP reverse proxy engine (routing, streaming, health integration)
  director.go        route matching, Backend selection, context propagation, split routing
  transport.go       RoundTripper wrapper with Acquire/Release/ReportFailure
  headers.go         hop-by-hop header stripping, X-Forwarded-For/X-Real-IP injection
  server.go          listener lifecycle, graceful shutdown, TLS termination, sticky cookie setting
  tls_helpers.go     TLS min version, cipher suite selection, body size limit handler
  sticky.go          sticky session cookie validation and pool membership check
internal/runtime/    atomic runtime state for hot reload
  snapshot.go        Snapshot type, BuildSnapshot, DiffListeners, AtomicSnapshot
  partial_update.go  UpdatePoolBackends for discovery-driven pool membership changes
  poller.go          Poller wrapping Discovery adapters for periodic backend resolution
  discovery_default.go  adapter factory (default build)
  discovery_consul.go   adapter factory (consul build tag)
internal/metrics/    Prometheus-compatible metrics (hand-rolled, no external deps)
  registry.go        Counter, Gauge, Histogram, Vec types, Registry
  exposition.go      Prometheus text exposition format writer
  server.go          Dedicated metrics HTTP listener
  accesslog.go       Structured JSON access logger
  cleanup.go         Cardinality cleanup helper for removed backends
internal/admin/        admin REST API (control plane) + embedded dashboard
  types.go             request/response JSON shapes
  auth.go              bearer token middleware (constant-time)
  handlers.go          endpoint handler functions
  server.go            listener lifecycle, concurrency limiter, dashboard routing, WS conn tracking for graceful shutdown
  dashboard/           embedded web dashboard, isolated from the REST handlers above
    embed.go             go:embed static assets, AssetHandler
    websocket.go         hand-rolled RFC 6455 handshake, framing, push/disconnect-detection loop
    snapshot_feed.go     Snapshot/Feed: builds dashboard state from the runtime snapshot + metrics registry
    static/              index.html, style.css, app.js — vanilla JS, zero CDN dependencies
internal/ratelimit/   per-client token bucket rate limiting
  limiter.go          Limiter with per-IP buckets, idle eviction sweep
internal/circuitbreaker/  per-backend circuit breaker
  breaker.go          Breaker: closed/open/half-open state machine
internal/restart/    zero-downtime restart via FD handoff (SIGUSR2, admin API)
  restart.go        Trigger, ReconstructListeners, FD mapping, readiness pipe
  signal_unix.go    SIGUSR2 handler (Linux/Darwin)
  signal_stub.go    platform guard (unsupported OS)
internal/reload/     config file watcher and hot reload orchestration
  watcher.go         polling watcher, SIGHUP handler, validate/diff/swap
internal/discovery/  service discovery adapters
  types.go           Discovery interface, Backend struct, HostLookuper
  static.go          static (fixed) adapter
  dns.go             DNS adapter with injectable lookup
examples/config.yaml sample config exercising every supported field
```

## Building and running

This project has **zero external dependencies** — standard library
only, by design. Requires Go 1.22+.

```sh
go build ./...
go vet ./...

go run ./cmd/lb -config examples/config.yaml
```

Send SIGINT/SIGTERM (e.g. Ctrl-C) to shut down.

## Configuration

See `examples/config.yaml` for a fully-annotated example. The schema
has three top-level sections:

- `global` — process-wide settings (log level, graceful shutdown
  timeout).
- `listeners` — network listeners to open (name, address, protocol,
  which pool they route into, and optional TLS configuration).
- `pools` — named groups of weighted backends that listeners route
  traffic into. Each pool selects a load balancing algorithm via the
  `algorithm` field and optionally configures health checking via the
  `health_check` field and sticky sessions via the `sticky` field.

### HTTP routing

HTTP listeners support optional host/path/header-based routing via
the `routes` field. When present, incoming requests are matched against
the routes before falling back to the listener's default `pool`.

```yaml
listeners:
  - name: web
    address: "0.0.0.0:8080"
    protocol: http
    pool: web_pool
    routes:
      - host: api.example.com
        path_prefix: /v2
        pool: api_pool
      - path_prefix: /static
        pool: static_pool
      - path_prefix: /beta
        header:
          name: X-Beta-User
          value: "true"
        pool: beta_pool
```

Routes are matched by longest `path_prefix` among those whose `host`
and `header` conditions match (empty host matches any request host,
absent header condition matches any request). If multiple routes have
the same prefix length, the first in config order wins. If no route
matches, the listener's default `pool` is used.

All conditions on a route are ANDed: host, path_prefix, and header
must all match for the route to match.

The `routes` field is only valid on HTTP listeners — specifying it on
TCP or UDP listeners is a validation error.

#### Header matching

Routes can include an optional `header` condition for exact name+value
matching against the incoming request's headers. The header name is
matched case-insensitively (per HTTP semantics, which
`net/http.Header.Get` already provides).

```yaml
routes:
  - path_prefix: /beta
    header:
      name: X-Beta-User
      value: "true"
    pool: beta_pool
```

### Canary traffic splitting

Instead of routing to a single pool, a route can define a `split` — a
weighted list of pools for canary-style traffic distribution. Traffic
is distributed proportionally to weight via per-request random
selection. The `pool` and `split` fields are mutually exclusive on the
same route.

```yaml
routes:
  - path_prefix: /checkout
    split:
      - pool: checkout_stable
        weight: 90
      - pool: checkout_canary
        weight: 10
```

**Validation rules for split:**

- Must contain at least 2 entries.
- All weights must be positive integers.
- Every referenced pool must exist in the `pools` section.
- `pool` and `split` cannot both be specified on the same route.

### Sticky sessions

Pools can enable cookie-based sticky sessions via the `sticky` block.
When enabled, the proxy sets a cookie identifying the backend chosen
for a request. Subsequent requests carrying that cookie are routed
directly to the same backend (if it is still in the pool and healthy),
bypassing the pool's load balancing algorithm.

```yaml
pools:
  - name: web_pool
    sticky:
      cookie_name: hoot_lb_sticky    # default "hoot_lb_sticky"
      ttl: 1h                        # cookie Max-Age; default 1h
    backends:
      - address: "10.0.0.1:8080"
        weight: 1
      - address: "10.0.0.2:8080"
        weight: 1
```

**Behavior:**

- **Sticky cookie present and valid**: routes directly to the
  previously selected backend (if it's still in the pool and healthy).
- **No cookie / invalid cookie / backend removed or unhealthy**:
  falls back to normal `Pick` and sets a fresh sticky cookie.
- **Sticky + ip_hash**: sticky takes priority over ip_hash when a
  valid cookie is present. On a fresh visit, Pick runs normally (which
  may itself be ip_hash), and the result becomes the newly-stuck
  backend.

Cookie attributes: `Secure`, `HttpOnly`, `SameSite=Lax`, `Path=/`,
`Max-Age` matching the configured TTL. The cookie value is the backend
address — no cryptographic signing is needed because the value is only
trusted after verifying the backend is a current member of the pool.

**Validation:** `ttl` must be positive when `sticky` is present.

### TLS

Listeners can optionally enable TLS via the `tls` block. Two modes
are supported:

#### TLS Termination (HTTP listeners)

The proxy decrypts incoming TLS traffic and forwards plain HTTP to
backends. The client sees the proxy's certificate; the backend never
sees encrypted traffic.

```yaml
listeners:
  - name: web_tls
    address: "0.0.0.0:8443"
    protocol: http
    pool: web_pool
    tls:
      mode: terminate
      certificates:
        - host: "example.com"       # SNI match
          cert_file: "/path/cert.pem"
          key_file: "/path/key.pem"
        - host: ""                   # empty = default/fallback cert
          cert_file: "/path/default-cert.pem"
          key_file: "/path/default-key.pem"
```

- **`mode: terminate`** — requires `protocol: http`.
- **`certificates`** — at least one entry required. Each entry maps
  an SNI hostname to a cert/key file pair. At most one entry may have
  an empty `host`, serving as the default when SNI is absent or
  unmatched.
- **`min_version`** — minimum TLS version accepted. Valid values:
  `"tls12"` (default) or `"tls13"`. TLS 1.0 and 1.1 are excluded
  because they use deprecated cryptographic primitives (RFC 8996).
  When set to `"tls13"`, only TLS 1.3 connections are accepted.

**Cipher suites (TLS 1.2 termination):**

When `min_version` is `"tls12"` (default), the following AEAD cipher
suites are enabled (Go's default secure set for TLS 1.2):

- `TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384`
- `TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384`
- `TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256`
- `TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256`
- `TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305`
- `TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305`

When `min_version` is `"tls13"`, cipher suites are not configurable
(TLS 1.3 cipher suites are enforced by the Go runtime).

#### TLS SNI Passthrough (TCP listeners)

The proxy inspects the TLS ClientHello to extract the SNI hostname,
routes the connection to the appropriate backend pool, and relays the
original encrypted bytes unmodified. The proxy never holds a private
key and never decrypts traffic — the backend performs its own TLS
handshake with the client.

```yaml
listeners:
  - name: passthrough_tls
    address: "0.0.0.0:9443"
    protocol: tcp
    pool: default_pool              # fallback when SNI doesn't match
    tls:
      mode: passthrough
      routes:
        - host: "a.example.com"
          pool: pool_a
        - host: "b.example.com"
          pool: pool_b
```

- **`mode: passthrough`** — requires `protocol: tcp`.
- **`routes`** — maps SNI hostnames to backend pools. The listener's
  own `pool` field serves as the fallback when no route matches or SNI
  is absent.

### Load balancing algorithms

The `algorithm` field on each pool selects the load balancing
strategy. Must be one of:

- **`round_robin`** — simple unweighted round-robin. Cycles through
  backends in order, ignoring weight. Good when all backends are equal.
- **`weighted_round_robin`** (default) — smooth weighted round-robin
  using the interleaved algorithm. Backends with higher weight receive
  proportionally more traffic without bursty clumping.
- **`least_conn`** — selects the backend with the fewest active
  (in-flight) connections. Uses weight as a tiebreaker. Requires the
  proxy layer to call Acquire/Release via the ConnReleaser interface.
- **`random`** — uniform random selection with probability
  proportional to weight. Good when a simple, stateless algorithm
  suffices.
- **`ip_hash`** — deterministic hash-based selection. The same client
  IP always maps to the same backend (as long as the backend set
  doesn't change). Useful for session affinity at both L4 and L7.

When omitted, `algorithm` defaults to `weighted_round_robin`.

### Health checking

Each pool can optionally configure health checking via the
`health_check` block. When omitted, the pool defaults to TCP health
checking with the default thresholds.

```yaml
health_check:
  type: tcp              # tcp | http | none (default: tcp)
  path: /healthz         # required only when type: http
  interval: 5s           # default 5s
  timeout: 2s            # default 2s
  healthy_threshold: 2   # consecutive successes to mark healthy (default 2)
  unhealthy_threshold: 3 # consecutive failures to mark unhealthy (default 3)
```

**Fields:**

- **`type`** — `tcp` probes by opening a TCP connection (liveness
  check). `http` probes by sending a GET request to `path` and
  expecting a 2xx response. `none` disables all health checking for
  the pool — backends stay permanently healthy. This is a deliberate
  escape hatch for backends that don't support standard probes.
- **`path`** — the HTTP path to probe when `type` is `http`. Must
  start with `/`. Required when `type` is `http`.
- **`interval`** — how often each backend is probed. Must be positive.
- **`timeout`** — maximum time to wait for a single probe. Must be
  positive and less than `interval`.
- **`healthy_threshold`** — consecutive successful probes required to
  mark an unhealthy backend as healthy. Must be >= 1.
- **`unhealthy_threshold`** — consecutive failed probes required to
  mark a healthy backend as unhealthy. Must be >= 1.

**Active vs. passive checking:**

- **Active** checking runs one goroutine per backend, each probing on
  the configured interval with staggered initial delays (to avoid
  synchronized probe bursts). Uses hysteresis thresholds to prevent
  flapping — a single failure never immediately marks a healthy
  backend unhealthy, and vice versa.
- **Passive** checking notifies the health subsystem immediately when a
  real client dial fails (not a synthetic probe). A single dial failure
  marks the backend unhealthy immediately — no threshold. This is a
  confirmed failure on real traffic. Recovery still requires
  `healthy_threshold` consecutive active successes.

**Validation rules:**

- `interval` and `timeout` must be positive.
- `timeout` must be less than `interval`.
- Both thresholds must be >= 1.
- `path` is required when `type` is `http` and must start with `/`.
- `type` must be one of: `tcp`, `http`, `none`.

Configuration is validated at load time; errors name the specific
field that's wrong (e.g. `pools[0].health_check.timeout: must be less
than interval`) rather than failing generically.

Note: there is intentionally no external YAML library dependency, so
`internal/config` includes a small parser for the subset of YAML this
schema needs (nested mappings, sequences of mappings/scalars, quoted
strings, comments). It is not a general-purpose YAML parser — see the
comment at the top of `internal/config/yaml.go` before extending the
schema with YAML features it doesn't yet handle.

### Per-client rate limiting

Listeners can optionally enable per-client rate limiting via the
`rate_limit` block. Each client IP gets its own token bucket with a
configurable sustained rate and burst capacity. When a client exceeds
its limit, the request is rejected.

```yaml
listeners:
  - name: web
    rate_limit:
      requests_per_second: 100    # sustained rate per client IP
      burst: 200                   # bucket capacity (max burst)
      client_idle_eviction: 5m     # evict idle buckets after this TTL
```

**Rejection behavior:**

- **L7 (HTTP)**: returns `429 Too Many Requests` with a
  `Retry-After: 1` header.
- **L4 (TCP/UDP)**: the connection is closed immediately after
  accept, before dialing any backend. There is no clean way to send
  an application-level error over raw TCP.

**Eviction:** idle buckets are swept by a background goroutine. A
bucket that hasn't seen a request for `client_idle_eviction` is
removed from memory to prevent unbounded growth. When omitted,
`client_idle_eviction` defaults to 5 minutes.

**Per-client isolation:** rate limits are tracked independently per
client IP. One client exhausting its budget does not affect any other
client.

**Validation:** `requests_per_second`, `burst`, and
`client_idle_eviction` must all be positive.

### Request body size limits (L7 only)

HTTP listeners can optionally cap the maximum request body size via
`max_request_body_bytes`. This prevents unbounded memory consumption
from oversized uploads. Requests exceeding the limit receive a
`413 Request Entity Too Large` response.

```yaml
listeners:
  - name: web
    protocol: http
    max_request_body_bytes: 10485760  # 10 MB
```

When `max_request_body_bytes` is zero (default), no limit is applied.

### Per-backend circuit breaking

Pools can optionally enable circuit breaking via the
`circuit_breaker` block. Each backend gets its own circuit breaker
that tracks consecutive failures and transitions between three
states:

```yaml
pools:
  - name: web_pool
    circuit_breaker:
      failure_threshold: 5        # consecutive failures to open
      open_duration: 30s           # cooldown before half-open probe
      half_open_max_probes: 1      # concurrent probes in half-open
```

**State machine:**

- **Closed** (normal): traffic flows freely.
  `failure_threshold` consecutive failures -> Open.
- **Open**: the backend is skipped by Pick(). After `open_duration`
  elapses -> Half-Open.
- **Half-Open**: up to `half_open_max_probes` concurrent requests
  are let through as live probes. Extra requests are treated as if
  the circuit were still open.
  - Probe succeeds -> Closed (reset failure count, normal traffic
    resumes).
  - Probe fails -> Open (restart `open_duration` cooldown).

**Independence from health:** circuit breaking is a separate axis
from health checking and draining. A backend can be healthy AND
circuit-open simultaneously. Health means "can we reach it at all"
(probed independently). Circuit breaking means "it's reachable but
failing too much on real traffic."

**Failure signal:** circuit breakers react to both dial failures AND
application-level failures (L7 responses with status >= 500). A
backend returning 500 repeatedly opens its circuit even though every
dial succeeded. 4xx responses are treated as client problems, not
backend failures.

**Validation:** `failure_threshold` and `half_open_max_probes` must
be positive integers. `open_duration` must be positive.

## Hot configuration reload

The load balancer supports zero-downtime hot reload of certain
configuration changes without restarting the process. Hot reload is
triggered by:

- **File polling**: The config file is polled at a configurable
  interval (default 5 seconds). When the file's mtime and size change,
  the new config is loaded and validated.
- **SIGHUP signal**: Send `kill -HUP <pid>` for an immediate reload
  without waiting for the next poll tick.

### What can be hot-reloaded

- **Pool composition**: Backend addresses, weights, load balancing
  algorithm, health check configuration, and sticky session settings.
- **Routes and splits**: HTTP listener route tables (host, path, header
  matching, canary splits) are rebuilt from the new config.
- **TLS certificates**: Certificate files for TLS-terminate listeners
  are reloaded from disk via the existing atomic CertStore mechanism.

### What requires a restart

- **Listener-level changes**: Adding/removing a listener, or changing
  its address, protocol, or TLS mode requires a full process restart.
  This is explicitly rejected during reload with a clear error message.
  Use SIGUSR2 or `POST /admin/restart` for zero-downtime restart.

### Configuration

```yaml
global:
  log_level: info
  shutdown_timeout: 10s
  reload_check_interval: 5s  # 0 or omitted disables polling
  max_connections_per_listener: 1000  # 0 = unlimited
  tcp_idle_timeout: 30s               # 0 = disabled
```

- `reload_check_interval`: How often the config file is polled for
  changes. Accepts any duration string (e.g. `"5s"`, `"30s"`). When
  zero or omitted, file polling is disabled and only SIGHUP triggers
  reload.
- `max_connections_per_listener`: Caps the number of concurrent
  connections accepted per TCP or HTTP listener. When a listener
  reaches this limit, new connections are closed immediately before
  reading any data. UDP listeners are excluded (connection-oriented
  counting does not map cleanly to datagram protocols). Default:
  unlimited (0).
- `tcp_idle_timeout`: Maximum time a TCP or TLS passthrough relay may
  remain idle (no data flowing in either direction) before the
  connection is closed. The deadline resets whenever data is forwarded
  in either direction, so this is an *idle* timeout — not a total
  connection lifetime cap. Default: disabled (0).

### Reload behavior

1. The new config is read and parsed.
2. Full validation is run (same pipeline as startup — no loose
   validation path for reload).
3. The listener set is diffed against the running configuration. Any
   listener-level change rejects the entire reload (all-or-nothing).
4. For TLS-terminate listeners, new certificates are loaded and
   atomically swapped into the existing CertStore.
5. A new Snapshot is built with fresh LoadBalancers, Servers, route
   tables, and HealthCheckers.
6. New HealthCheckers are started.
7. The Snapshot pointer is atomically swapped — all new connections/
   requests immediately see the new state.
8. Old HealthCheckers are stopped (after the swap, never before).

On any error at steps 1–4, the error is logged and the current
configuration remains untouched.

## Zero-downtime restart

For changes that hot reload cannot handle (listener address/protocol/TLS
mode), Hoot-Lb supports zero-downtime binary restart via the classic
Unix socket handoff pattern: the parent passes its already-bound
listening socket FDs to a freshly-exec'd child, the child inherits them
directly (no re-bind, no port-in-use race), signals readiness, and only
then does the parent stop accepting and drain its in-flight connections.

This is triggered by:

- **SIGUSR2**: Send `kill -USR2 <pid>` (same convention as
  Unicorn/Puma).
- **Admin API**: `POST /admin/restart` (same bearer-token auth as
  every other admin endpoint). Returns `202 Accepted` immediately;
  the handoff is asynchronous.

### Safety properties

- **Readiness handshake**: The child signals readiness over a pipe
  *before* the parent closes any of its own listeners. If the child
  fails to start (bad config, crash) within 15 seconds, the parent
  aborts the handoff and continues serving exactly as before.
- **Single-attempt guard**: A second SIGUSR2 or admin trigger while
  a handoff is already in progress is rejected with a clear log line.
  Exactly one fork/exec can be in progress at a time.
- **Binary upgrade**: Because `os.Executable()` is used, restarting
  after placing a new binary on disk picks up the *new* binary, not
  the old one.

### Platform constraint

FD-passing via fork/exec is only supported on Linux and Darwin. On
other platforms, the restart trigger returns a clear error.

### Out of scope

In-memory state (rate limiter buckets, circuit breaker counters,
metrics counters) is **not** preserved across a restart — the child
starts fresh with only the listening sockets handed off. This is
expected and documented.

## Service discovery

Pools can optionally use service discovery instead of a static backend
list. The `discovery` block on a pool configures a dynamic backend
source that is polled at a configured interval.

### DNS discovery

```yaml
pools:
  - name: web_pool
    algorithm: weighted_round_robin
    discovery:
      type: dns
      dns:
        host: "backend.service.consul"
        port: 8080
        refresh_interval: 30s
    health_check:
      type: tcp
```

**DNS-TTL caveat:** Go's standard library resolver does not expose
actual DNS record TTLs through the `LookupHost` API. The operator
**must** set `refresh_interval` at or below their actual DNS TTL.
This system cannot auto-detect the correct refresh interval.

**Weight behavior:** DNS discovery does not encode per-backend
weights. Every discovered backend defaults to weight 1. When the
pool's algorithm is `weighted_round_robin`, weights are effectively
uniform across all discovered backends — this is expected behavior.

### Consul discovery

Consul discovery is available only when built with the `consul` build
tag:

```sh
go build -tags consul ./...
```

```yaml
pools:
  - name: web_pool
    algorithm: weighted_round_robin
    discovery:
      type: consul
      consul:
        address: "http://127.0.0.1:8500"
        service: "web"
        refresh_interval: 30s
    health_check:
      type: tcp
```

If the config specifies `type: consul` but the binary was built
without `-tags consul`, config validation fails with a clear error.

### Discovery behavior

- A pool has EITHER static `backends` OR a `discovery` block — never
  both (config validation error).
- Discovery errors never empty a pool's backend list. The pool keeps
  serving whatever it last successfully resolved.
- On backend-set change, only the affected pool is rebuilt. Other
  pools are untouched (partial update via pointer identity).
- Health checkers follow start-before-stop ordering during partial
  updates.

## Admin API

Hoot-Lb provides an admin REST API for runtime control-plane operations.
By default it binds to loopback only (`127.0.0.1:9091`).

A live web dashboard rides on this same listener — see
[Dashboard](#dashboard) below. There is no separate dashboard config
block: enabling the admin server enables the dashboard, and disabling
it disables both.

### Configuration

```yaml
global:
  admin:
    enabled: true
    address: "127.0.0.1:9091"
    token_env: "HOOT_LB_ADMIN_TOKEN"
    max_concurrent_requests: 10
```

**Important:** The admin token is read from the environment variable
named in `token_env`. Never put the token literally in the config file.
The named env var must be present and non-empty at startup — the
process will fail fast with a clear error otherwise.

### Authentication

All admin endpoints require a bearer token via the `Authorization`
header:

```
Authorization: Bearer <token>
```

The token is compared using constant-time comparison to prevent
timing side-channel attacks. This applies to all endpoints,
including read-only ones, for a uniform mental model.

### Endpoints

| Method | Path | Body | Description |
|--------|------|------|-------------|
| GET | `/admin/pools` | — | List all pools with backend state |
| POST | `/admin/pools/{pool}/backends` | `{"address":"...","weight":N}` | Add a backend |
| DELETE | `/admin/pools/{pool}/backends/{address}` | — | Remove a backend |
| POST | `/admin/pools/{pool}/backends/{address}/drain` | — | Set draining state |
| POST | `/admin/pools/{pool}/backends/{address}/undrain` | — | Clear draining state |

**GET /admin/pools response:**

```json
{
  "pools": [{
    "name": "web_pool",
    "algorithm": "weighted_round_robin",
    "discovery": false,
    "backends": [{
      "address": "10.0.0.1:8080",
      "weight": 5,
      "healthy": true,
      "draining": false
    }]
  }]
}
```

**Error responses:**

| Status | Meaning |
|--------|---------|
| 401 | Missing or invalid bearer token |
| 404 | Pool or backend not found |
| 409 | Backend already exists, or pool uses discovery (add/remove) |
| 503 | Concurrency limit exceeded |

### Discovery-driven pool rejection

Admin-driven backend **add/remove** is only valid for pools with
static `backends` configuration. Pools using `discovery` reject
add/remove with a 409 error, because the discovery poller is that
pool's source of truth and would silently overwrite an admin-applied
change on its next tick.

**Drain/undrain** works on any pool's backends regardless of how
they're populated, since draining doesn't change membership — only
selectability.

### Draining semantics

A backend can be **healthy-and-draining** simultaneously — these are
independent axes. A draining backend:

- Is **never selected** for a new `Pick` (by any algorithm, by sticky
  session assignment, or as a fresh fallback when an existing sticky
  cookie is invalidated)
- Has **existing in-flight** connections/requests left alone — no
  active eviction is performed
- When **all** backends in a pool are draining (or unhealthy, or both),
  `Pick` returns the existing "no healthy backends available" error

### Runtime-only changes (not persisted)

Admin-driven backend additions and removals are **runtime-only** and
are **NOT** written back to the config file. A subsequent full
file-based reload (SIGHUP or file polling) will reset pool membership
to whatever the file currently says, discarding any admin-applied
membership changes. This is by design — the config file is the
source of truth for declared state.

### Dashboard

The embedded dashboard is served from the admin listener's root path
(`/`) — e.g. `http://127.0.0.1:9091/`. Open it in a browser:

- The page itself loads with **no authentication** — it's static
  HTML/CSS/JS, embedded into the binary via `go:embed`, with zero
  external dependencies (no CDN fonts, icons, or JS libraries; it
  works fully offline).
- On first load it prompts for the admin bearer token (the same one
  configured via `token_env`). The token is stored only in
  `sessionStorage` for that browser tab — never `localStorage`, so it
  doesn't persist across browser restarts.
- Every data-fetching call the page makes — both the REST calls behind
  the drain/undrain buttons and the live feed below — requires that
  token, exactly like any other admin API client.
- It shows one row per backend across all pools: pool name, address,
  weight, healthy/draining state, and a live connection count. A
  drain/undrain button per row calls the existing REST endpoints
  directly — the dashboard is a frontend wired against the admin API,
  not a new control plane.

**Live updates (WebSocket):** rather than the browser polling the REST
API on an interval (which would mean every open dashboard tab hammers
the admin API independently), the dashboard opens one WebSocket
connection to `/admin/ws` and the server pushes a fresh JSON snapshot
of pool/backend state every 1–2 seconds. This endpoint is a from-
scratch, hand-rolled implementation of the WebSocket protocol
(RFC 6455) — handshake, framing, the lot — since this project takes no
external dependencies and gorilla/websocket (or similar) is off the
table.

**Auth caveat for the WebSocket endpoint:** browsers' native
WebSocket API has no way to set a custom `Authorization` header on
the upgrade request. The token is instead passed as a query
parameter — `/admin/ws?token=...` — and validated server-side with the
same constant-time comparison the REST API uses. Because the token
travels in the URL, it can end up in anything that logs raw request
URLs; Hoot-Lb's own logging never does this — every log line near the
WebSocket handler logs the request path only, never the query string —
but if you put a reverse proxy or any other access-logging
infrastructure in front of the admin listener, configure it not to log
full URLs for this endpoint, or you'll leak the token into those logs
even though Hoot-Lb itself doesn't.

### Concurrency protection

The admin API has a self-protection concurrency cap
(`max_concurrent_requests`, default 10). Requests arriving when the
semaphore is full receive an immediate 503 response — this is not
a fair queue, it's a hard cap to prevent resource exhaustion.

## Testing

This project uses the Go standard library test framework exclusively.
All tests run with `-race` by default.

### Running tests

```sh
# Standard test suite (all packages, with race detector)
go test -race ./...

# Tests with consul build tag
go test -race -tags consul ./...

# Benchmarks (3s per benchmark)
go test -bench=. -benchtime=3s ./internal/balancer/ ./internal/metrics/ \
    ./internal/proxy/l4/ ./internal/proxy/l7/

# Load test (1000 concurrent requests, p99 assertion)
go test -tags load -run TestLoadTest100k -timeout 120s ./internal/proxy/l7/

# Chaos tests (requires building hoot-lb binary, ~30s each)
go test -tags chaos -timeout 300s ./internal/chaos/
```

### Benchmarks

Key latency-critical paths are benchmarked:

| Benchmark | Package | What it measures |
|-----------|---------|-----------------|
| `BenchmarkTCPRelay` | `proxy/l4` | Bidirectional TCP relay throughput (1KB–1MB payloads) |
| `BenchmarkHTTPProxy` | `proxy/l7` | Single round-trip through full L7 stack |
| `BenchmarkHTTPProxyParallel` | `proxy/l7` | L7 proxy under concurrent load |
| `BenchmarkPickRoundRobin` | `balancer` | Round-robin Pick latency (10 and 100 backends) |
| `BenchmarkPickLeastConn` | `balancer` | Least-connections Pick latency (10 and 100 backends) |
| `BenchmarkCounterAdd` | `metrics` | Atomic counter increment (~1.6ns/op) |
| `BenchmarkHistogramObserve` | `metrics` | Atomic histogram observe (~2.9ns/op) |

### Load test

`TestLoadTest100k` (build tag: `load`) fires 100 concurrent goroutines,
each making 1000 sequential HTTP requests through a real L7Server
backed by a real `httptest.Server`. Assertions:

- Zero failures across all 100,000 requests.
- p99 latency < 50ms (measured via sorted histogram).
- No goroutine leak after test completion.

### Chaos tests

Chaos tests (build tag: `chaos`) use real `os/exec` to start hoot-lb
as a child process — never in-process mocks. Each scenario tests
resilience under adverse conditions:

| Scenario | What it tests |
|----------|--------------|
| **Backend failure mid-traffic** | Kill one of 3 backends during sustained traffic; assert automatic failover within `health_check.interval * unhealthy_threshold` |
| **Config flapping** | Rewrite config + SIGHUP 20 times rapidly during traffic; assert process stays up and error rate stays below 1% |
| **SIGUSR2 restart under load** | Trigger zero-downtime binary restart with 10 concurrent client goroutines for 10s; assert zero dropped connections |
| **Resource exhaustion recovery** | Set `max_connections_per_listener: 50`, open 200 connections, verify 50 served / 150 refused, then verify recovery after release |

### Goroutine leak detection

Every component that starts goroutines and has a `Stop()`/`Close()`
method is covered by a goroutine-leak test using the
`runtime.NumGoroutine()` baseline pattern:

| Component | Leak test |
|-----------|-----------|
| `l4.TCPServer` | `TestTCPProxyClientClose`, `TestTCPProxyManyConnections` |
| `l4.UDPServer` | `TestUDPProxyManyDatagrams` |
| `l4.TLSPassthroughServer` | `TestTLSPassthroughGoroutineLeak` |
| `l7.L7Server` | `TestL7ServerCloseGoroutineLeak` |
| `admin.Server` (WebSocket) | `TestDashboardWebSocketGoroutineLeak` |
| `health.Checker` | `TestHealthCheckerGoroutineLeak` |
| `reload.Watcher` | `TestWatcherStopGoroutineLeak` |
| `ratelimit.Limiter` | `TestStopGoroutineLeak` |
| `runtime.Poller` | `TestPollerStopGoroutineLeak` |
| `circuitbreaker.Breaker` | `TestCircuitBreakerChurn` |

## Known limitations

- **No etcd service discovery.** Only DNS and Consul (via build tag)
  are supported. etcd integration may be added in a future release.
- **In-memory state is not preserved across restarts.** Rate limiter
  buckets, circuit breaker counters, and metrics counters reset when
  the process restarts (even during zero-downtime FD handoff).
- **DNS discovery cannot auto-detect TTL.** The operator must set
  `refresh_interval` at or below their actual DNS TTL.
- **No active connection draining on backend removal.** When a backend
  is removed, existing in-flight connections are left to complete
  naturally — there is no forced drain of active connections.

## Roadmap

Future work may include:

- Additional service discovery adapters (etcd, Consul long-poll)
- Connection draining with configurable eviction on backend removal
- Request queuing when all backends are at capacity

## Metrics

Hoot-Lb exposes Prometheus-compatible metrics on a dedicated HTTP
listener, separate from proxied traffic. By default it binds to
loopback only (`127.0.0.1:9090`).

### Configuration

```yaml
global:
  metrics:
    enabled: true            # default true
    address: "127.0.0.1:9090"
    path: "/metrics"          # default "/metrics"
```

### Exposed metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `lb_connections_total` | Counter | listener, pool, backend, protocol | Total connections established |
| `lb_connections_active` | Gauge | listener, pool, backend, protocol | Currently active connections |
| `lb_request_duration_seconds` | Histogram | listener, pool, backend | L7 request duration (fixed buckets: 5ms–10s) |
| `lb_bytes_transferred_total` | Counter | listener, pool, backend, direction | Bytes transferred (direction: upstream/downstream) |
| `lb_dial_failures_total` | Counter | listener, pool, backend | Total dial failures |
| `lb_backend_healthy` | Gauge | pool, backend | Backend health status (1=healthy, 0=unhealthy) |

### Hot-path performance

All counter/gauge/histogram increment operations use atomic operations
only — no mutex on the hot path. Benchmarks show ~1.6ns/op for
Counter/Gauge and ~2.9ns/op for Histogram on Apple M4.

### Cardinality cleanup

When backends are removed from a pool (via discovery update or config
reload), their metric label entries are automatically cleaned up to
prevent unbounded cardinality growth.

## Access logging

Structured JSON access logs are emitted for every completed connection
(L4) or request (L7) to stdout.

### Configuration

```yaml
global:
  access_log:
    enabled: true
    format: json              # only "json" for now
```

### Fields

| Field | Type | Description |
|-------|------|-------------|
| `timestamp` | string | RFC3339Nano timestamp |
| `listener` | string | Listener name |
| `pool` | string | Backend pool name |
| `backend` | string | Backend address |
| `protocol` | string | Protocol (tcp, udp, http) |
| `client_addr` | string | Client IP address |
| `duration_ms` | int | Connection/request duration in milliseconds |
| `bytes_sent` | int | Bytes sent upstream |
| `bytes_received` | int | Bytes received downstream |
| `method` | string | HTTP method (L7 only, omitted for L4) |
| `path` | string | HTTP path (L7 only, omitted for L4) |
| `status_code` | int | HTTP status code (L7 only, omitted for L4) |
