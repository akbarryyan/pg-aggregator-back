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
├── cmd/configenv/        # Cetak nilai config sebagai `export` untuk script shell
├── internal/
│   ├── config/           # Load & validasi .config.toml
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
├── Dockerfile            # image: build / dev (hot reload) / runtime (GHCR)
├── docker-compose.yaml   # stack produksi (caddy + api + postgres); lokal + override
├── Caddyfile             # HTTPS api.whuzpay.web.id
├── DEPLOY.md             # panduan deploy VPS
├── migrations/            # SQL migration bernomor urut, dijalankan manual
├── seeds/                 # SQL seed data
├── bruno/                 # Koleksi Bruno untuk testing manual API (referensi kontrak)
└── pkg/logger/             # Logger sederhana (wrapper log.Logger standar)
```

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

### Config Penting (`.config.toml`)

| Key | Keterangan |
|---|---|
| `app.url`, `app.frontend_url` | Wajib diisi — `config.Validate()` gagal start kalau kosong |
| `app.environment` | Persis `development` atau `production`; nilai lain ditolak |
| `security.jwt_secret` | Tidak boleh kosong. Di production (`app.environment = "production"`): minimal 32 karakter dan bukan placeholder `change-this-secret` |
| (production) `app.url`, `app.frontend_url` | Di production wajib berawalan `https://` — kalau tidak, server menolak start |
| `cashi.api_key` / `cashi.secret_key` | Wajib di production; sandbox merchant tidak butuh ini (mock, tanpa HTTP call) |
| `[db]` | Koneksi PostgreSQL (`host = "postgres"`); `password` sama dengan isi `db_password`; script migrate/seed membacanya lewat `configenv` |
| `[rate_limit]` | Batas request per IP (`auth`, `public`, `sensitive`, `api` — keempat sub-section wajib ada) dan `trusted_proxies` — CIDR proxy yang `X-Forwarded-For`-nya dipercaya (`[]` lokal, `["10.89.0.2/32"]` di VPS) |

Error config (key hilang, key tidak dikenal, sintaks) hanya menyebut nama
key dan nomor baris, tidak pernah nilainya, karena file ini berisi secret.

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

Empat tingkat rate limiter per-IP (`internal/middleware/rate_limit.go`):

Nilai default di bawah diatur di `[rate_limit.auth|public|sensitive|api]` pada `.config.toml` (section `[rate_limit.api]` wajib ada; tanpanya API gagal start); di belakang Caddy, IP klien diambil dari `X-Forwarded-For` hanya jika koneksi datang dari `trusted_proxies`.

| Limiter | Rate | Dipakai untuk |
|---|---|---|
| `authRateLimiter` | 5 req/min | Login (admin & merchant) — target klasik brute-force |
| `sensitiveRateLimiter` | 10 req/min | Aksi mutasi akun setelah lolos auth: change-password, create/rotate/revoke API key, regenerate webhook secret — sebelumnya endpoint ini tidak dibatasi sama sekali setelah token JWT tervalidasi |
| `publicRateLimiter` | 120 req/min | Endpoint publik (checkout, payment-link, webhook provider, `GET /payments/{id}` dan `/status` dengan API key) — trafik legit frekuensi tinggi, cuma untuk meredam flood |
| `apiRateLimiter` | 120 req/min (burst 60) | Route `/admin/*` dan `/merchant/*` (JWT) serta `POST /payments` (API key) — dipasang sebelum pengecekan auth yang menyentuh DB. Bucket sendiri, terpisah dari `publicRateLimiter`, supaya polling status tidak menghabiskan jatah create |

## Routes (ringkas)

