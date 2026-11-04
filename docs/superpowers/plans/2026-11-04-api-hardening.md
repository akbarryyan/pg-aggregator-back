# Pengerasan API (Paket 3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Menutup delapan temuan audit 2026-11-04 yang berupa perubahan kode/config API: idempotensi pembuatan payment, penegakan merchant nonaktif + pencabutan token, batas body/header, error 500 bersih, validasi config, rate limit rute ber-JWT, dan password untuk regenerate webhook secret.

**Architecture:** Perubahan aditif pada layer yang ada (config → middleware → handler → service → repository). Dua migration baru (017 idempotensi, 018 `sessions_valid_after`). Pengecekan akun/token diletakkan di `AuthService` (dipakai middleware **dan** handler `/auth/*` yang mem-parse JWT sendiri). Idempotensi memakai tabel klaim yang di-claim sebelum memanggil provider.

**Tech Stack:** Go 1.25, gorilla/mux, lib/pq (raw SQL), BurntSushi/toml, golang-jwt, PostgreSQL 18; Next.js 16 / React 19 untuk Task 8.

**Spec:** [`back/docs/superpowers/specs/2026-11-04-api-hardening-design.md`](../specs/2026-11-04-api-hardening-design.md)

## Global Constraints

- **NO git commits, NO `git add`, NO branches** (instruksi eksplisit user). Tiap task diakhiri "report changed files".
- Jangan pernah mencetak/membaca secret: `back/.config.toml`, `back/db_password`, `back/.env`, `front/.env.local`, kunci Cashi, PAT, `.pem`. `.config.example.toml` boleh dibaca.
- Jangan mengganggu proyek/Docker/port lain milik user (PostgreSQL sistem di 5432). Test PG opsional **hanya** di PG18 sementara: data dir di scratchpad, socket `/tmp/claude-1000/pgsock`, port 54999, `listen_addresses=''`, dihapus setelah selesai (verifikasi `ls /tmp/claude-1000/pgsock` hilang dan tak ada proses postgres di 54999).
- Gaya kode: ikuti file sekitar (komentar bahasa Inggris yang menjelaskan *why*, table-driven tests, fake in-memory, `sqlmock`/PG opsional via `TEST_DATABASE_URL`). Tanpa dependensi baru.
- Setiap task: `gofmt -l ./internal ./cmd` kosong, `go vet ./...` bersih, `go test ./... -race -count=1` hijau. Tes baru harus terbukti membedakan lewat **mutasi** (rusakkan logika sebentar, tes gagal, kembalikan) dan dilaporkan.
- Nilai persis dari spec: body maksimum **1 MiB**; `ReadHeaderTimeout` **5s**; `MaxHeaderBytes` **64 << 10**; write deadline export **60s**; kunci idempotensi **1–128** karakter ASCII 0x21–0x7E; TTL **24 jam**; klaim ditinggalkan setelah **2 menit**; job purge **tiap jam**; JWT secret production minimal **32** karakter; `app.environment` ∈ {`development`,`production`}; respons token ditolak: **401 "Invalid or expired token"**; merchant nonaktif membuat payment: **403**; kunci dipakai request lain: **422**; sedang diproses: **409**; header replay: `Idempotent-Replayed: true`.
- Front-end (Task 8): wajib membaca `front/AGENTS.md` dan dokumen relevan di `front/node_modules/next/dist/docs/` sebelum menulis kode; verifikasi dengan `npm run lint` dan `npm run build` dari `front/`.
- Abaikan `back/.superpowers/`.

## Urutan dan File

| Task | Isi | File utama |
|---|---|---|
| 1 | Validasi config + `[rate_limit.api]` | `internal/config/config.go`, `config_test.go`, `.config.example.toml` |
| 2 | Batas body/header, 413 webhook, write deadline export | `internal/middleware/body_limit.go`, `cmd/api/main.go`, `internal/handler/webhook_handler.go`, `internal/handler/export_deadline.go` |
| 3 | Error 500 bersih + audit `err.Error()` | `internal/handler/merchant_handler.go`, `admin_handler.go` |
| 4 | Akun nonaktif + pencabutan token (migration 018) | `migrations/018_*.sql`, domain admin/merchant, repo admin/merchant_user, `internal/service/auth_service.go`, middleware, `auth_handler.go`, router |
| 5 | Merchant nonaktif tidak bisa membuat payment | `internal/service/payment_service.go`, `internal/handler/payment_handler.go` |
| 6 | Idempotensi (migration 017) | `migrations/017_*.sql`, `internal/domain/idempotency/`, `internal/repository/idempotency_repository.go`, `payment_service.go`, handler, `main.go` |
| 7 | Rate limit `api` + password regenerate webhook secret + CORS | `cmd/api/main.go`, `router_test.go`, `merchant_handler.go` |
| 8 | Front-end: logout, 401, kolom password regenerate | `front/lib/*-auth.ts`, `front/app/dashboard/settings/page.tsx` |
| 9 | Dokumentasi | `docs/openapi.yaml`, `README.md`, `DEPLOY.md` |

Task 1–3 independen. Task 4 sebelum 7 (router). Task 3 sebelum 6 (keduanya menyentuh `merchantHandler.CreatePayment`). Task 5 sebelum 6.

---

### Task 1: Validasi config dan section `[rate_limit.api]`

**Files:** Modify `internal/config/config.go`, `internal/config/config_test.go`, `.config.example.toml`.

**Interfaces:** Produces `RateLimitConfig.API RateLimitRule` (toml `api`) yang dipakai Task 7.

- [ ] **Step 1: Tes gagal** — di `config_test.go`: (a) tambahkan ke konstanta `validTOML`, setelah blok `[rate_limit.sensitive]`:

```toml

[rate_limit.api]
requests_per_minute = 120
burst               = 60
```

(b) tambahkan helper dan tes (pakai `writeConfig`, `replaceOnce`, `validTOML` yang sudah ada):

```go
// productionTOML turns validTOML into a config that satisfies every
// production rule, so each test below breaks exactly one rule.
func productionTOML(t *testing.T) string {
	t.Helper()
	s := validTOML
	s = replaceOnce(t, s, `environment  = "development"`, `environment  = "production"`)
	s = replaceOnce(t, s, `url          = "http://localhost:8090"`, `url          = "https://api.example.com"`)
	s = replaceOnce(t, s, `frontend_url = "http://localhost:3000"`, `frontend_url = "https://app.example.com"`)
	s = replaceOnce(t, s, `api_key    = ""`, `api_key    = "cashi-api-key"`)
	s = replaceOnce(t, s, `secret_key = ""`, `secret_key = "cashi-secret-key"`)
	s = replaceOnce(t, s, `jwt_secret = "dev-jwt-secret"`, `jwt_secret = "`+strings.Repeat("k", 40)+`"`)
	return s
}

func TestLoad_ProductionValidConfig(t *testing.T) {
	if _, err := Load(writeConfig(t, productionTOML(t))); err != nil {
		t.Fatalf("a fully valid production config must load: %v", err)
	}
}

func TestLoad_HardenedValidation(t *testing.T) {
	tests := []struct {
		name    string
		toml    func(t *testing.T) string
		wantErr string
	}{
		{"typo in environment skips no check", func(t *testing.T) string {
			return replaceOnce(t, productionTOML(t), `environment  = "production"`, `environment  = "prod"`)
		}, `app.environment must be "development" or "production"`},
		{"empty environment", func(t *testing.T) string {
			return replaceOnce(t, validTOML, `environment  = "development"`, `environment  = ""`)
		}, `app.environment must be "development" or "production"`},
		{"short jwt secret in production", func(t *testing.T) string {
			return replaceOnce(t, productionTOML(t), strings.Repeat("k", 40), strings.Repeat("k", 31))
		}, "security.jwt_secret must be at least 32 characters in production"},
		{"http app url in production", func(t *testing.T) string {
			return replaceOnce(t, productionTOML(t), `url          = "https://api.example.com"`, `url          = "http://api.example.com"`)
		}, "app.url must start with https:// in production"},
		{"http frontend url in production", func(t *testing.T) string {
			return replaceOnce(t, productionTOML(t), `frontend_url = "https://app.example.com"`, `frontend_url = "http://app.example.com"`)
		}, "app.frontend_url must start with https:// in production"},
		{"zero api rate limit", func(t *testing.T) string {
			return replaceOnce(t, validTOML, "requests_per_minute = 120\nburst               = 60", "requests_per_minute = 0\nburst               = 60")
		}, "rate_limit.api.requests_per_minute must be greater than 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.toml(t)))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoad_DevelopmentMayUseHTTPAndShortSecret(t *testing.T) {
	if _, err := Load(writeConfig(t, validTOML)); err != nil {
		t.Fatalf("development config with http URLs and a short secret must load: %v", err)
	}
}
```

