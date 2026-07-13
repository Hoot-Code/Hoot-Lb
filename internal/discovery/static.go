package discovery

import "context"

// Static returns a fixed set of backends. Used internally for pools
// that don't use discovery (plain backends list). Not config-exposed
// since the config layer handles static backends directly.
type Static struct {
	backends []Backend
	name     string
}

// NewStatic creates a Static adapter that always returns the given
// backends.
func NewStatic(name string, backends []Backend) *Static {
	return &Static{backends: backends, name: name}
}

// Resolve returns the fixed backend list. It never returns an error.
func (s *Static) Resolve(_ context.Context) ([]Backend, error) {
	return s.backends, nil
}

// Name returns the identifier for this discovery source.
func (s *Static) Name() string { return s.name }
