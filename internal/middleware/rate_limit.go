package middleware

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// IPRateLimiter grants each client IP its own token bucket, refilled at a
// steady rate. No external dependency (e.g. golang.org/x/time/rate) needed
// for a check this simple — consistent with keeping background jobs
// dependency-free too (see internal/scheduler).
type IPRateLimiter struct {
	mu         sync.Mutex
	buckets    map[string]*tokenBucket
	maxTokens  float64
	refillRate float64 // tokens per second
	now        func() time.Time
}

type tokenBucket struct {
	tokens     float64
	lastRefill time.Time
}

// NewIPRateLimiter allows an immediate burst of `burst` requests per client
// IP, then refills at requestsPerMinute steady-state.
func NewIPRateLimiter(requestsPerMinute float64, burst int) *IPRateLimiter {
	return &IPRateLimiter{
		buckets:    make(map[string]*tokenBucket),
		maxTokens:  float64(burst),
		refillRate: requestsPerMinute / 60,
		now:        time.Now,
	}
}

func (l *IPRateLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[ip]
	if !ok {
		b = &tokenBucket{tokens: l.maxTokens, lastRefill: now}
		l.buckets[ip] = b
	}

	if elapsed := now.Sub(b.lastRefill).Seconds(); elapsed > 0 {
		b.tokens += elapsed * l.refillRate
		if b.tokens > l.maxTokens {
			b.tokens = l.maxTokens
		}
		b.lastRefill = now
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Cleanup evicts buckets untouched for longer than idleAfter, so memory
// doesn't grow unbounded over long uptimes from one-off or attacker IPs.
// Call it periodically (see internal/scheduler.RunPeriodic) from main.go.
func (l *IPRateLimiter) Cleanup(idleAfter time.Duration) {
	cutoff := l.now().Add(-idleAfter)

	l.mu.Lock()
	defer l.mu.Unlock()
	for ip, b := range l.buckets {
		if b.lastRefill.Before(cutoff) {
			delete(l.buckets, ip)
		}
	}
}

// Limit returns middleware that enforces this limiter's rate per client IP,
// responding 429 once exhausted.
func (l *IPRateLimiter) Limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(clientIP(r)) {
			writeJSONError(w, http.StatusTooManyRequests, "Too many requests, please try again later")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP uses the direct TCP peer address only. Headers like
// X-Forwarded-For are attacker-controlled unless a trusted reverse proxy
// strips/overwrites them first, and this API has no such trust boundary
// configured — honoring them here would let a client bypass the limit by
// sending a different value on every request.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
