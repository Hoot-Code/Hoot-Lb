package admin

import (
	"context"
	"net/http"
	"testing"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

func TestAddBackend_StaticPool(t *testing.T) {
	baseURL, atomicSnap, _, cleanup := startTestServer(t)
	defer cleanup()

	newBackend := AddBackendRequest{Address: "10.0.0.3:8080", Weight: 1}
	resp := doReq(t, http.MethodPost, baseURL+"/admin/pools/static-pool/backends", authHeader(), newBackend)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	snap := atomicSnap.Load()
	ps, ok := snap.PoolStates["static-pool"]
	if !ok {
		t.Fatal("static-pool not found in snapshot")
	}

	if len(ps.Servers) != 3 {
		t.Fatalf("expected 3 servers, got %d", len(ps.Servers))
	}

	addrs := map[string]bool{}
	for i := 0; i < 100; i++ {
		b, err := ps.LB.Pick(context.Background())
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		addrs[b.Address()] = true
	}

	if !addrs["10.0.0.3:8080"] {
		t.Fatal("new backend 10.0.0.3:8080 never picked")
	}

	discPS, ok := snap.PoolStates["discovery-pool"]
	if !ok {
		t.Fatal("discovery-pool not found")
	}
	if discPS.LB == nil {
		t.Fatal("discovery-pool LB is nil")
	}

	// Verify original config still has 2 servers (admin changes are
	// snapshot-only, not persisted to config).
	staticBackends := []balancer.Backend{
		balancer.NewServer("10.0.0.1:8080", 1),
		balancer.NewServer("10.0.0.2:8080", 1),
	}
	staticServers := make([]*balancer.Server, len(staticBackends))
	for i, b := range staticBackends {
		staticServers[i] = b.(*balancer.Server)
	}
	newSnap := &runtime.Snapshot{
		PoolStates: map[string]*runtime.PoolState{
			"static-pool": {
				LB:      balancer.NewRoundRobin(staticBackends),
				Servers: staticServers,
			},
		},
	}
	newPS := newSnap.PoolStates["static-pool"]
	if len(newPS.Servers) != 2 {
		t.Fatalf("expected 2 servers after reload, got %d", len(newPS.Servers))
	}
}

func TestAddBackend_DiscoveryPool_Rejected(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	newBackend := AddBackendRequest{Address: "10.0.0.3:8080", Weight: 1}
	resp := doReq(t, http.MethodPost, baseURL+"/admin/pools/discovery-pool/backends", authHeader(), newBackend)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
}

func TestRemoveBackend(t *testing.T) {
	baseURL, atomicSnap, _, cleanup := startTestServer(t)
	defer cleanup()

	addReq := AddBackendRequest{Address: "10.0.0.3:8080", Weight: 1}
	resp := doReq(t, http.MethodPost, baseURL+"/admin/pools/static-pool/backends", authHeader(), addReq)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add: expected 201, got %d", resp.StatusCode)
	}

	resp = doReq(t, http.MethodDelete, baseURL+"/admin/pools/static-pool/backends/10.0.0.1:8080", authHeader(), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	snap := atomicSnap.Load()
	ps := snap.PoolStates["static-pool"]

	addrs := map[string]bool{}
	for _, srv := range ps.Servers {
		addrs[srv.Address()] = true
	}
	if addrs["10.0.0.1:8080"] {
		t.Fatal("removed backend still in snapshot")
	}
	if !addrs["10.0.0.2:8080"] || !addrs["10.0.0.3:8080"] {
		t.Fatalf("unexpected backends: %v", addrs)
	}

	picked := map[string]bool{}
	for i := 0; i < 100; i++ {
		b, err := ps.LB.Pick(context.Background())
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		picked[b.Address()] = true
	}
	if picked["10.0.0.1:8080"] {
		t.Fatal("removed backend picked")
	}
}

func TestRemoveBackend_DiscoveryPool_Rejected(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	resp := doReq(t, http.MethodDelete, baseURL+"/admin/pools/discovery-pool/backends/10.0.0.1:8080", authHeader(), nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
}

func TestDuplicateBackend(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	newBackend := AddBackendRequest{Address: "10.0.0.1:8080", Weight: 1}
	resp := doReq(t, http.MethodPost, baseURL+"/admin/pools/static-pool/backends", authHeader(), newBackend)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
}

func TestPoolNotFound(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	newBackend := AddBackendRequest{Address: "10.0.0.1:8080", Weight: 1}
	resp := doReq(t, http.MethodPost, baseURL+"/admin/pools/nonexistent/backends", authHeader(), newBackend)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestAddBackend_MissingAddress(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	newBackend := AddBackendRequest{Weight: 1}
	resp := doReq(t, http.MethodPost, baseURL+"/admin/pools/static-pool/backends", authHeader(), newBackend)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestRemoveNonexistentBackend(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	resp := doReq(t, http.MethodDelete, baseURL+"/admin/pools/static-pool/backends/10.0.99.99:8080", authHeader(), nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestAddBackend_NonStaticPool_DrivenByDiscovery(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	newBackend := AddBackendRequest{Address: "10.0.0.1:8080", Weight: 1}
	resp := doReq(t, http.MethodPost, baseURL+"/admin/pools/discovery-pool/backends", authHeader(), newBackend)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
}
