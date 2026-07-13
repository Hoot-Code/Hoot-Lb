package balancer

import (
	"context"
	"testing"
)

// BenchmarkPickRoundRobin measures the latency of a single Pick call
// on a RoundRobin balancer with 10 healthy backends.
func BenchmarkPickRoundRobin(b *testing.B) {
	backends := make([]Backend, 10)
	for i := range backends {
		backends[i] = NewServer("10.0.0."+string(rune('1'+i))+":8080", 1)
	}
	rr := NewRoundRobin(backends)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := rr.Pick(ctx)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPickRoundRobin100 is like BenchmarkPickRoundRobin but with
// 100 backends to test scan cost at larger pool sizes.
func BenchmarkPickRoundRobin100(b *testing.B) {
	backends := make([]Backend, 100)
	for i := range backends {
		backends[i] = NewServer("10.0.0.1:808"+string(rune('0'+i%10)), 1)
	}
	rr := NewRoundRobin(backends)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := rr.Pick(ctx)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPickLeastConn measures the latency of a single Pick call
// on a LeastConnections balancer with 10 healthy backends.
func BenchmarkPickLeastConn(b *testing.B) {
	backends := make([]Backend, 10)
	for i := range backends {
		backends[i] = NewServer("10.0.0."+string(rune('1'+i))+":8080", 1)
	}
	lc := NewLeastConnections(backends)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := lc.Pick(ctx)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPickLeastConn100 is like BenchmarkPickLeastConn but with
// 100 backends.
func BenchmarkPickLeastConn100(b *testing.B) {
	backends := make([]Backend, 100)
	for i := range backends {
		backends[i] = NewServer("10.0.0.1:808"+string(rune('0'+i%10)), 1)
	}
	lc := NewLeastConnections(backends)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := lc.Pick(ctx)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPickWeightedRoundRobin measures the latency of a single
// Pick call on a WeightedRoundRobin balancer with 10 backends.
func BenchmarkPickWeightedRoundRobin(b *testing.B) {
	backends := make([]Backend, 10)
	for i := range backends {
		backends[i] = NewServer("10.0.0."+string(rune('1'+i))+":8080", i+1)
	}
	wrr := NewWeightedRoundRobin(backends)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := wrr.Pick(ctx)
		if err != nil {
			b.Fatal(err)
		}
	}
}
