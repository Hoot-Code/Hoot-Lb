package config

import (
	"fmt"
	"os"
)

// Load reads the YAML configuration file at path, parses it, decodes
// it into a Config, and validates the result. It returns a fully
// populated and validated Config, or an error that pinpoints exactly
// what is wrong (including the offending field path where applicable).
//
// This is the only entry point most callers need; Parse and Validate
// are exposed separately mainly to make the loader testable in pieces.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("config file %q: %w", path, err)
	}
	if err := Validate(cfg); err != nil {
		return nil, fmt.Errorf("config file %q is invalid:\n%w", path, err)
	}
	return cfg, nil
}

// Parse decodes raw YAML bytes into a Config, applying defaults for
// optional fields. It does not run cross-field validation (duplicate
// names, address syntax, pool references); call Validate separately,
// or use Load to do both in one step.
func Parse(data []byte) (*Config, error) {
	tree, err := parseYAML(data)
	if err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}
	cfg, err := decode(tree)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}
