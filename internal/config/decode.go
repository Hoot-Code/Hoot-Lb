package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func decode(tree map[string]interface{}) (*Config, error) {
	trueVal := true
	cfg := &Config{
		Global: GlobalConfig{
			LogLevel:        "info",
			ShutdownTimeout: 10 * time.Second,
			Metrics: MetricsConfig{
				Enabled: &trueVal,
				Address: "127.0.0.1:9090",
				Path:    "/metrics",
			},
			AccessLog: AccessLogConfig{
				Enabled: &trueVal,
				Format:  "json",
			},
			Admin: AdminConfig{
				Address:               "127.0.0.1:9091",
				MaxConcurrentRequests: 10,
			},
		},
	}

	if raw, ok := tree["global"]; ok {
		gm, ok := raw.(map[string]interface{})
		if !ok {
			return nil, newFieldError("global", "must be a mapping")
		}
		if err := decodeGlobal(gm, &cfg.Global); err != nil {
			return nil, err
		}
	}

	listenersRaw, ok := tree["listeners"]
	if !ok {
		return nil, newFieldError("listeners", "missing required section")
	}
	listenerItems, ok := listenersRaw.([]interface{})
	if !ok {
		return nil, newFieldError("listeners", "must be a sequence of listener entries")
	}
	for i, item := range listenerItems {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, newFieldError(fmt.Sprintf("listeners[%d]", i), "must be a mapping")
		}
		lc, err := decodeListener(m, i)
		if err != nil {
			return nil, err
		}
		lc.Global = cfg.Global
		cfg.Listeners = append(cfg.Listeners, *lc)
	}

	poolsRaw, ok := tree["pools"]
	if !ok {
		return nil, newFieldError("pools", "missing required section")
	}
	poolItems, ok := poolsRaw.([]interface{})
	if !ok {
		return nil, newFieldError("pools", "must be a sequence of pool entries")
	}
	for i, item := range poolItems {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, newFieldError(fmt.Sprintf("pools[%d]", i), "must be a mapping")
		}
		pc, err := decodePool(m, i)
		if err != nil {
			return nil, err
		}
		cfg.Pools = append(cfg.Pools, *pc)
	}

	return cfg, nil
}

func stringField(m map[string]interface{}, key string) (string, bool) {
	raw, present := m[key]
	if !present {
		return "", false
	}
	s, ok := raw.(string)
	return s, ok
}

func parseInt(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}

func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}