Perbaiki juga tes yang sudah ada yang gagal karena section/aturan baru (mis. `TestLoad_MissingKeys`, `TestLoad_MissingRateLimitSubTable`, fixture production di `TestLoad_Validation`): fixture production harus memakai `jwt_secret` ≥ 32 karakter dan URL `https`.

- [ ] **Step 2: Jalankan** `go test ./internal/config -count=1` → gagal (field/aturan belum ada).

- [ ] **Step 3: Implementasi** — `config.go`:

(a) `RateLimitConfig` tambah field setelah `Sensitive`:

```go
	// API throttles JWT-authenticated dashboard/admin routes and API-key
	// payment creation (they were previously unlimited).
	API RateLimitRule `toml:"api"`
```

(b) tambahkan konstanta di dekat `placeholderJWTSecret`:

```go
// minProductionJWTSecretLen is the shortest HS256 signing secret accepted in
// production; shorter secrets are brute-forceable.
const minProductionJWTSecretLen = 32
```

(c) di `Validate()`, setelah cek `c.App.FrontendURL == ""`, tambahkan:

```go
	switch c.App.Environment {
	case "development", "production":
	default:
		// A typo such as "prod" would otherwise silently skip every
		// production-only check below.
		return errors.New(`app.environment must be "development" or "production"`)
	}
```

(d) tambahkan `{"api", c.RateLimit.API},` ke slice aturan rate limit; (e) ganti blok `if c.App.Environment == "production" {…}` menjadi:

```go
	if c.App.Environment == "production" {
		if c.Cashi.APIKey == "" || c.Cashi.SecretKey == "" {
			return errors.New("cashi.api_key and cashi.secret_key are required in production")
		}
		if c.Security.JWTSecret == placeholderJWTSecret {
			return errors.New("security.jwt_secret must be changed in production")
		}
		if len(c.Security.JWTSecret) < minProductionJWTSecretLen {
			return errors.New("security.jwt_secret must be at least 32 characters in production")
		}
		for _, u := range []struct{ name, value string }{
			{"app.url", c.App.URL},
			{"app.frontend_url", c.App.FrontendURL},
		} {
			if !strings.HasPrefix(strings.ToLower(u.value), "https://") {
				return fmt.Errorf("%s must start with https:// in production", u.name)
			}
		}
	}
```

(Pesan hanya menyebut nama key, tidak pernah nilainya.) (f) `.config.example.toml`: tambahkan setelah `[rate_limit.sensitive]`:

```toml
[rate_limit.api]        # JWT-authenticated dashboard/admin routes + API-key payment creation
requests_per_minute = 120
burst               = 60
```

dan perbarui komentar `jwt_secret` menjadi: `# must not be empty; in production at least 32 chars and not the placeholder`.

- [ ] **Step 4: Verifikasi** `go test ./internal/config -race -count=1` hijau (termasuk `TestLoad_ExampleFileIsValid` dan `TestLoad_ErrorsNeverContainSecretValues`). Mutasi: hapus cek panjang → `TestLoad_HardenedValidation` "short jwt secret" gagal.
- [ ] **Step 5: Report** file yang berubah (tanpa commit).

---

### Task 2: Batas body/header, 413 webhook, write deadline export

**Files:** Create `internal/middleware/body_limit.go`, `internal/middleware/body_limit_test.go`, `internal/handler/export_deadline.go`. Modify `cmd/api/main.go`, `internal/handler/webhook_handler.go`, `internal/handler/webhook_handler_test.go`, export handlers.

**Interfaces:** Produces `middleware.LimitBody(max int64) func(http.Handler) http.Handler` dan `extendWriteDeadline(w http.ResponseWriter, d time.Duration)` (paket `handler`).

- [ ] **Step 1: Tes gagal** — `body_limit_test.go`:

```go
package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLimitBody(t *testing.T) {
	var readErr error
	h := LimitBody(1024)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))

	t.Run("within limit is read fully", func(t *testing.T) {
		readErr = nil
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", 1024))))
		if readErr != nil {
			t.Fatalf("unexpected error: %v", readErr)
		}
	})
	t.Run("over limit fails with MaxBytesError", func(t *testing.T) {
		readErr = nil
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", 1025))))
		var mbe *http.MaxBytesError
		if !errors.As(readErr, &mbe) {
			t.Fatalf("want *http.MaxBytesError, got %v", readErr)
		}
	})
	t.Run("request without body is untouched", func(t *testing.T) {
		readErr = nil
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		if readErr != nil {
			t.Fatalf("unexpected error: %v", readErr)
		}
	})
}
```

`webhook_handler_test.go`: tambahkan tes yang memanggil handler (lewat router mux atau `mux.SetURLVars`, ikuti tes webhook yang ada) dengan body dibungkus `http.MaxBytesReader(rec, body, 16)` berisi 64 byte dan mengharapkan **413** dan body respons tanpa teks error internal.

- [ ] **Step 2:** jalankan → gagal (paket/handler belum ada).

- [ ] **Step 3: Implementasi.**

`internal/middleware/body_limit.go`:

```go
package middleware

import "net/http"

// LimitBody caps every request body at max bytes. Reads past the cap fail with
// *http.MaxBytesError, so a flood of large bodies (notably on the
// unauthenticated provider webhook, which reads the whole body before the
// signature is checked) cannot exhaust memory.
func LimitBody(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil && r.Body != http.NoBody {
				r.Body = http.MaxBytesReader(w, r.Body, max)
			}
			next.ServeHTTP(w, r)
		})
	}
}
```

`webhook_handler.go` — ganti blok `io.ReadAll` error menjadi (tambahkan import `errors` bila belum ada):

```go
	rawPayload, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			respondError(w, http.StatusRequestEntityTooLarge, "Request body too large")
			return
		}
		logger.ErrorfCtx(r.Context(), "Failed to read webhook payload: %v", err)
		respondError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}
```

`internal/handler/export_deadline.go`:

```go
package handler

import (
	"net/http"
	"time"
)

// exportWriteTimeout lifts the server-wide 15s WriteTimeout for CSV exports
// (up to thousands of rows), which would otherwise be cut off mid-stream.
const exportWriteTimeout = 60 * time.Second

// extendWriteDeadline is best-effort: a ResponseWriter that doesn't support
// deadlines (wrappers, test recorders) returns an error we deliberately ignore.
func extendWriteDeadline(w http.ResponseWriter, d time.Duration) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d))
}
```

Panggil `extendWriteDeadline(w, exportWriteTimeout)` sebagai baris pertama di `merchantHandler.ExportPayments`, `adminHandler.ExportPayments`, `adminHandler.ExportMerchants`, `adminHandler.ExportLogs` (cari dengan `grep -n "func (h \*.*) Export" internal/handler/*.go`).

`cmd/api/main.go` — ganti `handler := c.Handler(router)` dengan `handler := middleware.LimitBody(1 << 20)(c.Handler(router))` (komentar: 1 MiB cap on every request body) dan lengkapi `http.Server`:

```go
		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    64 << 10,
```

- [ ] **Step 4: Verifikasi** `go test ./internal/middleware ./internal/handler ./cmd/api -race -count=1` hijau; mutasi: hapus `MaxBytesReader` → tes middleware gagal.
- [ ] **Step 5: Report** (tanpa commit).

---

### Task 3: Error 500 bersih

**Files:** Modify `internal/handler/merchant_handler.go`, `internal/handler/admin_handler.go`; tests di `merchant_handler_test.go`, `payment_handler_test.go` atau file tes handler yang relevan.

- [ ] **Step 1: Inventaris** — `grep -n "err.Error()" internal/handler/*.go | grep -v _test`. Untuk **setiap** titik klasifikasikan dan catat di laporan task: (a) respons 400/409 dengan error domain yang dikenal → biarkan; (b) respons 500 → ganti generik; (c) respons 400 yang bisa berisi error tak dikenal (mis. `merchant_handler.go` `UpdateBusiness` setelah `respondInvalidCallbackURL`, dan `adminHandler.UpdateMerchant` serupa) → pakai pola di Step 3.

