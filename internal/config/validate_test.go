package config

import (
	"strings"
	"testing"
	"time"
)

func validTestConfig() *Config {
	return &Config{
		Global: GlobalConfig{
			LogLevel:        "info",
			ShutdownTimeout: 10 * time.Second,
		},
		Listeners: []ListenerConfig{
			{
				Name:     "test",
				Address:  "0.0.0.0:8080",
				Protocol: "http",
				Pool:     "pool_a",
			},
		},
		Pools: []PoolConfig{
			{
				Name:      "pool_a",
				Algorithm: "round_robin",
				Backends:  []BackendConfig{{Address: "127.0.0.1:8080", Weight: 1}},
			},
			{
				Name:      "pool_b",
				Algorithm: "round_robin",
				Backends:  []BackendConfig{{Address: "127.0.0.1:8081", Weight: 1}},
			},
		},
	}
}

func TestValidatePoolSplitMutualExclusivity(t *testing.T) {
	cfg := validTestConfig()
	cfg.Listeners[0].Routes = []RouteConfig{
		{
			PathPrefix: "/test",
			Pool:       "pool_a",
			Split: []SplitConfig{
				{Pool: "pool_a", Weight: 50},
				{Pool: "pool_b", Weight: 50},
			},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for pool+split, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected 'mutually exclusive', got: %v", err)
	}
}

func TestValidateSplitNonPositiveWeight(t *testing.T) {
	cfg := validTestConfig()
	cfg.Listeners[0].Routes = []RouteConfig{
		{
			PathPrefix: "/test",
			Split: []SplitConfig{
				{Pool: "pool_a", Weight: 50},
				{Pool: "pool_b", Weight: 0},
			},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for zero weight, got nil")
	}
	if !strings.Contains(err.Error(), "positive integer") {
		t.Errorf("expected 'positive integer', got: %v", err)
	}
}

func TestValidateSplitNegativeWeight(t *testing.T) {
	cfg := validTestConfig()
	cfg.Listeners[0].Routes = []RouteConfig{
		{
			PathPrefix: "/test",
			Split: []SplitConfig{
				{Pool: "pool_a", Weight: -5},
				{Pool: "pool_b", Weight: 50},
			},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for negative weight, got nil")
	}
	if !strings.Contains(err.Error(), "positive integer") {
		t.Errorf("expected 'positive integer', got: %v", err)
	}
}

func TestValidateSplitDanglingPool(t *testing.T) {
	cfg := validTestConfig()
	cfg.Listeners[0].Routes = []RouteConfig{
		{
			PathPrefix: "/test",
			Split: []SplitConfig{
				{Pool: "pool_a", Weight: 50},
				{Pool: "nonexistent", Weight: 50},
			},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for dangling pool, got nil")
	}
	if !strings.Contains(err.Error(), "undefined pool") {
		t.Errorf("expected 'undefined pool', got: %v", err)
	}
}

func TestValidateSplitTooFewEntries(t *testing.T) {
	cfg := validTestConfig()
	cfg.Listeners[0].Routes = []RouteConfig{
		{
			PathPrefix: "/test",
			Split: []SplitConfig{
				{Pool: "pool_a", Weight: 100},
			},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for 1-entry split, got nil")
	}
	if !strings.Contains(err.Error(), "at least 2") {
		t.Errorf("expected 'at least 2', got: %v", err)
	}
}

func TestValidateStickyTTLMustBePositive(t *testing.T) {
	cfg := validTestConfig()
	cfg.Pools[0].Sticky = &StickyConfig{
		CookieName: "sticky",
		TTL:        0,
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for zero TTL, got nil")
	}
	if !strings.Contains(err.Error(), "positive duration") {
		t.Errorf("expected 'positive duration', got: %v", err)
	}
}

func TestValidateStickyNegativeTTL(t *testing.T) {
	cfg := validTestConfig()
	cfg.Pools[0].Sticky = &StickyConfig{
		CookieName: "sticky",
		TTL:        -1 * time.Second,
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for negative TTL, got nil")
	}
	if !strings.Contains(err.Error(), "positive duration") {
		t.Errorf("expected 'positive duration', got: %v", err)
	}
}

func TestValidateStickyValidConfig(t *testing.T) {
	cfg := validTestConfig()
	cfg.Pools[0].Sticky = &StickyConfig{
		CookieName: "my_sticky",
		TTL:        1 * time.Hour,
	}

	err := Validate(cfg)
	if err != nil {
		t.Fatalf("unexpected error for valid sticky config: %v", err)
	}
}

func TestValidateSplitValidConfig(t *testing.T) {
	cfg := validTestConfig()
	cfg.Listeners[0].Routes = []RouteConfig{
		{
			PathPrefix: "/test",
			Split: []SplitConfig{
				{Pool: "pool_a", Weight: 90},
				{Pool: "pool_b", Weight: 10},
			},
		},
	}

	err := Validate(cfg)
	if err != nil {
		t.Fatalf("unexpected error for valid split config: %v", err)
	}
}

func TestValidateHeaderValidConfig(t *testing.T) {
	cfg := validTestConfig()
	cfg.Listeners[0].Routes = []RouteConfig{
		{
			PathPrefix: "/test",
			Header:     &HeaderConfig{Name: "X-Debug", Value: "1"},
			Pool:       "pool_a",
		},
	}

	err := Validate(cfg)
	if err != nil {
		t.Fatalf("unexpected error for valid header config: %v", err)
	}
}

func TestValidateSplitDefaultWeight(t *testing.T) {
	cfg := validTestConfig()
	cfg.Listeners[0].Routes = []RouteConfig{
		{
			PathPrefix: "/test",
			Split: []SplitConfig{
				{Pool: "pool_a", Weight: 1},
				{Pool: "pool_b", Weight: 1},
			},
		},
	}

	err := Validate(cfg)
	if err != nil {
		t.Fatalf("unexpected error for valid split config: %v", err)
	}
}

func TestValidateDiscoveryAndBackendsMutualExclusivity(t *testing.T) {
	cfg := validTestConfig()
	cfg.Pools[0].Backends = []BackendConfig{{Address: "127.0.0.1:8080", Weight: 1}}
	cfg.Pools[0].Discovery = &DiscoveryConfig{
		Type: "dns",
		DNS:  &DNSDiscoveryConfig{Host: "backend.local", Port: 8080, RefreshInterval: 30 * time.Second},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for backends+discovery, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected 'mutually exclusive', got: %v", err)
	}
}

func TestValidateDiscoveryMissingBothBackendsAndDiscovery(t *testing.T) {
	cfg := validTestConfig()
	cfg.Pools[0].Backends = nil

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing backends and discovery, got nil")
	}
	if !strings.Contains(err.Error(), "is required") {
		t.Errorf("expected 'is required', got: %v", err)
	}
}

func TestValidateDiscoveryInvalidType(t *testing.T) {
	cfg := validTestConfig()
	cfg.Pools[0].Backends = nil
	cfg.Pools[0].Discovery = &DiscoveryConfig{Type: "etcd"}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for invalid discovery type, got nil")
	}
	if !strings.Contains(err.Error(), "must be one of: dns, consul") {
		t.Errorf("expected type enum error, got: %v", err)
	}
}

func TestValidateDiscoveryDNSMissingHost(t *testing.T) {
	cfg := validTestConfig()
	cfg.Pools[0].Backends = nil
	cfg.Pools[0].Discovery = &DiscoveryConfig{
		Type: "dns",
		DNS:  &DNSDiscoveryConfig{Port: 8080, RefreshInterval: 30 * time.Second},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing DNS host, got nil")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("expected host error, got: %v", err)
	}
}

func TestValidateDiscoveryValidDNS(t *testing.T) {
	cfg := validTestConfig()
	cfg.Pools[0].Backends = nil
	cfg.Pools[0].Discovery = &DiscoveryConfig{
		Type: "dns",
		DNS: &DNSDiscoveryConfig{
			Host:            "backend.local",
			Port:            8080,
			RefreshInterval: 30 * time.Second,
		},
	}

	err := Validate(cfg)
	if err != nil {
		t.Fatalf("unexpected error for valid DNS discovery: %v", err)
	}
}

func TestValidateDiscoveryDNSInvalidPort(t *testing.T) {
	cfg := validTestConfig()
	cfg.Pools[0].Backends = nil
	cfg.Pools[0].Discovery = &DiscoveryConfig{
		Type: "dns",
		DNS: &DNSDiscoveryConfig{
			Host:            "backend.local",
			Port:            99999,
			RefreshInterval: 30 * time.Second,
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for invalid port, got nil")
	}
	if !strings.Contains(err.Error(), "must be between 0 and 65535") {
		t.Errorf("expected port range error, got: %v", err)
	}
}
