# Paket 2: Validasi URL (SSRF) dan Callback Asinkron — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cegah SSRF lewat URL webhook/callback merchant, dan pindahkan pengiriman callback merchant ke antrean database + worker supaya tidak lagi menahan webhook Cashi, reconcile, dan expire.

**Architecture:** Paket kecil `internal/netsafe` menyediakan validator URL (saat simpan), pemeriksa IP, dan `http.Client` yang memeriksa IP tujuan saat koneksi dibuat serta tidak mengikuti redirect. `NotifyMerchantPaymentEvent` hanya menulis baris delivery `pending`; job `deliver-merchant-callbacks` (tiap 5 detik) mengklaim baris dengan `FOR UPDATE SKIP LOCKED`, mengirimnya lewat client aman, dan menjadwalkan ulang yang gagal sebagai percobaan baru.

**Tech Stack:** Go 1.25, PostgreSQL (raw SQL, `lib/pq`), go-sqlmock, pustaka standar (`net`, `net/netip`, `net/url`, `net/http`).

**Spec:** [`back/docs/superpowers/specs/2026-11-03-callback-ssrf-async-design.md`](../specs/2026-11-03-callback-ssrf-async-design.md)

## Global Constraints

- **NO git commits, NO `git add`, NO branches** (instruksi eksplisit user). Tiap task diakhiri "report changed files".
- **Jangan pernah mencetak secret** (`back/.config.toml`, `back/db_password`, `back/.env`).
- Hanya pustaka standar; tanpa dependensi Go baru; **tanpa migration** (kolom `status` adalah `VARCHAR(50)` tanpa CHECK, lihat `migrations/008`).
- Semua perintah Go dijalankan dari `back/` (module `github.com/akbarryyan/pg-aggregator-back`). Verifikasi: `gofmt -l ./cmd ./internal`, `go vet ./...`, `go test ./... -race -count=1`.
- **Amandemen A8 (keputusan user, hasil review Task 1):** aturan IP/host (tidak boleh privat, loopback, link-local, metadata, `localhost`) berlaku di **semua** mode, termasuk sandbox, di validator **dan** di dialer. Pengecualian sandbox hanya melonggarkan **skema**: `http` boleh (`allowHTTP = true`). Alasan: `localhost` merchant tidak pernah terjangkau dari server kita, sedangkan pengecualian IP membuka SSRF.
- Production: wajib `https` dan publik. Sandbox (`allowHTTP = true`): `http` atau `https`, tetap publik. `user:password@` ditolak di semua mode.
- `merchants.webhook_url` divalidasi dengan `allowHTTP = false`. `payments.callback_url` divalidasi dengan `allowHTTP = (Environment == sandbox)` setelah `ParseEnvironment`.
- API `netsafe` setelah Task 1 (fix round 1): `ValidateCallbackURL(raw string, allowHTTP bool) error`, `NewSafeClient(timeout time.Duration) *http.Client` (penjaga IP **selalu** aktif), `IsBlockedIP(net.IP) bool`, `DescribeError(err error) string` (membuang URL dan IP internal dari error klien; wajib dipakai untuk `error_message` delivery).
- Pesan error validasi tidak memuat URL lengkap. Error sentinel: `netsafe.ErrInvalidCallbackURL` (400) dan `netsafe.ErrBlockedAddress` (diblokir saat koneksi).
- Konstanta: interval worker `5 * time.Second` (`service.CallbackDeliveryInterval`), batch 50, `staleAfter` baris `sending` = 2 menit, timeout HTTP 10 detik, batas percobaan `maxCallbackAttempts = 5` (tidak berubah), jadwal retry 5 menit (tidak berubah).
- Status delivery baru: `merchant.CallbackStatusSending = "sending"`.
- Pengiriman memakai konteks `detached(ctx)` (Paket 1, `internal/service/payment_ctx.go`) agar hasil tercatat walau konteks pemanggil dibatalkan.
- Retry manual dari admin (`RetryMerchantCallback`) tetap sinkron.
- Abaikan `back/.superpowers/`. Komentar menjelaskan *kenapa*; test table-driven; tanpa library mock selain sqlmock.

## File Structure

| File | Perubahan |
|---|---|
| `internal/netsafe/netsafe.go` (baru), `netsafe_test.go` (baru) | `ErrInvalidCallbackURL`, `ErrBlockedAddress`, `IsBlockedIP`, `ValidateCallbackURL`, `NewSafeClient` |
| `internal/domain/payment/payment.go`, `payment_test.go` | validasi `CallbackURL` di `CreatePaymentRequest.Validate` |
| `internal/service/admin_merchant_service.go`, `_test.go` | validasi `WebhookURL` di Create/Update merchant |
| `internal/handler/callback_url_errors.go` (baru), `_test.go` (baru), `admin_handler.go`, `merchant_handler.go` | pemetaan 400 |
| `internal/domain/merchant/callback.go` | `CallbackStatusSending` |
| `internal/repository/merchant_callback_repository.go`, `_test.go`, `payment_repository_pg_test.go` | `ClaimDue`, `RequeueDueFailed`, hapus `ListDueForRetry` (Task 4) |
| `internal/service/interfaces.go` | interface `merchantCallbackRepository` |
| `internal/service/merchant_callback_service.go`, `_test.go`, `payment_service.go`, `payment_fakes_test.go` | callback asinkron, client aman |
| `cmd/api/main.go`, `cmd/api/jobs.go` (baru), `cmd/api/jobs_test.go` (baru) | job baru, tunggu job saat shutdown |
| `docs/payment-state-machine.md`, `docs/openapi.yaml`, `README.md`, `DEPLOY.md` | dokumentasi |

---

### Task 1: Paket `netsafe`

> **Diamandemen saat eksekusi (A8, keputusan user):** kode di bawah adalah versi awal. Hasil review menemukan bahwa pengecualian sandbox yang melonggarkan alamat lokal membuka SSRF, sehingga yang berlaku adalah `ValidateCallbackURL(raw, allowHTTP)` (aturan IP/host selalu aktif), `NewSafeClient(timeout)` (penjaga dial selalu aktif), serta `DescribeError`. Lihat Global Constraints dan `task-1-report.md` (fix round 1).

**Files:** Create `internal/netsafe/netsafe.go`, `internal/netsafe/netsafe_test.go`

**Interfaces — Produces:**
- `var ErrInvalidCallbackURL = errors.New("invalid callback url")`
- `var ErrBlockedAddress = errors.New("destination address not allowed")`
- `func IsBlockedIP(ip net.IP) bool`
- `func ValidateCallbackURL(raw string, allowHTTP bool) error` (error membungkus `ErrInvalidCallbackURL`) — *diubah oleh A8, lihat Global Constraints*
- `func NewSafeClient(timeout time.Duration) *http.Client` — *tanpa parameter bool setelah A8*
- `func DescribeError(err error) string` — *ditambahkan oleh fix round 1*

- [ ] **Step 1: Test dulu** — buat `internal/netsafe/netsafe_test.go`:

```go
package netsafe

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{
		"0.0.0.0", "127.0.0.1", "127.8.8.8", "10.1.2.3", "172.16.0.1", "172.31.255.255",
		"192.168.0.1", "169.254.169.254", "100.64.0.1", "192.0.0.1", "192.0.2.1",
		"198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1", "255.255.255.255",
		"::", "::1", "fe80::1", "fc00::1", "fd12:3456::1", "ff02::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "64:ff9b::7f00:1", "2001:db8::1", "2002:7f00:1::1", "2001::1",
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test bug: %q does not parse", s)
		}
		if !IsBlockedIP(ip) {
			t.Errorf("IsBlockedIP(%s) = false, want true", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "172.32.0.1", "2606:4700:4700::1111"}
	for _, s := range allowed {
		if IsBlockedIP(net.ParseIP(s)) {
			t.Errorf("IsBlockedIP(%s) = true, want false", s)
		}
	}
	if !IsBlockedIP(nil) {
		t.Error("IsBlockedIP(nil) must fail closed")
	}
}

func TestValidateCallbackURL(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", 2100)
	cases := []struct {
		name          string
		url           string
		allowInsecure bool
		wantErr       bool
	}{
		{"https domain", "https://example.com/hook", false, false},
		{"https with port and query", "https://api.example.com:8443/x?y=1", false, false},
		{"https public ip", "https://8.8.8.8/h", false, false},
		{"surrounding spaces are trimmed", "  https://example.com/h  ", false, false},

		{"empty", "", false, true},
		{"no host", "https://", false, true},
		{"too long", long, false, true},
		{"malformed", "https://exa mple.com", false, true},
		{"http in production", "http://example.com/h", false, true},
		{"ftp", "ftp://example.com/h", false, true},
		{"javascript", "javascript:alert(1)", false, true},
		{"credentials", "https://user:pw@example.com/h", false, true},
		{"localhost", "https://localhost/x", false, true},
		{"subdomain of localhost", "https://app.localhost/x", false, true},
		{"loopback ip", "https://127.0.0.1/x", false, true},
		{"private 10/8", "https://10.0.0.5/x", false, true},
		{"private 192.168", "https://192.168.1.1/", false, true},
		{"private 172.16", "https://172.16.0.1/", false, true},
		{"metadata", "https://169.254.169.254/latest/meta-data", false, true},
		{"ipv6 loopback", "https://[::1]/", false, true},
		{"ipv6 link-local", "https://[fe80::1]/", false, true},
		{"ipv6 unique local", "https://[fd00::1]/", false, true},
		{"ipv4-mapped ipv6 loopback", "https://[::ffff:127.0.0.1]/", false, true},
		{"decimal ip", "https://2130706433/", false, true},
		{"hex ip", "https://0x7f.0.0.1/", false, true},
		{"octal ip", "https://017700000001/", false, true},
		{"out-of-range dotted", "https://999.1.1.1/", false, true},
		{"single label host", "https://intranet/", false, true},
		{"cgnat", "https://100.64.0.1/", false, true},
		{"unspecified", "https://0.0.0.0/", false, true},
		{"zone id", "https://[fe80::1%25eth0]/", false, true},

		{"sandbox http localhost", "http://localhost:3000/hook", true, false},
		{"sandbox http loopback", "http://127.0.0.1:8080/x", true, false},
		{"sandbox https private", "https://10.0.0.1/x", true, false},
		{"sandbox still rejects ftp", "ftp://example.com/h", true, true},
		{"sandbox still rejects credentials", "https://u:p@localhost/x", true, true},
		{"sandbox still rejects empty", "", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCallbackURL(tc.url, tc.allowInsecure)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidCallbackURL) {
					t.Fatalf("want ErrInvalidCallbackURL, got %v", err)
				}
				if tc.url != "" && strings.Contains(err.Error(), tc.url) && len(tc.url) > 12 {
					t.Errorf("error must not echo the URL: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func newCountingServer(hits *int32, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.WriteHeader(status)
	}))
}

func TestSafeClient_StrictBlocksLoopback(t *testing.T) {
	var hits int32
	srv := newCountingServer(&hits, http.StatusOK)
	defer srv.Close()

	_, err := NewSafeClient(false, 5*time.Second).Get(srv.URL)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("want ErrBlockedAddress, got %v", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("blocked request must never reach the server, hits=%d", hits)
	}
}

// A hostname that resolves to an internal address must be rejected at
// connect time — this is the DNS-rebinding case: the name looked fine when
// it was saved, but the IP it resolves to now is internal.
func TestSafeClient_StrictBlocksHostnameResolvingToLoopback(t *testing.T) {
	var hits int32
	srv := newCountingServer(&hits, http.StatusOK)
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))

	_, err := NewSafeClient(false, 5*time.Second).Get("http://localhost:" + port)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("want ErrBlockedAddress, got %v", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("blocked request must never reach the server, hits=%d", hits)
	}
}

func TestSafeClient_InsecureAllowsLoopback(t *testing.T) {
	var hits int32
	srv := newCountingServer(&hits, http.StatusOK)
	defer srv.Close()

	resp, err := NewSafeClient(true, 5*time.Second).Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("status=%d hits=%d, want 200 and 1", resp.StatusCode, hits)
	}
}

func TestSafeClient_DoesNotFollowRedirects(t *testing.T) {
	var targetHits int32
	target := newCountingServer(&targetHits, http.StatusOK)
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	resp, err := NewSafeClient(true, 5*time.Second).Get(redirector.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 302 returned as-is", resp.StatusCode)
	}
	if atomic.LoadInt32(&targetHits) != 0 {
		t.Errorf("redirect target must not be contacted, hits=%d", targetHits)
	}
}
```

- [ ] **Step 2: Jalankan** `go test ./internal/netsafe/ -count=1` → FAIL (paket belum ada). Simpan outputnya sebagai bukti RED.
- [ ] **Step 3: Implementasi** — buat `internal/netsafe/netsafe.go`:

```go
// Package netsafe keeps outbound requests to merchant-supplied URLs from
// reaching internal infrastructure (SSRF). Callback and webhook URLs are
// merchant input, but the server makes the request, from inside our network.
package netsafe

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

var (
	// ErrInvalidCallbackURL means a URL failed the storage-time rules.
	ErrInvalidCallbackURL = errors.New("invalid callback url")
	// ErrBlockedAddress means a connection was refused because the resolved
	// destination address is not public.
	ErrBlockedAddress = errors.New("destination address not allowed")
)

const maxURLLength = 2048

// blockedPrefixes are ranges netip's helper methods don't already cover:
// reserved, documentation, benchmarking, CGNAT, and IPv6 forms that embed
// an IPv4 address (NAT64, 6to4, Teredo).
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

// IsBlockedIP reports whether ip must never be contacted. It fails closed:
// a nil or malformed address counts as blocked. IPv4-mapped IPv6 addresses
// are judged by the IPv4 address they wrap.
func IsBlockedIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() || addr.IsLoopback() || addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func invalid(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidCallbackURL, reason)
}

// ValidateCallbackURL applies the storage-time rules. It does no DNS
// lookup (no I/O on the request path); the connect-time check in
// NewSafeClient is what actually stops DNS tricks. allowInsecure is true
// only for sandbox payments, so merchants can test against http and local
// addresses; credentials and non-http(s) schemes are rejected regardless.
func ValidateCallbackURL(raw string, allowInsecure bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return invalid("url is empty")
	}
	if len(raw) > maxURLLength {
		return invalid("url is too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		// url.Parse's error text embeds the URL; keep it out of ours.
		return invalid("url is malformed")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowInsecure {
			return invalid("url must use https")
		}
	default:
		return invalid("url must use https")
	}
	if u.User != nil {
		return invalid("credentials in url are not allowed")
	}
	host := u.Hostname()
	if host == "" {
		return invalid("url has no host")
	}
	if allowInsecure {
		return nil
	}

	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if strings.Contains(host, "%") {
		return invalid("host zone is not allowed")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return invalid("host is not public")
	}
	if ip := net.ParseIP(host); ip != nil {
		if IsBlockedIP(ip) {
			return invalid("host is not a public address")
		}
		return nil
	}
	if hostLooksNumeric(host) {
		// Decimal/hex/octal spellings (2130706433, 0x7f.0.0.1) that some
		// resolvers turn into an IP.
		return invalid("numeric host is not allowed")
	}
	if !strings.Contains(host, ".") {
		return invalid("host must be a fully qualified domain name")
	}
	return nil
}

func hostLooksNumeric(host string) bool {
	labels := strings.Split(host, ".")
	last := labels[len(labels)-1]
	if last == "" {
		return false
	}
	if strings.HasPrefix(last, "0x") || strings.HasPrefix(last, "0X") {
		return true
	}
	for _, r := range last {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// NewSafeClient returns the client for outbound merchant callbacks. Unless
// allowInsecure, every connection's destination IP is checked right before
// connecting (after DNS resolution), which also defeats DNS rebinding.
// Redirects are never followed (a 3xx is returned as the response, which
// callers treat as a failed delivery), and environment proxies are ignored
// so the IP check can't be bypassed through one.
func NewSafeClient(allowInsecure bool, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if !allowInsecure {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return ErrBlockedAddress
			}
			ip := net.ParseIP(host)
			if ip == nil || IsBlockedIP(ip) {
				return ErrBlockedAddress
			}
			return nil
		}
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 5 * time.Second,
			DisableKeepAlives:   true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
```