- [ ] **Step 2: Tes gagal** — di tes handler tambahkan satu tes per titik 500 yang dikoreksi: service palsu mengembalikan `errors.New("pq: relation \"payments\" does not exist")`; ekspektasi status 500 dan body **tidak** mengandung `pq:`/`relation`. Untuk `merchantHandler.CreatePayment` gunakan pola fake yang dipakai `merchant_handler_test.go` (baca dulu); bila membuat `*service.PaymentService` yang gagal tidak mudah, uji lewat `PaymentService` dengan `fakePaymentRepo` yang `Create` mengembalikan error (lihat `fakes_test.go`).

- [ ] **Step 3: Implementasi.**

`merchant_handler.go` (blok akhir `CreatePayment`, ~baris 303–309) menjadi:

```go
	p, err := h.paymentService.CreatePayment(r.Context(), &req)
	if err != nil {
		if respondInvalidCallbackURL(w, err) {
			return
		}
		logger.ErrorfCtx(r.Context(), "merchant create payment: %v", err)
		respondCreatePaymentError(w, err)
		return
	}
```

(`respondCreatePaymentError` sudah memetakan error provider ke 503/400/502 dan sisanya ke 500 generik "Failed to create payment".)

`admin_handler.go` (~baris 169–172): ganti `respondError(w, http.StatusInternalServerError, err.Error())` dengan `respondError(w, http.StatusInternalServerError, "Failed to create payment")`.

Untuk titik kategori (c): tambahkan helper di `internal/handler/callback_url_errors.go`-sebelahnya, file baru `internal/handler/safe_error.go`:

```go
package handler

import (
	"errors"
	"net/http"
)

// respondValidationOrInternal sends err.Error() (HTTP 400) only when err is one
// of the known domain validation errors; anything else is logged by the caller
// and answered with a generic 500 so DB/provider text never reaches the client.
func respondValidationOrInternal(w http.ResponseWriter, err error, known ...error) {
	for _, k := range known {
		if errors.Is(err, k) {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	respondError(w, http.StatusInternalServerError, "Internal server error")
}
```

dan pakai di `UpdateBusiness`/`UpdateMerchant` dengan daftar error validasi dari `merchant.UpdateMerchantRequest.Validate()` (baca fungsi itu; sertakan juga `merchant.ErrMerchantNotFound` → 404 bila belum ditangani) disertai `logger.ErrorfCtx` sebelum respons 500.

- [ ] **Step 4: Verifikasi** `go test ./internal/handler -race -count=1`; mutasi: kembalikan `err.Error()` pada satu titik → tes gagal.
- [ ] **Step 5: Report** + tabel inventaris titik→kategori (tanpa commit).

---

### Task 4: Akun nonaktif dan pencabutan token (migration 018)

**Files:** Create `migrations/018_add_sessions_valid_after.sql`, `internal/service/auth_session_test.go`. Modify `internal/domain/admin/admin.go`, `internal/domain/merchant/*` (struct `User`), `internal/repository/admin_repository.go`, `internal/repository/merchant_user_repository.go`, `internal/service/auth_service.go`, `internal/service/auth_service_test.go` (fake repos), `internal/middleware/admin_auth.go`, `internal/handler/auth_handler.go`, `cmd/api/main.go` (route), `cmd/api/router_test.go`.

**Interfaces — Produces:**

```go
// service
func (s *AuthService) AuthorizeAdmin(ctx context.Context, c *AdminClaims) error
func (s *AuthService) AuthorizeMerchant(ctx context.Context, c *MerchantClaims) error
func (s *AuthService) LogoutAdmin(ctx context.Context, adminID uuid.UUID) error
func (s *AuthService) LogoutMerchant(ctx context.Context, userID uuid.UUID) error
// repo (kedua repo): RevokeSessions(ctx context.Context, id uuid.UUID, at time.Time) error
// domain: admin.Admin.SessionsValidAfter *time.Time; merchant.User.SessionsValidAfter *time.Time (json:"-")
```

`AuthorizeX` mengembalikan `admin.ErrInvalidToken` / `merchant.ErrMerchantInvalidCredentials` untuk akun/merchant nonaktif, user tidak ada, atau token dicabut (→ 401); error lain dikembalikan apa adanya (→ 500).

- [ ] **Step 1: Migration** `migrations/018_add_sessions_valid_after.sql`:

```sql
-- Token revocation: a JWT whose iat is before sessions_valid_after is rejected.
-- NULL means no cutoff, so nothing is invalidated when this migration runs.
-- Set on password change and on logout.
ALTER TABLE admins ADD COLUMN IF NOT EXISTS sessions_valid_after TIMESTAMPTZ;
ALTER TABLE merchant_users ADD COLUMN IF NOT EXISTS sessions_valid_after TIMESTAMPTZ;

COMMENT ON COLUMN admins.sessions_valid_after IS 'JWTs issued before this instant are rejected (logout / password change)';
COMMENT ON COLUMN merchant_users.sessions_valid_after IS 'JWTs issued before this instant are rejected (logout / password change)';
```

- [ ] **Step 2: Tes gagal** — `auth_session_test.go` (gunakan fake repo auth di `auth_service_test.go`; baca dulu cara `AuthService` dibangun di sana — `NewAuthService(...)`+`WithMerchantAuth(...)` — dan tambahkan `RevokeSessions` ke fake-nya; fake harus menyimpan `SessionsValidAfter`):

```go
func TestSessionRevoked(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	at := func(d time.Duration) *jwt.NumericDate { return jwt.NewNumericDate(now.Add(d)) }
	cutoff := now
	tests := []struct {
		name       string
		iat        *jwt.NumericDate
		validAfter *time.Time
		want       bool
	}{
		{"no cutoff", at(-time.Hour), nil, false},
		{"issued before cutoff", at(-time.Second), &cutoff, true},
		{"issued at cutoff second", at(0), &cutoff, false},
		{"issued after cutoff", at(time.Second), &cutoff, false},
		{"cutoff with sub-second part keeps same-second token", at(0), ptrTime(now.Add(900 * time.Millisecond)), false},
		{"missing iat with cutoff", nil, &cutoff, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionRevoked(tc.iat, tc.validAfter); got != tc.want {
				t.Errorf("sessionRevoked = %v, want %v", got, tc.want)
			}
		})
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
```

Tambahkan tes `AuthorizeMerchant`/`AuthorizeAdmin` (tabel): user aktif+merchant aktif → nil; user nonaktif → `ErrMerchantInvalidCredentials`; merchant nonaktif → sama; user tidak ada → sama; token terbit sebelum `SessionsValidAfter` → sama; error repo (`errors.New("db down")`) → dikembalikan apa adanya (bukan invalid). Untuk admin analog. Tambahkan tes: `ChangeMerchantPassword`/`ChangePassword` mengisi `SessionsValidAfter` (fake `UpdatePasswordHash` memanggil logika yang sama seperti SQL: set `SessionsValidAfter=now`) — token yang diterbitkan sebelum ganti password lalu ditolak `Authorize*`; `LogoutMerchant`/`LogoutAdmin` melakukan hal serupa.

- [ ] **Step 3: Jalankan** → gagal (belum ada fungsi).

- [ ] **Step 4: Domain** — tambahkan ke `admin.Admin` dan `merchant.User`:

```go
	SessionsValidAfter *time.Time `json:"-" db:"sessions_valid_after"`
```

- [ ] **Step 5: Repository.**

`admin_repository.go`: tambahkan `sessions_valid_after` (setelah `updated_at`) ke SELECT `GetByEmail`, `GetByID` dan ke `RETURNING` `UpdateProfile`, serta `&a.SessionsValidAfter,` ke ketiga `Scan`. `UpdatePasswordHash` menjadi:

```go
	query := `
		UPDATE admins
		SET password_hash = $1, sessions_valid_after = $2, updated_at = $2
		WHERE id = $3
	`
```

(sisanya tak berubah — `now` sebagai $2), dan tambahkan:

```go
// RevokeSessions invalidates every JWT issued before at (logout, password change).
func (r *AdminRepository) RevokeSessions(ctx context.Context, id uuid.UUID, at time.Time) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE admins SET sessions_valid_after = $2, updated_at = $2 WHERE id = $1
	`, id, at.UTC())
	if err != nil {
		return fmt.Errorf("failed to revoke admin sessions: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to read revoke result: %w", err)
	}
	if rows == 0 {
		return admin.ErrAdminNotFound
	}
	return nil
}
```

`merchant_user_repository.go`: tambahkan `sessions_valid_after` ke kedua SELECT (`GetByEmail`, `GetByID`), `sessions_valid_after` sebagai kolom terakhir di `scanMerchantUser` memakai `sql.NullTime`:

```go
	var (
		u         merchant.User
		lastLogin sql.NullTime
		validAfter sql.NullTime
	)
	err := row.Scan(
		&u.ID, &u.MerchantID, &u.Name, &u.Email, &u.PasswordHash, &u.Role, &u.IsActive,
		&lastLogin, &u.CreatedAt, &u.UpdatedAt, &validAfter,
	)
	...
	if validAfter.Valid {
		t := validAfter.Time
		u.SessionsValidAfter = &t
	}
