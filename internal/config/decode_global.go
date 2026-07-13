package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func decodeGlobal(m map[string]interface{}, g *GlobalConfig) error {
	if raw, ok := m["log_level"]; ok {
		s, ok := raw.(string)
		if !ok {
			return newFieldError("global.log_level", "must be a string")
		}
		g.LogLevel = s
	}
	if raw, ok := m["shutdown_timeout"]; ok {
		s, ok := raw.(string)
		if !ok {
			return newFieldError("global.shutdown_timeout", "must be a string duration, e.g. \"10s\"")
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return newFieldError("global.shutdown_timeout", "invalid duration %q: %v", s, err)
		}
		g.ShutdownTimeout = d
	}
	if raw, ok := m["reload_check_interval"]; ok {
		s, ok := raw.(string)
		if !ok {
			return newFieldError("global.reload_check_interval", "must be a string duration, e.g. \"5s\"")
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return newFieldError("global.reload_check_interval", "invalid duration %q: %v", s, err)
		}
		g.ReloadCheckInterval = d
	}
	if raw, ok := m["max_connections_per_listener"]; ok {
		switch v := raw.(type) {
		case int:
			g.MaxConnectionsPerListener = v
		case int64:
			g.MaxConnectionsPerListener = int(v)
		case float64:
			g.MaxConnectionsPerListener = int(v)
		case string:
			n, err := strconv.Atoi(v)
			if err != nil {
				return newFieldError("global.max_connections_per_listener", "must be an integer")
			}
			g.MaxConnectionsPerListener = n
		default:
			return newFieldError("global.max_connections_per_listener", "must be an integer")
		}
	}
	if raw, ok := m["tcp_idle_timeout"]; ok {
		s, ok := raw.(string)
		if !ok {
			return newFieldError("global.tcp_idle_timeout", "must be a string duration, e.g. \"30s\"")
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return newFieldError("global.tcp_idle_timeout", "invalid duration %q: %v", s, err)
		}
		g.TCPIdleTimeout = d
	}
	if raw, ok := m["metrics"]; ok {
		mm, ok := raw.(map[string]interface{})
		if !ok {
			return newFieldError("global.metrics", "must be a mapping")
		}
		if err := decodeMetrics(mm, &g.Metrics); err != nil {
			return err
		}
	}
	if raw, ok := m["access_log"]; ok {
		alm, ok := raw.(map[string]interface{})
		if !ok {
			return newFieldError("global.access_log", "must be a mapping")
		}
		if err := decodeAccessLog(alm, &g.AccessLog); err != nil {
			return err
		}
	}
	if raw, ok := m["admin"]; ok {
		am, ok := raw.(map[string]interface{})
		if !ok {
			return newFieldError("global.admin", "must be a mapping")
		}
		if err := decodeAdmin(am, &g.Admin); err != nil {
			return err
		}
	}
	return nil
}

