// Package discovery provides service discovery adapters for dynamic
// backend pool membership. Each adapter resolves a set of backend
// addresses from an external source (DNS, Consul, etc.) and returns
// them as Backend values that the load balancer can use.
package discovery

import "context"

// Backend represents a discovered upstream server. Unlike
// balancer.Backend, this is a plain data struct used during
// discovery resolution — the runtime converts these into
// balancer.Server instances.
type Backend struct {
	// Address is the dial address of the backend (e.g.
	// "10.0.0.1:8080").
	Address string
	// Weight is the relative weight for load balancing. DNS
	// discovery always sets this to 1 (DNS has no weight concept).
	Weight int
}

// Discovery resolves a set of backends from an external source.
// Implementations must be safe for concurrent use. A failed
// resolution must not produce an empty backend list — the caller
// retains the last-known-good set on error.
type Discovery interface {
	// Resolve queries the discovery source and returns the current set
	// of backends. An error indicates the source is temporarily
	// unreachable; the caller should keep the previous backend set
	// and retry on the next interval.
	Resolve(ctx context.Context) ([]Backend, error)
	// Name returns a human-readable identifier for this discovery
	// source, used in log messages.
	Name() string
}

// HostLookuper is the interface used by the DNS adapter to resolve
// hostnames. The standard library's *net.Resolver satisfies this
// interface, making the adapter testable without a real DNS server.
type HostLookuper interface {
	// LookupHost looks up the given host and returns a list of
	// addresses. The context controls cancellation and timeouts.
	LookupHost(ctx context.Context, host string) ([]string, error)
}