```

`UpdatePasswordHash`: `UPDATE merchant_users SET password_hash = $2, sessions_valid_after = $3, updated_at = $3 WHERE id = $1`. Tambahkan:

```go
func (r *MerchantUserRepository) RevokeSessions(ctx context.Context, id uuid.UUID, at time.Time) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE merchant_users SET sessions_valid_after = $2, updated_at = $2 WHERE id = $1
	`, id, at.UTC())
	if err != nil {
		return fmt.Errorf("failed to revoke merchant user sessions: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return merchant.ErrMerchantUserNotFound
	}
	return nil
}
```

Cek juga query lain yang memanggil `scanMerchantUser` (`grep -n scanMerchantUser internal/repository`) agar kolom SELECT-nya ikut diperbarui.

- [ ] **Step 6: AuthService** — tambahkan `RevokeSessions(ctx context.Context, id uuid.UUID, at time.Time) error` ke interface `authMerchantUserRepository` dan `authAdminRepository`; pastikan `LoginAdmin` mengisi `IssuedAt: jwt.NewNumericDate(now)` pada claims admin (tambahkan bila belum ada; merchant sudah). Tambahkan di `auth_service.go`:

```go
// sessionRevoked reports whether a token issued at iat predates the account's
// revocation cutoff. JWT iat has one-second precision, so the cutoff is
// truncated to the second: a token minted in the same second as a logout or
// password change survives (a window under one second, accepted by the spec),
// which also lets a fresh login right after a logout work.
func sessionRevoked(iat *jwt.NumericDate, validAfter *time.Time) bool {
	if validAfter == nil {
		return false
	}
	if iat == nil {
		return true
	}
	return iat.Time.Before(validAfter.Truncate(time.Second))
}

// AuthorizeAdmin re-checks, on every request, what a valid signature alone
// cannot: the admin still exists, is active, and the token was not revoked.
func (s *AuthService) AuthorizeAdmin(ctx context.Context, c *AdminClaims) error {
	a, err := s.adminRepo.GetByID(ctx, c.AdminID)
	if err != nil {
		if errors.Is(err, admin.ErrAdminNotFound) {
			return admin.ErrInvalidToken
		}
		return err
	}
	if !a.IsActive || sessionRevoked(c.IssuedAt, a.SessionsValidAfter) {
		return admin.ErrInvalidToken
	}
	return nil
}

// AuthorizeMerchant is AuthorizeAdmin for merchant dashboard tokens; it also
// requires the parent merchant to be active.
func (s *AuthService) AuthorizeMerchant(ctx context.Context, c *MerchantClaims) error {
	if s.merchantUserRepo == nil {
		return fmt.Errorf("merchant auth not configured")
	}
	u, err := s.merchantUserRepo.GetByID(ctx, c.UserID)
	if err != nil {
		if errors.Is(err, merchant.ErrMerchantUserNotFound) {
			return merchant.ErrMerchantInvalidCredentials
		}
		return err
	}
	if !u.IsActive || sessionRevoked(c.IssuedAt, u.SessionsValidAfter) {
		return merchant.ErrMerchantInvalidCredentials
	}
	if s.merchantRepo != nil {
		m, err := s.merchantRepo.GetByID(ctx, u.MerchantID)
		if err != nil {
			if errors.Is(err, merchant.ErrMerchantNotFound) {
				return merchant.ErrMerchantInvalidCredentials
			}
			return err
		}
		if !m.IsActive {
			return merchant.ErrMerchantInvalidCredentials
		}
	}
	return nil
}

func (s *AuthService) LogoutAdmin(ctx context.Context, adminID uuid.UUID) error {
	return s.adminRepo.RevokeSessions(ctx, adminID, time.Now().UTC())
}

func (s *AuthService) LogoutMerchant(ctx context.Context, userID uuid.UUID) error {
	if s.merchantUserRepo == nil {
		return fmt.Errorf("merchant auth not configured")
	}
	return s.merchantUserRepo.RevokeSessions(ctx, userID, time.Now().UTC())
}
```

(Pastikan `errors` ter-import; `UpdatePasswordHash` di kedua repo sudah memutus sesi, jadi `ChangePassword`/`ChangeMerchantPassword` tak perlu perubahan.)

- [ ] **Step 7: Middleware + handler.** Di `admin_auth.go`, setelah `ParseMerchantToken` sukses:

```go
		if err := m.authService.AuthorizeMerchant(r.Context(), claims); err != nil {
			if errors.Is(err, merchant.ErrMerchantInvalidCredentials) {
				writeJSONError(w, http.StatusUnauthorized, "Invalid or expired token")
				return
			}
			logger.ErrorfCtx(r.Context(), "merchant session check failed: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
```

dan padanannya di `RequireAdmin` (`AuthorizeAdmin`, `admin.ErrInvalidToken`). Tambahkan import `errors`, domain `admin`/`merchant`, dan `logger` sesuai kebutuhan (cek apakah paket middleware sudah mengimpor logger; pola impor sama dengan `rate_limit.go`/`request_id.go`).

Di `auth_handler.go`, tambahkan pemeriksaan yang sama di akhir `authenticateMerchant` dan `authenticateAdmin` (mereka mem-parse JWT sendiri untuk `/auth/me`, `/auth/profile`, `/auth/change-password`, …): setelah parse sukses panggil `h.authService.AuthorizeX`; error invalid → `respondError(w, 401, "Invalid or expired token")`; lainnya → log + `respondError(w, 500, "Internal server error")`, `return nil, false`.

Handler logout baru (auth_handler.go):

```go
func (h *AuthHandler) LogoutMerchant(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticateMerchant(w, r)
	if !ok {
		return
	}
	if err := h.authService.LogoutMerchant(r.Context(), claims.UserID); err != nil {
		logger.ErrorfCtx(r.Context(), "Merchant logout failed: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to sign out")
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "Signed out"})
}

func (h *AuthHandler) LogoutAdmin(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticateAdmin(w, r)
	if !ok {
		return
	}
	if err := h.authService.LogoutAdmin(r.Context(), claims.AdminID); err != nil {
		logger.ErrorfCtx(r.Context(), "Admin logout failed: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to sign out")
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "Signed out"})
}
```

Route di `setupRouter` (dekat rute `/auth/*` lain): `api.HandleFunc("/auth/logout", authHandler.LogoutMerchant).Methods("POST")` dan `api.HandleFunc("/auth/admin/logout", authHandler.LogoutAdmin).Methods("POST")`.

- [ ] **Step 8: Tes handler/router** — tambahkan tes di `auth_handler_test.go`/`router_test.go` (ikuti pola fake di sana; perbarui fake repo handler dengan `RevokeSessions`): setelah `POST /auth/logout`, memanggil `/auth/me` dengan token yang sama → 401; merchant dinonaktifkan → `/merchant/dashboard/summary` 401; token baru yang diterbitkan setelah logout (detik berbeda) → 200.
- [ ] **Step 9: Tes PG opsional** — di `internal/repository/` (pola `payment_repository_pg_test.go`, gated `TEST_DATABASE_URL`, terapkan migrasi 001–018): `RevokeSessions` mengisi kolom dan `GetByID` membacanya; `UpdatePasswordHash` mengisi `sessions_valid_after`. Jalankan di PG18 sementara sesuai Global Constraints.
- [ ] **Step 10: Verifikasi** suite penuh + mutasi (hapus `sessionRevoked` check → tes token dicabut gagal; hapus `m.IsActive` → tes merchant nonaktif gagal). **Report** (tanpa commit).

---

### Task 5: Merchant nonaktif tidak bisa membuat payment

**Files:** Modify `internal/service/payment_service.go`, `internal/handler/payment_handler.go` (`respondCreatePaymentError`), handler checkout payment link bila memetakan error sendiri (baca `payment_link_handler.go`); tests `payment_service_test.go`, `payment_handler_test.go`.

