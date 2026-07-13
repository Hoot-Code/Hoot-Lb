package admin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

func TestListPools(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	resp := doReq(t, http.MethodGet, baseURL+"/admin/pools", authHeader(), nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body ListPoolsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(body.Pools) != 2 {
		t.Fatalf("expected 2 pools, got %d", len(body.Pools))
	}

	found := map[string]bool{}
	for _, p := range body.Pools {
		found[p.Name] = true
	}
	if !found["static-pool"] || !found["discovery-pool"] {
		t.Fatalf("unexpected pools: %v", body.Pools)
	}
}

func TestRuntimeAddThenReload(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	newBackend := AddBackendRequest{Address: "10.0.0.3:8080", Weight: 1}
	resp := doReq(t, http.MethodPost, baseURL+"/admin/pools/static-pool/backends", authHeader(), newBackend)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add: expected 201, got %d", resp.StatusCode)
	}

	// Build snapshot manually to avoid DNS resolution for discovery-pool.
	staticBackends := []balancer.Backend{
		balancer.NewServer("10.0.0.1:8080", 1),
		balancer.NewServer("10.0.0.2:8080", 1),
	}
	staticServers := make([]*balancer.Server, len(staticBackends))
	for i, b := range staticBackends {
		staticServers[i] = b.(*balancer.Server)
	}
	reloadedSnap := &runtime.Snapshot{
		PoolStates: map[string]*runtime.PoolState{
			"static-pool": {
				LB:      balancer.NewRoundRobin(staticBackends),
				Servers: staticServers,
			},
		},
	}

	ps := reloadedSnap.PoolStates["static-pool"]
	if len(ps.Servers) != 2 {
		t.Fatalf("expected 2 servers after reload, got %d", len(ps.Servers))
	}

	addrs := map[string]bool{}
	for _, srv := range ps.Servers {
		addrs[srv.Address()] = true
	}
	if addrs["10.0.0.3:8080"] {
		t.Fatal("admin-added backend still present after reload")
	}
}