- [ ] **Step 4: Jalankan** `go test ./internal/netsafe/ -race -count=1 -v` → PASS semua. Jika sebuah kasus tabel gagal karena perilaku `url.Parse` (mis. `"https://exa mple.com"` atau zone id), sesuaikan **kasus test** hanya bila perilakunya memang aman (ditolak dengan `ErrInvalidCallbackURL` oleh cabang lain); jangan melonggarkan aturan di implementasi. Catat penyesuaian di laporan.
- [ ] **Step 5: Verifikasi** `gofmt -l ./cmd ./internal && go vet ./... && go test ./... -race -count=1`.
- [ ] **Step 6: Report** file yang berubah (tanpa commit).

---

### Task 2: Validasi URL di semua pintu masuk + pemetaan 400

**Files:** `internal/domain/payment/payment.go`, `payment_test.go`, `internal/service/admin_merchant_service.go`, `admin_merchant_service_test.go`, `internal/handler/callback_url_errors.go` (baru), `callback_url_errors_test.go` (baru), `admin_handler.go`, `merchant_handler.go`

**Interfaces — Consumes:** `netsafe.ValidateCallbackURL`, `netsafe.ErrInvalidCallbackURL` (Task 1). **Produces:** `func respondInvalidCallbackURL(w http.ResponseWriter, err error) bool` (paket `handler`).

- [ ] **Step 1: Test dulu.**
  - `internal/domain/payment/payment_test.go` — tambah:

```go
func TestCreatePaymentRequest_ValidateCallbackURL(t *testing.T) {
	base := func(env, cb string) *CreatePaymentRequest {
		r := &CreatePaymentRequest{MerchantID: uuid.New(), Amount: 10000, PaymentMethod: PaymentMethodQRIS, Description: "x", Environment: env}
		if cb != "NIL" {
			r.CallbackURL = &cb
		}
		return r
	}
	cases := []struct {
		name    string
		env     string
		cb      string
		wantErr bool
		wantNil bool
	}{
		{"production https ok", EnvironmentProduction, "https://shop.example.com/cb", false, false},
		{"production http rejected", EnvironmentProduction, "http://shop.example.com/cb", true, false},
		{"production internal rejected", EnvironmentProduction, "https://169.254.169.254/x", true, false},
		{"sandbox http public ok", EnvironmentSandbox, "http://shop.example.com/cb", false, false},
		{"sandbox http localhost rejected", EnvironmentSandbox, "http://localhost:3000/cb", true, false},
		{"sandbox internal ip rejected", EnvironmentSandbox, "http://169.254.169.254/x", true, false},
		{"empty env means sandbox, http public ok", "", "http://shop.example.com/cb", false, false},
		{"blank callback becomes nil", EnvironmentProduction, "   ", false, true},
		{"absent callback ok", EnvironmentProduction, "NIL", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base(tc.env, tc.cb)
			err := r.Validate()
			if tc.wantErr {
				if !errors.Is(err, netsafe.ErrInvalidCallbackURL) {
					t.Fatalf("want ErrInvalidCallbackURL, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil && r.CallbackURL != nil {
				t.Errorf("CallbackURL = %q, want nil", *r.CallbackURL)
			}
		})
	}
}
```
  (import `github.com/akbarryyan/pg-aggregator-back/internal/netsafe`)
  - `internal/service/admin_merchant_service_test.go` — tambah (pakai helper `newAdminMerchantServiceWithMock`; kedua validasi terjadi sebelum SQL apa pun, jadi **tidak ada** expectation):

```go
func TestAdminMerchantService_WebhookURLValidation(t *testing.T) {
	bad := "http://10.0.0.5/hook"
	t.Run("create rejects", func(t *testing.T) {
		svc, mock := newAdminMerchantServiceWithMock(t)
		_, err := svc.CreateMerchant(context.Background(), &merchant.CreateMerchantRequest{
			Name: "Acme", Email: "a@example.com", BusinessName: "Acme", WebhookURL: &bad,
		})
		if !errors.Is(err, netsafe.ErrInvalidCallbackURL) {
			t.Fatalf("want ErrInvalidCallbackURL, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unexpected SQL: %v", err)
		}
	})
	t.Run("update rejects", func(t *testing.T) {
		svc, mock := newAdminMerchantServiceWithMock(t)
		_, err := svc.UpdateMerchant(context.Background(), uuid.New(), &merchant.UpdateMerchantRequest{WebhookURL: &bad})
		if !errors.Is(err, netsafe.ErrInvalidCallbackURL) {
			t.Fatalf("want ErrInvalidCallbackURL, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unexpected SQL: %v", err)
		}
	})
}
```
  Periksa field wajib `CreateMerchantRequest` (nama field persis) sebelum menulis test; sesuaikan literal-nya, bukan aturannya.
  - `internal/handler/callback_url_errors_test.go`:

```go
package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/netsafe"
)

func TestRespondInvalidCallbackURL(t *testing.T) {
	rec := httptest.NewRecorder()
	err := fmt.Errorf("create merchant: %w", netsafe.ErrInvalidCallbackURL)
	if !respondInvalidCallbackURL(rec, err) {
		t.Fatal("expected the error to be handled")
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	if respondInvalidCallbackURL(rec, fmt.Errorf("other")) {
		t.Fatal("unrelated errors must not be handled")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("nothing should be written for unrelated errors, got %q", rec.Body.String())
	}
}
```
- [ ] **Step 2: Jalankan** `go test ./internal/domain/payment/ ./internal/service/ ./internal/handler/ -run 'CallbackURL|WebhookURLValidation' -count=1` → FAIL (RED).
- [ ] **Step 3: Implementasi.**
  - `internal/domain/payment/payment.go`, di `Validate`, **setelah** blok `r.Environment = env`:

```go
	if r.CallbackURL != nil {
		trimmed := strings.TrimSpace(*r.CallbackURL)
		if trimmed == "" {
			r.CallbackURL = nil
		} else {
			// Sandbox payments may use plain http (tunnels, test servers);
			// the IP rules still apply to them — our server can't reach a
			// merchant's localhost, and allowing it would be SSRF.
			if err := netsafe.ValidateCallbackURL(trimmed, r.Environment == EnvironmentSandbox); err != nil {
				return err
			}
			r.CallbackURL = &trimmed
		}
	}
```
  (import `github.com/akbarryyan/pg-aggregator-back/internal/netsafe`; `strings` sudah diimpor.)
  - `internal/service/admin_merchant_service.go`: pada **kedua** blok trim `WebhookURL` (di `CreateMerchant` dan `UpdateMerchant`), di cabang `else` setelah `req.WebhookURL = &trimmed`, tambahkan:

```go
			if err := netsafe.ValidateCallbackURL(trimmed, false); err != nil {
				return nil, err
			}
```
  - `internal/handler/callback_url_errors.go`:

```go
package handler

import (
	"errors"
	"net/http"

	"github.com/akbarryyan/pg-aggregator-back/internal/netsafe"
)

// respondInvalidCallbackURL writes a 400 and returns true when err is a
// callback/webhook URL validation failure. The error text names the broken
// rule but never echoes the URL.
func respondInvalidCallbackURL(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, netsafe.ErrInvalidCallbackURL) {
		return false
	}
	respondError(w, http.StatusBadRequest, err.Error())
	return true
}
```
  - Pasang di handler. Jalur-jalur ini memakai `switch err { case ... }` (perbandingan langsung) sehingga error terbungkus tidak cocok; pemanggilan helper harus **sebelum** `switch` di blok `if err != nil`:
    - `admin_handler.go`: `CreateMerchant`, `UpdateMerchant`, `CreatePayment` — tambahkan `if respondInvalidCallbackURL(w, err) { return }` di awal blok error.
    - `merchant_handler.go`: handler update merchant (yang memanggil `h.adminMerchantService.UpdateMerchant`, ~baris 422) dan `CreatePayment` — sama.
    - `payment_handler.go` (API-key create) dan jalur dashboard `CreatePayment` sudah mengembalikan 400 untuk **semua** error `Validate`; pastikan dengan membaca kodenya dan catat di laporan.
    - Telusuri semua penulis lain: `grep -rn "CreateMerchantRequest{\|UpdateMerchantRequest\|WebhookURL" internal --include=*.go | grep -v _test` — pastikan tidak ada jalur lain yang menulis `merchants.webhook_url` tanpa validasi; daftarkan hasilnya di laporan.