> Kontrak API lengkap (semua 78 route, request/response schema, auth per
> endpoint) ada di [docs/openapi.yaml](docs/openapi.yaml) — OpenAPI 3.0,
> bisa dibuka di [Swagger Editor](https://editor.swagger.io) atau tool
> serupa. Dibuat dari route table `main.go` + domain struct sesungguhnya,
> menggantikan koleksi Bruno manual sebagai referensi kontrak.

Semua di bawah prefix `/api/v1`:

- `POST /auth/login`, `/auth/register`, `/auth/admin/login` — rate limited (5 req/min)
- `GET|PUT /auth/me`, `/auth/profile`, `/auth/change-password` (+ varian `/admin/*`) — `change-password` rate limited (10 req/min)
- `POST /auth/logout`, `/auth/admin/logout` — mencabut semua token sesi akun itu (lihat bawah)
- `/admin/*` — protected admin panel API (dashboard, payments, merchants, providers, routing, reconciliation, callbacks, logs)
- `/merchant/*` — protected merchant dashboard API (dashboard, payments, api-keys, payment-links, webhook secret)
- `GET /public/payments/by-reference/{reference}` — publik, untuk halaman checkout
- `GET /public/payment-links/{slug}`, `POST /public/payment-links/{slug}/pay` — publik
- `POST /payments`, `GET /payments/{id}`, `GET /payments/{id}/status` — merchant API key
- `POST /provider-webhooks/{providerName}` — webhook provider, rate limited per-IP
- `GET /health` — readiness: `200 {"status":"ok"}` bila database menjawab ping (batas 2 detik), `503 {"status":"unavailable"}` bila tidak

### Idempotency-Key (`POST /payments`)

`POST /payments` (API key) dan pembuatan payment dari dashboard
(`POST /merchant/payments`) menerima header opsional `Idempotency-Key`: 1-128
karakter ASCII tercetak (0x21-0x7E), berlaku per merchant + environment selama
24 jam. Pembuatan payment via payment link dan via admin tidak memakainya.

```bash
curl -X POST http://localhost:8090/api/v1/payments \
  -H "X-API-Key: $API_KEY" -H "Content-Type: application/json" \
  -H "Idempotency-Key: 0b0e7a53-6f0c-4d4a-9a55-2f5d2a1c9e11" \
  -d '{"amount": 10000, "currency": "IDR", "payment_method": "qris", "description": "INV-001"}'
```

Gunakan UUID baru per niat bayar, dan ulangi kunci yang sama saat retry
(timeout, koneksi putus). Hasilnya:

| Situasi | Respons |
|---|---|
| Kunci baru | `201`, payment dibuat |
| Kunci sama, request sama | `201` dengan header `Idempotent-Replayed: true`; isinya kondisi payment **saat ini** (bisa sudah `paid`), bukan salinan beku |
| Kunci sama, request pertama masih diproses | `409` — coba lagi sebentar lagi (klaim tanpa payment dianggap terbengkalai setelah 2 menit) |
| Kunci sama, tapi isi request berbeda | `422` |
| Kunci tidak valid (kosong, >128, bukan ASCII tercetak) | `400` |

Klaim kunci dicatat di tabel `idempotency_keys` sebelum provider dipanggil dan
dihapus job `purge-idempotency-keys` (tiap 1 jam) setelah lewat 24 jam. Merchant
yang dinonaktifkan mendapat `403` saat membuat payment.

### Sesi, logout, dan batas ukuran body

- Token JWT dicabut di sisi server: `POST /auth/logout` dan
  `POST /auth/admin/logout` mencabut **semua** token milik akun itu (bukan
  hanya token yang dikirim), dan **ganti password** juga mencabut semua token
  yang ada. Token yang dicabut, atau milik akun yang nonaktif, ditolak `401`
  `Invalid or expired token` (kolom `sessions_valid_after`, migration `018`).
- `POST /merchant/webhook-secret/regenerate` kini wajib body `{"password": "..."}`
  (`400` bila kosong, `401` bila salah); membaca secret
  (`GET /merchant/webhook-secret`) tidak memerlukan password.
- Body request dibatasi 1 MiB. Webhook provider menjawab `413` bila melebihi;
  endpoint JSON lain menjawab `400 Invalid request body`.

### Health check dan integritas data

`api -healthcheck` memanggil `GET /api/v1/health` di `127.0.0.1` pada port
`app.port`, tidak mencetak apa pun, dan keluar dengan kode 0 (sehat) atau 1;
ini dipakai healthcheck Docker di `docker-compose.yaml`. Tabel `payments`
dilindungi constraint database (migration 019): `amount > 0`, status hanya
`pending/paid/expired/failed/cancelled`, `(provider_name, provider_reference)`
unik, dan merchant yang masih punya payment tidak bisa dihapus. Detail
deploy ada di [DEPLOY.md](DEPLOY.md) bagian 4.4.

## Biaya provider (customer-borne)

Siapa yang menanggung fee persentase provider (Cashi 0,7%) adalah pengaturan
**per merchant**: `merchants.fee_bearer`, default `merchant` (perilaku lama:
customer membayar nominal, fee dipotong dari penerimaan merchant). Bila
`customer`, fee ditambahkan di atas nominal pada payment **baru**.

- Total `G` adalah bilangan bulat terkecil dengan `G - ceil(G * 0,007) >= nominal`,
  dihitung dengan aritmetika integer di `internal/fee`. Contoh: nominal
  Rp 2.000 -> customer membayar Rp 2.015, fee Rp 15.
- Field di respons payment, status, lookup publik, payload callback merchant,
  dan dua kolom terakhir export CSV: `amount` = nominal merchant (tidak
  berubah), `fee_amount` = fee yang ditambahkan (0 bila merchant yang
  menanggung), `total_amount` = `amount + fee_amount` (yang dibayar customer).
- Sandbox memakai aturan dan angka yang sama (tidak ada lagi akhiran acak 1-99).
- Hanya admin yang bisa mengubahnya: `PUT /admin/merchants/{id}/fee-bearer`
  (`{"fee_bearer": "merchant" | "customer"}`). Merchant tidak bisa mengubahnya
  sendiri, dan perubahan hanya memengaruhi payment baru.

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
test di bawah ini hermetic (kecuali test PostgreSQL opt-in, yang di-skip tanpa `TEST_DATABASE_URL`).

Beberapa gaya test dipakai tergantung layer:

- **`domain/*`, `service`, `provider`, `middleware`, `scheduler`** — `go
  test` standar + fakes manual in-memory (lihat
  `service/payment_fakes_test.go`), bukan mock library.
- **`repository`** — [go-sqlmock](https://github.com/DATA-DOG/go-sqlmock)
  memvalidasi SQL & scanning nyata (termasuk urutan `$N` pada query
  filter dinamis) tanpa Postgres sungguhan. Lihat
  `repository/payment_repository_test.go` untuk polanya.
- **`repository` (PostgreSQL nyata, opt-in)** —
  `repository/payment_repository_pg_test.go` membuktikan locking/`RETURNING`
  `UpdateStatus` dan keyset reconcile yang tidak bisa dibuktikan sqlmock.
  Hanya jalan bila `TEST_DATABASE_URL` di-set (selain itu di-skip): isi
  dengan DSN berformat URL ke database sekali-pakai yang usernya boleh
  `CREATE SCHEMA`. Tiap test membuat schema unik, menerapkan
  `migrations/*.sql`, lalu men-drop schema-nya. Contoh (kredensial hanya
  placeholder):

  ```bash
  TEST_DATABASE_URL='postgres://user:pass@localhost:5432/whuzpay_test?sslmode=disable' \
    go test ./internal/repository/ -run PG -v
  ```
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

- `expire-payments` (tiap 1 menit) — expire payment `pending` yang lewat
  deadline lebih dari 5 menit (masa tenggang, supaya pembayaran di detik
  terakhir tidak dilaporkan `expired` lalu `paid`), dan mengantre callback
  `payment.expired` ke merchant
- `reconcile-payments` (tiap 2 menit) — cek ulang payment produksi
  `pending` yang dibuat ≤24 jam lalu dan `expired` yang deadline-nya lewat
  ≤6 jam lalu ke check-status provider (verifikasi nominal); jaring pengaman
  bila webhook hilang. Tiap run memeriksa ≤50 kandidat (terbaru dulu)
  dengan kursor `(created_at, id)` in-memory yang berputar, sehingga
  seluruh kandidat tercakup tiap ⌈N/50⌉ run; restart mulai lagi dari yang
  terbaru
- `deliver-merchant-callbacks` (tiap 5 detik) — kirim callback merchant yang
  antre dan jadwalkan ulang yang gagal
- `purge-idempotency-keys` (tiap 1 jam) — hapus klaim `Idempotency-Key` yang
  lebih tua dari 24 jam (tabel dibuat migration `017`)
- `rate-limiter-cleanup` (tiap 10 menit) — bersihkan bucket rate limit idle

Saat shutdown, server menunggu job yang sedang berjalan selesai (maks. 15
detik) sebelum keluar.

**Jaminan pengiriman callback merchant: at-least-once.** Callback bisa
diterima lebih dari sekali (proses mati di tengah kirim, hasil kirim gagal
dicatat, retry manual admin, atau delivery yang masuk jendela stale lalu
diklaim ulang); merchant harus men-deduplikasi berdasarkan (`payment_id`,
`event`). Callback hanya hilang bila pengantreannya sendiri gagal, dan itu
dicatat di log (selain gagal permanen yang tercatat: URL tidak valid/terblokir,
atau batas 5 percobaan). `webhook_url` merchant **selalu** `https` dengan host
publik; hanya `callback_url` sebuah payment yang boleh `http` biasa, dan hanya
di sandbox. Domain kita sendiri (`app.url` dan `app.frontend_url`, termasuk
subdomainnya) ditolak sebagai tujuan callback. Retry manual admin memakai URL
**terkini** (`callback_url` payment, lalu `webhook_url` merchant; URL tersimpan
bila keduanya kosong). Indeks antrean ada di migration `016`. Redirect (3xx) tidak diikuti dan dihitung gagal, jadi merchant harus
mendaftarkan URL final. Detail: [docs/payment-state-machine.md](docs/payment-state-machine.md#callback-merchant).

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
