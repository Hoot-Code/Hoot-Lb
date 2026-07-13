package config

// validateRetry checks a retry configuration for semantic correctness:
// positive max_attempts, valid budget ratio, positive durations.
func validateRetry(rc *RetryConfig, prefix string) []error {
	var errs []error

	if rc.MaxAttempts < 2 {
		errs = append(errs, newFieldError(prefix+".max_attempts", "must be >= 2 for retries to occur (got %d)", rc.MaxAttempts))
	}
	if rc.RetryBudgetRatio < 0 || rc.RetryBudgetRatio > 1 {
		errs = append(errs, newFieldError(prefix+".retry_budget_ratio", "must be between 0 and 1 (got %f)", rc.RetryBudgetRatio))
	}
	if rc.BackoffBase <= 0 {
		errs = append(errs, newFieldError(prefix+".backoff_base", "must be a positive duration"))
	}
	if rc.BackoffMax <= 0 {
		errs = append(errs, newFieldError(prefix+".backoff_max", "must be a positive duration"))
	}
	if rc.BackoffMax < rc.BackoffBase {
		errs = append(errs, newFieldError(prefix+".backoff_max", "must be >= backoff_base"))
	}
	if len(rc.RetryableStatusCodes) == 0 {
		errs = append(errs, newFieldError(prefix+".retryable_status_codes", "must contain at least one status code"))
	}

	return errs
}