- [ ] **Step 1: Tes gagal** (`payment_service_test.go`): bangun service seperti `newTestPaymentService()` lalu `svc.WithMerchantCallbackDeps(merchantRepo, newFakeMerchantCallbackRepo())` dengan `merchantRepo := newFakeMerchantRepo()` berisi merchant `IsActive:false`:

```go
func TestCreatePayment_InactiveMerchantIsRejected(t *testing.T) {
	svc, paymentRepo, _, _ := newTestPaymentService()
	sandbox := &fakeProvider{name: "sandbox"}
	merchantRepo := newFakeMerchantRepo()
	m := &merchant.Merchant{ID: uuid.New(), IsActive: false}
	merchantRepo.byID[m.ID] = m
	svc = svc.WithSandboxProvider(sandbox).WithMerchantCallbackDeps(merchantRepo, newFakeMerchantCallbackRepo())

	req := baseCreateRequest(m.ID)
	req.Environment = payment.EnvironmentSandbox
	_, err := svc.CreatePayment(context.Background(), req)
	if !errors.Is(err, merchant.ErrMerchantInactive) {
		t.Fatalf("want ErrMerchantInactive, got %v", err)
	}
	if sandbox.createCalls != 0 {
		t.Errorf("provider must not be called for an inactive merchant")
	}
	if len(paymentRepo.byID) != 0 { // ganti dengan field penyimpan payment pada fakePaymentRepo
		t.Errorf("no payment may be stored")
	}
}

func TestCreatePayment_ActiveOrUnknownMerchantProceeds(t *testing.T) {
	// aktif -> sukses; merchant tidak dikenal oleh repo (ErrMerchantNotFound) -> tidak diblokir di sini
	// (FK di database menolaknya); merchantRepo nil -> tidak diblokir.
}
```

(Isi tes kedua dengan tiga subkasus eksplisit; sesuaikan nama field penyimpan di `fakePaymentRepo` — baca `payment_fakes_test.go:28-60`. Tambahkan impor `merchant`.) Di `payment_handler_test.go`: `respondCreatePaymentError` untuk `merchant.ErrMerchantInactive` → **403** dengan body "Merchant account is inactive".

- [ ] **Step 2: Implementasi** — di awal `CreatePayment`, setelah `req.Validate()`:

```go
	if err := s.ensureMerchantActive(ctx, req.MerchantID); err != nil {
		return nil, err
	}
```

dan fungsi:

```go
// ensureMerchantActive is the single choke point for every payment-creation
// path (API key, dashboard, public payment-link checkout, admin): a merchant
// deactivated after a token or link was issued must not create new payments.
// An unknown merchant is left to the foreign key; no merchant repo (unit-test
// wiring) skips the check.
func (s *PaymentService) ensureMerchantActive(ctx context.Context, merchantID uuid.UUID) error {
	if s.merchantRepo == nil {
		return nil
	}
	m, err := s.merchantRepo.GetByID(ctx, merchantID)
	if err != nil {
		if errors.Is(err, merchant.ErrMerchantNotFound) {
			return nil
		}
		return fmt.Errorf("failed to load merchant: %w", err)
	}
	if !m.IsActive {
		return merchant.ErrMerchantInactive
	}
	return nil
}
```

`respondCreatePaymentError` tambah case: `case errors.Is(err, merchant.ErrMerchantInactive): respondError(w, http.StatusForbidden, "Merchant account is inactive")`. Periksa `payment_link_handler.go` (`InitiatePaymentLinkCheckout`) dan `merchant_handler.go`/`admin_handler.go` `CreatePayment`: pastikan `ErrMerchantInactive` menghasilkan 403 (bukan 500) di setiap jalur; tambahkan case bila peta error mereka sendiri.

- [ ] **Step 3: Verifikasi** + mutasi (hapus pengecekan `IsActive` → tes gagal). **Report** (tanpa commit).

---

### Task 6: Idempotensi pembuatan payment (migration 017)

**Files:** Create `migrations/017_create_idempotency_keys.sql`, `internal/domain/idempotency/idempotency.go`, `idempotency_test.go`, `internal/repository/idempotency_repository.go`, `idempotency_repository_pg_test.go`, `internal/handler/idempotency_errors.go`. Modify `internal/service/interfaces.go`, `internal/service/payment_service.go` (+ file baru `internal/service/payment_idempotency.go`), `internal/service/payment_fakes_test.go`, `internal/handler/payment_handler.go`, `internal/handler/merchant_handler.go`, `cmd/api/main.go`.

**Interfaces — Produces:**

```go
// package idempotency
const TTL = 24 * time.Hour
const AbandonedAfter = 2 * time.Minute
var ErrKeyInvalid, ErrKeyReused, ErrInProgress error
type Record struct { ID, MerchantID uuid.UUID; Environment, Key, RequestHash string; PaymentID *uuid.UUID; CreatedAt time.Time }
func ValidateKey(key string) error
type RequestFields struct{ ... }
func RequestHash(f RequestFields) string
// service
type IdempotentPayment struct { Payment *payment.Payment; Replayed bool }
func (s *PaymentService) WithIdempotency(repo idempotencyRepository) *PaymentService
func (s *PaymentService) CreatePaymentIdempotent(ctx context.Context, req *payment.CreatePaymentRequest, key string) (*IdempotentPayment, error)
func (s *PaymentService) PurgeIdempotencyKeys(ctx context.Context) error
```

- [ ] **Step 1: Migration** `017_create_idempotency_keys.sql`:

```sql
-- Idempotency claims for merchant payment creation (Idempotency-Key header).
-- A claim is inserted BEFORE the provider is called, so two concurrent requests
-- with the same key cannot both create a payment. payment_id stays NULL until
-- the payment exists. Rows older than 24h are ignored and purged hourly.
CREATE TABLE IF NOT EXISTS idempotency_keys (
    id              UUID PRIMARY KEY,
    merchant_id     UUID NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    environment     VARCHAR(20) NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    request_hash    CHAR(64) NOT NULL,
    payment_id      UUID REFERENCES payments(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_idempotency_scope UNIQUE (merchant_id, environment, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_idempotency_keys_created_at ON idempotency_keys (created_at);
```

- [ ] **Step 2: Tes domain gagal** — `idempotency_test.go`:

```go
package idempotency

import (
	"strings"
	"testing"
)

func TestValidateKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		ok   bool
	}{
		{"typical uuid", "3f1c2d9e-8a7b-4c6d-9e0f-1a2b3c4d5e6f", true},
		{"one char", "a", true},
		{"max length", strings.Repeat("a", 128), true},
		{"empty", "", false},
		{"too long", strings.Repeat("a", 129), false},
		{"space", "has space", false},
		{"control char", "a\tb", false},
		{"non-ascii", "kunci-é", false},
		{"del char", "a\x7fb", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateKey(tc.key)
			if (err == nil) != tc.ok {
				t.Errorf("ValidateKey(%q) err=%v, want ok=%v", tc.key, err, tc.ok)
			}
		})
	}
}

func TestRequestHash_StableAndSensitive(t *testing.T) {
	name := "Budi"
	base := RequestFields{Amount: 1000, Currency: "IDR", PaymentMethod: "qris", Description: "x", CustomerName: &name, ExpiresInMinutes: 30}
	if RequestHash(base) != RequestHash(base) {
		t.Fatal("hash must be deterministic")
	}
	changed := base
	changed.Amount = 1001
	if RequestHash(base) == RequestHash(changed) {
		t.Error("amount must change the hash")
	}
	other := "Siti"
	changed = base
	changed.CustomerName = &other
	if RequestHash(base) == RequestHash(changed) {
		t.Error("customer name must change the hash")
	}
	if len(RequestHash(base)) != 64 {
		t.Errorf("want 64 hex chars, got %d", len(RequestHash(base)))
	}
}
```

- [ ] **Step 3: Domain** `internal/domain/idempotency/idempotency.go`:

