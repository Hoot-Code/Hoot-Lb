package config

import (
	"fmt"
	"strconv"
	"time"
)

func decodeListener(m map[string]interface{}, idx int) (*ListenerConfig, error) {
	prefix := fmt.Sprintf("listeners[%d]", idx)

	name, ok := stringField(m, "name")
	if !ok || name == "" {
		return nil, newFieldError(prefix+".name", "is required and must be a non-empty string")
	}
	addr, ok := stringField(m, "address")
	if !ok || addr == "" {
		return nil, newFieldError(prefix+".address", "is required and must be a non-empty string")
	}
	proto, ok := stringField(m, "protocol")
	if !ok || proto == "" {
		return nil, newFieldError(prefix+".protocol", "is required and must be one of: tcp, udp, http")
	}
	pool, _ := stringField(m, "pool")

	lc := &ListenerConfig{
		Name:     name,
		Address:  addr,
		Protocol: proto,
		Pool:     pool,
	}

	if raw, ok := m["routes"]; ok {
		routesRaw, ok := raw.([]interface{})
		if !ok {
			return nil, newFieldError(prefix+".routes", "must be a sequence of route entries")
		}
		for j, item := range routesRaw {
			rm, ok := item.(map[string]interface{})
			if !ok {
				return nil, newFieldError(fmt.Sprintf("%s.routes[%d]", prefix, j), "must be a mapping")
			}
			route, err := decodeRoute(rm, prefix, j)
			if err != nil {
				return nil, err
			}
			lc.Routes = append(lc.Routes, *route)
		}
	}

	if raw, ok := m["tls"]; ok {
		tlsMap, ok := raw.(map[string]interface{})
		if !ok {
			return nil, newFieldError(prefix+".tls", "must be a mapping")
		}
		tlsCfg, err := decodeTLS(tlsMap, prefix)
		if err != nil {
			return nil, err
		}
		lc.TLS = tlsCfg
	}

	if raw, ok := m["rate_limit"]; ok {
		rlm, ok := raw.(map[string]interface{})
		if !ok {
			return nil, newFieldError(prefix+".rate_limit", "must be a mapping")
		}
		rl, err := decodeRateLimit(rlm, prefix)
		if err != nil {
			return nil, err
		}
		lc.RateLimit = rl
	}

	if raw, ok := m["max_request_body_bytes"]; ok {
		switch v := raw.(type) {
		case int:
			lc.MaxRequestBodyBytes = int64(v)
		case int64:
			lc.MaxRequestBodyBytes = v
		case float64:
			lc.MaxRequestBodyBytes = int64(v)
		case string:
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, newFieldError(prefix+".max_request_body_bytes", "must be an integer, got %q", v)
			}
			lc.MaxRequestBodyBytes = n
		default:
			return nil, newFieldError(prefix+".max_request_body_bytes", "must be an integer")
		}
	}

	return lc, nil
}

func decodeRateLimit(m map[string]interface{}, listenerPrefix string) (*RateLimitConfig, error) {
	prefix := listenerPrefix + ".rate_limit"

	rl := &RateLimitConfig{
		ClientIdleEviction: 5 * time.Minute,
	}

	if v, ok := m["requests_per_second"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, newFieldError(prefix+".requests_per_second", "must be a number")
		}
		n, err := parseFloat(s)
		if err != nil {
			return nil, newFieldError(prefix+".requests_per_second", "must be a number, got %q", s)
		}
		rl.RequestsPerSecond = n
	}
	if v, ok := m["burst"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, newFieldError(prefix+".burst", "must be an integer")
		}
		n, err := parseInt(s)
		if err != nil {
			return nil, newFieldError(prefix+".burst", "must be an integer, got %q", s)
		}
		rl.Burst = n
	}
	if v, ok := m["client_idle_eviction"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, newFieldError(prefix+".client_idle_eviction", "must be a duration string")
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, newFieldError(prefix+".client_idle_eviction", "invalid duration %q: %v", s, err)
		}
		rl.ClientIdleEviction = d
	}

	return rl, nil
}

func decodeRoute(m map[string]interface{}, listenerPrefix string, idx int) (*RouteConfig, error) {
	prefix := fmt.Sprintf("%s.routes[%d]", listenerPrefix, idx)

	host, _ := stringField(m, "host")
	pathPrefix, _ := stringField(m, "path_prefix")

	pool, hasPool := stringField(m, "pool")
	if hasPool && pool == "" {
		return nil, newFieldError(prefix+".pool", "must be a non-empty string when specified")
	}

	var split []SplitConfig
	if raw, ok := m["split"]; ok {
		splitItems, ok := raw.([]interface{})
		if !ok {
			return nil, newFieldError(prefix+".split", "must be a sequence of split entries")
		}
		for j, item := range splitItems {
			sm, ok := item.(map[string]interface{})
			if !ok {
				return nil, newFieldError(fmt.Sprintf("%s.split[%d]", prefix, j), "must be a mapping")
			}
			se, err := decodeSplitEntry(sm, prefix, j)
			if err != nil {
				return nil, err
			}
			split = append(split, *se)
		}
	}

	if hasPool && len(split) > 0 {
		return nil, newFieldError(prefix, "pool and split are mutually exclusive — specify exactly one")
	}
	if !hasPool && len(split) == 0 {
		return nil, newFieldError(prefix+".pool", "is required when split is not specified")
	}

	var header *HeaderConfig
	if raw, ok := m["header"]; ok {
		hm, ok := raw.(map[string]interface{})
		if !ok {
			return nil, newFieldError(prefix+".header", "must be a mapping")
		}
		h, err := decodeHeader(hm, prefix)
		if err != nil {
			return nil, err
		}
		header = h
	}

	rc := &RouteConfig{
		Host:       host,
		PathPrefix: pathPrefix,
		Header:     header,
		Pool:       pool,
		Split:      split,
	}
	return rc, nil
}

func decodeSplitEntry(m map[string]interface{}, routePrefix string, idx int) (*SplitConfig, error) {
	prefix := fmt.Sprintf("%s.split[%d]", routePrefix, idx)

	pool, ok := stringField(m, "pool")
	if !ok || pool == "" {
		return nil, newFieldError(prefix+".pool", "is required and must be a non-empty string")
	}

	se := &SplitConfig{Pool: pool, Weight: 1}
	if raw, ok := m["weight"]; ok {
		s, ok := raw.(string)
		if !ok {
			return nil, newFieldError(prefix+".weight", "must be an integer")
		}
		w, err := parseInt(s)
		if err != nil {
			return nil, newFieldError(prefix+".weight", "must be an integer, got %q", s)
		}
		se.Weight = w
	}
	return se, nil
}

func decodeHeader(m map[string]interface{}, routePrefix string) (*HeaderConfig, error) {
	prefix := routePrefix + ".header"

	name, ok := stringField(m, "name")
	if !ok || name == "" {
		return nil, newFieldError(prefix+".name", "is required and must be a non-empty string")
	}
	value, _ := stringField(m, "value")

	return &HeaderConfig{Name: name, Value: value}, nil
}
