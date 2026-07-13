package config

import "fmt"

// FieldError is a configuration error tied to a specific field, named
// by its path within the config file (e.g. "listeners[0].address" or
// "global.log_level"). Carrying the field path lets users locate and
// correct the offending entry without needing file line numbers.
type FieldError struct {
	// Field is the dotted/indexed path to the offending field.
	Field string
	// Msg describes what is wrong with the field's value.
	Msg string
}

// Error implements the error interface, formatting the field path and
// message as "<field>: <message>".
func (e *FieldError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Msg)
}

// newFieldError is a small convenience constructor used throughout the
// decode and validate steps.
func newFieldError(field, format string, args ...any) *FieldError {
	return &FieldError{Field: field, Msg: fmt.Sprintf(format, args...)}
}
