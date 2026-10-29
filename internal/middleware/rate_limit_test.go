package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func newTestRequest(remoteAddr string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/whatever", nil)
	req.RemoteAddr = remoteAddr
	return req
}

func doLimited(l *IPRateLimiter, remoteAddr string) int {
	rec := httptest.NewRecorder()
	handler := l.Limit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(rec, newTestRequest(remoteAddr))
	return rec.Code
}

func doLimitedReq(l *IPRateLimiter, req *http.Request) int {
	rec := httptest.NewRecorder()
	l.Limit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	return rec.Code
}

func TestIPRateLimiter_AllowsUpToBurstThenBlocks(t *testing.T) {
	l := NewIPRateLimiter(60, 3) // 1/sec steady-state, burst 3
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return fixed }

	for i := 0; i < 3; i++ {
		if code := doLimited(l, "10.0.0.1:1111"); code != http.StatusOK {
			t.Fatalf("request %d: expected 200 within burst, got %d", i+1, code)
		}
	}

	if code := doLimited(l, "10.0.0.1:1111"); code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 once burst is exhausted, got %d", code)
	}
}

func TestIPRateLimiter_BlockedResponseIsJSON(t *testing.T) {
	l := NewIPRateLimiter(60, 1)
	fixed := time.Now()
	l.now = func() time.Time { return fixed }

	doLimited(l, "10.0.0.2:1111") // consume the only token

	rec := httptest.NewRecorder()
	handler := l.Limit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(rec, newTestRequest("10.0.0.2:1111"))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected JSON content type, got %q", ct)
	}
	if rec.Body.Len() == 0 {
		t.Errorf("expected a JSON error body")
	}
}

func TestIPRateLimiter_RefillsOverTime(t *testing.T) {
	l := NewIPRateLimiter(60, 1) // 1 token/sec, burst 1
	current := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return current }

	if code := doLimited(l, "10.0.0.3:1111"); code != http.StatusOK {
		t.Fatalf("expected first request to succeed, got %d", code)
	}
	if code := doLimited(l, "10.0.0.3:1111"); code != http.StatusTooManyRequests {
		t.Fatalf("expected second immediate request to be blocked, got %d", code)
	}

	current = current.Add(1500 * time.Millisecond) // enough to refill 1 token
	if code := doLimited(l, "10.0.0.3:1111"); code != http.StatusOK {
		t.Fatalf("expected request to succeed after refill window, got %d", code)
	}
}

func TestIPRateLimiter_TracksPerIPIndependently(t *testing.T) {
	l := NewIPRateLimiter(60, 1)
	fixed := time.Now()
	l.now = func() time.Time { return fixed }

	if code := doLimited(l, "10.0.0.4:1111"); code != http.StatusOK {
		t.Fatalf("expected first IP's request to succeed, got %d", code)
	}
	if code := doLimited(l, "10.0.0.4:1111"); code != http.StatusTooManyRequests {
		t.Fatalf("expected first IP to be exhausted, got %d", code)
	}
	// A different client IP must not be affected by the first one's usage.
	if code := doLimited(l, "10.0.0.5:2222"); code != http.StatusOK {
		t.Fatalf("expected second IP's own bucket to be untouched, got %d", code)
	}
}

func TestIPRateLimiter_CleanupEvictsIdleBuckets(t *testing.T) {
	l := NewIPRateLimiter(60, 1)
	current := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return current }

	doLimited(l, "10.0.0.6:1111")
	if len(l.buckets) != 1 {
		t.Fatalf("expected 1 bucket to exist before cleanup, got %d", len(l.buckets))
	}

	current = current.Add(31 * time.Minute)
	l.Cleanup(30 * time.Minute)

	if len(l.buckets) != 0 {
		t.Fatalf("expected idle bucket to be evicted, got %d remaining", len(l.buckets))
	}
}

func TestIPRateLimiter_CleanupKeepsRecentBuckets(t *testing.T) {
	l := NewIPRateLimiter(60, 1)
	current := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return current }

	doLimited(l, "10.0.0.7:1111")
	current = current.Add(5 * time.Minute)
	l.Cleanup(30 * time.Minute)

	if len(l.buckets) != 1 {
		t.Fatalf("expected recently-used bucket to survive cleanup, got %d", len(l.buckets))
	}
}

