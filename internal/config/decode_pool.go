package config

import (
	"fmt"
	"time"
)

func decodePool(m map[string]interface{}, idx int) (*PoolConfig, error) {
	prefix := fmt.Sprintf("pools[%d]", idx)

	name, ok := stringField(m, "name")
	if !ok || name == "" {
		return nil, newFieldError(prefix+".name", "is required and must be a non-empty string")
	}

	pc := &PoolConfig{
		Name:      name,
		Algorithm: "weighted_round_robin",
	}

	algorithm, ok := stringField(m, "algorithm")
	if ok {
		pc.Algorithm = algorithm
	}

	backendsRaw, hasBackends := m["backends"]
	discoveryRaw, hasDiscovery := m["discovery"]

	if hasBackends && hasDiscovery {
		return nil, newFieldError(prefix, "backends and discovery are mutually exclusive — specify exactly one")
	}

	if hasBackends {
		items, ok := backendsRaw.([]interface{})
		if !ok {
			return nil, newFieldError(prefix+".backends", "must be a sequence of backend entries")
		}
		for i, item := range items {
			bm, ok := item.(map[string]interface{})
			if !ok {
				return nil, newFieldError(fmt.Sprintf("%s.backends[%d]", prefix, i), "must be a mapping")
			}
			bc, err := decodeBackend(bm, prefix, i)
			if err != nil {
				return nil, err
			}
			pc.Backends = append(pc.Backends, *bc)
		}
	} else if hasDiscovery {
		dm, ok := discoveryRaw.(map[string]interface{})
		if !ok {
			return nil, newFieldError(prefix+".discovery", "must be a mapping")
		}
		dc, err := decodeDiscovery(dm, prefix)
		if err != nil {
			return nil, err
		}
		pc.Discovery = dc
	} else {
		return nil, newFieldError(prefix+".backends", "is required when discovery is not specified")
	}

	if raw, ok := m["health_check"]; ok {
		hcm, ok := raw.(map[string]interface{})
		if !ok {
			return nil, newFieldError(prefix+".health_check", "must be a mapping")
		}
		hc, err := decodeHealthCheck(hcm, prefix)
		if err != nil {
			return nil, err
		}
		pc.HealthCheck = hc
	}

	if raw, ok := m["sticky"]; ok {
		sm, ok := raw.(map[string]interface{})
		if !ok {
			return nil, newFieldError(prefix+".sticky", "must be a mapping")
		}
		sc, err := decodeSticky(sm, prefix)
		if err != nil {
			return nil, err
		}
		pc.Sticky = sc
	}

	if raw, ok := m["circuit_breaker"]; ok {
		cbm, ok := raw.(map[string]interface{})
		if !ok {
			return nil, newFieldError(prefix+".circuit_breaker", "must be a mapping")
		}
		cb, err := decodeCircuitBreaker(cbm, prefix)
		if err != nil {
			return nil, err
		}
		pc.CircuitBreaker = cb
	}

	if raw, ok := m["retry"]; ok {
		rm, ok := raw.(map[string]interface{})
		if !ok {
			return nil, newFieldError(prefix+".retry", "must be a mapping")
		}
		rc, err := decodeRetry(rm, prefix)
		if err != nil {
			return nil, err
		}
		pc.Retry = rc
	}

	return pc, nil
}

func decodeDiscovery(m map[string]interface{}, poolPrefix string) (*DiscoveryConfig, error) {
	prefix := poolPrefix + ".discovery"

	dc := &DiscoveryConfig{}

	typ, ok := stringField(m, "type")
	if !ok || typ == "" {
		return nil, newFieldError(prefix+".type", "is required and must be one of: dns, consul")
	}
	dc.Type = typ

	switch typ {
	case "dns":
		dnsRaw, ok := m["dns"]
		if !ok {
			return nil, newFieldError(prefix+".dns", "is required when type is dns")
		}
		dnsM, ok := dnsRaw.(map[string]interface{})
		if !ok {
			return nil, newFieldError(prefix+".dns", "must be a mapping")
		}
		dnsCfg, err := decodeDNSDiscovery(dnsM, prefix)
		if err != nil {
			return nil, err
		}
		dc.DNS = dnsCfg

	case "consul":
		consulRaw, ok := m["consul"]
		if !ok {
			return nil, newFieldError(prefix+".consul", "is required when type is consul")
		}
		consulM, ok := consulRaw.(map[string]interface{})
		if !ok {
			return nil, newFieldError(prefix+".consul", "must be a mapping")
		}
		consulCfg, err := decodeConsulDiscovery(consulM, prefix)
		if err != nil {
			return nil, err
		}
		dc.Consul = consulCfg

	case "k8s":
		k8sRaw, ok := m["k8s"]
		if !ok {
			return nil, newFieldError(prefix+".k8s", "is required when type is k8s")
		}
		k8sM, ok := k8sRaw.(map[string]interface{})
		if !ok {
			return nil, newFieldError(prefix+".k8s", "must be a mapping")
		}
		k8sCfg, err := decodeK8sDiscovery(k8sM, prefix)
		if err != nil {
			return nil, err
		}
		dc.K8s = k8sCfg

	case "docker":
		dockerRaw, ok := m["docker"]
		if !ok {
			return nil, newFieldError(prefix+".docker", "is required when type is docker")
		}
		dockerM, ok := dockerRaw.(map[string]interface{})
		if !ok {
			return nil, newFieldError(prefix+".docker", "must be a mapping")
		}
		dockerCfg, err := decodeDockerDiscovery(dockerM, prefix)
		if err != nil {
			return nil, err
		}
		dc.Docker = dockerCfg

	default:
		return nil, newFieldError(prefix+".type", "must be one of: dns, consul, k8s, docker (got %q)", typ)
	}

	return dc, nil
}

