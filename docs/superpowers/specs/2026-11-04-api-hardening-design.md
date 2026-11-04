# Pengerasan API (Paket 3) — Desain

Tanggal: 2026-11-04
Status: desain disetujui user per bagian; menunggu review spec tertulis
Sumber: audit 2026-11-04 (`back/.superpowers/audit/2026-11-04/{auth,authz-input,ops-data}.md`)
Lanjutan dari: `2026-10-30-payment-hardening-design.md` (Paket 1), `2026-11-03-callback-ssrf-async-design.md` (Paket 2)

## 1. Latar Belakang

Audit read-only atas autentikasi, otorisasi, dan operasional tidak menemukan celah Critical, tetapi menemukan delapan hal yang harus beres sebelum menerima uang sungguhan. Paket 3 menutup yang berupa perubahan kode dan config API:

1. Pembuatan payment tidak idempoten: retry setelah timeout membuat payment dan tagihan Cashi ganda.
2. Merchant yang dinonaktifkan tetap bisa bertransaksi hingga JWT (24 jam) habis; token tidak bisa dicabut (tidak ada logout, ganti password tidak membatalkan token).
3. Tidak ada batas ukuran body; webhook publik membaca seluruh body sebelum memeriksa signature.
4. Dua respons 500 membocorkan `err.Error()`.
5. Validasi config tipis (salah ketik `environment` melewati cek production; tidak ada batas panjang JWT secret).
6. Rute ber-JWT tidak punya rate limit.
7. Webhook secret bisa di-regenerate hanya dengan JWT.

Di luar kode (menunggu VPS baru, bukan bagian paket ini): password admin seed, jadwal backup, nilai `trusted_proxies`, monitoring.

## 2. Keputusan (disetujui user)

| # | Keputusan |
|---|---|
| H1 | Idempotensi lewat header `Idempotency-Key` (bukan `merchant_reference`). Opsional; tanpa header perilaku tidak berubah. |
| H2 | Token dicabut lewat kolom `sessions_valid_after` ditambah pengecekan akun tiap request; ganti password dan endpoint logout mengisinya. |
| H3 | Regenerate webhook secret wajib password merchant. Reveal (GET) tetap hanya JWT. |
| H4 | `sslmode=disable` tidak dilarang (stack compose memakai Postgres di jaringan internal). |

Bagian 3 (butir 3.3–3.5 di bawah) dipresentasikan sekali; yang dibahas dan diputuskan user hanya H3.

## 3. Perubahan

### 3.1 Idempotensi pembuatan payment (migration 017)

Tabel baru `idempotency_keys`: `id UUID PK`, `merchant_id UUID NOT NULL`, `environment VARCHAR NOT NULL`, `key VARCHAR(128) NOT NULL`, `request_hash CHAR(64) NOT NULL`, `payment_id UUID NULL`, `created_at TIMESTAMPTZ NOT NULL DEFAULT now()`, unik pada `(merchant_id, environment, key)`. Indeks pada `created_at` untuk pembersihan.

`request_hash` = SHA-256 dari field yang menentukan payment: amount, currency, payment_method, description, customer_name, customer_email, callback_url, expires_in_minutes, use_custom_merchant_name (urutan dan encoding tetap).

Berlaku pada handler pembuatan payment merchant (`PaymentHandler.CreatePayment`: jalur API key dan jalur JWT dashboard). Tidak berlaku pada checkout payment link publik dan pembuatan payment oleh admin.

Alur:
1. Tanpa header: tidak berubah.
2. Header: 1–128 karakter ASCII tercetak (0x21–0x7E), selain itu 400.
3. Klaim sebelum memanggil provider: `INSERT … ON CONFLICT DO NOTHING`. Baris yang lebih tua dari 24 jam diperlakukan tidak ada (dihapus lalu diklaim ulang dalam satu statement/transaksi). Baris tanpa `payment_id` yang lebih tua dari 2 menit dianggap ditinggalkan dan boleh diklaim ulang.
4. Pemenang membuat payment, lalu mengisi `payment_id`. Bila pembuatan gagal (validasi, error provider), klaim dihapus agar retry bisa berjalan.
5. Kalah klaim: hash sama dan `payment_id` terisi → kembalikan payment yang sama (status dan body asli) dengan header `Idempotent-Replayed: true`; hash berbeda → 422; `payment_id` kosong → 409 "sedang diproses".
6. Job terjadwal `purge-idempotency-keys` (tiap jam) menghapus baris lebih tua dari 24 jam.
7. CORS: `Idempotency-Key` ditambahkan ke `AllowedHeaders`; `Idempotent-Replayed` ke `ExposedHeaders`.

Batasan diketahui: bila proses mati setelah Cashi menagih tetapi sebelum `payment_id` tersimpan, tagihan yatim tetap mungkin. Risiko ini sudah ada sebelumnya.

### 3.2 Akun nonaktif dan pencabutan token (migration 018)