func TestClientIP(t *testing.T) {
	l := NewIPRateLimiter(60, 1)
	cases := map[string]string{
		"1.2.3.4:5678":    "1.2.3.4",
		"[::1]:5678":      "::1",
		"no-port-address": "no-port-address", // falls back to RemoteAddr verbatim
	}
	for remoteAddr, want := range cases {
		req := newTestRequest(remoteAddr)
		if got := l.clientIP(req); got != want {
			t.Errorf("clientIP(%q) = %q, want %q", remoteAddr, got, want)
		}
	}
}

func TestClientIP_TrustedProxies(t *testing.T) {
	trusted := []netip.Prefix{
		netip.MustParsePrefix("10.89.0.2/32"),
		netip.MustParsePrefix("fd00::/8"),
	}
	tests := []struct {
		name       string
		noTrust    bool // limiter without WithTrustedProxies
		remoteAddr string
		xff        []string // one element per X-Forwarded-For header line
		want       string
	}{
		{"no trusted proxies configured ignores XFF", true, "10.89.0.2:4000", []string{"203.0.113.7"}, "10.89.0.2"},
		{"trusted proxy reports client", false, "10.89.0.2:4000", []string{"203.0.113.7"}, "203.0.113.7"},
		{"spoofed XFF from untrusted peer is ignored", false, "198.51.100.9:4000", []string{"1.2.3.4"}, "198.51.100.9"},
		{"client-forged left entries are ignored", false, "10.89.0.2:4000", []string{"1.2.3.4, 203.0.113.7"}, "203.0.113.7"},
		{"multiple header lines are joined in order", false, "10.89.0.2:4000", []string{"1.2.3.4", "203.0.113.7"}, "203.0.113.7"},
		{"trusted hops on the right are skipped", false, "10.89.0.2:4000", []string{"203.0.113.7, 10.89.0.2"}, "203.0.113.7"},
		{"no header from trusted proxy falls back to peer", false, "10.89.0.2:4000", nil, "10.89.0.2"},
		{"unparseable entry falls back to peer", false, "10.89.0.2:4000", []string{"203.0.113.7, garbage"}, "10.89.0.2"},
		{"all entries trusted falls back to peer", false, "10.89.0.2:4000", []string{"10.89.0.2"}, "10.89.0.2"},
		{"IPv6 trusted proxy", false, "[fd00::5]:4000", []string{"2001:db8::1"}, "2001:db8::1"},
		{"IPv4-mapped client is unmapped", false, "10.89.0.2:4000", []string{"::ffff:203.0.113.7"}, "203.0.113.7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := NewIPRateLimiter(60, 1)
			if !tt.noTrust {
				l = l.WithTrustedProxies(trusted)
			}
			req := newTestRequest(tt.remoteAddr)
			for _, v := range tt.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			if got := l.clientIP(req); got != tt.want {
				t.Errorf("clientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

// The bug this feature fixes: behind Caddy every request arrives from the
// proxy's address, so without trusted proxies all visitors would share one
// bucket (e.g. one 5/min login budget for everyone).
func TestIPRateLimiter_SeparateBucketsBehindTrustedProxy(t *testing.T) {
	l := NewIPRateLimiter(60, 1).WithTrustedProxies([]netip.Prefix{netip.MustParsePrefix("10.89.0.2/32")})
	fixed := time.Now()
	l.now = func() time.Time { return fixed }

	viaProxy := func(client string) *http.Request {
		req := newTestRequest("10.89.0.2:4000")
		req.Header.Set("X-Forwarded-For", client)
		return req
	}

	if code := doLimitedReq(l, viaProxy("203.0.113.7")); code != http.StatusOK {
		t.Fatalf("client A first request: got %d, want 200", code)
	}
	if code := doLimitedReq(l, viaProxy("203.0.113.7")); code != http.StatusTooManyRequests {
		t.Fatalf("client A second request: got %d, want 429", code)
	}
	if code := doLimitedReq(l, viaProxy("198.51.100.9")); code != http.StatusOK {
		t.Fatalf("client B must have its own bucket: got %d, want 200", code)
	}
}