- [ ] **Step 4: Jalankan** test Step 2 → PASS, lalu `gofmt -l ./cmd ./internal && go vet ./... && go test ./... -race -count=1`. Jika test lama gagal karena memakai `CallbackURL` http di request production, perbaiki test itu (set `Environment: payment.EnvironmentSandbox` atau URL https) dan daftarkan.
- [ ] **Step 5: Report** (tanpa commit).

---

### Task 3: Klaim antrean di repository (`ClaimDue`, `RequeueDueFailed`)

**Files:** `internal/domain/merchant/callback.go`, `internal/repository/merchant_callback_repository.go`, `internal/repository/merchant_callback_repository_test.go` (buat bila belum ada; ikuti pola file repository test lain), `internal/repository/payment_repository_pg_test.go`

**Catatan:** `ListDueForRetry` **dibiarkan** di task ini (masih dipakai service sampai Task 4).

**Interfaces — Produces:**
- `merchant.CallbackStatusSending = "sending"`
- `func (r *MerchantCallbackRepository) ClaimDue(ctx context.Context, now time.Time, limit int, staleAfter time.Duration) ([]merchant.CallbackDelivery, error)` — mengklaim baris `pending` dan baris `sending` yang `updated_at <= now - staleAfter`, menandainya `sending`, dan mengembalikannya (status di hasil = `sending`).
- `func (r *MerchantCallbackRepository) RequeueDueFailed(ctx context.Context, now time.Time, limit int) (int64, error)` — untuk baris `failed` dengan `next_retry_at <= now`: hapus `next_retry_at` dan buat satu baris `pending` baru (`attempt_number + 1`). Mengembalikan jumlah baris baru.

- [ ] **Step 1: Test dulu (sqlmock).** Gunakan `newMockDB` (`internal/repository/testdb_test.go`), `sqlmock.AnyArg()` untuk waktu:

```go
func TestMerchantCallbackRepository_ClaimDue(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantCallbackRepository(db)
	now := time.Now().UTC()
	id := uuid.New()

	cols := []string{"id", "payment_id", "merchant_id", "event_type", "target_url", "request_payload",
		"attempt_number", "status", "http_status", "response_body", "error_message",
		"delivered_at", "next_retry_at", "created_at", "updated_at"}
	mock.ExpectQuery(`(?s)UPDATE merchant_callback_deliveries.*SET status = \$1, updated_at = \$2.*WHERE id IN \(.*status = \$3 OR \(status = \$1 AND updated_at <= \$4\).*ORDER BY created_at.*LIMIT \$5.*FOR UPDATE SKIP LOCKED.*\).*RETURNING`).
		WithArgs(merchant.CallbackStatusSending, sqlmock.AnyArg(), merchant.CallbackStatusPending, sqlmock.AnyArg(), 50).
		WillReturnRows(sqlmock.NewRows(cols).AddRow(
			id, uuid.New(), uuid.New(), "payment.paid", "https://x.example/h", []byte(`{"a":1}`),
			1, merchant.CallbackStatusSending, nil, nil, nil, nil, nil, now, now))

	got, err := repo.ClaimDue(context.Background(), now, 50, 2*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != id || got[0].Status != merchant.CallbackStatusSending {
		t.Fatalf("unexpected claim result: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestMerchantCallbackRepository_RequeueDueFailed(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantCallbackRepository(db)

	mock.ExpectExec(`(?s)WITH due AS \(.*status = \$1 AND next_retry_at IS NOT NULL AND next_retry_at <= \$2.*LIMIT \$3.*FOR UPDATE SKIP LOCKED.*\), cleared AS \(.*UPDATE merchant_callback_deliveries.*SET next_retry_at = NULL.*\)\s*INSERT INTO merchant_callback_deliveries.*attempt_number \+ 1.*`).
		WithArgs(merchant.CallbackStatusFailed, sqlmock.AnyArg(), 50, merchant.CallbackStatusPending).
		WillReturnResult(sqlmock.NewResult(0, 2))

	n, err := repo.RequeueDueFailed(context.Background(), time.Now().UTC(), 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 2 {
		t.Errorf("requeued = %d, want 2", n)
	}
}
```
  Tambahan sqlmock: `limit <= 0` → 50 untuk keduanya (satu test kecil).
- [ ] **Step 2: Jalankan** `go test ./internal/repository/ -run 'ClaimDue|RequeueDueFailed' -count=1` → FAIL.
- [ ] **Step 3: Implementasi.**
  - `internal/domain/merchant/callback.go`: tambah `CallbackStatusSending = "sending"` di blok const.
  - `merchant_callback_repository.go` — tambah (pakai `scanCallbackDelivery` yang sudah ada; urutan kolom `RETURNING` **harus** sama dengan `ListDueForRetry`):

```go
// ClaimDue atomically takes up to limit deliveries to send: pending ones,
// plus 'sending' ones whose worker evidently died (untouched for staleAfter).
// FOR UPDATE SKIP LOCKED means two workers never claim the same row.
func (r *MerchantCallbackRepository) ClaimDue(ctx context.Context, now time.Time, limit int, staleAfter time.Duration) ([]merchant.CallbackDelivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
		UPDATE merchant_callback_deliveries
		SET status = $1, updated_at = $2
		WHERE id IN (
			SELECT id FROM merchant_callback_deliveries
			WHERE status = $3 OR (status = $1 AND updated_at <= $4)
			ORDER BY created_at
			LIMIT $5
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, payment_id, merchant_id, event_type, target_url, request_payload,
		          attempt_number, status, http_status, response_body, error_message,
		          delivered_at, next_retry_at, created_at, updated_at
	`, merchant.CallbackStatusSending, now, merchant.CallbackStatusPending, now.Add(-staleAfter), limit)
	if err != nil {
		return nil, fmt.Errorf("failed to claim callback deliveries: %w", err)
	}
	defer rows.Close()

	items := make([]merchant.CallbackDelivery, 0)
	for rows.Next() {
		d, err := scanCallbackDelivery(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *d)
	}
	return items, rows.Err()
}

