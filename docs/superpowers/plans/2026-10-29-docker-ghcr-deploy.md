# Docker + GHCR + Caddy Deploy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run the Go backend in Docker locally (hot reload) and on an AWS VPS behind Caddy HTTPS, with multi-arch images pushed to private GHCR, and move rate-limiter settings (incl. trusted proxies) into `.config.toml`.

**Architecture:** One `Dockerfile` with `build` (cross-compile), `dev` (Go + air, local only) and `runtime` (alpine + binaries + psql, pushed) stages. `docker-compose.yaml` is the production stack (caddy → api → postgres, plus a `tools`-profile `migrate` one-shot); `docker-compose.override.yaml`, present only in the local checkout, swaps in the dev image with the source mounted. The rate limiter trusts `X-Forwarded-For` only from CIDRs listed in `[rate_limit] trusted_proxies`.

**Tech Stack:** Go 1.25, Docker Engine 29 + Compose v2 + buildx, `golang:1.25-alpine`, `alpine:3.24`, `postgres:18-alpine`, `caddy:2.11-alpine`, air v1.67.1, bash.

**Spec:** [`back/docs/superpowers/specs/2026-10-29-docker-ghcr-deploy-design.md`](../specs/2026-10-29-docker-ghcr-deploy-design.md)

## Global Constraints

- **NO git commits, NO `git add`, NO branches — ever — in this work (explicit user instruction).** Every task ends with "report changed files", not a commit.
- **Never print secrets.** Never `cat`/`echo`/`grep`-print `back/.config.toml`, `back/db_password`, `back/.env`, `flow-test/.env`, or `configenv` stdout. Only key names may appear in output/reports.
- **Ask the user before:** modifying `back/.config.toml`, installing binfmt/QEMU on the laptop, pushing to GHCR, or running anything on the VPS.
- Go module path `github.com/akbarryyan/pg-aggregator-back`; Go commands run from `back/`.
- No new Go dependencies (only the standard library, incl. `net/netip`).
- Config stays TOML-only with no defaults: every key is required; unknown keys rejected; error messages name keys/indices, never values.
- Image: `ghcr.io/akbarryyan/whuzpay-backend:<VERSION>`, platforms `linux/amd64,linux/arm64`, no `latest` tag.
- Pinned versions: `golang:1.25-alpine`, `alpine:3.24`, `postgres:18-alpine`, `postgresql18-client`, `caddy:2.11-alpine`, `github.com/air-verse/air@v1.67.1` (last air release whose go.mod is `go 1.25`; v1.67.2+ needs Go 1.26).
- Runtime container user uid/gid **10001**; Postgres container user uid **70**.
- Compose network `whuzpay` subnet `10.89.0.0/24`; Caddy fixed IP `10.89.0.2`; VPS `trusted_proxies = ["10.89.0.2/32"]`.
- Postgres in compose: `POSTGRES_USER=postgres`, `POSTGRES_DB=pg_aggregator`, password from secret file `./db_password` (must equal `db.password`).
- Local ports: API `localhost:8090` → container `8080`; Postgres `127.0.0.1:5433` → `5432`.
- Rate-limit values (template + local + VPS): auth 5 rpm / burst 5; public 120 / 30; sensitive 10 / 5.
- Ignore `back/.superpowers/` (orchestration snapshots) in greps and reviews.
- Comments explain *why*; tests are table-driven with the standard `testing` package; docs are in Indonesian.

## File Structure

| File | Action | Responsibility |
|---|---|---|
| `back/internal/config/config.go` | Modify | `RateLimitConfig`/`RateLimitRule`, `TrustedProxyPrefixes`, validation |
| `back/internal/config/config_test.go` | Modify | `[rate_limit]` fixture + tests |
| `back/internal/middleware/rate_limit.go` | Rewrite | `WithTrustedProxies`, XFF-aware client IP |
| `back/internal/middleware/rate_limit_test.go` | Modify | trusted-proxy tests |
| `back/cmd/api/main.go` | Modify | limiters built from config |
| `back/Dockerfile`, `back/.dockerignore` | Create | 3-stage image; keep secrets out of context |
| `back/scripts/lib/db.sh` | Create | `load_config` + `psql_cmd` shared by scripts |
| `back/scripts/migrate.sh`, `back/scripts/seed.sh` | Rewrite | use `scripts/lib/db.sh` |
| `back/scripts/seed-admin.sh` | Create | first admin on an empty `admins` table only |
| `back/scripts/db-create.sh` | Delete | DB is created by the Postgres container |
| `back/docker-compose.yaml` | Create | production stack |
| `back/docker-compose.override.yaml` | Create | local dev overrides (auto-merged) |
| `back/Caddyfile` | Create | HTTPS reverse proxy |
| `back/Makefile` | Rewrite | `migrate`, `seed-admin`, `seed` (local-only), `release` |
| `back/.gitignore` | Modify | ignore `db_password` |
| `back/.config.example.toml` | Modify | `[rate_limit]` + Docker-local values |
| `back/DEPLOY.md` | Create | VPS guide |
| `README.md`, `RUNNING.md`, `back/README.md` | Modify | Docker workflow docs |

---

### Task 1: `[rate_limit]` config section

**Files:**
- Modify: `back/internal/config/config.go`
- Modify: `back/internal/config/config_test.go`
- Modify: `back/.config.example.toml` (append section)

**Interfaces:**
- Consumes: existing `config.Load`, `Validate`, `missingKeys` reflection walk (recurses into struct-typed fields; slices are leaves).
- Produces (used by Task 2):
  - `Config.RateLimit RateLimitConfig` (toml `rate_limit`)
  - `type RateLimitConfig struct { TrustedProxies []string; Auth, Public, Sensitive RateLimitRule }` (toml `trusted_proxies`, `auth`, `public`, `sensitive`)
  - `type RateLimitRule struct { RequestsPerMinute int; Burst int }` (toml `requests_per_minute`, `burst`)
  - `func (r RateLimitConfig) TrustedProxyPrefixes() ([]netip.Prefix, error)`

- [ ] **Step 1: Extend the test fixture**

In `back/internal/config/config_test.go`, the `validTOML` constant currently ends with:

```
[security]
jwt_secret = "dev-jwt-secret"
`
```

Replace that ending with (the burst values 4/30/6 deliberately differ from each other so `replaceOnce` can target each line uniquely):

```
[security]
jwt_secret = "dev-jwt-secret"

[rate_limit]
trusted_proxies = []

[rate_limit.auth]
requests_per_minute = 5
burst               = 4

[rate_limit.public]
requests_per_minute = 120
burst               = 30

[rate_limit.sensitive]
requests_per_minute = 10
burst               = 6
`
```

- [ ] **Step 2: Add the failing tests**

2a. Add `"net/netip"` to the test file's import block (keep it sorted: `"net/netip"`, `"os"`, `"path/filepath"`, `"strings"`, `"testing"`).

2b. In `TestLoad_ValidFile`, add after the existing `ConnectionString` check (before the closing `}`):

```go
	rl := cfg.RateLimit
	if len(rl.TrustedProxies) != 0 {
		t.Errorf("trusted proxies = %d entries, want 0", len(rl.TrustedProxies))
	}
	if rl.Auth != (RateLimitRule{RequestsPerMinute: 5, Burst: 4}) ||
		rl.Public != (RateLimitRule{RequestsPerMinute: 120, Burst: 30}) ||
		rl.Sensitive != (RateLimitRule{RequestsPerMinute: 10, Burst: 6}) {
		t.Errorf("rate limit rules = %+v", rl)
	}
```

2c. In `TestLoad_Validation`, the `tests` slice ends with the `"valid production"` entry followed by `}` (closing the slice). Insert these entries after the `"valid production"` entry, before that closing `}`:

```go
		{
			name:    "rate limit requests_per_minute zero",
			edits:   []edit{{`requests_per_minute = 5`, `requests_per_minute = 0`}},
			wantErr: "rate_limit.auth.requests_per_minute must be greater than 0",
		},
		{
			name:    "rate limit burst zero",
			edits:   []edit{{`burst               = 30`, `burst               = 0`}},
			wantErr: "rate_limit.public.burst must be at least 1",
		},
		{
			name:    "rate limit burst negative",
			edits:   []edit{{`burst               = 6`, `burst               = -1`}},
			wantErr: "rate_limit.sensitive.burst must be at least 1",
		},
		{
			name:    "invalid trusted proxy CIDR",
			edits:   []edit{{`trusted_proxies = []`, `trusted_proxies = ["10.89.0.2/32", "not-a-cidr"]`}},
			wantErr: "rate_limit.trusted_proxies[1] is not a valid CIDR",
		},
		{
			name:  "valid trusted proxies (IPv4 and IPv6)",
			edits: []edit{{`trusted_proxies = []`, `trusted_proxies = ["10.89.0.2/32", "2001:db8::/32"]`}},
		},
```

