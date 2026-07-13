package config

import (
	"fmt"
	"os"
)

// validateAdmin checks admin configuration for semantic correctness.
func validateAdmin(ac *AdminConfig) []error {
	var errs []error

	if ac.Enabled != nil && *ac.Enabled {
		if ac.Address == "" {
			errs = append(errs, newFieldError("global.admin.address", "is required when admin is enabled"))
		} else if err := validateHostPort(ac.Address); err != nil {
			errs = append(errs, newFieldError("global.admin.address", "invalid address %q: %v", ac.Address, err))
		}
		// TokenEnv and Roles are mutually exclusive.
		hasTokenEnv := ac.TokenEnv != ""
		hasRoles := len(ac.Roles) > 0
		if hasTokenEnv && hasRoles {
			errs = append(errs, newFieldError("global.admin", "token_env and roles are mutually exclusive — specify exactly one"))
		}
		if !hasTokenEnv && !hasRoles {
			errs = append(errs, newFieldError("global.admin.token_env", "is required when admin is enabled (or specify roles)"))
		}
		if hasTokenEnv && os.Getenv(ac.TokenEnv) == "" {
			errs = append(errs, newFieldError("global.admin.token_env", "environment variable %q is not set or empty", ac.TokenEnv))
		}
		if ac.MaxConcurrentRequests <= 0 {
			errs = append(errs, newFieldError("global.admin.max_concurrent_requests", "must be a positive integer"))
		}
		// Validate mTLS config.
		if ac.MTLS != nil && ac.MTLS.Enabled {
			if ac.MTLS.CAFile == "" {
				errs = append(errs, newFieldError("global.admin.mtls.ca_file", "is required when mtls is enabled"))
			}
		}
		// Validate roles.
		for i, role := range ac.Roles {
			prefix := fmt.Sprintf("global.admin.roles[%d]", i)
			if role.TokenEnv == "" {
				errs = append(errs, newFieldError(prefix+".token_env", "is required"))
			} else if os.Getenv(role.TokenEnv) == "" {
				errs = append(errs, newFieldError(prefix+".token_env", "environment variable %q is not set or empty", role.TokenEnv))
			}
			if len(role.Permissions) == 0 {
				errs = append(errs, newFieldError(prefix+".permissions", "must contain at least one permission"))
			}
			for _, perm := range role.Permissions {
				switch perm {
				case "read", "drain", "restart", "backends", "config":
				default:
					errs = append(errs, newFieldError(prefix+".permissions", "unknown permission %q (valid: read, drain, restart, backends, config)", perm))
				}
			}
		}
	}

	return errs
}