// RequeueDueFailed turns each failed delivery whose retry time has come into
// a fresh pending attempt (attempt_number + 1, history kept) and clears the
// old row's schedule so it is never picked up twice — one statement, so
// concurrent workers cannot both requeue the same failure.
func (r *MerchantCallbackRepository) RequeueDueFailed(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 50
	}
	res, err := r.db.ExecContext(ctx, `
		WITH due AS (
			SELECT id FROM merchant_callback_deliveries
			WHERE status = $1 AND next_retry_at IS NOT NULL AND next_retry_at <= $2
			ORDER BY next_retry_at
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		), cleared AS (
			UPDATE merchant_callback_deliveries d
			SET next_retry_at = NULL, updated_at = $2
			FROM due
			WHERE d.id = due.id
			RETURNING d.payment_id, d.merchant_id, d.event_type, d.target_url, d.request_payload, d.attempt_number
		)
		INSERT INTO merchant_callback_deliveries (
			id, payment_id, merchant_id, event_type, target_url, request_payload,
			attempt_number, status, created_at, updated_at
		)
		SELECT gen_random_uuid(), payment_id, merchant_id, event_type, target_url, request_payload,
		       attempt_number + 1, $4, $2, $2
		FROM cleared
	`, merchant.CallbackStatusFailed, now, limit, merchant.CallbackStatusPending)
	if err != nil {
		return 0, fmt.Errorf("failed to requeue failed callbacks: %w", err)
	}
	return res.RowsAffected()
}
```
  Periksa tipe kolom waktu di `migrations/012` (tabel ini ada di sana → `TIMESTAMPTZ`); bind `time.Time` UTC seperti kode lain.
- [ ] **Step 4: Test PostgreSQL asli (opsional via `TEST_DATABASE_URL`)** — tambah ke `payment_repository_pg_test.go`, memakai helper schema/migrasi/insert yang sudah ada di file itu (baca bagian atas file; tambahkan helper `insertPGCallback` bila perlu, SQL biasa, patuhi kolom NOT NULL: butuh baris `merchants` dan `payments` induk, gunakan helper insert payment yang ada):
  - `TestPG_ClaimDue_ClaimsPendingAndStaleSending`: seed `pending` (diklaim), `sending` dengan `updated_at` 3 menit lalu (diklaim ulang), `sending` dengan `updated_at` sekarang (tidak), `success` (tidak), `failed` (tidak). Panggil `ClaimDue(now, 50, 2*time.Minute)`; ekspektasi tepat dua ID; semua berstatus `sending` di DB sesudahnya.
  - `TestPG_ClaimDue_ConcurrentClaimsNeverOverlap`: seed 20 `pending`; dua goroutine memanggil `ClaimDue(…, 20, …)` bersamaan (`sync.WaitGroup`); gabungan ID tidak ada yang kembar dan total 20.
  - `TestPG_RequeueDueFailed`: seed `failed` jatuh tempo (attempt 2), `failed` belum jatuh tempo, `failed` dengan `next_retry_at` NULL; panggil `RequeueDueFailed`; ekspektasi n=1, baris lama `next_retry_at IS NULL`, ada satu baris baru `pending` dengan `attempt_number = 3` dan payload/URL sama; panggilan kedua mengembalikan 0.
- [ ] **Step 5: Jalankan** PG test pada server sementara yang kamu mulai sendiri (`/usr/lib/postgresql/18/bin`: `initdb`, `pg_ctl`; data dir di `/tmp/claude-1000/-home-akbar-Kerjaan-personal-whuzpay-pg/7c52190e-8489-4b73-82d0-fbd45268a89f/scratchpad/pgtest/`; socket dir `/tmp/claude-1000/pgsock`; port 54999; `listen_addresses=''`). `go test ./internal/repository/ -run PG -race -count=1 -v` dengan `TEST_DATABASE_URL` mengarah ke socket itu — **semua** PG test (termasuk milik Paket 1) harus lulus. Setelah selesai `pg_ctl stop`, `rm -rf` kedua direktori, dan verifikasi dengan `ls`. **Jangan sentuh** PostgreSQL sistem di port 5432, container Docker apa pun, atau database yang sudah ada (milik proyek lain user).
- [ ] **Step 6: Verifikasi** `gofmt -l ./cmd ./internal && go vet ./... && go test ./... -race -count=1` (tanpa env var; PG test ter-skip).
- [ ] **Step 7: Report** termasuk bukti pembersihan server sementara (tanpa commit).

---

### Task 4: Callback asinkron di service

**Files:** `internal/service/interfaces.go`, `internal/service/payment_service.go`, `internal/service/merchant_callback_service.go`, `internal/service/payment_fakes_test.go`, `internal/service/merchant_callback_service_test.go`, `internal/repository/merchant_callback_repository.go` + `_test.go` (hapus `ListDueForRetry`), test lain yang terdampak

**Interfaces — Consumes:** Task 1 (`netsafe.*`), Task 3 (`ClaimDue`, `RequeueDueFailed`, `CallbackStatusSending`), Paket 1 (`detached`, `payment.EnvironmentSandbox`). **Produces:**
- `const CallbackDeliveryInterval = 5 * time.Second` (exported, paket `service`)
- `func (s *PaymentService) DeliverMerchantCallbacks(ctx context.Context) error`
- field `PaymentService`: `callbackValidate func(raw string, allowHTTP bool) error`, `callbackClient func() *http.Client` (default-nya `netsafe`; test boleh menggantinya)
- interface `merchantCallbackRepository`: **hapus** `ListDueForRetry`, **tambah** `ClaimDue(ctx, now time.Time, limit int, staleAfter time.Duration) ([]merchant.CallbackDelivery, error)` dan `RequeueDueFailed(ctx, now time.Time, limit int) (int64, error)`
- **hapus** `RetryDueMerchantCallbacks`

- [ ] **Step 1: Fakes & helper test.** Di `payment_fakes_test.go`, ganti `ListDueForRetry` pada `fakeMerchantCallbackRepo` dengan:

```go
func (f *fakeMerchantCallbackRepo) ClaimDue(ctx context.Context, now time.Time, limit int, staleAfter time.Duration) ([]merchant.CallbackDelivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]merchant.CallbackDelivery, 0)
	for _, d := range f.byID {
		stale := d.Status == merchant.CallbackStatusSending && !d.UpdatedAt.After(now.Add(-staleAfter))
		if d.Status != merchant.CallbackStatusPending && !stale {
			continue
		}
		d.Status = merchant.CallbackStatusSending
		d.UpdatedAt = now
		out = append(out, *d)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeMerchantCallbackRepo) RequeueDueFailed(ctx context.Context, now time.Time, limit int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, d := range f.byID {
		if d.Status != merchant.CallbackStatusFailed || d.NextRetryAt == nil || d.NextRetryAt.After(now) {
			continue
		}
		retry := &merchant.CallbackDelivery{
			ID: uuid.New(), PaymentID: d.PaymentID, MerchantID: d.MerchantID, EventType: d.EventType,
			TargetURL: d.TargetURL, RequestPayload: d.RequestPayload, AttemptNumber: d.AttemptNumber + 1,
			Status: merchant.CallbackStatusPending, CreatedAt: now, UpdatedAt: now,
		}
		d.NextRetryAt = nil
		f.byID[retry.ID] = retry
		f.created = append(f.created, retry)
		n++
		if limit > 0 && n >= int64(limit) {
			break
		}
	}
	return n, nil
}
```
  (Memodifikasi `f.byID` saat iterasi map di Go diperbolehkan tetapi baris baru mungkin ikut terlihat; kumpulkan dulu ke slice lalu tambahkan setelah loop bila test menunjukkan duplikasi.) Di `merchant_callback_service_test.go` tambah helper:

```go
// useLoopbackCallbacks lets a test deliver to an httptest server on
// 127.0.0.1: the production validator and dialer (correctly) refuse
// loopback, so tests that exercise delivery mechanics swap them out.
// Tests of the SSRF protection itself must NOT call this.
func (s *PaymentService) useLoopbackCallbacks() *PaymentService {
	s.callbackValidate = func(string, bool) error { return nil }
	s.callbackClient = func() *http.Client { return &http.Client{Timeout: 10 * time.Second} }
	return s
}
```
- [ ] **Step 2: Test dulu** — di `merchant_callback_service_test.go`: hapus `TestRetryDueMerchantCallbacks_OnlyRetriesDueFailedDeliveries`, `_ClearsSourceRetrySchedule`, `_NoopWithoutCallbackDeps`, `_ReusesRowsAndCachesWebhookSecret` (baca dulu apa yang diverifikasi masing-masing; semua perilakunya diganti oleh test di bawah, **termasuk** pembuktian cache secret satu kali per merchant) dan tambah:

```go
func counting(hits *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
}

func TestNotifyMerchantPaymentEvent_OnlyQueuesPendingDelivery(t *testing.T) {
	var hits int32
	server := counting(&hits)
	defer server.Close()
	svc, merchantRepo, callbackRepo := newCallbackTestService(t)
	svc.useLoopbackCallbacks()

	p := &payment.Payment{ID: uuid.New(), Reference: "PAY-Q1", MerchantID: uuid.New(), Amount: 1000,
		Currency: payment.CurrencyIDR, Status: payment.StatusPaid, ProviderName: "cashi"}
	url := server.URL
	merchantRepo.byID[p.MerchantID] = &merchant.Merchant{ID: p.MerchantID, WebhookURL: &url}

	svc.NotifyMerchantPaymentEvent(context.Background(), p, "payment.paid", false)

	if len(callbackRepo.created) != 1 || callbackRepo.created[0].Status != merchant.CallbackStatusPending {
		t.Fatalf("expected exactly one pending delivery, got %+v", callbackRepo.created)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("Notify must not perform HTTP, hits=%d", hits)
	}
}

