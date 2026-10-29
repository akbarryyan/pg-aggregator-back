package middleware

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// IPRateLimiter grants each client IP its own token bucket, refilled at a
// steady rate. No external dependency (e.g. golang.org/x/time/rate) needed
// for a check this simple — consistent with keeping background jobs
// dependency-free too (see internal/scheduler).
type IPRateLimiter struct {
	mu             sync.Mutex
	buckets        map[string]*tokenBucket
	maxTokens      float64
	refillRate     float64 // tokens per second
	trustedProxies []netip.Prefix
	now            func() time.Time
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

// WithTrustedProxies makes the limiter key requests by the client IP that a
// trusted reverse proxy (e.g. Caddy) reports in X-Forwarded-For, instead of
// the proxy's own address — otherwise every visitor behind the proxy would
// share one bucket. Without it (or with an empty list) only the TCP peer
// address is used.
func (l *IPRateLimiter) WithTrustedProxies(prefixes []netip.Prefix) *IPRateLimiter {
	l.trustedProxies = prefixes
	return l
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
		if !l.allow(l.clientIP(r)) {
			writeJSONError(w, http.StatusTooManyRequests, "Too many requests, please try again later")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the key a request is limited under. The TCP peer address
// is authoritative unless the peer is a configured trusted proxy: only then
// is X-Forwarded-For consulted, walked right to left, and the first address
// that isn't itself a trusted proxy is the client. Entries further left were
// supplied by the client and could be forged, so they are never used. Any
// doubt (no header, an unparseable entry, every entry trusted) falls back to
// the peer address.
func (l *IPRateLimiter) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || !l.isTrustedProxy(peer) {
		return host
	}

	var entries []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		entries = append(entries, strings.Split(v, ",")...)
	}
	for i := len(entries) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(entries[i]))
		if err != nil {
			return host
		}
		addr = addr.Unmap()
		if !l.isTrustedProxy(addr) {
			return addr.String()
		}
	}
	return host
}

func (l *IPRateLimiter) isTrustedProxy(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range l.trustedProxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
