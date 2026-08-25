package ratelimit

import (
	"net"
	"sync"
	"time"
)

// Limiter implements a thread-safe token bucket rate limiter keyed by IP or identifier.
type Limiter struct {
	mu           sync.Mutex
	enabled      bool
	rate         float64 // tokens per second
	burst        int     // bucket size
	visitors     map[string]*visitor
	lastCleanup  time.Time
	cleanupEvery time.Duration
}

type visitor struct {
	tokens     float64
	lastRefill time.Time
}

// New creates a new Limiter.
// maxPerMinute is the allowed number of requests per minute.
// burst is the max burst tokens allowed.
func New(enabled bool, maxPerMinute int, burst int) *Limiter {
	if maxPerMinute <= 0 {
		maxPerMinute = 60
	}
	if burst <= 0 {
		burst = maxPerMinute
	}

	rate := float64(maxPerMinute) / 60.0

	return &Limiter{
		enabled:      enabled,
		rate:         rate,
		burst:        burst,
		visitors:     make(map[string]*visitor),
		lastCleanup:  time.Now(),
		cleanupEvery: 5 * time.Minute,
	}
}

// Allow checks if a request from the given key (IP, client ID, sender) is permitted.
func (l *Limiter) Allow(key string) bool {
	if !l.enabled {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()

	// Periodic cleanup of stale visitor entries (older than 10 minutes)
	if now.Sub(l.lastCleanup) > l.cleanupEvery {
		for k, v := range l.visitors {
			if now.Sub(v.lastRefill) > 10*time.Minute {
				delete(l.visitors, k)
			}
		}
		l.lastCleanup = now
	}

	v, exists := l.visitors[key]
	if !exists {
		l.visitors[key] = &visitor{
			tokens:     float64(l.burst - 1),
			lastRefill: now,
		}
		return true
	}

	// Refill tokens based on elapsed time
	elapsed := now.Sub(v.lastRefill).Seconds()
	v.lastRefill = now
	v.tokens += elapsed * l.rate
	if v.tokens > float64(l.burst) {
		v.tokens = float64(l.burst)
	}

	if v.tokens >= 1.0 {
		v.tokens -= 1.0
		return true
	}

	return false
}

// AllowIP is a helper that extracts IP string and checks rate limit.
func (l *Limiter) AllowIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return l.Allow(ip.String())
}