func TestDeliverMerchantCallbacks_SendsPendingAndMarksSuccess(t *testing.T) {
	var hits int32
	server := counting(&hits)
	defer server.Close()
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.useLoopbackCallbacks()
	d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, server.URL, 1, nil)

	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := callbackRepo.byID[d.ID]
	if got.Status != merchant.CallbackStatusSuccess || got.HTTPStatus == nil || *got.HTTPStatus != 200 {
		t.Fatalf("unexpected delivery state: %+v", got)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
}

func TestDeliverMerchantCallbacks_RequeuesDueFailedAsNewAttempt(t *testing.T) {
	var hits int32
	server := counting(&hits)
	defer server.Close()
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.useLoopbackCallbacks()
	past := time.Now().UTC().Add(-time.Minute)
	source := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusFailed, server.URL, 1, &past)

	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callbackRepo.byID[source.ID].NextRetryAt != nil {
		t.Error("source retry schedule must be cleared")
	}
	if len(callbackRepo.created) != 1 || callbackRepo.created[0].AttemptNumber != 2 {
		t.Fatalf("expected one new attempt #2, got %+v", callbackRepo.created)
	}
	// The worker sends a copy and UpdateResult replaces the stored row, so
	// read the outcome back by ID.
	if got := callbackRepo.byID[callbackRepo.created[0].ID]; got.Status != merchant.CallbackStatusSuccess || atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("new attempt should have been delivered once: status=%s hits=%d", got.Status, hits)
	}
}

func TestDeliverMerchantCallbacks_IgnoresNotDueOrFinal(t *testing.T) {
	var hits int32
	server := counting(&hits)
	defer server.Close()
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.useLoopbackCallbacks()
	future := time.Now().UTC().Add(10 * time.Minute)
	seedCallbackDelivery(callbackRepo, merchant.CallbackStatusFailed, server.URL, 1, &future) // not due
	seedCallbackDelivery(callbackRepo, merchant.CallbackStatusFailed, server.URL, 5, nil)     // capped
	seedCallbackDelivery(callbackRepo, merchant.CallbackStatusSuccess, server.URL, 1, nil)    // done

	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt32(&hits) != 0 || len(callbackRepo.created) != 0 {
		t.Fatalf("nothing should be sent or requeued: hits=%d created=%d", hits, len(callbackRepo.created))
	}
}

func TestDeliverMerchantCallbacks_ReclaimsOnlyStaleSending(t *testing.T) {
	var hits int32
	server := counting(&hits)
	defer server.Close()
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.useLoopbackCallbacks()
	stale := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusSending, server.URL, 1, nil)
	stale.UpdatedAt = time.Now().UTC().Add(-3 * time.Minute)
	fresh := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusSending, server.URL, 1, nil)

	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Read back from the repo: the worker works on copies and UpdateResult
	// replaces the stored row, so the seeded pointers can be stale.
	if got := callbackRepo.byID[stale.ID]; got.Status != merchant.CallbackStatusSuccess {
		t.Errorf("stale 'sending' row should have been re-sent, status=%s", got.Status)
	}
	if got := callbackRepo.byID[fresh.ID]; got.Status != merchant.CallbackStatusSending || atomic.LoadInt32(&hits) != 1 {
		t.Errorf("fresh 'sending' row must be left alone: status=%s hits=%d", got.Status, hits)
	}
}

func TestDeliverMerchantCallbacks_CachesWebhookSecretPerMerchant(t *testing.T) {
	var hits int32
	server := counting(&hits)
	defer server.Close()
	svc, merchantRepo, callbackRepo := newCallbackTestService(t)
	svc.useLoopbackCallbacks()
	merchantID := uuid.New()
	secret := "s3cret"
	merchantRepo.byID[merchantID] = &merchant.Merchant{ID: merchantID, WebhookSecret: &secret}
	for i := 0; i < 3; i++ {
		d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, server.URL, 1, nil)
		d.MerchantID = merchantID
	}
	merchantRepo.getByIDCalls = 0

	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if merchantRepo.getByIDCalls != 1 {
		t.Errorf("merchant lookups = %d, want 1 (secret cached per batch)", merchantRepo.getByIDCalls)
	}
	if atomic.LoadInt32(&hits) != 3 {
		t.Errorf("hits = %d, want 3", hits)
	}
}

func TestDeliverMerchantCallbacks_NoopWithoutCallbackDeps(t *testing.T) {
	svc := NewPaymentService(newFakePaymentRepo(), &fakeMerchantProviderConfigRepo{}, newFakeWebhookEventRepo(), providerPkg.NewProviderRouter(), "http://localhost:8080")
	if err := svc.DeliverMerchantCallbacks(context.Background()); err != nil {
		t.Fatalf("expected nil without callback deps, got %v", err)
	}
}

// --- SSRF protection: these use the REAL validator and dialer. ---

func seedPaymentFor(t *testing.T, svc *PaymentService, env string) *payment.Payment {
	t.Helper()
	p := &payment.Payment{ID: uuid.New(), Reference: "PAY-" + uuid.NewString()[:8], MerchantID: uuid.New(),
		Amount: 1000, Currency: payment.CurrencyIDR, Status: payment.StatusPaid, ProviderName: "cashi", Environment: env}
	if err := svc.paymentRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("seed payment: %v", err)
	}
	return p
}

func TestExecuteCallbackDelivery_ProductionRejectsInternalHTTPTarget(t *testing.T) {
	var hits int32
	server := counting(&hits) // http://127.0.0.1:port
	defer server.Close()
	svc, _, callbackRepo := newCallbackTestService(t)
	p := seedPaymentFor(t, svc, payment.EnvironmentProduction)
	d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, server.URL, 1, nil)
	d.PaymentID = p.ID

	svc.executeCallbackDelivery(context.Background(), d, nil)

	if d.Status != merchant.CallbackStatusFailed || d.NextRetryAt != nil || d.ErrorMessage == nil {
		t.Fatalf("expected permanent failure with message, got %+v", d)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("no request may be made, hits=%d", hits)
	}
}

// Sandbox relaxes only the scheme: it still may not target internal
// addresses (a merchant's localhost is not reachable from our server anyway).
func TestExecuteCallbackDelivery_SandboxPaymentStillRejectsLocalTarget(t *testing.T) {
	var hits int32
	server := counting(&hits) // http://127.0.0.1:port
	defer server.Close()
	svc, _, callbackRepo := newCallbackTestService(t)
	p := seedPaymentFor(t, svc, payment.EnvironmentSandbox)
	d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, server.URL, 1, nil)
	d.PaymentID = p.ID

	svc.executeCallbackDelivery(context.Background(), d, nil)

	if d.Status != merchant.CallbackStatusFailed || d.NextRetryAt != nil || atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("sandbox must not reach loopback: %+v hits=%d", d, hits)
	}
}

