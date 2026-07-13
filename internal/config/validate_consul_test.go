//go:build !consul

package config

import (
	"strings"
	"testing"
	"time"
)

func TestValidateDiscoveryConsulWithoutBuildTag(t *testing.T) {
	cfg := validTestConfig()
	cfg.Pools[0].Backends = nil
	cfg.Pools[0].Discovery = &DiscoveryConfig{
		Type: "consul",
		Consul: &ConsulDiscoveryConfig{
			Address:         "http://127.0.0.1:8500",
			Service:         "web",
			RefreshInterval: 30 * time.Second,
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for consul without build tag, got nil")
	}
	if !strings.Contains(err.Error(), "consul discovery requires building with -tags consul") {
		t.Errorf("expected consul build tag error, got: %v", err)
	}
}