func decodeAdmin(m map[string]interface{}, ac *AdminConfig) error {
	if raw, ok := m["enabled"]; ok {
		b, ok := raw.(bool)
		if !ok {
			s, ok := raw.(string)
			if !ok {
				return newFieldError("global.admin.enabled", "must be a boolean")
			}
			b = strings.EqualFold(strings.TrimSpace(s), "true")
		}
		ac.Enabled = &b
	}
	if raw, ok := m["address"]; ok {
		s, ok := raw.(string)
		if !ok {
			return newFieldError("global.admin.address", "must be a string")
		}
		ac.Address = s
	}
	if raw, ok := m["token_env"]; ok {
		s, ok := raw.(string)
		if !ok {
			return newFieldError("global.admin.token_env", "must be a string")
		}
		ac.TokenEnv = s
	}
	if raw, ok := m["max_concurrent_requests"]; ok {
		switch v := raw.(type) {
		case int:
			ac.MaxConcurrentRequests = v
		case int64:
			ac.MaxConcurrentRequests = int(v)
		case float64:
			ac.MaxConcurrentRequests = int(v)
		case string:
			n, err := strconv.Atoi(v)
			if err != nil {
				return newFieldError("global.admin.max_concurrent_requests", "must be an integer")
			}
			ac.MaxConcurrentRequests = n
		default:
			return newFieldError("global.admin.max_concurrent_requests", "must be an integer")
		}
	}
	if raw, ok := m["mtls"]; ok {
		mm, ok := raw.(map[string]interface{})
		if !ok {
			return newFieldError("global.admin.mtls", "must be a mapping")
		}
		mcfg := &AdminMTLSConfig{}
		if v, ok := mm["enabled"]; ok {
			b, ok := v.(bool)
			if !ok {
				s, ok := v.(string)
				if !ok {
					return newFieldError("global.admin.mtls.enabled", "must be a boolean")
				}
				b = strings.EqualFold(strings.TrimSpace(s), "true")
			}
			mcfg.Enabled = b
		}
		if v, ok := mm["ca_file"]; ok {
			s, ok := v.(string)
			if !ok {
				return newFieldError("global.admin.mtls.ca_file", "must be a string")
			}
			mcfg.CAFile = s
		}
		ac.MTLS = mcfg
	}
	if raw, ok := m["roles"]; ok {
		roles, ok := raw.([]interface{})
		if !ok {
			return newFieldError("global.admin.roles", "must be a sequence of role entries")
		}
		for i, item := range roles {
			rm, ok := item.(map[string]interface{})
			if !ok {
				return newFieldError(fmt.Sprintf("global.admin.roles[%d]", i), "must be a mapping")
			}
			role := AdminRoleConfig{}
			if v, ok := rm["token_env"]; ok {
				s, ok := v.(string)
				if !ok {
					return newFieldError(fmt.Sprintf("global.admin.roles[%d].token_env", i), "must be a string")
				}
				role.TokenEnv = s
			}
			if v, ok := rm["permissions"]; ok {
				perms, ok := v.([]interface{})
				if !ok {
					return newFieldError(fmt.Sprintf("global.admin.roles[%d].permissions", i), "must be a sequence of strings")
				}
				for j, p := range perms {
					ps, ok := p.(string)
					if !ok {
						return newFieldError(fmt.Sprintf("global.admin.roles[%d].permissions[%d]", i, j), "must be a string")
					}
					role.Permissions = append(role.Permissions, ps)
				}
			}
			ac.Roles = append(ac.Roles, role)
		}
	}
	if raw, ok := m["audit_log"]; ok {
		am, ok := raw.(map[string]interface{})
		if !ok {
			return newFieldError("global.admin.audit_log", "must be a mapping")
		}
		alog := &AdminAuditLogConfig{}
		if v, ok := am["enabled"]; ok {
			b, ok := v.(bool)
			if !ok {
				s, ok := v.(string)
				if !ok {
					return newFieldError("global.admin.audit_log.enabled", "must be a boolean")
				}
				b = strings.EqualFold(strings.TrimSpace(s), "true")
			}
			alog.Enabled = b
		}
		ac.AuditLog = alog
	}
	return nil
}

func decodeMetrics(m map[string]interface{}, mc *MetricsConfig) error {
	if raw, ok := m["enabled"]; ok {
		b, ok := raw.(bool)
		if !ok {
			s, ok := raw.(string)
			if !ok {
				return newFieldError("global.metrics.enabled", "must be a boolean")
			}
			b = strings.EqualFold(strings.TrimSpace(s), "true")
		}
		mc.Enabled = &b
	}
	if raw, ok := m["address"]; ok {
		s, ok := raw.(string)
		if !ok {
			return newFieldError("global.metrics.address", "must be a string")
		}
		mc.Address = s
	}
	if raw, ok := m["path"]; ok {
		s, ok := raw.(string)
		if !ok {
			return newFieldError("global.metrics.path", "must be a string")
		}
		mc.Path = s
	}
	return nil
}

func decodeAccessLog(m map[string]interface{}, al *AccessLogConfig) error {
	if raw, ok := m["enabled"]; ok {
		b, ok := raw.(bool)
		if !ok {
			s, ok := raw.(string)
			if !ok {
				return newFieldError("global.access_log.enabled", "must be a boolean")
			}
			b = strings.EqualFold(strings.TrimSpace(s), "true")
		}
		al.Enabled = &b
	}
	if raw, ok := m["format"]; ok {
		s, ok := raw.(string)
		if !ok {
			return newFieldError("global.access_log.format", "must be a string")
		}
		al.Format = s
	}
	return nil
}
