package config

import (
	"fmt"
	"time"
)

func decodeRetry(m map[string]interface{}, poolPrefix string) (*RetryConfig, error) {
	prefix := poolPrefix + ".retry"

	rc := &RetryConfig{
		MaxAttempts:          3,
		RetryBudgetRatio:     0.2,
		BackoffBase:          50 * time.Millisecond,
		BackoffMax:           1 * time.Second,
		RetryableStatusCodes: []int{502, 503, 504},
	}

	if v, ok := m["max_attempts"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, newFieldError(prefix+".max_attempts", "must be an integer")
		}
		n, err := parseInt(s)
		if err != nil {
			return nil, newFieldError(prefix+".max_attempts", "must be an integer, got %q", s)
		}
		rc.MaxAttempts = n
	}
	if v, ok := m["retry_budget_ratio"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, newFieldError(prefix+".retry_budget_ratio", "must be a number")
		}
		f, err := parseFloat(s)
		if err != nil {
			return nil, newFieldError(prefix+".retry_budget_ratio", "must be a number, got %q", s)
		}
		rc.RetryBudgetRatio = f
	}
	if v, ok := m["backoff_base"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, newFieldError(prefix+".backoff_base", "must be a duration string")
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, newFieldError(prefix+".backoff_base", "invalid duration %q: %v", s, err)
		}
		rc.BackoffBase = d
	}
	if v, ok := m["backoff_max"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, newFieldError(prefix+".backoff_max", "must be a duration string")
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, newFieldError(prefix+".backoff_max", "invalid duration %q: %v", s, err)
		}
		rc.BackoffMax = d
	}
	if v, ok := m["retryable_status_codes"]; ok {
		codes, ok := v.([]interface{})
		if !ok {
			return nil, newFieldError(prefix+".retryable_status_codes", "must be a sequence of integers")
		}
		rc.RetryableStatusCodes = make([]int, 0, len(codes))
		for i, c := range codes {
			s, ok := c.(string)
			if !ok {
				return nil, newFieldError(fmt.Sprintf("%s.retryable_status_codes[%d]", prefix, i), "must be an integer")
			}
			n, err := parseInt(s)
			if err != nil {
				return nil, newFieldError(fmt.Sprintf("%s.retryable_status_codes[%d]", prefix, i), "must be an integer, got %q", s)
			}
			rc.RetryableStatusCodes = append(rc.RetryableStatusCodes, n)
		}
	}

	return rc, nil
}
