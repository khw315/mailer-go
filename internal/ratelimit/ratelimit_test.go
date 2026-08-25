package ratelimit

import (
	"net"
	"testing"
	"time"
)

func TestLimiterDisabled(t *testing.T) {
	limiter := New(false, 1, 1)
	for i := 0; i < 10; i++ {
		if !limiter.Allow("192.168.1.1") {
			t.Errorf("expected limiter to allow all when disabled")
		}
	}
}

func TestLimiterBurstAndThrottle(t *testing.T) {
	// 60 per minute = 1 token/sec, burst of 3
	limiter := New(true, 60, 3)

	// First 3 should succeed
	for i := 0; i < 3; i++ {
		if !limiter.Allow("10.0.0.1") {
			t.Errorf("request %d should have been allowed in burst", i+1)
		}
	}

	// 4th immediate request should be throttled
	if limiter.Allow("10.0.0.1") {
		t.Errorf("4th immediate request should be denied by rate limiter")
	}

	// Different IP should still have its full burst
	if !limiter.Allow("10.0.0.2") {
		t.Errorf("different IP should be allowed")
	}

	// Wait 1.1s to regenerate 1 token
	time.Sleep(1100 * time.Millisecond)
	if !limiter.Allow("10.0.0.1") {
		t.Errorf("after waiting token refill, request should be allowed")
	}
}

func TestLimiterAllowIP(t *testing.T) {
	limiter := New(true, 60, 2)
	ip := net.ParseIP("127.0.0.1")

	if !limiter.AllowIP(ip) {
		t.Errorf("expected 1st AllowIP to succeed")
	}
	if !limiter.AllowIP(ip) {
		t.Errorf("expected 2nd AllowIP to succeed")
	}
	if limiter.AllowIP(ip) {
		t.Errorf("expected 3rd AllowIP to fail")
	}
	if !limiter.AllowIP(nil) {
		t.Errorf("expected nil IP to be allowed")
	}
}

func TestLimiterCleanup(t *testing.T) {
	limiter := New(true, 60, 5)
	limiter.cleanupEvery = 1 * time.Millisecond

	limiter.Allow("stale-ip")

	// Artificially age the visitor
	limiter.mu.Lock()
	if v, ok := limiter.visitors["stale-ip"]; ok {
		v.lastRefill = time.Now().Add(-15 * time.Minute)
		limiter.lastCleanup = time.Now().Add(-10 * time.Minute)
	}
	limiter.mu.Unlock()

	// Next allow will trigger cleanup of stale-ip
	limiter.Allow("fresh-ip")

	limiter.mu.Lock()
	_, exists := limiter.visitors["stale-ip"]
	limiter.mu.Unlock()

	if exists {
		t.Errorf("expected stale-ip visitor to be cleaned up")
	}
}
