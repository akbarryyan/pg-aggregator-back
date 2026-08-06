# Payment Gateway Aggregator — Backend

Backend service untuk platform pembayaran QRIS menggunakan Go. Bagian dari
workspace [WhuzPay](../README.md); lihat README root untuk gambaran
keseluruhan sistem (backend + frontend).

## Tech Stack

- **Language**: Go 1.25
- **Router**: Gorilla Mux
- **Database**: PostgreSQL (driver `lib/pq`, tanpa ORM — query SQL manual)
- **Auth**: JWT (`golang-jwt/jwt/v5`), password hashing via `golang.org/x/crypto`
- **Cache/Queue**: Redis (config sudah ada, belum dipakai — background job
  masih single-instance in-process ticker)
- **Provider**: Cashi (real, production QRIS) + sandbox mock (in-process, tanpa network)

## Project Structure

```
back/
├── cmd/api/              # Entry point (main.go): wiring DI manual + router setup
├── internal/
│   ├── config/           # Load & validasi .env
│   ├── domain/            # Model + error domain, tanpa dependency ke layer lain
│   │   ├── payment/        # Payment, status, error
│   │   ├── paymentlink/    # Payment Link (reusable checkout link)
│   │   ├── merchant/       # Merchant, dashboard user, API key, callback config
│   │   ├── admin/          # Admin user
│   │   └── provider/       # Tipe request/response provider-agnostic
│   ├── handler/           # HTTP layer: decode request → panggil service → encode response
│   ├── service/           # Business logic (PaymentService, AuthService, dst)
│   ├── repository/        # Akses data (query SQL manual)
│   ├── provider/           # Adapter pattern provider eksternal
│   │   ├── cashi/           # Adapter Cashi (HTTP real)
│   │   └── sandbox/         # Adapter mock in-memory
│   ├── middleware/         # Auth (admin JWT, merchant JWT, API key), rate limiting
│   └── scheduler/          # Background job periodik in-process
├── migrations/            # SQL migration bernomor urut, dijalankan manual
├── seeds/                 # SQL seed data
├── bruno/                 # Koleksi Bruno untuk testing manual API (referensi kontrak)
└── pkg/logger/             # Logger sederhana (wrapper log.Logger standar)
```

## Setup

1. Copy environment file:
```bash
cp .env.example .env
```

2. Update `.env` dengan konfigurasi kamu (lihat tabel di bawah)

3. Install dependencies:
```bash
go mod download
```

4. Jalankan migration:
```bash
make migrate
```

5. Seed initial data:
```bash
make seed
```

6. Jalankan server:
```bash
make run
# atau hot reload (butuh air terpasang):
make dev
```

Helper database opsional:
```bash
make db-create
```

### Environment Variables Penting

| Var | Keterangan |
|---|---|
| `APP_URL`, `FRONTEND_URL` | Wajib diisi — `config.Validate()` gagal start kalau kosong |
| `JWT_SECRET` | Wajib diganti di production (`APP_ENV=production` menolak nilai default) |
| `CASHI_API_KEY` / `CASHI_SECRET_KEY` | Wajib di production; sandbox merchant tidak butuh ini (mock, tanpa HTTP call) |
| `DB_*` | Koneksi PostgreSQL |

## Arsitektur & Alur Request

> Transisi status payment (`pending → paid/expired/failed/cancelled`) dan
> aturannya didokumentasikan terpisah di
> [docs/payment-state-machine.md](docs/payment-state-machine.md).

Layering khas Go clean architecture, dependency injection **manual** di
`cmd/api/main.go` (tidak ada DI container):

```
handler → service → repository → PostgreSQL
              │
              └→ provider.ProviderRouter → (cashi | sandbox) adapter
```

Contoh alur **create payment**:
1. `PaymentHandler.CreatePayment` decode request, inject `merchant_id` /
   `environment` dari context auth (kalau via API key).
2. `PaymentService.CreatePayment` — kalau environment sandbox, panggil
   sandbox adapter langsung; kalau production, `ProviderRouter` memilih
   kandidat provider (dengan failover) — sandbox **selalu dikecualikan**
   dari trafik production.
3. Simpan hasil ke DB, kembalikan response.

Alur **webhook**: provider POST ke `/api/v1/provider-webhooks/{providerName}`
dengan header `x-gateway-signature` → adapter validasi signature →
`PaymentService.ProcessWebhook` update status & trigger callback merchant.

## Authentication

Tiga jalur auth yang independen (lihat `internal/middleware/`):

| Jalur | Middleware | Dipakai untuk |
|---|---|---|
| Admin JWT | `AuthMiddleware.RequireAdmin` | Panel admin (`/api/v1/admin/*`) |
| Merchant JWT | `AuthMiddleware.RequireMerchant` | Dashboard merchant (`/api/v1/merchant/*`) |
| API Key | `MerchantAPIAuthMiddleware.RequireMerchantAPIKey` | Integrasi programatik merchant (`Authorization: Bearer <key>` atau `X-API-Key`) |

Token JWT admin dan merchant memakai context key & claim yang berbeda —
keduanya tidak bisa dipakai bertukar. Endpoint publik (checkout,
payment-link publik, webhook provider) tidak memakai JWT/API key sama
sekali, tapi tetap melewati rate limiter per-IP.

Tiga tingkat rate limiter per-IP (`internal/middleware/rate_limit.go`):