```go
// Package idempotency holds the types behind the Idempotency-Key header on
// payment creation. It deliberately imports nothing from the payment package
// (the service builds RequestFields) to avoid an import cycle.
package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	// TTL is how long a key is remembered; older claims are treated as absent.
	TTL = 24 * time.Hour
	// AbandonedAfter is when a claim that never got a payment (process died, or
	// the request is still stuck) may be claimed again. It must exceed the
	// provider client timeout (30s) comfortably.
	AbandonedAfter = 2 * time.Minute

	maxKeyLen = 128
)

var (
	ErrKeyInvalid = errors.New("Idempotency-Key must be 1-128 visible ASCII characters")
	ErrKeyReused  = errors.New("Idempotency-Key was already used with a different request")
	ErrInProgress = errors.New("a request with this Idempotency-Key is still being processed; retry shortly")
)

type Record struct {
	ID          uuid.UUID
	MerchantID  uuid.UUID
	Environment string
	Key         string
	RequestHash string
	PaymentID   *uuid.UUID
	CreatedAt   time.Time
}

// ValidateKey accepts 1-128 printable, non-space ASCII bytes (0x21-0x7E).
func ValidateKey(key string) error {
	if len(key) == 0 || len(key) > maxKeyLen {
		return ErrKeyInvalid
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			return ErrKeyInvalid
		}
	}
	return nil
}

// RequestFields are the request values that define "the same payment". The
// struct (not a map) keeps JSON field order, and therefore the hash, stable.
type RequestFields struct {
	Amount                int64   `json:"amount"`
	Currency              string  `json:"currency"`
	PaymentMethod         string  `json:"payment_method"`
	Description           string  `json:"description"`
	CustomerName          *string `json:"customer_name"`
	CustomerEmail         *string `json:"customer_email"`
	CallbackURL           *string `json:"callback_url"`
	ExpiresInMinutes      int     `json:"expires_in_minutes"`
	UseCustomMerchantName bool    `json:"use_custom_merchant_name"`
}

func RequestHash(f RequestFields) string {
	b, _ := json.Marshal(f) // a struct of plain values cannot fail to marshal
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Repository** `internal/repository/idempotency_repository.go`:

```go
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/idempotency"
	"github.com/google/uuid"
)

type IdempotencyRepository struct {
	db *sql.DB
}

func NewIdempotencyRepository(db *sql.DB) *IdempotencyRepository {
	return &IdempotencyRepository{db: db}
}

// Claim tries to take (merchant, environment, key). It returns (rec, true) when
// this caller now owns the key, (existing, false) when someone else does, and
// (nil, false) when the conflicting row vanished before it could be read (the
// caller retries). A row is taken over when it is older than idempotency.TTL, or
// when it never received a payment and is older than idempotency.AbandonedAfter.
func (r *IdempotencyRepository) Claim(
	ctx context.Context, merchantID uuid.UUID, environment, key, requestHash string, now time.Time,
) (*idempotency.Record, bool, error) {
	id := uuid.New()
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO idempotency_keys (id, merchant_id, environment, idempotency_key, request_hash, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (merchant_id, environment, idempotency_key) DO UPDATE
		SET id = EXCLUDED.id, request_hash = EXCLUDED.request_hash,
		    payment_id = NULL, created_at = EXCLUDED.created_at
		WHERE idempotency_keys.created_at < $7
		   OR (idempotency_keys.payment_id IS NULL AND idempotency_keys.created_at < $8)
		RETURNING id
	`, id, merchantID, environment, key, requestHash, now,
		now.Add(-idempotency.TTL), now.Add(-idempotency.AbandonedAfter)).Scan(&id)
	if err == nil {
		return &idempotency.Record{ID: id, MerchantID: merchantID, Environment: environment,
			Key: key, RequestHash: requestHash, CreatedAt: now}, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("failed to claim idempotency key: %w", err)
	}

	rec := &idempotency.Record{}
	var paymentID uuid.NullUUID
	err = r.db.QueryRowContext(ctx, `
		SELECT id, merchant_id, environment, idempotency_key, request_hash, payment_id, created_at
		FROM idempotency_keys
		WHERE merchant_id = $1 AND environment = $2 AND idempotency_key = $3
	`, merchantID, environment, key).Scan(
		&rec.ID, &rec.MerchantID, &rec.Environment, &rec.Key, &rec.RequestHash, &paymentID, &rec.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to read idempotency key: %w", err)
	}
	if paymentID.Valid {
		rec.PaymentID = &paymentID.UUID
	}
	return rec, false, nil
}

// Complete links the claim to the payment it produced.
func (r *IdempotencyRepository) Complete(ctx context.Context, id, paymentID uuid.UUID) error {
	if _, err := r.db.ExecContext(ctx,
		`UPDATE idempotency_keys SET payment_id = $2 WHERE id = $1`, id, paymentID); err != nil {
		return fmt.Errorf("failed to complete idempotency key: %w", err)
	}
	return nil
}

// Release drops a claim whose payment was never created, so a retry can run.
func (r *IdempotencyRepository) Release(ctx context.Context, id uuid.UUID) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE id = $1 AND payment_id IS NULL`, id); err != nil {
		return fmt.Errorf("failed to release idempotency key: %w", err)
	}
	return nil
}

func (r *IdempotencyRepository) PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("failed to purge idempotency keys: %w", err)
	}
	return res.RowsAffected()
}
```

- [ ] **Step 5: Tes PG** `idempotency_repository_pg_test.go` (salin penyiapan dan skip-gate dari `payment_repository_pg_test.go`; migrasi 001–017 diterapkan oleh helper yang sama — periksa dan tambahkan 017 bila daftar migrasinya eksplisit): (a) `Claim` pertama menang; `Claim` kedua dengan kunci sama → `(existing,false)` dengan `PaymentID==nil`; (b) `Complete` lalu `Claim` → record punya `PaymentID`; (c) `Release` lalu `Claim` menang lagi; (d) waktu `now` digeser +2m01s: klaim tanpa payment bisa diambil alih, klaim dengan payment **tidak**; (e) `now` +24h01m: klaim dengan payment pun diambil alih (hash baru tersimpan); (f) dua goroutine `Claim` paralel (kunci sama) → tepat satu `won`; (g) lingkup berbeda (environment lain / merchant lain) tidak bentrok; (h) `PurgeOlderThan` menghapus baris lama saja.

- [ ] **Step 6: Service** — `interfaces.go`:

```go
type idempotencyRepository interface {
	Claim(ctx context.Context, merchantID uuid.UUID, environment, key, requestHash string, now time.Time) (*idempotency.Record, bool, error)
	Complete(ctx context.Context, id, paymentID uuid.UUID) error
	Release(ctx context.Context, id uuid.UUID) error
	PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
}
```

`PaymentService` tambah field `idempotencyRepo idempotencyRepository`. File baru `internal/service/payment_idempotency.go`:

```go
package service

import (
	"context"
	"errors"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/idempotency"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
)

// maxClaimAttempts bounds the loop that retries when a conflicting claim
// disappears between the failed insert and the read.
const maxClaimAttempts = 3

type IdempotentPayment struct {
	Payment  *payment.Payment
	Replayed bool
}

func (s *PaymentService) WithIdempotency(repo idempotencyRepository) *PaymentService {
	s.idempotencyRepo = repo
	return s
}