2d. In `TestLoad_ErrorsNeverContainSecretValues`, add this entry to the `cases := map[string]string{ ... }` literal, after the `"type mismatch"` entry:

```go
		// trusted_proxies errors are reported by index only
		"invalid trusted proxy": replaceOnce(t, secrets, `trusted_proxies = []`, `trusted_proxies = ["LEAKCHECK-cidr"]`),
```

2e. In `TestLoad_ExampleFileIsValid`, add before its closing `}`:

```go
	if cfg.RateLimit.Auth != (RateLimitRule{RequestsPerMinute: 5, Burst: 5}) ||
		cfg.RateLimit.Public != (RateLimitRule{RequestsPerMinute: 120, Burst: 30}) ||
		cfg.RateLimit.Sensitive != (RateLimitRule{RequestsPerMinute: 10, Burst: 5}) {
		t.Errorf("example rate limits = %+v; want auth 5/5, public 120/30, sensitive 10/5", cfg.RateLimit)
	}
```

2f. Append two new test functions at the end of the file:

```go
func TestLoad_MissingRateLimitSubTable(t *testing.T) {
	content := replaceOnce(t, validTOML, "[rate_limit.auth]\nrequests_per_minute = 5\nburst               = 4\n", "")
	_, err := Load(writeConfig(t, content))
	if err == nil {
		t.Fatal("expected missing-key error")
	}
	want := "missing keys: rate_limit.auth.burst, rate_limit.auth.requests_per_minute"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err, want)
	}
}

func TestRateLimitConfig_TrustedProxyPrefixes(t *testing.T) {
	rl := RateLimitConfig{TrustedProxies: []string{"10.89.0.2/32", "2001:db8::/32"}}
	got, err := rl.TrustedProxyPrefixes()
	if err != nil {
		t.Fatalf("TrustedProxyPrefixes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d prefixes, want 2", len(got))
	}
	if !got[0].Contains(netip.MustParseAddr("10.89.0.2")) || got[0].Contains(netip.MustParseAddr("10.89.0.3")) {
		t.Errorf("prefix 0 = %v, want exactly 10.89.0.2", got[0])
	}
	if !got[1].Contains(netip.MustParseAddr("2001:db8::1")) {
		t.Errorf("prefix 1 = %v, want 2001:db8::/32", got[1])
	}

	// A bare IP is not a CIDR; the error names the index, never the value.
	_, err = RateLimitConfig{TrustedProxies: []string{"10.89.0.2"}}.TrustedProxyPrefixes()
	if err == nil || err.Error() != "rate_limit.trusted_proxies[0] is not a valid CIDR" {
		t.Errorf("bare IP error = %v", err)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/config/ -count=1`
Expected: FAIL — compile errors `cfg.RateLimit undefined` / `undefined: RateLimitRule` / `undefined: RateLimitConfig`.

- [ ] **Step 4: Implement in `back/internal/config/config.go`**

4a. Add `"net/netip"` to the import block (sorted: `"errors"`, `"fmt"`, `"io/fs"`, `"net/netip"`, `"reflect"`, `"sort"`, `"strings"`).

4b. In `type Config struct`, add after the `Security` field:

```go
	RateLimit RateLimitConfig `toml:"rate_limit"`
```

4c. Add after `type SecurityConfig struct { ... }`:

```go
// RateLimitConfig holds the per-client-IP limiter budgets and the reverse
// proxies whose X-Forwarded-For header may name the client IP.
type RateLimitConfig struct {
	// TrustedProxies lists CIDRs of reverse proxies (e.g. Caddy) allowed to
	// report the client IP via X-Forwarded-For. Empty means the header is
	// never trusted and the TCP peer address is used.
	TrustedProxies []string      `toml:"trusted_proxies"`
	Auth           RateLimitRule `toml:"auth"`
	Public         RateLimitRule `toml:"public"`
	Sensitive      RateLimitRule `toml:"sensitive"`
}

type RateLimitRule struct {
	RequestsPerMinute int `toml:"requests_per_minute"`
	Burst             int `toml:"burst"`
}

// TrustedProxyPrefixes parses TrustedProxies. Errors name the entry's index,
// never its value, like every other config error.
func (r RateLimitConfig) TrustedProxyPrefixes() ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(r.TrustedProxies))
	for i, raw := range r.TrustedProxies {
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("rate_limit.trusted_proxies[%d] is not a valid CIDR", i)
		}
		prefixes = append(prefixes, p)
	}
	return prefixes, nil
}
```

4d. In `Validate()`, insert right after the `security.jwt_secret is required` check and before `if c.App.Environment == "production" {`:

```go
	if _, err := c.RateLimit.TrustedProxyPrefixes(); err != nil {
		return err
	}

	for _, r := range []struct {
		name string
		rule RateLimitRule
	}{
		{"auth", c.RateLimit.Auth},
		{"public", c.RateLimit.Public},
		{"sensitive", c.RateLimit.Sensitive},
	} {
		if r.rule.RequestsPerMinute <= 0 {
			return fmt.Errorf("rate_limit.%s.requests_per_minute must be greater than 0", r.name)
		}
		// A zero burst makes the limiter reject every request — login would
		// silently stop working instead of failing at startup.
		if r.rule.Burst < 1 {
			return fmt.Errorf("rate_limit.%s.burst must be at least 1", r.name)
		}
	}
```

- [ ] **Step 5: Append `[rate_limit]` to `back/.config.example.toml`**

Append at the end of the file:

```toml

[rate_limit]  # per-client-IP token buckets (internal/middleware/rate_limit.go)
# CIDRs of reverse proxies whose X-Forwarded-For header names the client IP.
# Must be CIDRs ("10.89.0.2/32", not "10.89.0.2"). Empty = never trust the
# header (use the TCP peer). On the VPS: ["10.89.0.2/32"] (Caddy).
trusted_proxies = []

[rate_limit.auth]       # login & register — brute-force target
requests_per_minute = 5  # must be > 0
burst               = 5  # must be >= 1

[rate_limit.public]     # checkout, payment links, provider webhooks, merchant API key
requests_per_minute = 120
burst               = 30

[rate_limit.sensitive]  # change-password, API key create/rotate/revoke, webhook secret rotation
requests_per_minute = 10
burst               = 5
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/config/ -count=1 -v`
Expected: PASS for every `TestLoad_*` (incl. all `TestLoad_Validation` subtests) and `TestRateLimitConfig_TrustedProxyPrefixes`.

- [ ] **Step 7: gofmt, vet, full suite**

Run: `gofmt -l ./internal/config && go vet ./... && go test ./... -race -count=1`
Expected: gofmt prints nothing; vet clean; all tests pass. (Note: `back/.config.toml` on this machine now lacks `[rate_limit]`, so the local backend/configenv will refuse to start until Task 6 — expected; tests do not read it.)

- [ ] **Step 8: Report (no commit)**

Changed: `internal/config/config.go`, `internal/config/config_test.go`, `.config.example.toml`.

---

### Task 2: Trusted-proxy-aware rate limiter + wiring

**Files:**
- Rewrite: `back/internal/middleware/rate_limit.go`
- Modify: `back/internal/middleware/rate_limit_test.go`
- Modify: `back/cmd/api/main.go` (imports; limiter block ~lines 108–121; new helper above `func setupRouter(`)

**Interfaces:**
- Consumes (Task 1): `config.RateLimitRule{RequestsPerMinute int; Burst int}`, `cfg.RateLimit.{Auth,Public,Sensitive}`, `cfg.RateLimit.TrustedProxyPrefixes() ([]netip.Prefix, error)`.
- Produces: `func (l *IPRateLimiter) WithTrustedProxies(prefixes []netip.Prefix) *IPRateLimiter`; unexported `(l *IPRateLimiter) clientIP(r *http.Request) string`. `NewIPRateLimiter(requestsPerMinute float64, burst int)` unchanged (router tests use it).

- [ ] **Step 1: Write the failing tests**

In `back/internal/middleware/rate_limit_test.go`:

1a. Add `"net/netip"` to imports (sorted: `"net/http"`, `"net/http/httptest"`, `"net/netip"`, `"testing"`, `"time"`).

1b. Add this helper after `doLimited`:

```go
func doLimitedReq(l *IPRateLimiter, req *http.Request) int {
	rec := httptest.NewRecorder()
	l.Limit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	return rec.Code
}
```