- `admins` dan `merchant_users` mendapat `sessions_valid_after TIMESTAMPTZ NULL` (NULL = tanpa pemutusan; tidak ada token gugur saat deploy).
- Pastikan JWT admin dan merchant memuat `iat` (claim terdaftar `IssuedAt`).
- `RequireMerchant`: setelah JWT sah, satu query join mengambil `is_active` user, `is_active` merchant, dan `sessions_valid_after`. `RequireAdmin`: satu query untuk admin. Ditolak bila akun/merchant nonaktif atau `iat` < `sessions_valid_after`. Respons 401 "Invalid or expired token" tanpa membedakan sebab; error database → 500 generik.
- `ChangePassword` dan `ChangeMerchantPassword` mengisi `sessions_valid_after = now()` dalam UPDATE yang sama dengan hash baru.
- Endpoint baru `POST /auth/logout` dan `POST /auth/admin/logout` (JWT wajib) mengisi kolom yang sama. Front-end memanggilnya saat Logout (best-effort; token lokal dihapus apa pun hasilnya) dan menangani 401 dengan kembali ke login.
- `PaymentService.CreatePayment` menolak merchant nonaktif dengan 403 generik; titik tunggal untuk jalur API key, dashboard, checkout payment link, dan admin. Payment yang sudah ada dan callback yang sudah terantri tidak disentuh.

Batasan: `iat` berpresisi detik, jadi token yang terbit dalam detik yang sama dengan ganti password/logout bisa lolos (jendela < 1 detik, diterima). Tambahan satu query primary-key per request dashboard.

### 3.3 HTTP

- Middleware global membungkus `r.Body` dengan `http.MaxBytesReader` 1 MiB. Webhook mengembalikan 413 eksplisit; handler JSON lain tetap 400 "Invalid request body".
- `http.Server`: `ReadHeaderTimeout: 5s`, `MaxHeaderBytes: 64 << 10`. `ReadTimeout`/`WriteTimeout` 15s tetap (`ReadTimeout` sudah membatasi pembacaan header).
- Handler export CSV (merchant dan admin) memperpanjang write deadline menjadi 60 detik lewat `http.ResponseController`.

### 3.4 Error response

- `merchant_handler.go:308` dan `admin_handler.go:171` (500 dengan `err.Error()`) diganti pesan generik; detail hanya di log (sudah ber-request-ID).
- Titik 400 yang meneruskan `err.Error()` ditinjau satu per satu: hanya error domain yang dikenal (via `errors.Is`/`errors.As`) yang diteruskan; error tak dikenal menjadi 500 generik. Daftar titik dan keputusan per titik ada di plan.

### 3.5 Config dan rate limit

- `app.environment` ∈ {`development`, `production`}, selain itu gagal start.
- Di production: `security.jwt_secret` minimal 32 karakter dan bukan placeholder; `app.url` dan frontend URL harus `https`.
- Section baru `[rate_limit.api]` (`requests_per_minute`, `burst`, mengikuti pola auth/public/sensitive), diterapkan pada grup rute merchant dan admin yang ber-JWT serta pembuatan payment lewat API key, dengan kunci per IP lewat mekanisme `trusted_proxies` yang ada. Loader config menolak key hilang, sehingga `.config.toml` yang sudah ada wajib ditambah section ini; `.config.example.toml`, README, dan DEPLOY.md diperbarui.

### 3.6 Webhook secret

`POST` regenerate menerima `{ "password": "…" }` dan memverifikasinya dengan `VerifyMerchantPassword` di bawah `sensitiveRateLimiter` (pola rotasi API key). Dashboard (`handleRegenerate`, halaman settings) menampilkan kolom password. Reveal (GET) tidak berubah.

## 4. Di Luar Cakupan

Paket 4 / menunggu VPS: constraint migration (`CHECK(amount>0)`, `UNIQUE(provider_name, provider_reference)`, `ON DELETE CASCADE`), `/health` dengan cek DB, security headers/HSTS di Caddy, healthcheck container, CSP, backup terjadwal, password admin seed, nilai `trusted_proxies` di server, monitoring. Temuan Low/Medium lain dari audit yang tidak disebut di atas (formula injection CSV, kolom `name` sebagai penentu environment API key, enumerasi akun, pemisahan secret JWT admin/merchant, sandbox key membaca payment production, kunci gabungan payment link) dicatat di laporan audit, tidak dikerjakan di paket ini.

## 5. Migrasi dan Rilis

Migration 017 (tabel idempotensi) dan 018 (`sessions_valid_after`), keduanya aditif dan idempoten (`IF NOT EXISTS`). `make migrate` sebelum image baru dijalankan. Config: section `[rate_limit.api]` wajib. DEPLOY.md mendapat bagian "Cek sebelum deploy Paket 3".

## 6. Pengujian

Tabel-driven dengan fake repository, ditambah test PostgreSQL opsional (`TEST_DATABASE_URL`) untuk klaim idempotensi (dua klaim paralel, kedaluwarsa 24 jam, klaim ditinggalkan) dan kueri auth-state. Skenario kunci: retry dengan kunci sama mengembalikan payment yang sama dan tidak memanggil provider kedua kali; hash beda → 422; klaim dihapus saat provider gagal; token sebelum `sessions_valid_after` ditolak; merchant nonaktif ditolak di semua jalur pembuatan payment; body > 1 MiB ditolak; config `environment` salah ketik ditolak. Pembuktian test membedakan lewat mutasi, seperti paket sebelumnya.

## 7. Risiko

Pengecekan akun per request menambah beban database kecil; bila kelak menjadi masalah, tambahkan cache TTL pendek (tidak sekarang). Section config baru yang hilang membuat server gagal start (disengaja, fail-fast), sehingga DEPLOY.md harus jelas.