// CreatePaymentIdempotent creates a payment, or replays the one an earlier call
// with the same Idempotency-Key created. Without a key (or without a configured
// store, as in unit-test wiring) it behaves exactly like CreatePayment. The key
// is claimed BEFORE the provider is called so concurrent duplicates cannot both
// charge. Known limit: a crash after the provider charged but before the claim
// is completed leaves an orphan charge (the same risk as before this feature).
func (s *PaymentService) CreatePaymentIdempotent(ctx context.Context, req *payment.CreatePaymentRequest, key string) (*IdempotentPayment, error) {
	if key == "" || s.idempotencyRepo == nil {
		p, err := s.CreatePayment(ctx, req)
		if err != nil {
			return nil, err
		}
		return &IdempotentPayment{Payment: p}, nil
	}
	if err := idempotency.ValidateKey(key); err != nil {
		return nil, err
	}
	if err := req.Validate(); err != nil { // resolves defaults/environment before hashing
		return nil, err
	}
	hash := idempotency.RequestHash(idempotency.RequestFields{
		Amount:                req.Amount,
		Currency:              req.Currency,
		PaymentMethod:         req.PaymentMethod,
		Description:           req.Description,
		CustomerName:          req.CustomerName,
		CustomerEmail:         req.CustomerEmail,
		CallbackURL:           req.CallbackURL,
		ExpiresInMinutes:      req.ExpiresInMinutes,
		UseCustomMerchantName: req.UseCustomMerchantName,
	})

	for attempt := 0; attempt < maxClaimAttempts; attempt++ {
		rec, won, err := s.idempotencyRepo.Claim(ctx, req.MerchantID, req.Environment, key, hash, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if won {
			return s.createClaimed(ctx, rec.ID, req)
		}
		if rec == nil {
			continue // the conflicting claim vanished; try to take it
		}
		if rec.RequestHash != hash {
			return nil, idempotency.ErrKeyReused
		}
		if rec.PaymentID == nil {
			return nil, idempotency.ErrInProgress
		}
		p, err := s.paymentRepo.GetByID(ctx, *rec.PaymentID)
		if err != nil {
			if errors.Is(err, payment.ErrPaymentNotFound) {
				return nil, idempotency.ErrInProgress
			}
			return nil, err
		}
		return &IdempotentPayment{Payment: p, Replayed: true}, nil
	}
	return nil, idempotency.ErrInProgress
}

func (s *PaymentService) createClaimed(ctx context.Context, claimID uuid.UUID, req *payment.CreatePaymentRequest) (*IdempotentPayment, error) {
	p, err := s.CreatePayment(ctx, req)
	// The claim must be settled even if the caller disconnected meanwhile.
	dctx, cancel := detached(ctx)
	defer cancel()
	if err != nil {
		if relErr := s.idempotencyRepo.Release(dctx, claimID); relErr != nil {
			logger.ErrorfCtx(ctx, "Failed to release idempotency claim %s: %v", claimID, relErr)
		}
		return nil, err
	}
	if cErr := s.idempotencyRepo.Complete(dctx, claimID, p.ID); cErr != nil {
		logger.ErrorfCtx(ctx, "Failed to complete idempotency claim %s for payment %s: %v", claimID, p.ID, cErr)
	}
	return &IdempotentPayment{Payment: p}, nil
}

// PurgeIdempotencyKeys is the hourly job body.
func (s *PaymentService) PurgeIdempotencyKeys(ctx context.Context) error {
	if s.idempotencyRepo == nil {
		return nil
	}
	n, err := s.idempotencyRepo.PurgeOlderThan(ctx, time.Now().UTC().Add(-idempotency.TTL))
	if err != nil {
		return err
	}
	if n > 0 {
		logger.InfofCtx(ctx, "Purged %d expired idempotency keys", n)
	}
	return nil
}
```

- [ ] **Step 7: Fake + tes service** — di `payment_fakes_test.go` tambahkan fake in-memory yang meniru aturan SQL (pakai `idempotency.TTL`/`AbandonedAfter`, `sync.Mutex`, kunci map `merchant|env|key`, hitung `claimCalls`, bisa menyuntik `completeErr`). Tes di `payment_idempotency_test.go` (bangun service seperti `TestCreatePayment_Sandbox_UsesSandboxProviderOnly`, ditambah `.WithIdempotency(fake)`; req sandbox via `baseCreateRequest`):

1. tanpa kunci → dua panggilan membuat dua payment;
2. kunci sama + request sama → payment kedua `Replayed==true`, **ID sama**, `sandbox.createCalls == 1`;
3. kunci sama + amount beda → `errors.Is(err, idempotency.ErrKeyReused)`, `createCalls == 1`;
4. klaim tanpa `PaymentID` (di-seed di fake, `CreatedAt` baru) → `ErrInProgress`, provider tidak dipanggil;
5. klaim tanpa `PaymentID` lebih tua dari `AbandonedAfter` → diambil alih, payment dibuat;
6. provider gagal (`fakeProvider.createErr`) → error dikembalikan dan klaim **dilepas** (panggilan kedua dengan provider sehat sukses dan `createCalls==2`);
7. kunci tidak valid → `ErrKeyInvalid`, repo tidak disentuh (`claimCalls==0`);
8. dua goroutine paralel kunci sama (`sync.WaitGroup`, `-race`) → tepat satu payment tersimpan, `createCalls==1`, yang lain `ErrInProgress` atau replay;
9. `Complete` gagal → payment tetap dikembalikan (tidak error);
10. kunci sama beda merchant / beda environment → tidak bentrok;
11. `PurgeIdempotencyKeys` memanggil `PurgeOlderThan` dengan cutoff ≈ now−24h.

- [ ] **Step 8: Handler.** File baru `internal/handler/idempotency_errors.go`:

```go
package handler

import (
	"errors"
	"net/http"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/idempotency"
)

const idempotencyKeyHeader = "Idempotency-Key"

// respondIdempotencyError maps idempotency errors to their HTTP status and
// reports whether it handled err.
func respondIdempotencyError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, idempotency.ErrKeyInvalid):
		respondError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, idempotency.ErrKeyReused):
		respondError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, idempotency.ErrInProgress):
		respondError(w, http.StatusConflict, err.Error())
	default:
		return false
	}
	return true
}
```

`PaymentHandler.CreatePayment` (setelah `req.Validate()`), ganti pembuatan + respons:

```go
	res, err := h.paymentService.CreatePaymentIdempotent(r.Context(), &req, r.Header.Get(idempotencyKeyHeader))
	if err != nil {
		if respondIdempotencyError(w, err) {
			return
		}
		logger.ErrorfCtx(r.Context(), "Failed to create payment: %v", err)
		respondCreatePaymentError(w, err)
		return
	}
	if res.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	respondJSON(w, http.StatusCreated, payment.ToPaymentResponse(res.Payment, h.frontendURL))
```

`merchantHandler.CreatePayment`: sama (pertahankan `respondInvalidCallbackURL` dan `respondCreatePaymentError` hasil Task 3/5). Tes handler (`payment_handler_test.go`): `newTestPaymentService()` di paket handler perlu `.WithIdempotency(fakeIdempotencyRepo)` — tambahkan fake serupa di `fakes_test.go` handler (atau varian khusus tes ini): (1) dua request header sama → keduanya 201, `reference` sama, kedua `Idempotent-Replayed` ada hanya pada yang kedua; (2) body beda → 422; (3) header berisi spasi/lebih dari 128 → 400; (4) tanpa header → dua payment berbeda.

- [ ] **Step 9: Wiring** `cmd/api/main.go`: `idempotencyRepo := repository.NewIdempotencyRepository(db)`; sambungkan `paymentService.WithIdempotency(idempotencyRepo)` pada rantai pembuatan service; tambahkan `startJob(time.Hour, "purge-idempotency-keys", paymentService.PurgeIdempotencyKeys)`; CORS: `AllowedHeaders` tambah `"Idempotency-Key"`, dan tambahkan `ExposedHeaders: []string{"Idempotent-Replayed"}`.
- [ ] **Step 10: Verifikasi** suite penuh + tes PG di PG18 sementara. Mutasi: (a) hapus `Release` saat error → tes 6 gagal; (b) abaikan perbandingan hash → tes 3 gagal; (c) ganti klaim-sebelum-provider dengan klaim-sesudah → tes 8 gagal. **Report** (tanpa commit).

---

### Task 7: Rate limit `api` dan password untuk regenerate webhook secret

**Files:** Modify `cmd/api/main.go`, `cmd/api/router_test.go`, `internal/handler/merchant_handler.go`, `internal/handler/merchant_handler_test.go`.

**Interfaces:** Consumes `cfg.RateLimit.API` (Task 1). `setupRouter` mendapat satu parameter tambahan `apiRateLimiter *middleware.IPRateLimiter` **setelah** `sensitiveRateLimiter`.

- [ ] **Step 1: Tes gagal** — `merchant_handler_test.go`: `RegenerateWebhookSecret` (pola fake yang sama dengan tes handler merchant lain; lihat `fakes_test.go` untuk fake auth/merchant): (a) tanpa body/password kosong → 400 "Password is required to regenerate webhook secret"; (b) password salah → 401 "Invalid password" dan secret **tidak** berubah; (c) password benar → 200 dan secret berubah. `router_test.go`: tes bahwa melebihi `api` limiter pada `/api/v1/merchant/dashboard/summary` menghasilkan 429 sebelum auth dijalankan, dan `POST /api/v1/payments` (API key) memakai `apiRateLimiter` terpisah dari `publicRateLimiter` (poll `GET /payments/{id}/status` tidak menghabiskan budget create). Sesuaikan pemanggilan `setupRouter` yang sudah ada di `router_test.go` dengan parameter baru.

- [ ] **Step 2: Implementasi handler** (`merchant_handler.go`), pola sama dengan `UpsertAPIKey`:

```go
func (h *MerchantHandler) RegenerateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	userID, ok := middleware.MerchantUserIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		respondError(w, http.StatusBadRequest, "Password is required to regenerate webhook secret")
		return
	}
	// Regenerating invalidates the old secret at once, so every callback we send
	// stops verifying on the merchant's side: re-authenticate like API-key rotation.
	if err := h.authService.VerifyMerchantPassword(r.Context(), userID, req.Password); err != nil {
		respondError(w, http.StatusUnauthorized, "Invalid password")
		return
	}
	secret, err := h.paymentService.RegenerateMerchantWebhookSecret(r.Context(), merchantID)
	if err != nil {
		logger.ErrorfCtx(r.Context(), "Failed to regenerate webhook secret for merchant %s: %v", merchantID, err)
		respondError(w, http.StatusInternalServerError, "Failed to regenerate webhook secret")
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"webhook_secret": secret})
}
```

(`GetWebhookSecret` tidak berubah.)

- [ ] **Step 3: Router/main.** Di `main.go`: `apiRateLimiter := newRateLimiter(cfg.RateLimit.API, trustedProxies)`; tambahkan `apiRateLimiter.Cleanup(30 * time.Minute)` pada job `rate-limiter-cleanup`; teruskan ke `setupRouter`. Di `setupRouter`: sebelum `adminAPI.Use(authMiddleware.RequireAdmin)` tambahkan `adminAPI.Use(apiRateLimiter.Limit)` dan sebelum `merchantDash.Use(authMiddleware.RequireMerchant)` tambahkan `merchantDash.Use(apiRateLimiter.Limit)` (urutan `Use` = urutan eksekusi: limiter lebih dulu, agar token palsu pun tidak membebani database). Ganti rute pembuatan payment API key menjadi:

```go
	merchantAPI.Handle("/payments", apiRateLimiter.Limit(http.HandlerFunc(paymentHandler.CreatePayment))).Methods("POST")
