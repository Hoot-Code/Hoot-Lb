package config

// validateRateLimit checks a rate limit configuration for semantic
// correctness: positive requests_per_second, positive burst, and
// positive client_idle_eviction.
func validateRateLimit(rl *RateLimitConfig, prefix string) []error {
	var errs []error

	if rl.RequestsPerSecond <= 0 {
		errs = append(errs, newFieldError(prefix+".requests_per_second", "must be a positive number (got %v)", rl.RequestsPerSecond))
	}
	if rl.Burst <= 0 {
		errs = append(errs, newFieldError(prefix+".burst", "must be a positive integer (got %d)", rl.Burst))
	}
	if rl.ClientIdleEviction <= 0 {
		errs = append(errs, newFieldError(prefix+".client_idle_eviction", "must be a positive duration"))
	}

	return errs
}
