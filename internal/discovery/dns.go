package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
)

// DNS resolves backends by looking up a hostname via the standard
// library's DNS resolver (or any injectable HostLookuper). Each
// resolved IP address is combined with the configured port to form
// the backend address. DNS has no weight concept, so every discovered
// backend gets weight 1.
//
// Go's standard library resolver does not expose actual DNS record
// TTLs through the LookupHost API. The caller must set the poll
// interval at or below their actual DNS TTL — this adapter cannot
// auto-detect the correct refresh interval.
type DNS struct {
	lookuper HostLookuper
	host     string
	port     int
	logger   *slog.Logger
	name     string
}

// NewDNS creates a DNS discovery adapter. The lookuper is typically
// net.DefaultResolver or a *net.Resolver — anything satisfying
// HostLookuper. The host is the DNS name to resolve (e.g.
// "backend.service.consul"). If port is non-zero, it is appended to
// each resolved IP as "ip:port"; if zero and host contains a port
// suffix, that port is used.
func NewDNS(name string, lookuper HostLookuper, host string, port int, logger *slog.Logger) *DNS {
	return &DNS{
		lookuper: lookuper,
		host:     host,
		port:     port,
		logger:   logger,
		name:     name,
	}
}

// Resolve looks up the configured hostname and returns the resolved
// addresses as Backends. Each address gets weight 1 (DNS has no
// weight concept). On failure, it returns an error; the caller
// retains the previous backend set.
func (d *DNS) Resolve(ctx context.Context) ([]Backend, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	host := d.host
	port := d.port
	if h, p, err := net.SplitHostPort(host); err == nil {
		host = h
		if port == 0 {
			port, _ = strconv.Atoi(p)
		}
	}

	ips, err := d.lookuper.LookupHost(lookupCtx, host)
	if err != nil {
		return nil, fmt.Errorf("dns lookup %q: %w", d.host, err)
	}

	backends := make([]Backend, 0, len(ips))
	for _, ip := range ips {
		var addr string
		if port > 0 {
			addr = net.JoinHostPort(ip, strconv.Itoa(port))
		} else {
			addr = ip
		}
		backends = append(backends, Backend{
			Address: addr,
			Weight:  1,
		})
	}

	d.logger.Debug("dns resolution",
		slog.String(logging.ComponentKey, "discovery"),
		slog.String("host", d.host),
		slog.Int("backends", len(backends)))

	return backends, nil
}

// Name returns the identifier for this discovery source.
func (d *DNS) Name() string { return d.name }
