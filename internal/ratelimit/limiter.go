// Package ratelimit implements per-client token bucket rate limiting
// for the load balancer's listeners. Each client IP gets its own token
// bucket with configurable capacity (burst) and refill rate (requests
// per second). Idle buckets are evicted by a background goroutine to
// prevent unbounded memory growth.
package ratelimit

import (
	"hash/fnv"
	"sync"
	"time"
)

const numShards = 16

type shard struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	_       [40]byte // prevent false sharing between adjacent shards
}

// Limiter implements per-client IP token bucket rate limiting. It is
// safe for concurrent use. Each unique client IP gets its own bucket
// with the configured capacity and refill rate.
type Limiter struct {
	shards   [numShards]shard
	rate     float64
	capacity float64
	idleTTL  time.Duration
	stop     chan struct{}
	done     chan struct{}
}

// NewLimiter creates a Limiter with the given requests per second,
// burst capacity, and idle eviction TTL. If idleTTL is positive, a
// background goroutine sweeps and evicts buckets that have been idle
// for longer than idleTTL. Call Stop to halt the sweep goroutine.
func NewLimiter(requestsPerSecond float64, burst int, idleTTL time.Duration) *Limiter {
	l := &Limiter{
		rate:     requestsPerSecond,
		capacity: float64(burst),
		idleTTL:  idleTTL,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	for i := range l.shards {
		l.shards[i].buckets = make(map[string]*bucket)
	}
	if idleTTL > 0 {
		go l.evictLoop()
	} else {
		close(l.done)
	}
	return l
}

// Allow reports whether a request from the given client IP should be
// permitted. It returns true if a token is available (consuming one),
// and false if the client has exceeded its rate.
func (l *Limiter) Allow(clientIP string) bool {
	now := time.Now()
	s := &l.shards[fhash(clientIP)%numShards]
	s.mu.Lock()
	b, ok := s.buckets[clientIP]
	if !ok {
		b = &bucket{
			tokens:   l.capacity,
			lastTime: now,
		}
		s.buckets[clientIP] = b
	}
	result := b.allow(l.rate, l.capacity, now)
	s.mu.Unlock()

	return result
}

// bucket is a single client's token bucket state.
type bucket struct {
	tokens   float64
	lastTime time.Time
}

// allow refills tokens based on elapsed time and checks if one token
// is available. Must be called with Limiter.mu held.
func (b *bucket) allow(rate, capacity float64, now time.Time) bool {
	elapsed := now.Sub(b.lastTime).Seconds()
	if elapsed > 0 {
		b.tokens += rate * elapsed
		if b.tokens > capacity {
			b.tokens = capacity
		}
	}

	if b.tokens >= 1 {
		b.tokens--
		b.lastTime = now
		return true
	}
	b.lastTime = now
	return false
}

// evictLoop periodically scans and removes buckets that have been
// idle for longer than the configured TTL.
func (l *Limiter) evictLoop() {
	defer close(l.done)

	ticker := time.NewTicker(l.idleTTL / 2)
	defer ticker.Stop()

	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			l.evict()
		}
	}
}

// evict removes all buckets that have been idle for longer than idleTTL.
func (l *Limiter) evict() {
	now := time.Now()
	for i := range l.shards {
		s := &l.shards[i]
		s.mu.Lock()
		for ip, b := range s.buckets {
			if now.Sub(b.lastTime) > l.idleTTL {
				delete(s.buckets, ip)
			}
		}
		s.mu.Unlock()
	}
}

// Stop halts the background eviction goroutine. It is safe to call
// even if no eviction goroutine is running (when idleTTL was zero).
func (l *Limiter) Stop() {
	select {
	case <-l.done:
		return
	default:
		close(l.stop)
		<-l.done
	}
}

// Len returns the current number of tracked client buckets. This is
// primarily useful for testing.
func (l *Limiter) Len() int {
	var n int
	for i := range l.shards {
		s := &l.shards[i]
		s.mu.Lock()
		n += len(s.buckets)
		s.mu.Unlock()
	}
	return n
}

func fhash(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}
