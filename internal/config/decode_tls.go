package config

import (
	"fmt"
	"time"
)

// decodeTLS converts a tls mapping into a TLSConfig.
func decodeTLS(m map[string]interface{}, listenerPrefix string) (*TLSConfig, error) {
	prefix := listenerPrefix + ".tls"

	mode, ok := stringField(m, "mode")
	if !ok || mode == "" {
		return nil, newFieldError(prefix+".mode", "is required and must be one of: terminate, passthrough")
	}
	if mode != "terminate" && mode != "passthrough" {
		return nil, newFieldError(prefix+".mode", "must be one of: terminate, passthrough (got %q)", mode)
	}

	cfg := &TLSConfig{Mode: mode}

	if raw, ok := m["certificates"]; ok {
		certsRaw, ok := raw.([]interface{})
		if !ok {
			return nil, newFieldError(prefix+".certificates", "must be a sequence of certificate entries")
		}
		for j, item := range certsRaw {
			cm, ok := item.(map[string]interface{})
			if !ok {
				return nil, newFieldError(fmt.Sprintf("%s.certificates[%d]", prefix, j), "must be a mapping")
			}
			cert, err := decodeTLSCert(cm, prefix, j)
			if err != nil {
				return nil, err
			}
			cfg.Certificates = append(cfg.Certificates, *cert)
		}
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
			route, err := decodeTLSRoute(rm, prefix, j)
			if err != nil {
				return nil, err
			}
			cfg.Routes = append(cfg.Routes, *route)
		}
	}

	if v, ok := stringField(m, "handshake_timeout"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, newFieldError(prefix+".handshake_timeout", "invalid duration %q: %v", v, err)
		}
		cfg.HandshakeTimeout = d
	}

	if v, ok := stringField(m, "min_version"); ok {
		if v != "tls12" && v != "tls13" {
			return nil, newFieldError(prefix+".min_version", "must be one of: tls12, tls13 (got %q)", v)
		}
		cfg.MinVersion = v
	}

	return cfg, nil
}

// decodeTLSCert converts a single certificate mapping into a TLSCertConfig.
func decodeTLSCert(m map[string]interface{}, tlsPrefix string, idx int) (*TLSCertConfig, error) {
	prefix := fmt.Sprintf("%s.certificates[%d]", tlsPrefix, idx)

	host, _ := stringField(m, "host")

	certFile, ok := stringField(m, "cert_file")
	if !ok || certFile == "" {
		return nil, newFieldError(prefix+".cert_file", "is required and must be a non-empty string")
	}

	keyFile, ok := stringField(m, "key_file")
	if !ok || keyFile == "" {
		return nil, newFieldError(prefix+".key_file", "is required and must be a non-empty string")
	}

	return &TLSCertConfig{
		Host:     host,
		CertFile: certFile,
		KeyFile:  keyFile,
	}, nil
}

// decodeTLSRoute converts a single TLS route mapping into a TLSRouteConfig.
func decodeTLSRoute(m map[string]interface{}, tlsPrefix string, idx int) (*TLSRouteConfig, error) {
	prefix := fmt.Sprintf("%s.routes[%d]", tlsPrefix, idx)

	host, ok := stringField(m, "host")
	if !ok || host == "" {
		return nil, newFieldError(prefix+".host", "is required and must be a non-empty string")
	}

	pool, ok := stringField(m, "pool")
	if !ok || pool == "" {
		return nil, newFieldError(prefix+".pool", "is required and must be a non-empty string")
	}

	return &TLSRouteConfig{
		Host: host,
		Pool: pool,
	}, nil
}