1c. Replace the whole existing `TestClientIP` function with:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/middleware/ -count=1`
Expected: FAIL — `l.clientIP undefined` / `l.WithTrustedProxies undefined`.

- [ ] **Step 3: Rewrite `back/internal/middleware/rate_limit.go`**

Replace the whole file with:

```go
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
```

- [ ] **Step 4: Run the middleware tests**

Run: `go test ./internal/middleware/ -count=1 -v -run 'ClientIP|IPRateLimiter'`
Expected: PASS for all, including every `TestClientIP_TrustedProxies` subtest and `TestIPRateLimiter_SeparateBucketsBehindTrustedProxy`.

- [ ] **Step 5: Wire limiters to config in `back/cmd/api/main.go`**

5a. Add `"net/netip"` to the import block directly after `"net/http"`.

5b. Replace exactly:

```go
	// them with no throttling at all. Per-IP, in-process — see
	// internal/middleware/rate_limit.go.
	authRateLimiter := middleware.NewIPRateLimiter(5, 5)       // 5 req/min, burst 5
	publicRateLimiter := middleware.NewIPRateLimiter(120, 30)  // 120 req/min, burst 30
	sensitiveRateLimiter := middleware.NewIPRateLimiter(10, 5) // 10 req/min, burst 5
```

with:

```go
	// them with no throttling at all. Per-client-IP, in-process — see
	// internal/middleware/rate_limit.go. Budgets and the trusted reverse
	// proxies (Caddy on the VPS) come from [rate_limit] in .config.toml.
	trustedProxies, err := cfg.RateLimit.TrustedProxyPrefixes()
	if err != nil {
		// Validate already parsed these, so this only fires if the two drift.
		logger.Errorf("Invalid rate limit config: %v", err)
		os.Exit(1)
	}
	authRateLimiter := newRateLimiter(cfg.RateLimit.Auth, trustedProxies)
	publicRateLimiter := newRateLimiter(cfg.RateLimit.Public, trustedProxies)
	sensitiveRateLimiter := newRateLimiter(cfg.RateLimit.Sensitive, trustedProxies)
