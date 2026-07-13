//go:build consul

package runtime

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
)

func TestConsulAdapterResolve(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health/service/web" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("passing") != "true" {
			http.Error(w, "passing=true required", http.StatusBadRequest)
			return
		}

		resp := consulHealthResponse{
			{
				Service: struct {
					Address string `json:"Address"`
					Port    int    `json:"Port"`
				}{
					Address: "10.0.0.1",
					Port:    8080,
				},
			},
			{
				Service: struct {
					Address string `json:"Address"`
					Port    int    `json:"Port"`
				}{
					Address: "10.0.0.2",
					Port:    8080,
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	ts := httptest.NewServer(handler)
	defer ts.Close()

	adapter := newConsulAdapter("test-pool", &config.ConsulDiscoveryConfig{
		Address:         ts.URL,
		Service:         "web",
		RefreshInterval: 10 * time.Second,
	}, testConsulLogger())

	backends, err := adapter.Resolve(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(backends) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(backends))
	}

	if backends[0].Address != "10.0.0.1:8080" {
		t.Errorf("expected %q, got %q", "10.0.0.1:8080", backends[0].Address)
	}
	if backends[0].Weight != 1 {
		t.Errorf("expected weight 1, got %d", backends[0].Weight)
	}
	if backends[1].Address != "10.0.0.2:8080" {
		t.Errorf("expected %q, got %q", "10.0.0.2:8080", backends[1].Address)
	}
}

func TestConsulAdapterServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service not found", http.StatusNotFound)
	}))
	defer ts.Close()

	adapter := newConsulAdapter("test-pool", &config.ConsulDiscoveryConfig{
		Address:         ts.URL,
		Service:         "nonexistent",
		RefreshInterval: 10 * time.Second,
	}, testConsulLogger())

	_, err := adapter.Resolve(context.Background())
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}

func TestConsulAdapterEmptyResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "[]")
	}))
	defer ts.Close()

	adapter := newConsulAdapter("test-pool", &config.ConsulDiscoveryConfig{
		Address:         ts.URL,
		Service:         "empty",
		RefreshInterval: 10 * time.Second,
	}, testConsulLogger())

	backends, err := adapter.Resolve(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(backends) != 0 {
		t.Errorf("expected 0 backends, got %d", len(backends))
	}
}

func testConsulLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