| Limiter | Rate | Dipakai untuk |
|---|---|---|
| `authRateLimiter` | 5 req/min | Login (admin & merchant) — target klasik brute-force |
| `sensitiveRateLimiter` | 10 req/min | Aksi mutasi akun setelah lolos auth: change-password, create/rotate/revoke API key, regenerate webhook secret — sebelumnya endpoint ini tidak dibatasi sama sekali setelah token JWT tervalidasi |
| `publicRateLimiter` | 120 req/min | Endpoint publik (checkout, payment-link, webhook provider, merchant API key) — trafik legit frekuensi tinggi, cuma untuk meredam flood |

## Routes (ringkas)

> Kontrak API lengkap (semua 61 route, request/response schema, auth per
> endpoint) ada di [docs/openapi.yaml](docs/openapi.yaml) — OpenAPI 3.0,
> bisa dibuka di [Swagger Editor](https://editor.swagger.io) atau tool
> serupa. Dibuat dari route table `main.go` + domain struct sesungguhnya,
> menggantikan koleksi Bruno manual sebagai referensi kontrak.

Semua di bawah prefix `/api/v1`:

- `POST /auth/login`, `/auth/register`, `/auth/admin/login` — rate limited (5 req/min)
- `GET|PUT /auth/me`, `/auth/profile`, `/auth/change-password` (+ varian `/admin/*`) — `change-password` rate limited (10 req/min)
- `/admin/*` — protected admin panel API (dashboard, payments, merchants, providers, routing, reconciliation, callbacks, logs)
- `/merchant/*` — protected merchant dashboard API (dashboard, payments, api-keys, payment-links, webhook secret)
- `GET /public/payments/by-reference/{reference}` — publik, untuk halaman checkout
- `GET /public/payment-links/{slug}`, `POST /public/payment-links/{slug}/pay` — publik
- `POST /payments`, `GET /payments/{id}`, `GET /payments/{id}/status` — merchant API key
- `POST /provider-webhooks/{providerName}` — webhook provider, rate limited per-IP

## Logging

`pkg/logger` emit satu JSON object per baris (`time`, `level`, `msg`,
`request_id` opsional) ke stdout (info/warn/debug) / stderr (error) —
langsung bisa di-ingest log aggregator tanpa parsing. Dipanggil sebagai
`logger.Infof(...)` dsb setelah `logger.Init(...)` di `main.go`.

Setiap request dapat correlation ID lewat
`middleware.RequestID` (dipasang membungkus seluruh router di
`setupRouter`, bukan lewat `mux.Router.Use()` — gorilla/mux tidak
menjalankan `Use()` untuk route yang tidak match/404). ID diambil dari
header `X-Request-ID` kalau caller sudah mengirim satu, atau digenerate;
selalu di-echo balik di response header yang sama.

Untuk korelasi log per-request, panggil varian `*Ctx` (`logger.InfofCtx(ctx,
...)`, `logger.ErrorfCtx(ctx, ...)`, dst) dengan `r.Context()` — otomatis
menyertakan `request_id` di baris log. Fungsi non-Ctx (`logger.Infof`, dst)
tetap ada dan berfungsi sama seperti sebelumnya (tanpa `request_id`) untuk
compatibility dengan ~100 call site lama yang belum dimigrasi; saat ini
sudah dimigrasi ke `*Ctx`: `payment_handler.go`, `webhook_handler.go`,
`auth_handler.go` — handler lain masih pakai varian plain.

## Testing

```bash
go test ./...          # semua test, hermetic — tidak butuh Postgres nyata
go test ./... -race    # + race detector (dipakai di CI)
```

CI (`.github/workflows/ci.yml`) menjalankan `go vet`, `go test -race`, dan
`go build` di tiap push/PR ke `main` — tanpa service Postgres, karena semua
test di bawah ini hermetic.

Tiga gaya test dipakai tergantung layer:

- **`domain/*`, `service`, `provider`, `middleware`, `scheduler`** — `go
  test` standar + fakes manual in-memory (lihat
  `service/payment_fakes_test.go`), bukan mock library.
- **`repository`** — [go-sqlmock](https://github.com/DATA-DOG/go-sqlmock)
  memvalidasi SQL & scanning nyata (termasuk urutan `$N` pada query
  filter dinamis) tanpa Postgres sungguhan. Lihat
  `repository/payment_repository_test.go` untuk polanya.
- **`handler`** — `httptest` + service nyata yang di-wire ke fakes (bukan
  mock request/response), mencakup jalur create-payment, webhook, dan
  auth (admin & merchant).
- **`cmd/api`** (`router_test.go`) — integration test lewat `setupRouter`
  yang sesungguhnya dari `main.go`: create payment (API key) → webhook →
  status paid, end-to-end lewat HTTP request/response nyata. Ini jalur
  bisnis paling kritis di sistem, lihat
  [docs/payment-state-machine.md](docs/payment-state-machine.md) untuk
  aturan transisi status yang diujinya.

## Background Jobs

Dijalankan sebagai goroutine ticker in-process di `main.go`
(`internal/scheduler`), bukan Redis/Asynq — cukup untuk single-instance:

- `expire-payments` (tiap 1 menit) — expire payment yang lewat deadline
- `retry-merchant-callbacks` (tiap 1 menit) — retry callback merchant yang gagal
- `rate-limiter-cleanup` (tiap 10 menit) — bersihkan bucket rate limit idle

## Architecture Principles

- Payment orchestration backend (bukan wrapper Cashi)
- Provider-agnostic core system
- Cashi hanyalah satu provider adapter
- Semua logika spesifik provider diisolasi di adapter
- Status payment dinormalisasi secara internal

## Area Sensitif

Lihat [README root](../README.md) untuk daftar area berisiko tinggi
(provider routing/sandbox exclusion, validasi webhook signature, context
key middleware).
