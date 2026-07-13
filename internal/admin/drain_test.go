package admin

import (
	"context"
	"net/http"
	"testing"
)

func TestDrainBackend(t *testing.T) {
	baseURL, atomicSnap, _, cleanup := startTestServer(t)
	defer cleanup()

	resp := doReq(t, http.MethodPost, baseURL+"/admin/pools/static-pool/backends/10.0.0.1:8080/drain", authHeader(), nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	snap := atomicSnap.Load()
	ps := snap.PoolStates["static-pool"]

	for i := 0; i < 1000; i++ {
		b, err := ps.LB.Pick(context.Background())
		if err != nil {
			t.Fatalf("Pick on iteration %d: %v", i, err)
		}
		if b.Address() == "10.0.0.1:8080" {
			t.Fatalf("drained backend picked on iteration %d", i)
		}
	}

	for _, srv := range ps.Servers {
		if srv.Address() == "10.0.0.1:8080" && !srv.IsDraining() {
			t.Fatal("backend not marked as draining")
		}
	}
}

func TestUndrainBackend(t *testing.T) {
	baseURL, atomicSnap, _, cleanup := startTestServer(t)
	defer cleanup()

	resp := doReq(t, http.MethodPost, baseURL+"/admin/pools/static-pool/backends/10.0.0.1:8080/drain", authHeader(), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("drain: expected 204, got %d", resp.StatusCode)
	}

	resp = doReq(t, http.MethodPost, baseURL+"/admin/pools/static-pool/backends/10.0.0.1:8080/undrain", authHeader(), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("undrain: expected 204, got %d", resp.StatusCode)
	}

	snap := atomicSnap.Load()
	ps := snap.PoolStates["static-pool"]

	found := false
	for i := 0; i < 1000; i++ {
		b, err := ps.LB.Pick(context.Background())
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		if b.Address() == "10.0.0.1:8080" {
			found = true
			break
		}
	}

	if !found {
		t.Fatal("undrained backend never picked")
	}
}

func TestSticky_DrainingFallback(t *testing.T) {
	baseURL, atomicSnap, _, cleanup := startTestServer(t)
	defer cleanup()

	snap := atomicSnap.Load()
	ps := snap.PoolStates["static-pool"]

	b, err := ps.LB.Pick(context.Background())
	if err != nil {
		t.Fatalf("initial Pick: %v", err)
	}
	if b.Address() != "10.0.0.1:8080" && b.Address() != "10.0.0.2:8080" {
		t.Fatalf("unexpected backend: %s", b.Address())
	}

	resp := doReq(t, http.MethodPost, baseURL+"/admin/pools/static-pool/backends/10.0.0.1:8080/drain", authHeader(), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("drain: expected 204, got %d", resp.StatusCode)
	}

	for i := 0; i < 500; i++ {
		b, err = ps.LB.Pick(context.Background())
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		if b.Address() == "10.0.0.1:8080" {
			t.Fatalf("drained backend picked on iteration %d (sticky should have fallen back)", i)
		}
	}

	for _, srv := range ps.Servers {
		if srv.Address() == "10.0.0.1:8080" && !srv.IsDraining() {
			t.Fatal("backend should be draining")
		}
	}
}

func TestAllDrainingPool(t *testing.T) {
	baseURL, atomicSnap, _, cleanup := startTestServer(t)
	defer cleanup()

	for _, addr := range []string{"10.0.0.1:8080", "10.0.0.2:8080"} {
		resp := doReq(t, http.MethodPost, baseURL+"/admin/pools/static-pool/backends/"+addr+"/drain", authHeader(), nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("drain %s: expected 204, got %d", addr, resp.StatusCode)
		}
	}

	snap := atomicSnap.Load()
	ps := snap.PoolStates["static-pool"]

	_, err := ps.LB.Pick(context.Background())
	if err == nil {
		t.Fatal("expected error from Pick with all backends draining, got nil")
	}
}

func TestDrainNonexistentBackend(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	resp := doReq(t, http.MethodPost, baseURL+"/admin/pools/static-pool/backends/10.0.99.99:8080/drain", authHeader(), nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}