func decodeDNSDiscovery(m map[string]interface{}, prefix string) (*DNSDiscoveryConfig, error) {
	dnsPrefix := prefix + ".dns"

	host, ok := stringField(m, "host")
	if !ok || host == "" {
		return nil, newFieldError(dnsPrefix+".host", "is required and must be a non-empty string")
	}

	cfg := &DNSDiscoveryConfig{Host: host}

	if raw, ok := m["port"]; ok {
		s, ok := raw.(string)
		if !ok {
			return nil, newFieldError(dnsPrefix+".port", "must be an integer")
		}
		p, err := parseInt(s)
		if err != nil {
			return nil, newFieldError(dnsPrefix+".port", "must be an integer, got %q", s)
		}
		cfg.Port = p
	}

	if raw, ok := m["refresh_interval"]; ok {
		s, ok := raw.(string)
		if !ok {
			return nil, newFieldError(dnsPrefix+".refresh_interval", "must be a duration string")
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, newFieldError(dnsPrefix+".refresh_interval", "invalid duration %q: %v", s, err)
		}
		cfg.RefreshInterval = d
	}

	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = 30 * time.Second
	}

	return cfg, nil
}

func decodeConsulDiscovery(m map[string]interface{}, prefix string) (*ConsulDiscoveryConfig, error) {
	consulPrefix := prefix + ".consul"

	address, ok := stringField(m, "address")
	if !ok || address == "" {
		return nil, newFieldError(consulPrefix+".address", "is required and must be a non-empty string")
	}

	service, ok := stringField(m, "service")
	if !ok || service == "" {
		return nil, newFieldError(consulPrefix+".service", "is required and must be a non-empty string")
	}

	cfg := &ConsulDiscoveryConfig{
		Address: address,
		Service: service,
	}

	if raw, ok := m["refresh_interval"]; ok {
		s, ok := raw.(string)
		if !ok {
			return nil, newFieldError(consulPrefix+".refresh_interval", "must be a duration string")
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, newFieldError(consulPrefix+".refresh_interval", "invalid duration %q: %v", s, err)
		}
		cfg.RefreshInterval = d
	}

	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = 30 * time.Second
	}

	return cfg, nil
}

func decodeSticky(m map[string]interface{}, poolPrefix string) (*StickyConfig, error) {
	prefix := poolPrefix + ".sticky"

	sc := &StickyConfig{
		CookieName: "hoot_lb_sticky",
		TTL:        1 * time.Hour,
	}

	if v, ok := stringField(m, "cookie_name"); ok {
		sc.CookieName = v
	}
	if v, ok := stringField(m, "ttl"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, newFieldError(prefix+".ttl", "invalid duration %q: %v", v, err)
		}
		sc.TTL = d
	}

	return sc, nil
}

func decodeBackend(m map[string]interface{}, poolPrefix string, idx int) (*BackendConfig, error) {
	prefix := fmt.Sprintf("%s.backends[%d]", poolPrefix, idx)

	addr, ok := stringField(m, "address")
	if !ok || addr == "" {
		return nil, newFieldError(prefix+".address", "is required and must be a non-empty string")
	}

	bc := &BackendConfig{Address: addr, Weight: 1}
	if raw, ok := m["weight"]; ok {
		s, ok := raw.(string)
		if !ok {
			return nil, newFieldError(prefix+".weight", "must be an integer")
		}
		w, err := parseInt(s)
		if err != nil {
			return nil, newFieldError(prefix+".weight", "must be an integer, got %q", s)
		}
		bc.Weight = w
	}
	return bc, nil
}

func decodeCircuitBreaker(m map[string]interface{}, poolPrefix string) (*CircuitBreakerConfig, error) {
	prefix := poolPrefix + ".circuit_breaker"

	cb := &CircuitBreakerConfig{
		FailureThreshold:  5,
		OpenDuration:      30 * time.Second,
		HalfOpenMaxProbes: 1,
	}

	if v, ok := m["failure_threshold"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, newFieldError(prefix+".failure_threshold", "must be an integer")
		}
		n, err := parseInt(s)
		if err != nil {
			return nil, newFieldError(prefix+".failure_threshold", "must be an integer, got %q", s)
		}
		cb.FailureThreshold = n
	}
	if v, ok := m["open_duration"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, newFieldError(prefix+".open_duration", "must be a duration string")
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, newFieldError(prefix+".open_duration", "invalid duration %q: %v", s, err)
		}
		cb.OpenDuration = d
	}
	if v, ok := m["half_open_max_probes"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, newFieldError(prefix+".half_open_max_probes", "must be an integer")
		}
		n, err := parseInt(s)
		if err != nil {
			return nil, newFieldError(prefix+".half_open_max_probes", "must be an integer, got %q", s)
		}
		cb.HalfOpenMaxProbes = n
	}

	return cb, nil
}