// The environment only decides whether plain http is allowed.
func TestExecuteCallbackDelivery_AllowHTTPFollowsPaymentEnvironment(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want bool
	}{{payment.EnvironmentSandbox, true}, {payment.EnvironmentProduction, false}} {
		t.Run(tc.env, func(t *testing.T) {
			svc, _, callbackRepo := newCallbackTestService(t)
			var got bool
			svc.callbackValidate = func(_ string, allowHTTP bool) error { got = allowHTTP; return nil }
			svc.callbackClient = func() *http.Client {
				return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
				})}
			}
			p := seedPaymentFor(t, svc, tc.env)
			d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, "https://example.com/h", 1, nil)
			d.PaymentID = p.ID

			svc.executeCallbackDelivery(context.Background(), d, nil)

			if got != tc.want {
				t.Errorf("allowHTTP = %v, want %v", got, tc.want)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Defense in depth: even if a URL got past the storage-time check, the
// dialer refuses internal destinations, and that failure is not retried.
func TestExecuteCallbackDelivery_DialerBlocksInternalDestinationWithoutRetry(t *testing.T) {
	var hits int32
	server := counting(&hits)
	defer server.Close()
	svc, _, callbackRepo := newCallbackTestService(t)
	svc.callbackValidate = func(string, bool) error { return nil } // validator bypassed; real strict client kept
	p := seedPaymentFor(t, svc, payment.EnvironmentProduction)
	d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, server.URL, 1, nil)
	d.PaymentID = p.ID

	svc.executeCallbackDelivery(context.Background(), d, nil)

	if d.Status != merchant.CallbackStatusFailed || d.NextRetryAt != nil {
		t.Fatalf("blocked destination must fail without a retry schedule: %+v", d)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("no request may reach the server, hits=%d", hits)
	}
}

func TestExecuteCallbackDelivery_UnknownPaymentIsTreatedAsProduction(t *testing.T) {
	var hits int32
	server := counting(&hits)
	defer server.Close()
	svc, _, callbackRepo := newCallbackTestService(t)
	d := seedCallbackDelivery(callbackRepo, merchant.CallbackStatusPending, server.URL, 1, nil) // random PaymentID

	svc.executeCallbackDelivery(context.Background(), d, nil)

	if d.Status != merchant.CallbackStatusFailed || atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("unknown payment must get the strict rules: status=%s hits=%d", d.Status, hits)
	}
}
```
  Di file yang sama, di test lama yang masih ada dan memakai `httptest` (`TestExecuteCallbackDelivery_SignsPayloadWithHMAC`, `TestRetryMerchantCallback_StopsSchedulingAfterMaxAttempts`, dan lainnya bila ada) tambahkan `svc.useLoopbackCallbacks()` tepat setelah service dibuat. Tambah import `sync/atomic` bila perlu.
- [ ] **Step 3: Jalankan** `go test ./internal/service/ -run 'DeliverMerchantCallbacks|NotifyMerchantPaymentEvent_OnlyQueues|ExecuteCallbackDelivery' -count=1` → FAIL (RED; kompilasi gagal).
- [ ] **Step 4: Implementasi.**
  - `internal/service/interfaces.go`, `merchantCallbackRepository`: hapus `ListDueForRetry`, tambah `ClaimDue` dan `RequeueDueFailed` (signature di atas).
  - `internal/service/payment_service.go`: tambah field dan default di `NewPaymentService`:

```go
	callbackValidate func(raw string, allowHTTP bool) error
	callbackClient   func() *http.Client
```
```go
	callbackHTTP := netsafe.NewSafeClient(10 * time.Second) // one client; the dial-time IP guard is always on
	// ... di literal PaymentService:
		callbackValidate: netsafe.ValidateCallbackURL,
		callbackClient:   func() *http.Client { return callbackHTTP },
```
  (import `net/http`, `time` bila belum, dan `internal/netsafe`.)
  - `merchant_callback_service.go`:
    - `NotifyMerchantPaymentEvent`: hapus baris terakhir `s.executeCallbackDelivery(ctx, delivery, nil)`; ubah doc comment: hanya mengantre baris `pending`; pengiriman oleh `DeliverMerchantCallbacks`.
    - Hapus `RetryDueMerchantCallbacks`. Tambah:

```go
const (
	// CallbackDeliveryInterval is how often the worker polls for deliveries
	// to send; callbacks reach merchants within about this long of a change.
	CallbackDeliveryInterval = 5 * time.Second

	callbackBatchSize         = 50
	callbackSendingStaleAfter = 2 * time.Minute
)

// DeliverMerchantCallbacks is the scheduler job that sends queued merchant
// callbacks. It first turns failed deliveries whose retry time has come into
// fresh pending attempts, then claims and sends pending ones (plus 'sending'
// rows abandoned by a dead worker). Sending is sequential and a single
// delivery failing never stops the batch.
func (s *PaymentService) DeliverMerchantCallbacks(ctx context.Context) error {
	if s.callbackRepo == nil {
		return nil
	}
	now := time.Now().UTC()
	if _, err := s.callbackRepo.RequeueDueFailed(ctx, now, callbackBatchSize); err != nil {
		return fmt.Errorf("failed to requeue failed callbacks: %w", err)
	}
	due, err := s.callbackRepo.ClaimDue(ctx, now, callbackBatchSize, callbackSendingStaleAfter)
	if err != nil {
		return fmt.Errorf("failed to claim callbacks: %w", err)
	}

	secretCache := make(map[uuid.UUID]string)
	for i := range due {
		if ctx.Err() != nil {
			// Shutting down: unsent rows stay 'sending' and are re-claimed
			// after callbackSendingStaleAfter.
			break
		}
		s.executeCallbackDelivery(ctx, &due[i], secretCache)
	}
	return nil
}
```
    - Ganti `executeCallbackDelivery` seluruhnya dengan (secret/HMAC/header tetap persis seperti sekarang; hanya bagian awal, client, dan penanganan error dial yang berubah):

```go
func (s *PaymentService) executeCallbackDelivery(ctx context.Context, d *merchant.CallbackDelivery, secretCache map[uuid.UUID]string) {
	// Detached: once a delivery is claimed (or a manual retry started) its
	// outcome must be recorded even if the caller's context is cancelled
	// (shutdown, a disconnected admin client).
	ctx, cancel := detached(ctx)
	defer cancel()

	allowHTTP := s.callbackAllowsHTTP(ctx, d)
	if err := s.callbackValidate(d.TargetURL, allowHTTP); err != nil {
		// Typically a URL stored before the SSRF rules existed. Retrying
		// can't fix it, so fail permanently with the reason.
		s.failCallbackPermanently(ctx, d, err)
		return
	}

	body, err := json.Marshal(d.RequestPayload)
	if err != nil {
		s.failCallbackPermanently(ctx, d, err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TargetURL, bytes.NewReader(body))
	if err != nil {
		s.failCallbackPermanently(ctx, d, err)
		return
	}
	// ... (header Content-Type / User-Agent / X-PG-Event dan blok tanda tangan
	// HMAC: SALIN tanpa perubahan dari versi sekarang)

	resp, err := s.callbackClient().Do(req)
	if err != nil {
		// DescribeError drops the request URL (may carry tokens) and any
		// resolved internal IP before the text is stored.
		msg := netsafe.DescribeError(err)
		d.Status = merchant.CallbackStatusFailed
		d.ErrorMessage = &msg
		if errors.Is(err, netsafe.ErrBlockedAddress) {
			d.NextRetryAt = nil // retrying an internal destination can't succeed
		} else {
			d.NextRetryAt = nextRetryAt(d.AttemptNumber)
		}
		_ = s.callbackRepo.UpdateResult(ctx, d)
		logger.WarnfCtx(ctx, "Merchant callback failed for %s: %s", d.PaymentID, msg)
		return
	}
	// ... (sisanya — pembacaan body, penentuan success/failed, UpdateResult —
	// SALIN tanpa perubahan)
}

// callbackAllowsHTTP reports whether this delivery may use plain http: only
// for sandbox payments (IP rules apply regardless). A payment that can't be
// loaded gets the strict rules.
func (s *PaymentService) callbackAllowsHTTP(ctx context.Context, d *merchant.CallbackDelivery) bool {
	p, err := s.paymentRepo.GetByID(ctx, d.PaymentID)
	return err == nil && p.Environment == payment.EnvironmentSandbox
}

func (s *PaymentService) failCallbackPermanently(ctx context.Context, d *merchant.CallbackDelivery, cause error) {
	msg := cause.Error()
	d.Status = merchant.CallbackStatusFailed
	d.ErrorMessage = &msg
	d.NextRetryAt = nil
	_ = s.callbackRepo.UpdateResult(ctx, d)
	logger.WarnfCtx(ctx, "Merchant callback for %s not sent: %v", d.PaymentID, cause)
}
```
    Tambah import `errors`, `internal/netsafe`. Pada log, jangan mencetak isi URL bila error validasi (pesan `netsafe` sudah aman).
  - Repository: hapus `ListDueForRetry` dan test-nya (`merchant_callback_repository.go`, `_test.go`); `grep -rn ListDueForRetry internal cmd` harus kosong.
  - `RetryMerchantCallback` (retry manual admin) **tidak diubah**: tetap sinkron lewat `executeCallbackDelivery`.
- [ ] **Step 5: Jalankan** test Step 3 → PASS; lalu `go test ./internal/service/ -race -count=1`. Test lain di paket `service`/`handler`/`cmd/api` yang mengandalkan callback terkirim langsung dari `NotifyMerchantPaymentEvent`/`ProcessWebhook`/`ExpirePayments`/reconcile: periksa apa yang mereka assert. Yang hanya memeriksa **baris delivery dibuat** (`callbackRepo.created`, `EventType`, `RequestPayload["late_payment"]`) tetap valid. Yang memeriksa **status delivery `success`** atau jumlah hit server harus diubah: panggil `svc.DeliverMerchantCallbacks(ctx)` (dengan `useLoopbackCallbacks()` bila targetnya httptest) sebelum assert. Jangan melemahkan assertion; daftarkan tiap test yang diubah di laporan.
- [ ] **Step 6: Verifikasi** `gofmt -l ./cmd ./internal && go vet ./... && go test ./... -race -count=1`; jalankan juga `go test ./internal/service/ -race -count=10 -run 'DeliverMerchantCallbacks|ExecuteCallbackDelivery'` untuk stabilitas.
- [ ] **Step 7: Report** (tanpa commit).

---

### Task 5: Job terjadwal, shutdown yang menunggu job, dokumentasi

**Files:** `cmd/api/main.go`, `cmd/api/jobs.go` (baru), `cmd/api/jobs_test.go` (baru), `docs/payment-state-machine.md`, `docs/openapi.yaml`, `README.md`, `DEPLOY.md`

**Interfaces — Consumes:** `service.CallbackDeliveryInterval`, `PaymentService.DeliverMerchantCallbacks` (Task 4). **Produces:** `func waitForJobs(wg *sync.WaitGroup, timeout time.Duration) bool` (paket `main`).

- [ ] **Step 1: Test dulu** — `cmd/api/jobs_test.go`:

```go
package main

import (
	"sync"
	"testing"
	"time"
)

func TestWaitForJobs(t *testing.T) {
	t.Run("returns true once jobs finish", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { time.Sleep(20 * time.Millisecond); wg.Done() }()
		if !waitForJobs(&wg, time.Second) {
			t.Fatal("expected jobs to finish within the timeout")
		}
	})
	t.Run("returns false on timeout", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(1)
		defer wg.Done()
		if waitForJobs(&wg, 30*time.Millisecond) {
			t.Fatal("expected a timeout")
		}
	})
}
```
- [ ] **Step 2: Jalankan** `go test ./cmd/api/ -run TestWaitForJobs -count=1` → FAIL.
- [ ] **Step 3: Implementasi.**
  - `cmd/api/jobs.go`:

```go
package main

