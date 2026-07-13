package config

// validateCircuitBreaker checks a circuit breaker configuration for
// semantic correctness: positive failure_threshold, positive
// open_duration, and positive half_open_max_probes.
func validateCircuitBreaker(cb *CircuitBreakerConfig, prefix string) []error {
	var errs []error

	if cb.FailureThreshold <= 0 {
		errs = append(errs, newFieldError(prefix+".failure_threshold", "must be a positive integer (got %d)", cb.FailureThreshold))
	}
	if cb.OpenDuration <= 0 {
		errs = append(errs, newFieldError(prefix+".open_duration", "must be a positive duration"))
	}
	if cb.HalfOpenMaxProbes <= 0 {
		errs = append(errs, newFieldError(prefix+".half_open_max_probes", "must be a positive integer (got %d)", cb.HalfOpenMaxProbes))
	}

	return errs
}