```

(rute GET payment/status tetap hanya di bawah `publicRateLimiter`). Perbarui komentar di dekat pembuatan limiter menjelaskan `api`.

- [ ] **Step 4: Verifikasi** suite penuh + mutasi (hapus cek password → tes (b) gagal). **Report** (tanpa commit).

---

### Task 8: Front-end — logout, 401, kolom password regenerate

**Files:** Modify `front/lib/merchant-auth.ts`, `front/lib/admin-auth.ts`, `front/app/dashboard/settings/page.tsx` (+ pemanggil logout yang ditemukan lewat `grep -rn "logout\|clearAdminToken\|clearMerchantToken" front/app front/lib front/components`).

- [ ] **Step 1: Baca** `front/AGENTS.md` dan dokumen Next relevan di `front/node_modules/next/dist/docs/` (catat apa yang dibaca). Baca `merchant-auth.ts`, `admin-auth.ts` (fungsi simpan/hapus token, `API_URL`, helper fetch bertoken) dan `settings/page.tsx` (`handleRegenerate`, ~baris 72).
- [ ] **Step 2: Logout ke server (best-effort).** Di kedua modul auth tambahkan fungsi (sesuaikan nama helper token yang sudah ada):

```ts
// Tell the server to revoke this session, then forget the token locally. The
// local clear always happens: a failed call must never leave the user signed in.
export async function logoutMerchant(): Promise<void> {
  const token = getMerchantToken();
  try {
    if (token) {
      await fetch(`${API_URL}/api/v1/auth/logout`, {
        method: "POST",
        headers: { Authorization: `Bearer ${token}` },
      });
    }
  } catch {
    // ignore: the token is removed below regardless
  } finally {
    clearMerchantToken();
  }
}
```

dan padanan `logoutAdmin` ke `/api/v1/auth/admin/logout`. Ganti pemanggil tombol Logout yang sekarang hanya menghapus token agar memanggil fungsi baru lalu mengarahkan ke login (pertahankan perilaku redirect yang ada).
- [ ] **Step 3: Penanganan 401.** Periksa bagaimana respons 401 ditangani pada helper fetch dashboard/admin. Bila 401 belum membersihkan token dan mengarahkan ke login, tambahkan itu pada satu titik helper (jangan di tiap pemanggil). Ini penting karena ganti password sekarang mencabut token sesi aktif. Setelah sukses ganti password di halaman profil (`handlePassword`, `front/app/dashboard/profile/page.tsx:73`, dan padanan admin), arahkan ke login dengan toast "Password changed, please sign in again" bila belum begitu.
- [ ] **Step 4: Regenerate dengan password.** Di `settings/page.tsx`: tambahkan state `regeneratePassword` (string), kolom `<input type="password">` pada UI konfirmasi regenerate (gaya input yang sudah ada di file/halaman API keys yang sudah meminta password — ikuti pola itu), kirim `{ password }` pada `POST /merchant/webhook-secret/regenerate`, tombol dinonaktifkan saat password kosong, reset state setelah selesai/batal. Tampilkan pesan backend ("Invalid password") lewat `toast` yang sudah ada. Perbarui fungsi API di `front/lib` yang memanggil endpoint ini agar menerima password.
- [ ] **Step 5: Verifikasi** `npm run lint` dan `npm run build` dari `front/` bersih. Katakan jujur bila dev server tidak dijalankan. **Report** (tanpa commit).

---

### Task 9: Dokumentasi

**Files:** Modify `docs/openapi.yaml`, `README.md`, `DEPLOY.md`.

- [ ] **Step 1: openapi.yaml** — `POST /payments`: parameter header `Idempotency-Key` (opsional, 1–128 ASCII tercetak, berlaku 24 jam, per merchant+environment); respons 201 dengan header `Idempotent-Replayed` pada replay; 409 (sedang diproses), 422 (kunci dipakai request lain), 403 (merchant nonaktif), 413 pada webhook; `POST /auth/logout` dan `POST /auth/admin/logout`; `POST /merchant/webhook-secret/regenerate` dengan body `{password}` dan 400/401.
- [ ] **Step 2: README.md** — bagian API: contoh header `Idempotency-Key`, catatan "gunakan UUID baru per niat bayar, ulangi kunci yang sama saat retry"; catatan logout/pencabutan token; section `[rate_limit.api]` di contoh config.
- [ ] **Step 3: DEPLOY.md** — bagian baru "4.3 Cek sebelum deploy Paket 3" (ikuti gaya 4.2): (1) tambah `[rate_limit.api]` ke `.config.toml` di server, tanpa itu API gagal start (salin dari `.config.example.toml`); (2) `app.environment` harus persis `production`, `security.jwt_secret` ≥ 32 karakter, `app.url`/`app.frontend_url` berawalan `https://` — kalau tidak, server menolak start; (3) `make migrate` menjalankan migration 017 dan 018 sebelum image baru dijalankan (aditif, idempoten); (4) efek pada pengguna: ganti password dan logout mencabut token sesi; merchant yang dinonaktifkan langsung ditolak; (5) merchant yang memakai `Idempotency-Key` perlu tahu semantik 409/422.
- [ ] **Step 4: Verifikasi** — `grep` memastikan setiap endpoint/kode status yang ditulis ada di kode. **Report** (tanpa commit).

---

## Self-Review (spec → task)

| Spec | Task |
|---|---|
| 3.1 Idempotensi (tabel, klaim sebelum provider, 24 jam, 2 menit, purge, CORS) | 6 |
| 3.2 Token: kolom, middleware, ganti password, logout, front-end, merchant nonaktif di `CreatePayment` | 4, 5, 8 |
| 3.3 Body 1 MiB, header timeout, export deadline | 2 |
| 3.4 Error 500 bersih + audit 400 | 3 |
| 3.5 Validasi config + `[rate_limit.api]` | 1, 7 |
| 3.6 Password regenerate | 7, 8 |
| 5 Migrasi & DEPLOY, 6 Pengujian | 4, 6, 9 |

**Deviasi dari spec (disengaja, dilaporkan ke user):** pengecekan akun merchant memakai dua query primary-key (user lalu merchant lewat repo yang sudah ada) alih-alih satu join, agar memakai fake dan antarmuka yang ada; perilaku identik. Pengecekan `ensureMerchantActive` melewati merchant yang tidak ditemukan (ditolak oleh FK) dan merchantRepo nil (wiring tes).

**Konsistensi nama:** `AuthorizeAdmin/AuthorizeMerchant`, `LogoutAdmin/LogoutMerchant`, `RevokeSessions`, `SessionsValidAfter`, `sessionRevoked`; `CreatePaymentIdempotent`, `IdempotentPayment`, `WithIdempotency`, `PurgeIdempotencyKeys`, `idempotency.{TTL,AbandonedAfter,ErrKeyInvalid,ErrKeyReused,ErrInProgress,RequestFields,RequestHash,ValidateKey}`; `LimitBody`, `extendWriteDeadline`; `RateLimitConfig.API`, `apiRateLimiter`.