import (
	"sync"
	"time"
)

// waitForJobs waits for the background job goroutines to return, at most
// timeout, and reports whether they all did. The jobs stop on their own once
// their context is cancelled; waiting lets an in-flight delivery or status
// update finish writing its result before the process exits.
func waitForJobs(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}
```
  - `cmd/api/main.go`: ganti blok "Background jobs" (komentar + empat `go scheduler.RunPeriodic`) dengan:

```go
	// Background jobs: expire unpaid payments past their deadline, send
	// queued merchant callbacks (and retry failed ones) every few seconds,
	// re-check recent pending/expired production payments with the provider
	// (reconcile-payments), and evict idle rate-limit buckets. A single
	// in-process ticker is enough at this stage (see internal/scheduler) — no
	// Redis/Asynq needed until the API runs as more than one instance. Each
	// job is tracked so shutdown can wait for in-flight work to finish.
	bgCtx, cancelBackgroundJobs := context.WithCancel(context.Background())
	defer cancelBackgroundJobs()
	var jobs sync.WaitGroup
	startJob := func(interval time.Duration, name string, task func(context.Context) error) {
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			scheduler.RunPeriodic(bgCtx, interval, name, task)
		}()
	}
	startJob(time.Minute, "expire-payments", paymentService.ExpirePayments)
	startJob(service.CallbackDeliveryInterval, "deliver-merchant-callbacks", paymentService.DeliverMerchantCallbacks)
	startJob(service.ReconcileInterval, "reconcile-payments", paymentService.ReconcileRecentPayments)
	startJob(10*time.Minute, "rate-limiter-cleanup", func(ctx context.Context) error {
		authRateLimiter.Cleanup(30 * time.Minute)
		publicRateLimiter.Cleanup(30 * time.Minute)
		sensitiveRateLimiter.Cleanup(30 * time.Minute)
		return nil
	})
```
    Pada shutdown, setelah `srv.Shutdown(ctx)` dan sebelum `logger.Info("Server exited")`:

```go
	if !waitForJobs(&jobs, 15*time.Second) {
		logger.Warnf("Background jobs did not finish within 15s; unsent callbacks will be re-claimed after restart")
	}
```
    (import `sync`. Jika `logger.Warnf` tidak ada, pakai fungsi level-warn yang tersedia di `pkg/logger`.)
- [ ] **Step 4: Dokumentasi** (ikuti gaya dan bahasa tiap file; ubah hanya yang perlu).
  - `docs/payment-state-machine.md`: bagian callback merchant — jelaskan bahwa `NotifyMerchantPaymentEvent` hanya mengantre (`pending`), job `deliver-merchant-callbacks` (tiap 5 detik, batch 50) mengirim, status `pending → sending → success|failed`, retry membuat percobaan baru (maks. 5, jeda 5 menit), baris `sending` macet >2 menit diambil lagi, jeda kirim paling lama ±5 detik, retry manual admin tetap sinkron. Tambah aturan URL (https publik; sandbox boleh http/lokal; IP dicek saat koneksi; redirect tidak diikuti; URL lama yang tidak valid → `failed` tanpa retry) dan koreksi catatan lama bahwa satu webhook bisa melebihi `WriteTimeout` karena callback sinkron (kini tidak lagi).
  - `docs/openapi.yaml`: pada endpoint yang menerima `webhook_url` (create/update merchant, admin dan dashboard) dan `callback_url` (create payment): deskripsi aturan URL dan respons `400` untuk URL tidak valid. Telusuri dengan `grep -n "webhook_url\|callback_url" docs/openapi.yaml`.
  - `README.md` (Background Jobs): ganti baris `retry-merchant-callbacks` dengan `deliver-merchant-callbacks` (tiap 5 detik — kirim callback merchant yang antre dan jadwalkan ulang yang gagal); sebut shutdown menunggu job selesai (maks. 15 detik).
  - `DEPLOY.md`: tambah bagian singkat "Cek sebelum deploy Paket 2" dengan dua query berikut (jalankan di `psql` pada DB produksi; hasilnya daftar merchant/payment yang URL-nya akan ditolak):

```sql
SELECT id, webhook_url FROM merchants
WHERE webhook_url IS NOT NULL AND webhook_url !~* '^https://';

SELECT id, reference, callback_url FROM payments
WHERE callback_url IS NOT NULL AND callback_url !~* '^https://'
  AND environment = 'production' AND created_at > now() - interval '30 days';
```
    Catat bahwa query ini hanya menangkap skema non-https; alamat internal ber-https tetap diblokir saat pengiriman.
- [ ] **Step 5: Verifikasi** — `gofmt -l ./cmd ./internal && go vet ./... && go test ./... -race -count=1`; `python3 -I -c "import yaml; yaml.safe_load(open('docs/openapi.yaml'))" && echo ok` (bila PyYAML tidak ada, coba `ruby -ryaml -e 'YAML.load_file("docs/openapi.yaml")'`, atau laporkan).
- [ ] **Step 6: Report** (tanpa commit).
