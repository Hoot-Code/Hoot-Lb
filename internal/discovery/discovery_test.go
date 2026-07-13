package discovery

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"testing"
)

type fakeLookuper struct {
	ips    []string
	err    error
	called int
}

func (f *fakeLookuper) LookupHost(_ context.Context, _ string) ([]string, error) {
	f.called++
	return f.ips, f.err
}

func TestDNSResolveBasic(t *testing.T) {
	lk := &fakeLookuper{ips: []string{"10.0.0.1", "10.0.0.2"}}
	d := NewDNS("test-dns", lk, "backend.local", 8080, testLogger())

	backends, err := d.Resolve(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(backends) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(backends))
	}

	sort.Slice(backends, func(i, j int) bool {
		return backends[i].Address < backends[j].Address
	})

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

func TestDNSResolvePortFromHost(t *testing.T) {
	lk := &fakeLookuper{ips: []string{"10.0.0.1"}}
	d := NewDNS("test-dns", lk, "backend.local:9000", 0, testLogger())

	backends, err := d.Resolve(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(backends) != 1 {
		t.Fatalf("expected 1 backend, got %d", len(backends))
	}
	if backends[0].Address != "10.0.0.1:9000" {
		t.Errorf("expected %q, got %q", "10.0.0.1:9000", backends[0].Address)
	}
}

func TestDNSResolveError(t *testing.T) {
	lk := &fakeLookuper{err: fmt.Errorf("dns: no such host")}
	d := NewDNS("test-dns", lk, "nonexistent.local", 8080, testLogger())

	_, err := d.Resolve(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDNSResolveEmpty(t *testing.T) {
	lk := &fakeLookuper{ips: []string{}}
	d := NewDNS("test-dns", lk, "empty.local", 8080, testLogger())

	backends, err := d.Resolve(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(backends) != 0 {
		t.Errorf("expected 0 backends, got %d", len(backends))
	}
}

func TestStaticResolve(t *testing.T) {
	fixed := []Backend{
		{Address: "10.0.0.1:8080", Weight: 5},
		{Address: "10.0.0.2:8080", Weight: 3},
	}
	s := NewStatic("test-static", fixed)

	backends, err := s.Resolve(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(backends) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(backends))
	}

	for i, b := range backends {
		if b.Address != fixed[i].Address {
			t.Errorf("backend %d: expected %q, got %q", i, fixed[i].Address, b.Address)
		}
		if b.Weight != fixed[i].Weight {
			t.Errorf("backend %d: expected weight %d, got %d", i, fixed[i].Weight, b.Weight)
		}
	}
}

func TestStaticName(t *testing.T) {
	s := NewStatic("my-static", nil)
	if s.Name() != "my-static" {
		t.Errorf("expected name %q, got %q", "my-static", s.Name())
	}
}

func TestDNSName(t *testing.T) {
	lk := &fakeLookuper{}
	d := NewDNS("my-dns", lk, "host.local", 8080, testLogger())
	if d.Name() != "my-dns" {
		t.Errorf("expected name %q, got %q", "my-dns", d.Name())
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
