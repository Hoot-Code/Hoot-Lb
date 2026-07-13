package config

import (
	"time"
)

func decodeHealthCheck(m map[string]interface{}, poolPrefix string) (*HealthCheckConfig, error) {
	hc := &HealthCheckConfig{
		Type:               "tcp",
		Interval:           5 * time.Second,
		Timeout:            2 * time.Second,
		HealthyThreshold:   2,
		UnhealthyThreshold: 3,
	}

	if v, ok := stringField(m, "type"); ok {
		hc.Type = v
	}
	if v, ok := stringField(m, "path"); ok {
		hc.Path = v
	}
	if v, ok := stringField(m, "interval"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, newFieldError(poolPrefix+".health_check.interval", "invalid duration %q: %v", v, err)
		}
		hc.Interval = d
	}
	if v, ok := stringField(m, "timeout"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, newFieldError(poolPrefix+".health_check.timeout", "invalid duration %q: %v", v, err)
		}
		hc.Timeout = d
	}
	if v, ok := stringField(m, "healthy_threshold"); ok {
		n, err := parseInt(v)
		if err != nil {
			return nil, newFieldError(poolPrefix+".health_check.healthy_threshold", "must be an integer, got %q", v)
		}
		hc.HealthyThreshold = n
	}
	if v, ok := stringField(m, "unhealthy_threshold"); ok {
		n, err := parseInt(v)
		if err != nil {
			return nil, newFieldError(poolPrefix+".health_check.unhealthy_threshold", "must be an integer, got %q", v)
		}
		hc.UnhealthyThreshold = n
	}

	return hc, nil
}