```

5c. Add this function directly above `func setupRouter(`:

```go
func newRateLimiter(rule config.RateLimitRule, trustedProxies []netip.Prefix) *middleware.IPRateLimiter {
	return middleware.NewIPRateLimiter(float64(rule.RequestsPerMinute), rule.Burst).
		WithTrustedProxies(trustedProxies)
}
```

- [ ] **Step 6: gofmt, vet, full suite**

Run: `gofmt -l ./internal ./cmd && go vet ./... && go test ./... -race -count=1`
Expected: no gofmt output; vet clean; all tests pass (router tests still use `NewIPRateLimiter(1000, 1000)` unchanged).

- [ ] **Step 7: Report (no commit)**

Changed: `internal/middleware/rate_limit.go`, `internal/middleware/rate_limit_test.go`, `cmd/api/main.go`.

---

### Task 3: Dockerfile, `.dockerignore`, scripts

**Files:**
- Create: `back/Dockerfile`, `back/.dockerignore`, `back/scripts/lib/db.sh`, `back/scripts/seed-admin.sh`
- Rewrite: `back/scripts/migrate.sh`, `back/scripts/seed.sh`
- Delete: `back/scripts/db-create.sh`

**Interfaces:**
- Consumes: `cmd/api`, `cmd/configenv` (prints `export DB_HOST=… DB_PORT … DB_USER … DB_PASSWORD … DB_NAME … CASHI_SECRET_KEY`; default config path `.config.toml`; exit 1 + empty stdout on failure).
- Produces (used by Tasks 4–8):
  - Docker targets `runtime` (default/last stage; WORKDIR `/app`; `/app/bin/{api,configenv}` on PATH; `/app/{migrations,seeds,scripts}`; user 10001; `ENTRYPOINT ["/app/bin/api"]`) and `dev` (WORKDIR `/src`; Go + `air` + bash + psql).
  - Scripts run from the dir containing `scripts/` (back/ or /app): `bash scripts/migrate.sh`, `bash scripts/seed.sh`, `bash scripts/seed-admin.sh`.

- [ ] **Step 1: Create `back/scripts/lib/db.sh`**

```bash
# Shared helpers for the database scripts in scripts/. Sourced, not
# executed; callers cd into the directory holding scripts/ (back/ locally,
# /app in the runtime image) with `set -euo pipefail` active.

# load_config sets DB_* (and CASHI_SECRET_KEY) from .config.toml via
# configenv and checks the values psql needs. The dev container has Go and
# the source; the runtime image ships a compiled configenv instead.
load_config() {
  # Declared separately from the assignment so a configenv failure isn't
  # masked by `local`'s own exit status. Two steps on purpose: `eval
  # "$(cmd)"` would swallow the failure too (eval of "" returns 0).
  local config_env
  if command -v go >/dev/null 2>&1; then
    config_env="$(go run ./cmd/configenv)"
  else
    config_env="$(configenv)"
  fi
  eval "$config_env"

  : "${DB_HOST:?DB_HOST is required}"
  : "${DB_PORT:?DB_PORT is required}"
  : "${DB_USER:?DB_USER is required}"
  : "${DB_PASSWORD:?DB_PASSWORD is required}"
  : "${DB_NAME:?DB_NAME is required}"
}

psql_cmd() {
  PGPASSWORD="$DB_PASSWORD" psql \
    -h "$DB_HOST" \
    -p "$DB_PORT" \
    -U "$DB_USER" \
    -d "$DB_NAME" \
    -v ON_ERROR_STOP=1 \
    "$@"
}
```

- [ ] **Step 2: Rewrite `back/scripts/migrate.sh`**

```bash
#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

# shellcheck source=lib/db.sh
. scripts/lib/db.sh
load_config

# Track applied migrations so re-runs skip files that already succeeded.
psql_cmd -c "
CREATE TABLE IF NOT EXISTS schema_migrations (
    filename TEXT PRIMARY KEY,
    applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
"

applied=0
skipped=0

for file in migrations/*.sql; do
  base="$(basename "$file")"

  already_applied="$(
    psql_cmd -tA -c "SELECT 1 FROM schema_migrations WHERE filename = '$base' LIMIT 1;"
  )"

  if [[ "$already_applied" == "1" ]]; then
    echo "Skipping $file (already applied)"
    skipped=$((skipped + 1))
    continue
  fi

  echo "Running $file"
  psql_cmd -f "$file"
  psql_cmd -c "INSERT INTO schema_migrations (filename) VALUES ('$base') ON CONFLICT (filename) DO NOTHING;"
  applied=$((applied + 1))
done

echo "Migrations completed (applied: $applied, skipped: $skipped)"
```

- [ ] **Step 3: Rewrite `back/scripts/seed.sh`**

```bash
#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

# shellcheck source=lib/db.sh
. scripts/lib/db.sh
load_config

for file in seeds/*.sql; do
  echo "Seeding $file"
  psql_cmd -f "$file"
done

echo "Seed completed"
```

- [ ] **Step 4: Create `back/scripts/seed-admin.sh`** (then `chmod +x back/scripts/seed-admin.sh`)

```bash
#!/usr/bin/env bash
# Creates the initial platform admin (seeds/002_admin_user.sql:
# admin@pg-aggregator.local / Admin123!) on a fresh database. That seed is an
# upsert keyed on email, so re-running it would silently reset a changed
# password back to the publicly documented default — this script therefore
# refuses once any admin exists.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

# shellcheck source=lib/db.sh
. scripts/lib/db.sh
load_config

admin_count="$(psql_cmd -tA -c "SELECT count(*) FROM admins;")"
if [[ "$admin_count" != "0" ]]; then
  echo "admins table is not empty — seed-admin only runs on a fresh database" >&2
  exit 1
fi

psql_cmd -f seeds/002_admin_user.sql
echo "Admin created: admin@pg-aggregator.local / Admin123!"
echo "Change this password NOW (see DEPLOY.md, section 3) — it is published in the README."
```

- [ ] **Step 5: Delete `back/scripts/db-create.sh`**

Run: `rm back/scripts/db-create.sh` (plain `rm`, not `git rm`).

- [ ] **Step 6: Create `back/.dockerignore`**

```
# Everything here stays out of the build context, and therefore out of
# every image layer. Secrets above all: config and DB password are mounted
# at runtime, never baked in.
.git
.github
.env
.env.*
.config.toml
db_password
tmp/
.superpowers/
docs/
bruno/
docker-compose*.yaml
Caddyfile
Makefile
.air.toml
*.md
```

- [ ] **Step 7: Create `back/Dockerfile`**

```dockerfile
# syntax=docker/dockerfile:1

# Pinned so rebuilding a release is reproducible. postgresql18-client must
# match the postgres:18 server in docker-compose.yaml.
ARG GO_VERSION=1.25
ARG ALPINE_VERSION=3.24

# build: cross-compiles on the build host's native arch, so producing the
# arm64 image never runs the Go toolchain under emulation.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
      go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
      go build -trimpath -ldflags="-s -w" -o /out/configenv ./cmd/configenv

# dev: local hot reload only (docker-compose.override.yaml mounts the source
# at /src and runs air). Never pushed. air v1.67.1 is the last release that
# builds with Go 1.25.
FROM golang:${GO_VERSION}-alpine AS dev
RUN apk add --no-cache bash postgresql18-client \
 && go install github.com/air-verse/air@v1.67.1
WORKDIR /src

# runtime: the image pushed to GHCR. Must stay the LAST stage so a build
# without --target (make release) produces it.
FROM alpine:${ALPINE_VERSION} AS runtime
ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/akbarryyan/pg-aggregator-back" \
      org.opencontainers.image.title="whuzpay-backend" \
      org.opencontainers.image.version="${VERSION}"
RUN apk add --no-cache bash ca-certificates tzdata postgresql18-client \
 && addgroup -S -g 10001 app \
 && adduser -S -D -H -u 10001 -G app -h /app app
WORKDIR /app
COPY --from=build /out/api /out/configenv /app/bin/
COPY migrations ./migrations
COPY seeds ./seeds
COPY scripts ./scripts
ENV PATH="/app/bin:${PATH}"
USER app
EXPOSE 8080
# .config.toml is mounted at /app/.config.toml at runtime (never baked in);
# api's default -config path resolves to it from WORKDIR /app.
ENTRYPOINT ["/app/bin/api"]
```

- [ ] **Step 8: Syntax-check scripts**

Run (from `back/`): `for f in scripts/lib/db.sh scripts/migrate.sh scripts/seed.sh scripts/seed-admin.sh; do bash -n "$f" && echo "ok $f"; done; ls scripts`
Expected: four `ok` lines; `ls` shows `lib migrate.sh seed-admin.sh seed.sh` (no `db-create.sh`).

- [ ] **Step 9: Build and inspect the runtime image (native arch)**

Run (from `back/`):
```bash
docker build --target runtime --build-arg VERSION=check -t whuzpay-backend:check .
docker run --rm --entrypoint id whuzpay-backend:check
docker run --rm --entrypoint sh whuzpay-backend:check -c 'ls -A /app; ls -A /app/scripts /app/scripts/lib; command -v configenv psql bash; psql --version'
docker run --rm whuzpay-backend:check; echo "api exit=$?"
docker run --rm --entrypoint bash whuzpay-backend:check scripts/migrate.sh; echo "migrate exit=$?"
docker image inspect whuzpay-backend:check --format '{{index .Config.Labels "org.opencontainers.image.version"}} {{.Size}}'
```
Expected:
- `uid=10001(app) gid=10001(app)`.
- `/app` contains exactly `bin migrations scripts seeds` — **no** `.config.toml`, `.env*`, `db_password`, `docs`, `bruno`.
- `/app/bin/configenv`, `/usr/bin/psql`, `/bin/bash` found; `psql (PostgreSQL) 18.x`.
- api: error log containing `config file not found: .config.toml` and `api exit=1`.
- migrate: `configenv: config file not found: .config.toml …` and non-zero `migrate exit` (proves the no-Go fallback path and that the script stops).
- label `check`; size reported (expect roughly 40–60 MB).

- [ ] **Step 10: Build and inspect the dev image**

Run: `docker build --target dev -t whuzpay-backend:dev . && docker run --rm whuzpay-backend:dev sh -c 'air -v | head -3; psql --version; bash --version | head -1; go version'`
Expected: air banner with `v1.67.1`, `psql (PostgreSQL) 18.x`, bash 5.x, `go1.25.x`.

- [ ] **Step 11: Clean up and report (no commit)**

Run: `docker image rm whuzpay-backend:check`. (Keep `whuzpay-backend:dev`.)
Changed: `Dockerfile`, `.dockerignore`, `scripts/lib/db.sh`, `scripts/migrate.sh`, `scripts/seed.sh`, `scripts/seed-admin.sh`; deleted `scripts/db-create.sh`.

---

### Task 4: Compose, Caddy, Makefile, example config

**Files:**
- Create: `back/docker-compose.yaml`, `back/docker-compose.override.yaml`, `back/Caddyfile`
- Rewrite: `back/Makefile`, `back/.config.example.toml`
- Modify: `back/.gitignore`

**Interfaces:**
- Consumes (Task 3): image targets `runtime`/`dev`, scripts `scripts/{migrate,seed,seed-admin}.sh`.
- Produces (used by Tasks 5–8): services `caddy`, `api`, `postgres`, `migrate` (profile `tools`); commands `docker compose up -d`, `make migrate`, `make seed-admin`, `make seed` (local-only), `make release VERSION=x.y.z`; files on VPS `~/whuzpay/{docker-compose.yaml,Caddyfile,Makefile,.config.toml,db_password}`.

- [ ] **Step 1: Create `back/docker-compose.yaml`**

```yaml
# Production stack (VPS). Locally, docker-compose.override.yaml is merged in
# automatically and swaps api/migrate to the hot-reload dev image.
#
# New release: change the tag on the next line to the pushed version, then
# `docker compose up -d` (plus `make migrate` if the release adds
# migrations). api and migrate share this one tag.
x-api-image: &api-image ghcr.io/akbarryyan/whuzpay-backend:1.0.0

x-logging: &logging
  driver: json-file
  options:
    max-size: "10m"
    max-file: "3"

services:
  caddy:
    image: caddy:2.11-alpine
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config
    networks:
      whuzpay:
        # Fixed so .config.toml can trust exactly this address:
        # [rate_limit] trusted_proxies = ["10.89.0.2/32"]
        ipv4_address: 10.89.0.2
    depends_on:
      - api
    mem_limit: 128m
    logging: *logging

  api:
    image: *api-image
    restart: unless-stopped
    volumes:
      - ./.config.toml:/app/.config.toml:ro
    networks:
      - whuzpay
    depends_on:
      postgres:
        condition: service_healthy
    mem_limit: 192m
    logging: *logging

  postgres:
    image: postgres:18-alpine
    restart: unless-stopped
    environment:
      # Must match db.user / db.name in .config.toml. The password comes from
      # the db_password secret (must match db.password) and is only read when
      # the pgdata volume is first initialised.
      POSTGRES_USER: postgres
      POSTGRES_DB: pg_aggregator
      POSTGRES_PASSWORD_FILE: /run/secrets/db_password
    secrets:
      - db_password
    volumes:
      - pgdata:/var/lib/postgresql
    networks:
      - whuzpay
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres -d pg_aggregator"]
      interval: 5s
      timeout: 5s
      retries: 10
    mem_limit: 384m
    logging: *logging

  # One-shot for `make migrate` / `make seed-admin` (docker compose run --rm
  # migrate ...). The "tools" profile keeps it out of `docker compose up`.
  migrate:
    image: *api-image
    profiles: ["tools"]
    entrypoint: ["bash"]
    command: ["scripts/migrate.sh"]
    volumes:
      - ./.config.toml:/app/.config.toml:ro
    networks:
      - whuzpay
    depends_on:
      postgres:
        condition: service_healthy

secrets:
  db_password:
    file: ./db_password

networks:
  whuzpay:
    ipam:
      config:
        # Outside the AWS default VPC range (172.31.0.0/16) and Docker's
        # default 172.x address pools.
        - subnet: 10.89.0.0/24

volumes:
  pgdata:
  caddy_data:
  caddy_config:
```

- [ ] **Step 2: Create `back/docker-compose.override.yaml`**

```yaml
# Local development only. Compose merges this file automatically when it
# sits next to docker-compose.yaml; it is NOT copied to the VPS, which is
# what makes the same `docker compose up -d` mean "production stack" there
# and "hot-reload dev stack" here.

x-dev: &dev
  image: whuzpay-backend:dev
  build:
    context: .
    target: dev
  # Always rebuilt from the Dockerfile's dev stage; it copies no source, so
  # this is a cache hit unless the Dockerfile changed.
  pull_policy: build
  # Run as you so files air writes under tmp/ aren't root-owned and the
  # chmod-600 .config.toml stays readable.
  user: "${HOST_UID:-1000}:${HOST_GID:-1000}"
  working_dir: /src
  environment:
    HOME: /tmp
    GOCACHE: /src/tmp/go-build
    GOMODCACHE: /src/tmp/go-mod
    # golang:alpine has no git; without this the build fails trying to
    # stamp VCS info from the mounted .git.
    GOFLAGS: -buildvcs=false
  volumes:
    - .:/src

services:
  caddy:
    # Locally the API is reached directly on localhost:8090; Caddy would try
    # (and fail) to get a real certificate for api.whuzpay.web.id.
    profiles: ["prod-only"]

  api:
    <<: *dev
    command: ["air"]
    ports:
      - "8090:8080"

  migrate:
    <<: *dev

  postgres:
    ports:
      # 5432 is used by the native PostgreSQL on this machine; localhost only.
      - "127.0.0.1:5433:5432"
```

- [ ] **Step 3: Create `back/Caddyfile`**

```
# HTTPS for the API: Caddy obtains and renews the Let's Encrypt certificate
# automatically (DNS A record api.whuzpay.web.id must point at the VPS and
# ports 80/443 must be open). It replaces any client-sent X-Forwarded-For
# with the real peer address before proxying, which the API trusts via
# [rate_limit] trusted_proxies.
api.whuzpay.web.id {
	encode zstd gzip
	reverse_proxy api:8080
}
```

- [ ] **Step 4: Rewrite `back/Makefile`**

```make
SHELL := /bin/bash

IMAGE := ghcr.io/akbarryyan/whuzpay-backend
PLATFORMS := linux/amd64,linux/arm64

.PHONY: migrate seed-admin seed release

# Every target goes through docker compose, so the same command works here
# (docker-compose.override.yaml present: dev image, source mounted) and on
# the VPS (production image only). Start the stack with `docker compose up -d`.

migrate:
	docker compose run --rm migrate

# First admin on a fresh database only (refuses once any admin exists).
seed-admin:
	docker compose run --rm migrate scripts/seed-admin.sh

# Demo data (50 fake merchants, publicly documented credentials) must never
# reach production; the override file only exists in the local checkout.
seed:
	@if [ ! -f docker-compose.override.yaml ]; then \
		echo "make seed is local-only (demo data). On the server use: make seed-admin" >&2; \
		exit 1; \
	fi
	docker compose run --rm migrate scripts/seed.sh

# Multi-arch build + push to GHCR (requires `docker login ghcr.io` and an
# arm64-capable buildx builder — see DEPLOY.md).
release:
	@if [ -z "$(VERSION)" ]; then \
		echo "usage: make release VERSION=x.y.z" >&2; \
		exit 1; \
	fi
	docker buildx build --platform $(PLATFORMS) --build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(VERSION) --push .
```

- [ ] **Step 5: Update `back/.gitignore`**

Replace:

```
# Local config (secrets). .env* kept so stale pre-TOML files never get committed.
.config.toml
```

with:

```
# Local config and DB password (secrets). .env* kept so stale pre-TOML files never get committed.
.config.toml
db_password
```

- [ ] **Step 6: Rewrite `back/.config.example.toml`** (Docker-local values; `[rate_limit]` as added in Task 1)

```toml
# Backend config. Copy to .config.toml (gitignored) and fill in values:
#   cp .config.example.toml .config.toml && chmod 600 .config.toml
# The values below fit local Docker (`docker compose up -d`). For the VPS
# see DEPLOY.md. Every key is required — there are no defaults — and unknown
# keys are rejected, so a typo fails startup instead of being ignored.
# An empty string ("") is allowed unless noted otherwise.

[app]
name         = "pg-aggregator"           # must not be empty
environment  = "development"             # development | production
port         = 8080                      # inside the container; published as localhost:8090 locally
url          = "http://localhost:8090"   # must not be empty
frontend_url = "http://localhost:3000"   # must not be empty; also the CORS origin

[db]
host     = "postgres"                    # compose service name; must not be empty
port     = 5432
user     = "postgres"                    # must match POSTGRES_USER in docker-compose.yaml
password = "postgres"                    # must equal the contents of ./db_password
name     = "pg_aggregator"               # must match POSTGRES_DB; must not be empty
sslmode  = "disable"                     # traffic stays on the compose network

[redis]  # not used yet; kept so the config fields stay unchanged
host     = "localhost"
port     = 6379
password = ""

[cashi]  # real QRIS provider, used only for environment=production payments
base_url   = "https://cashi.id"
api_key    = ""                          # x-api-key header; required when app.environment = "production"
secret_key = ""                          # webhook HMAC secret; required when app.environment = "production"

[security]
jwt_secret = "change-this-secret"        # must not be empty; must be changed in production

[rate_limit]  # per-client-IP token buckets (internal/middleware/rate_limit.go)
# CIDRs of reverse proxies whose X-Forwarded-For header names the client IP.
# Must be CIDRs ("10.89.0.2/32", not "10.89.0.2"). Empty = never trust the
# header (use the TCP peer). On the VPS: ["10.89.0.2/32"] (Caddy).
trusted_proxies = []

[rate_limit.auth]       # login & register — brute-force target
requests_per_minute = 5  # must be > 0
burst               = 5  # must be >= 1

[rate_limit.public]     # checkout, payment links, provider webhooks, merchant API key
requests_per_minute = 120
burst               = 30

[rate_limit.sensitive]  # change-password, API key create/rotate/revoke, webhook secret rotation
requests_per_minute = 10
burst               = 5
```

- [ ] **Step 7: Static verification**

Run (from `back/`):
```bash
go test ./internal/config/ -run TestLoad_ExampleFileIsValid -count=1
docker compose config --services
docker compose --profile tools config --services
docker compose -f docker-compose.yaml config --services
docker compose -f docker-compose.yaml config -q && echo "prod compose ok"
docker run --rm -v "$PWD/Caddyfile:/etc/caddy/Caddyfile:ro" caddy:2.11-alpine caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
docker run --rm -v "$PWD/Caddyfile:/etc/caddy/Caddyfile:ro" caddy:2.11-alpine caddy fmt /etc/caddy/Caddyfile | diff - Caddyfile && echo "caddyfile formatted"
make release; echo "release-no-version exit=$?"
make -n release VERSION=9.9.9
tmp="$(mktemp -d)"; cp Makefile "$tmp"/; (cd "$tmp" && make seed; echo "seed-on-server exit=$?"); rm -rf "$tmp"
git check-ignore -v db_password
```
Expected:
- example test PASS.
- Local services (override merged): `api` and `postgres` only (caddy hidden by `prod-only`, migrate by `tools`); with `--profile tools`: also `migrate`.
- Prod-only file: `api`, `caddy`, `postgres`; `prod compose ok`. (`docker compose config` does not need `.config.toml`/`db_password` to exist.)
- `caddy validate` prints `Valid configuration`; `caddyfile formatted`.
- `make release` without VERSION prints the usage line and `release-no-version exit=2`.
- `make -n release VERSION=9.9.9` prints the `docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=9.9.9 -t ghcr.io/akbarryyan/whuzpay-backend:9.9.9 --push .` command (dry run — nothing built).
- `make seed` in a dir without the override prints `make seed is local-only …` and `seed-on-server exit=2`.
- `git check-ignore` prints `.gitignore:<n>:db_password	db_password`.

- [ ] **Step 8: Report (no commit)**

Changed: `docker-compose.yaml`, `docker-compose.override.yaml`, `Caddyfile`, `Makefile`, `.gitignore`, `.config.example.toml`.

---

### Task 5: Documentation

**Files:**
- Create: `back/DEPLOY.md`
- Modify: `RUNNING.md`, `README.md` (workspace root), `back/README.md`

**Interfaces:**
- Consumes: commands, ports, file names, and values from Tasks 1–4 and the Global Constraints.
- Produces: nothing for code; Task 7/8 follow `DEPLOY.md`.

- [ ] **Step 1: Create `back/DEPLOY.md`** with exactly this content:

````markdown
# Deploy ke VPS

Backend berjalan di VPS sebagai tiga container dari `docker-compose.yaml`:
Caddy (HTTPS) → `api` → PostgreSQL. Image `api` di-build di laptop dan
diambil dari GHCR (private). Target saat ini: AWS EC2 `t3.micro` (1 GB RAM),
Ubuntu 26.04, `api.whuzpay.web.id` → `13.48.71.251`.

```
Internet ──80/443──▶ caddy (10.89.0.2, Let's Encrypt) ──▶ api:8080 ──▶ postgres:5432
                                                           (internal)    (internal)
```

## 1. Rilis image (di laptop, dari `back/`)

Sekali saja — emulasi arm64 agar buildx bisa membuat image multi-arch:

```bash
docker run --privileged --rm tonistiigi/binfmt --install arm64
docker buildx ls          # builder default harus mencantumkan linux/arm64
```

Setiap rilis (sudah `docker login ghcr.io` dengan PAT ber-scope `write:packages`):

```bash
make release VERSION=1.0.0
docker buildx imagetools inspect ghcr.io/akbarryyan/whuzpay-backend:1.0.0   # harus ada linux/amd64 dan linux/arm64
```

Satu versi = satu tag; tag lama tidak pernah ditimpa.

## 2. Siapkan VPS (sekali)

### 2.1 Security group

- `22/tcp` hanya dari My IP.
- `80/tcp` dan `443/tcp` dari mana saja.
- Tutup port lain (mis. `8090` bila sempat dibuka). **Jangan buka `5432`.**

### 2.2 Swap 2 GB

```bash
sudo fallocate -l 2G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
free -h
```

### 2.3 Docker, Compose, make, jq

Repo resmi Docker:

```bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl make jq
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
  | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

Jika `apt-get update` gagal karena repo Docker belum mendukung `resolute`
(Ubuntu 26.04), pakai paket Ubuntu:

```bash
sudo rm /etc/apt/sources.list.d/docker.list
sudo apt-get update
sudo apt-get install -y docker.io docker-compose-v2
```

Lalu (logout-login SSH agar grup berlaku):

```bash
sudo usermod -aG docker ubuntu
docker compose version
```

### 2.4 Login GHCR

Buat PAT (classic) **terpisah** dengan scope `read:packages` saja — jangan
pakai PAT push. Token dibaca tanpa tampil dan tanpa masuk history shell:

```bash
read -rs GHCR_PAT && echo "$GHCR_PAT" | docker login ghcr.io -u akbarryyan --password-stdin; unset GHCR_PAT
```

### 2.5 Salin file deploy (dari laptop, folder `back/`)

```bash
ssh ubuntu@13.48.71.251 'mkdir -p ~/whuzpay'
scp docker-compose.yaml Caddyfile Makefile ubuntu@13.48.71.251:~/whuzpay/
```

**Jangan** salin `docker-compose.override.yaml` — keberadaan file itu yang
membuat compose berjalan dalam mode dev.

### 2.6 `.config.toml` dan `db_password` (di VPS)

```bash
cd ~/whuzpay
openssl rand -hex 24 | tr -d '\n' > db_password   # password PostgreSQL baru
openssl rand -hex 32                               # salin hasilnya untuk security.jwt_secret
nano .config.toml
```

Isi `.config.toml` (semua key wajib; tanda `<…>` diganti nilai asli):

```toml
[app]
name         = "pg-aggregator"
environment  = "production"
port         = 8080
url          = "https://api.whuzpay.web.id"
frontend_url = "https://whuzpay.web.id"      # origin CORS frontend; sesuaikan saat frontend di-deploy

[db]
host     = "postgres"
port     = 5432
user     = "postgres"                         # sama dengan POSTGRES_USER di docker-compose.yaml
password = "<isi persis dari file db_password>"
name     = "pg_aggregator"                    # sama dengan POSTGRES_DB
sslmode  = "disable"                          # hanya lewat network internal compose

[redis]
host     = "localhost"
port     = 6379
password = ""

[cashi]
base_url   = "https://cashi.id"
api_key    = "<CASHI_API_KEY produksi>"
secret_key = "<CASHI_SECRET_KEY produksi>"

[security]
jwt_secret = "<hasil openssl rand -hex 32>"

[rate_limit]
trusted_proxies = ["10.89.0.2/32"]           # hanya Caddy (IP tetap di docker-compose.yaml)

[rate_limit.auth]
requests_per_minute = 5
burst               = 5

[rate_limit.public]
requests_per_minute = 120
burst               = 30

[rate_limit.sensitive]
requests_per_minute = 10
burst               = 5
```

Kepemilikan file (container `api`/`migrate` berjalan sebagai uid 10001,
`postgres` sebagai uid 70):

```bash
sudo chown 10001:10001 .config.toml && sudo chmod 600 .config.toml
sudo chown 70:70 db_password && sudo chmod 600 db_password
```

Setelah ini, edit config dengan `sudo nano .config.toml`.

## 3. Deploy pertama

Pastikan `api.whuzpay.web.id` sudah mengarah ke IP VPS (`dig +short api.whuzpay.web.id`).

```bash
cd ~/whuzpay
docker compose up -d postgres
make migrate
make seed-admin
docker compose up -d
```

**Segera ganti password admin** (`Admin123!` tercantum di README publik).
Dari laptop:

```bash
API=https://api.whuzpay.web.id/api/v1
TOKEN=$(curl -s -X POST $API/auth/admin/login -H 'Content-Type: application/json' \
  -d '{"email":"admin@pg-aggregator.local","password":"Admin123!"}' | jq -r .token)
read -rs NEW_PW
curl -s -X POST $API/auth/admin/change-password -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "$(jq -n --arg p "$NEW_PW" '{current_password:"Admin123!", new_password:$p}')"; unset NEW_PW
```

(Password baru minimal 8 karakter.)

Verifikasi:

```bash
curl -s https://api.whuzpay.web.id/api/v1/health              # {"status":"ok"}
curl -sI http://api.whuzpay.web.id/api/v1/health | head -1    # 308 Permanent Redirect ke https
nc -zv -w 5 13.48.71.251 5432                                 # harus gagal (tertutup)
nc -zv -w 5 13.48.71.251 8080                                 # harus gagal (tertutup)
docker compose ps                                             # di VPS: caddy, api, postgres "Up"
docker stats --no-stream                                      # di VPS: memori di bawah batas
```

## 4. Rilis berikutnya

1. Laptop: `make release VERSION=1.0.1`.
2. VPS: `cd ~/whuzpay && nano docker-compose.yaml` — ganti tag di baris
   `x-api-image:` menjadi `1.0.1`.
3. VPS: `docker compose up -d` (menarik tag baru dan membuat ulang `api`).
4. Jika rilis membawa migration baru: `make migrate`.
5. Opsional: `docker image prune -f`.

## 5. Operasional

```bash
docker compose logs -f api              # log aplikasi (JSON per baris)
docker compose logs -f caddy            # sertifikat / akses
docker compose restart api

# Backup database
docker compose exec -T postgres pg_dump -U postgres -d pg_aggregator | gzip > backup-$(date +%F).sql.gz
# Restore
gunzip -c backup-YYYY-MM-DD.sql.gz | docker compose exec -T postgres psql -U postgres -d pg_aggregator
```

**Ganti password PostgreSQL** (`db_password` hanya dibaca saat volume
pertama kali dibuat):

```bash
docker compose exec postgres psql -U postgres -c '\password postgres'   # prompt interaktif, tidak masuk history
# lalu samakan isi db_password dan db.password di .config.toml, kemudian:
docker compose up -d --force-recreate api
```

`make seed` (data demo) sengaja ditolak di server.

## 6. Troubleshooting

| Gejala | Penyebab & solusi |
|---|---|
| Caddy gagal mendapat sertifikat | DNS `api.whuzpay.web.id` belum ke IP VPS, atau port 80/443 tertutup di security group. Cek `docker compose logs caddy`. |
| `api` restart terus | `docker compose logs api`: config tidak valid (`invalid config …`), atau `db.password` ≠ isi `db_password`. |
| `permission denied` membaca config | Kepemilikan file — ulangi `chown` di bagian 2.6. |
| `pull access denied` | Belum `docker login ghcr.io` di VPS, atau PAT tanpa `read:packages`. |
| `seed-admin` menolak | Admin sudah ada — memang disengaja agar password tidak ter-reset. |
````

- [ ] **Step 2: Update root `README.md`**

Replace the whole section starting at `## Menjalankan Secara Lokal` up to (not including) `## Area Sensitif (hati-hati saat mengubah)` with:

````markdown
## Menjalankan Secara Lokal

Panduan lengkap (setup, kredensial seed, skenario mencoba, troubleshooting)
ada di [RUNNING.md](RUNNING.md). Ringkasnya, backend (beserta PostgreSQL)
berjalan di Docker dengan hot reload, frontend sebagai proses Node:

```bash
# Backend — http://localhost:8090
cd back && cp .config.example.toml .config.toml && chmod 600 .config.toml \
  && printf '%s' postgres > db_password && chmod 644 db_password \
  && docker compose up -d && make migrate && make seed

# Frontend — http://localhost:3000
cd front && npm install && npm run dev
```

Deploy ke VPS (HTTPS `api.whuzpay.web.id`, image dari GHCR): lihat
[back/DEPLOY.md](back/DEPLOY.md).

````

- [ ] **Step 3: Update `RUNNING.md`**

3a. Replace the `## 1. Prasyarat` table and the "Cek cepat" code block (everything between `## 1. Prasyarat` and the next `---`) with:

````markdown
## 1. Prasyarat

| Kebutuhan               | Versi    | Dipakai untuk                                                       |
| ----------------------- | -------- | ------------------------------------------------------------------- |
| Docker Engine + Compose | v2       | backend + PostgreSQL (`docker compose up -d`)                       |
| Go                      | 1.25+    | `go test` / `go vet` di host, dan flow-test membaca config backend   |
| Node.js + npm           | Node 20+ | frontend `front/`                                                   |
| `jq`, `openssl`, `curl` | —        | hanya untuk `flow-test/`                                            |

PostgreSQL, `psql`, dan `air` tidak perlu dipasang di host — semuanya ada di
dalam container.

Cek cepat:

```bash
docker compose version && go version && node -v && npm -v
jq --version && openssl version   # opsional, untuk flow-test
```

````

3b. Under `## 2. Setup Pertama Kali` → `### Backend`, replace the code block with:

```bash
cd back
cp .config.example.toml .config.toml   # nilai template sudah cocok untuk Docker lokal
chmod 600 .config.toml                 # berisi secret — hanya bisa dibaca user kamu
printf '%s' postgres > db_password     # harus sama dengan db.password di .config.toml
chmod 644 db_password                  # dibaca container PostgreSQL (uid 70); password dev lokal saja
docker compose up -d                   # build image dev (sekali), start postgres + api (hot reload)
make migrate                           # idempoten — file yang sudah applied akan di-skip
make seed                              # admin, merchant demo, 50 merchant, ~200 payment contoh
```

3c. Replace the whole `> **Catatan port.** …` blockquote (from `> **Catatan port.**` through `> Sisa dokumen ini memakai \`8090\`. Ganti kalau setup kamu beda.`) with:

```markdown
> **Catatan port.** Di dalam container API listen di `app.port = 8080`;
> `docker-compose.override.yaml` mempublikasikannya ke **`localhost:8090`**.
> Tiga tempat ini harus konsisten:
>
> | File                | Key                   | Nilai                   |
> | ------------------- | --------------------- | ----------------------- |
> | `back/.config.toml` | `app.url`             | `http://localhost:8090` |
> | `front/.env.local`  | `NEXT_PUBLIC_API_URL` | `http://localhost:8090` |
> | `flow-test/.env`    | `BASE_URL`            | `http://localhost:8090` |
>
> PostgreSQL container bisa diakses dari host di `127.0.0.1:5433` (user
> `postgres`, DB `pg_aggregator`); port 5432 dibiarkan untuk PostgreSQL native.
```

3d. In the `### Config yang wajib benar` section, replace these two lines:

```markdown
untuk aturan di bawah. Script `make migrate`/`seed`/`db-create` membaca nilai
`[db]` lewat `go run ./cmd/configenv`.
```

with:

```markdown
untuk aturan di bawah. `make migrate`/`make seed` menjalankan script di
container `migrate`, yang membaca `[db]` lewat `configenv`.
```

Then in that section's table replace the row that starts with `` | `[db]` `` with the first line below, and add the second line as a new last row:

```markdown
| `[db]`                              | `host = "postgres"` (nama service compose); `password` harus sama dengan isi `db_password` |
| `[rate_limit]`                      | batas request per IP; `trusted_proxies = []` di lokal, `["10.89.0.2/32"]` di VPS (Caddy) |
```

3e. Replace the `## 3. Menjalankan` section body (from `Dua proses, dua terminal:` through the `go install github.com/air-verse/air@latest …` code block, keeping `### Verifikasi backend hidup` and what follows) with:

````markdown
```bash
# Backend — postgres + api dengan hot reload
cd back && docker compose up -d
docker compose logs -f api      # simpan file .go → air build ulang & restart otomatis

# Frontend (terminal lain)
cd front && npm run dev
```

Berhenti: `docker compose down` (data tetap di volume `pgdata`;
`docker compose down -v` menghapusnya). Image dev otomatis di-build ulang
saat `Dockerfile` berubah.
````

3f. In `## 9. Troubleshooting`:
- Replace the `bind: address already in use` row's solution with: `port 8090 (atau 5433) di host sudah dipakai proses lain. Cek \`ss -ltnp \| grep 8090\`; ubah mapping port di \`docker-compose.override.yaml\` (+ \`app.url\`, \`NEXT_PUBLIC_API_URL\`, \`BASE_URL\` flow-test).`
- Replace `` `config file not found` dari `make migrate`/`make run` `` with `` `config file not found` dari `make migrate` / log `api` ``.
- Replace `PostgreSQL belum jalan (\`systemctl status postgresql\`) atau section \`[db]\` di \`.config.toml\` salah.` with `container postgres belum sehat (\`docker compose ps\`), atau \`db.password\` ≠ isi \`db_password\` (PostgreSQL hanya membaca \`db_password\` saat volume pertama kali dibuat — reset dengan \`docker compose down -v\`).`
- Delete the `` `air not found` `` row.
- Append a row: `| perubahan \`.go\` tidak ter-reload | cek \`docker compose logs -f api\` (error build tampil di sana); pastikan service \`api\` jalan dari \`docker-compose.override.yaml\` (\`docker compose config --services\`). |`

3g. Replace the `### Reset data` code block and the "Reset total" line with:

````markdown
```bash
cd back
make migrate && make seed      # seed idempoten (upsert), aman diulang
```

Reset total (menghapus volume database): `docker compose down -v && docker compose up -d && make migrate && make seed`.
````

- [ ] **Step 4: Update `back/README.md`**

4a. In the `## Project Structure` code block, add directly above the `├── migrations/` line:

```
├── Dockerfile            # image: build / dev (hot reload) / runtime (GHCR)
├── docker-compose.yaml   # stack produksi (caddy + api + postgres); lokal + override
├── Caddyfile             # HTTPS api.whuzpay.web.id
├── DEPLOY.md             # panduan deploy VPS
```

4b. Replace the whole `## Setup` section (from `## Setup` up to, not including, `### Config Penting (\`.config.toml\`)`) with:

````markdown
## Setup

Backend dan PostgreSQL berjalan di Docker — lokal maupun di VPS memakai
`docker compose up -d`. Di lokal, `docker-compose.override.yaml` (otomatis
dibaca Compose) memakai image dev dengan source ter-mount dan `air` untuk
hot reload, API di `http://localhost:8090`.

```bash
cp .config.example.toml .config.toml && chmod 600 .config.toml
printf '%s' postgres > db_password && chmod 644 db_password   # sama dengan db.password
docker compose up -d
make migrate
make seed            # data demo — hanya lokal
docker compose logs -f api
```

Semua key `.config.toml` wajib ada (tanpa default) dan key tak dikenal
ditolak. Path config lain bisa dipakai lewat flag `-config`.

| Perintah | Fungsi |
|---|---|
| `docker compose up -d` / `down` | start / stop stack |
| `make migrate` | jalankan migration (container `migrate`) |
| `make seed` | data demo (ditolak di server) |
| `make seed-admin` | admin pertama, hanya jika tabel `admins` kosong |
| `make release VERSION=x.y.z` | build multi-arch + push ke GHCR |

Deploy ke VPS: [DEPLOY.md](DEPLOY.md).

````

4c. In `### Config Penting (\`.config.toml\`)`, replace the `[db]` row with
`| \`[db]\` | Koneksi PostgreSQL (\`host = "postgres"\`); \`password\` sama dengan isi \`db_password\`; script migrate/seed membacanya lewat \`configenv\` |`
and append the row
`| \`[rate_limit]\` | Batas request per IP (auth/public/sensitive) dan \`trusted_proxies\` — CIDR proxy yang \`X-Forwarded-For\`-nya dipercaya (\`[]\` lokal, \`["10.89.0.2/32"]\` di VPS) |`.

4d. Directly after the line `Tiga tingkat rate limiter per-IP (\`internal/middleware/rate_limit.go\`):`, add the sentence (as its own paragraph line before the table): `Nilai default di bawah diatur di \`[rate_limit]\` pada \`.config.toml\`; di belakang Caddy, IP klien diambil dari \`X-Forwarded-For\` hanya jika koneksi datang dari \`trusted_proxies\`.`

- [ ] **Step 5: Sweep for stale instructions**

Run (from workspace root):
```bash
grep -rn --exclude-dir=.superpowers --exclude-dir=node_modules -e 'make run' -e 'make dev' -e 'db-create' -e 'air not found' -e 'go install github.com/air-verse' -e 'systemctl status postgresql' README.md RUNNING.md back/README.md back/DEPLOY.md back/Makefile
```
Expected: no output. Fix any hit.

- [ ] **Step 6: Report (no commit)**

Changed: `back/DEPLOY.md` (new), `README.md`, `RUNNING.md`, `back/README.md`.

---

### Task 6: Local migration and end-to-end verification

**Files:**
- Modify (local only, gitignored, secret): `back/.config.toml`
- Create (local only, gitignored): `back/db_password`

**Interfaces:**
- Consumes: everything from Tasks 1–5.

- [ ] **Step 1: Ask the user** before touching `back/.config.toml`: it will change `[app] port` → `8080`, `[app] url` → `"http://localhost:8090"`, `[db] host` → `"postgres"`, `[db] port` → `5432`, append the `[rate_limit]` section (template values), and create `back/db_password` from the existing `db.password`; a backup goes to `back/tmp/.config.toml.pre-docker` (gitignored). Proceed only on approval.

- [ ] **Step 2: Edit `.config.toml` without printing secrets** (from `back/`)

```bash
install -m 600 .config.toml tmp/.config.toml.pre-docker
awk '
  /^\[/ { section = $1 }
  section == "[app]" && $1 == "port" { print "port         = 8080                      # inside the container; published as localhost:8090"; next }
  section == "[app]" && $1 == "url"  { print "url          = \"http://localhost:8090\""; next }
  section == "[db]"  && $1 == "host" { print "host     = \"postgres\""; next }
  section == "[db]"  && $1 == "port" { print "port     = 5432"; next }
  { print }
' tmp/.config.toml.pre-docker > .config.toml
grep -q '^\[rate_limit\]' .config.toml || cat >> .config.toml <<'EOF'

[rate_limit]
trusted_proxies = []

[rate_limit.auth]
requests_per_minute = 5
burst               = 5

[rate_limit.public]
requests_per_minute = 120
burst               = 30

[rate_limit.sensitive]
requests_per_minute = 10
burst               = 5
EOF
chmod 600 .config.toml
go run ./cmd/configenv > /dev/null && echo "config ok"
(config_env="$(go run ./cmd/configenv)" && eval "$config_env" && printf '%s' "$DB_PASSWORD" > db_password)
chmod 644 db_password
stat -c '%a %n' .config.toml db_password
```
Expected: `config ok`; `600 .config.toml`, `644 db_password`. Nothing secret printed.

- [ ] **Step 3: Start the local stack** (port 8090 must be free: `ss -ltn | grep ':8090 '` prints nothing; if busy, stop and report)

Run: `docker compose up -d && docker compose ps`
Expected: `postgres` healthy, `api` up; `docker compose logs api` shows air building and `Server is running on port 8080`.

- [ ] **Step 4: Migrate, seed, health**

```bash
make migrate
make seed
curl -s http://localhost:8090/api/v1/health
```
Expected: `Migrations completed (applied: 15, skipped: 0)` on the fresh volume; `Seed completed`; `{"status":"ok"}`.

- [ ] **Step 5: seed-admin refuses on a non-empty `admins` table**

Run: `make seed-admin; echo "exit=$?"`
Expected: `admins table is not empty — seed-admin only runs on a fresh database`, `exit=2`.

- [ ] **Step 6: Hot reload**

```bash
printf 'package main\n' > cmd/api/zz_reload_probe.go
rm cmd/api/zz_reload_probe.go
```
Then `docker compose logs --since 1m api`.
Expected: air reports the change, rebuilds, and the server restarts (`Server is running on port 8080` appears again); `curl -s http://localhost:8090/api/v1/health` still `{"status":"ok"}`.

- [ ] **Step 7: Rate limit sees distinct clients locally**

Run: `for i in 1 2 3 4 5 6; do curl -s -o /dev/null -w '%{http_code} ' -X POST http://localhost:8090/api/v1/auth/admin/login -H 'Content-Type: application/json' -d '{"email":"x@y.z","password":"wrong-pass"}'; done; echo`
Expected: five non-`429` responses (`401` for bad credentials, or `400` if validation rejects first) then `429` on the sixth (local: no trusted proxy, keyed on the Docker bridge peer).

- [ ] **Step 8: flow-test (sandbox)**

Run (from `flow-test/`), redacting tokens/keys: `./run_all.sh 2>&1 | sed -E 's/(pk_(test|live)_)[0-9a-f]+/\1<redacted>/g; s/eyJ[A-Za-z0-9._-]+/<jwt-redacted>/g'; echo "exit=${PIPESTATUS[0]}"`
Expected: all steps pass, `exit=0`, no `warning: could not read CASHI_SECRET_KEY`.

- [ ] **Step 9: Report**

Leave the stack running (it is the user's dev environment now). Report results of Steps 2–8, and that the native PostgreSQL was not touched.

---

### Task 7: Multi-arch release to GHCR

**Files:** none.

- [ ] **Step 1: Check arm64 support** — `docker buildx ls`. If the active builder does not list `linux/arm64`, **ask the user** for permission to run `docker run --privileged --rm tonistiigi/binfmt --install arm64` (installs QEMU binfmt handlers system-wide), then re-check.

- [ ] **Step 2: Ask the user to confirm the push** of `ghcr.io/akbarryyan/whuzpay-backend:1.0.0` (private package; their existing `docker login ghcr.io`).

- [ ] **Step 3: Release** — from `back/`: `make release VERSION=1.0.0`
Expected: both platforms build (the arm64 `apk add` runs under emulation; Go compiles natively) and push succeeds.

- [ ] **Step 4: Verify**

```bash
docker buildx imagetools inspect ghcr.io/akbarryyan/whuzpay-backend:1.0.0
docker run --rm --platform linux/arm64 ghcr.io/akbarryyan/whuzpay-backend:1.0.0; echo "arm64 exit=$?"
docker run --rm --platform linux/amd64 ghcr.io/akbarryyan/whuzpay-backend:1.0.0; echo "amd64 exit=$?"
```
Expected: manifest lists `linux/amd64` and `linux/arm64`; both runs log `config file not found` and exit 1 (proves each image starts on its arch).

- [ ] **Step 5: Report** the digest and platforms.

---

### Task 8: VPS deployment and verification

**Files:** none in the repo. All VPS commands are run **by the user** following `back/DEPLOY.md` sections 2–3 (or by the controller over SSH only if the user explicitly asks and provides access, confirming each command).

- [ ] **Step 1:** User completes `DEPLOY.md` §2 (security group, swap, Docker, GHCR login, copy files, `.config.toml` + `db_password` + ownership).
- [ ] **Step 2:** User runs `DEPLOY.md` §3 first deploy and changes the admin password.
- [ ] **Step 3: External verification** (controller, from the laptop):

```bash
dig +short api.whuzpay.web.id
curl -s https://api.whuzpay.web.id/api/v1/health
curl -sI http://api.whuzpay.web.id/api/v1/health | head -1
nc -zv -w 5 13.48.71.251 5432; echo "5432 exit=$?"
nc -zv -w 5 13.48.71.251 8080; echo "8080 exit=$?"
for i in 1 2 3 4 5 6; do curl -s -o /dev/null -w '%{http_code} ' -X POST https://api.whuzpay.web.id/api/v1/auth/admin/login -H 'Content-Type: application/json' -d '{"email":"x@y.z","password":"wrong-pass"}'; done; echo
```
Expected: `13.48.71.251`; `{"status":"ok"}` over valid HTTPS; a `308 Permanent Redirect` status line; both `nc` fail (non-zero exit); five non-`429` responses then `429`.

- [ ] **Step 4: Per-client buckets behind Caddy** — while the laptop is still at 429, user runs on the VPS: `curl -s -o /dev/null -w '%{http_code}\n' -X POST https://api.whuzpay.web.id/api/v1/auth/admin/login -H 'Content-Type: application/json' -d '{"email":"x@y.z","password":"wrong-pass"}'`
Expected: not `429` (the VPS's own address has a separate bucket — proves the client IP from Caddy's `X-Forwarded-For` is used; without trusted proxies every request would share Caddy's bucket and this would be `429`).

- [ ] **Step 5:** User runs `docker compose ps` and `docker stats --no-stream` on the VPS; all three containers `Up`, memory under limits.

- [ ] **Step 6: Report** results; nothing committed.
