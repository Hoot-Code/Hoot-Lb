package l7

import (
	"strings"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
)

func TestConfigPoolSplitMutualExclusivity(t *testing.T) {
	cfg := &config.Config{
		Global: config.GlobalConfig{
			LogLevel:        "info",
			ShutdownTimeout: 10 * time.Second,
		},
		Listeners: []config.ListenerConfig{
			{
				Name:     "test",
				Address:  "0.0.0.0:8080",
				Protocol: "http",
				Pool:     "pool_a",
				Routes: []config.RouteConfig{
					{
						PathPrefix: "/test",
						Pool:       "pool_a",
						Split: []config.SplitConfig{
							{Pool: "pool_a", Weight: 50},
							{Pool: "pool_b", Weight: 50},
						},
					},
				},
			},
		},
		Pools: []config.PoolConfig{
			{Name: "pool_a", Algorithm: "round_robin", Backends: []config.BackendConfig{{Address: "127.0.0.1:8080", Weight: 1}}},
			{Name: "pool_b", Algorithm: "round_robin", Backends: []config.BackendConfig{{Address: "127.0.0.1:8081", Weight: 1}}},
		},
	}

	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected error for pool+split mutual exclusivity, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected 'mutually exclusive' in error, got: %v", err)
	}
}

func TestConfigSplitNonPositiveWeight(t *testing.T) {
	cfg := &config.Config{
		Global: config.GlobalConfig{
			LogLevel:        "info",
			ShutdownTimeout: 10 * time.Second,
		},
		Listeners: []config.ListenerConfig{
			{
				Name:     "test",
				Address:  "0.0.0.0:8080",
				Protocol: "http",
				Pool:     "pool_a",
				Routes: []config.RouteConfig{
					{
						PathPrefix: "/test",
						Split: []config.SplitConfig{
							{Pool: "pool_a", Weight: 50},
							{Pool: "pool_b", Weight: 0},
						},
					},
				},
			},
		},
		Pools: []config.PoolConfig{
			{Name: "pool_a", Algorithm: "round_robin", Backends: []config.BackendConfig{{Address: "127.0.0.1:8080", Weight: 1}}},
			{Name: "pool_b", Algorithm: "round_robin", Backends: []config.BackendConfig{{Address: "127.0.0.1:8081", Weight: 1}}},
		},
	}

	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected error for non-positive split weight, got nil")
	}
	if !strings.Contains(err.Error(), "positive integer") {
		t.Errorf("expected 'positive integer' in error, got: %v", err)
	}
}

func TestConfigSplitDanglingPoolReference(t *testing.T) {
	cfg := &config.Config{
		Global: config.GlobalConfig{
			LogLevel:        "info",
			ShutdownTimeout: 10 * time.Second,
		},
		Listeners: []config.ListenerConfig{
			{
				Name:     "test",
				Address:  "0.0.0.0:8080",
				Protocol: "http",
				Pool:     "pool_a",
				Routes: []config.RouteConfig{
					{
						PathPrefix: "/test",
						Split: []config.SplitConfig{
							{Pool: "pool_a", Weight: 50},
							{Pool: "nonexistent", Weight: 50},
						},
					},
				},
			},
		},
		Pools: []config.PoolConfig{
			{Name: "pool_a", Algorithm: "round_robin", Backends: []config.BackendConfig{{Address: "127.0.0.1:8080", Weight: 1}}},
		},
	}

	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected error for dangling split pool reference, got nil")
	}
	if !strings.Contains(err.Error(), "undefined pool") {
		t.Errorf("expected 'undefined pool' in error, got: %v", err)
	}
}

func TestConfigSplitTooFewEntries(t *testing.T) {
	cfg := &config.Config{
		Global: config.GlobalConfig{
			LogLevel:        "info",
			ShutdownTimeout: 10 * time.Second,
		},
		Listeners: []config.ListenerConfig{
			{
				Name:     "test",
				Address:  "0.0.0.0:8080",
				Protocol: "http",
				Pool:     "pool_a",
				Routes: []config.RouteConfig{
					{
						PathPrefix: "/test",
						Split: []config.SplitConfig{
							{Pool: "pool_a", Weight: 100},
						},
					},
				},
			},
		},
		Pools: []config.PoolConfig{
			{Name: "pool_a", Algorithm: "round_robin", Backends: []config.BackendConfig{{Address: "127.0.0.1:8080", Weight: 1}}},
		},
	}

	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected error for split with only 1 entry, got nil")
	}
	if !strings.Contains(err.Error(), "at least 2") {
		t.Errorf("expected 'at least 2' in error, got: %v", err)
	}
}

func TestConfigStickyTTLMustBePositive(t *testing.T) {
	cfg := &config.Config{
		Global: config.GlobalConfig{
			LogLevel:        "info",
			ShutdownTimeout: 10 * time.Second,
		},
		Listeners: []config.ListenerConfig{
			{
				Name:     "test",
				Address:  "0.0.0.0:8080",
				Protocol: "http",
				Pool:     "pool_a",
			},
		},
		Pools: []config.PoolConfig{
			{
				Name:      "pool_a",
				Algorithm: "round_robin",
				Backends:  []config.BackendConfig{{Address: "127.0.0.1:8080", Weight: 1}},
				Sticky:    &config.StickyConfig{CookieName: "sticky", TTL: 0},
			},
		},
	}

	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected error for non-positive sticky TTL, got nil")
	}
	if !strings.Contains(err.Error(), "positive duration") {
		t.Errorf("expected 'positive duration' in error, got: %v", err)
	}
}
