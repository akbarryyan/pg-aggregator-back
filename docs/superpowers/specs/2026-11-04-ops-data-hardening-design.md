# Pengerasan Data dan Operasional (Paket 4) — Desain

Tanggal: 2026-11-04
Status: desain disetujui user per bagian; menunggu review spec tertulis
Sumber: audit 2026-11-04 (`back/.superpowers/audit/2026-11-04/ops-data.md`, temuan M1, M2, M4, M5, M6, L4)
Lanjutan dari: `2026-11-04-api-hardening-design.md` (Paket 3)

## 1. Latar Belakang

Paket 3 menutup temuan kode API. Audit yang sama menyisakan kelompok data dan operasional:

1. Skema tidak menjaga integritas uang: tidak ada `CHECK` pada nominal/status, `provider_reference` tidak unik, dan menghapus merchant menghapus seluruh riwayat payment (`ON DELETE CASCADE`).
2. `/health` hanya mengembalikan `ok` statis dan tidak ada healthcheck container; Caddy tidak menunggu API siap.
3. Caddy tanpa security headers, tanpa batas body, dan tanpa access log (dan access log bawaan Caddy akan mencatat header `Authorization`/`X-Api-Key`).
4. Level log diabaikan, DB tanpa timeout statement, dan timeout klien Cashi (30 dtk) melebihi `WriteTimeout` (15 dtk).

Di luar paket ini (dibahas terpisah): **Paket 5 — fee Cashi ditanggung customer** (nominal yang dibayar customer = nominal merchant + fee, supaya merchant menerima nominal penuh). Itu mengubah arti `amount` dan aturan verifikasi nominal Paket 1, sehingga mendapat spec sendiri.

## 2. Keputusan (disetujui user)

| # | Keputusan |
|---|---|
| D1 | Cakupan: constraint database, health/proxy, kebersihan kode backend. CI `govulncheck` dan header front-end (CSP) tidak masuk. |
| D2 | Fee Cashi ditanggung customer dikerjakan sebagai Paket 5 setelah Paket 4. |
| D3 | Tidak ada key config baru (loader mewajibkan semua key; setiap key baru memaksa edit `.config.toml`). Nilai baru berupa konstanta bernama. |
| D4 | Data lama yang melanggar constraint baru tidak dibersihkan otomatis; DEPLOY.md memuat query pengecekan yang harus kosong sebelum migrate. |

## 3. Perubahan

### 3.1 Constraint database (migration 019)

Satu file, dibungkus `BEGIN … COMMIT` agar gagal berarti tidak ada yang berubah:

1. `ALTER TABLE payments ADD CONSTRAINT payments_amount_positive CHECK (amount > 0)`.
2. `ALTER TABLE payments ADD CONSTRAINT payments_status_valid CHECK (status IN ('pending','paid','expired','failed','cancelled'))` — sama dengan konstanta status di `internal/domain/payment/payment.go`.
3. `CREATE UNIQUE INDEX uq_payments_provider_reference ON payments (provider_name, provider_reference) WHERE provider_reference IS NOT NULL`. Referensi Cashi adalah `order_id` internal yang sudah unik; referensi sandbox acak 16 hex (`SBX-…`), jadi tidak ada tabrakan.
4. FK `payments.merchant_id` diganti dari `ON DELETE CASCADE` ke `ON DELETE RESTRICT` (`DROP CONSTRAINT payments_merchant_id_fkey`, `ADD CONSTRAINT … REFERENCES merchants(id) ON DELETE RESTRICT`). Satu-satunya penghapusan merchant di kode adalah rollback registrasi (merchant baru tanpa payment) dan tetap berfungsi. Tabel lain (API key, user, konfigurasi, payment link, idempotency) tetap `CASCADE`.

Idempoten: constraint dibuat lewat blok `DO $$ … IF NOT EXISTS (SELECT 1 FROM pg_constraint …)`, index memakai `IF NOT EXISTS`, FK diganti hanya bila aksi hapusnya belum `RESTRICT`.

Pengecekan sebelum deploy (DEPLOY.md 4.4), ketiganya harus mengembalikan nol baris:

```sql
SELECT id, reference, amount FROM payments WHERE amount <= 0;
SELECT id, reference, status FROM payments WHERE status NOT IN ('pending','paid','expired','failed','cancelled');
SELECT provider_name, provider_reference, count(*) FROM payments WHERE provider_reference IS NOT NULL GROUP BY 1,2 HAVING count(*) > 1;
```

### 3.2 Health dan reverse proxy

**Endpoint.** `GET /api/v1/health` melakukan `db.PingContext` dengan batas 2 detik: sehat → 200 `{"status":"ok"}`; gagal → 503 `{"status":"unavailable"}` tanpa detail (galat masuk log ber-request-ID). Dipasang di bawah `publicRateLimiter`. `setupRouter` menerima satu fungsi pengecek health tambahan (`func(context.Context) error`); test router diperbarui.

**Flag `-healthcheck`.** `cmd/api` mendapat flag `-healthcheck`: memuat config (`-config` yang sama), memanggil `http://127.0.0.1:<app.port>/api/v1/health` dengan timeout 3 detik, keluar 0 bila 200, selain itu 1. Logika dipisah menjadi fungsi yang bisa diuji (`runHealthcheck`).

**Compose.** Service `api`: `healthcheck: test: ["CMD", "/app/bin/api", "-healthcheck"]`, `interval: 10s`, `timeout: 5s`, `retries: 3`, `start_period: 20s`. Service `caddy`: `depends_on: api: condition: service_healthy`. `docker-compose.override.yaml` (lokal) menonaktifkan healthcheck `api` (`healthcheck: {disable: true}`) karena image dev menjalankan `air`, bukan `/app/bin/api`.

**Caddyfile** (blok `api.whuzpay.web.id`):

```
	request_body {
		max_size 1MiB
	}
	header {
		Strict-Transport-Security "max-age=31536000"
		X-Content-Type-Options nosniff
		Referrer-Policy no-referrer
		X-Frame-Options DENY
		Cache-Control "no-store"
		-Server
	}
	log {
		output stdout
		format filter {
			request>headers>Authorization delete
			request>headers>X-Api-Key delete
			request>headers>Cookie delete
			resp_headers>Set-Cookie delete
			wrap json
		}
	}
```

`encode zstd gzip` dan `reverse_proxy api:8080` tetap. HSTS tanpa `includeSubDomains`/`preload`. Filter log wajib: tanpa itu Caddy mencatat semua header request, termasuk token dan API key.

### 3.3 Kebersihan kode backend

- **Level log.** `logger.Init(level)` menyaring `debug < info < warn < error` (sebelumnya parameter diabaikan). Awal proses tetap `info`; setelah config dimuat, `logger.SetLevel` diatur dari `app.environment`: `development` → `debug`, `production` → `info`.
- **Batas waktu DB.** `DBConfig.ConnectionString()` menambahkan `statement_timeout=30000` dan `idle_in_transaction_session_timeout=60000` (runtime parameter yang diteruskan `lib/pq`). Pool (25 open, 5 idle, 5 menit) menjadi konstanta bernama di `cmd/api` (`dbMaxOpenConns`, `dbMaxIdleConns`, `dbConnMaxLifetime`). `make migrate` memakai `psql` terpisah dan tidak terkena timeout ini.
- **Timeout klien Cashi.** Dari 30 dtk menjadi 10 dtk (konstanta di `internal/provider/cashi/client.go`), agar satu panggilan selesai di dalam `WriteTimeout` 15 dtk. Tagihan yatim di Cashi (dibuat tetapi tidak tercatat) tidak berbahaya karena QR-nya tidak pernah sampai ke siapa pun; merchant yang kena 502 mengulang dengan `Idempotency-Key`.

## 4. Di Luar Cakupan

Paket 5 (fee). CI `govulncheck`, CSP dan header front-end, `migrate.sh` mode transaksi tunggal/lock, backup terjadwal, monitoring/alerting, enkripsi `webhook_secret` di database, leader lock untuk job (cukup didokumentasikan: `api` harus satu replika), kolom `name` sebagai penentu environment API key.

## 5. Dokumentasi dan Rilis

DEPLOY.md bagian baru **4.4 "Cek sebelum deploy Paket 4"**: tiga query pengecekan data (3.1) yang harus kosong sebelum `make migrate`; `docker-compose.yaml` dan `Caddyfile` di server wajib diganti bersama image baru (healthcheck dan header baru); tidak ada key config baru; `api` harus satu replika. README: catatan health endpoint. Rilis digabung dengan Paket 5 dan perpindahan VPS; tag versi ditentukan nanti.

## 6. Pengujian

- Test PostgreSQL asli (`TEST_DATABASE_URL`, PG18 sementara): tiap constraint menolak data yang melanggar; unique parsial menolak duplikat tetapi mengizinkan banyak `NULL`; FK `RESTRICT` menolak hapus merchant ber-payment dan mengizinkan merchant kosong; migration 019 aman diterapkan dua kali; `SHOW statement_timeout` dari koneksi yang dibuat dengan `ConnectionString()` bernilai 30s.
- Test unit: handler health (sehat → 200, ping gagal → 503 tanpa membocorkan galat), `runHealthcheck` (200 → 0, 503/koneksi gagal → 1), penyaringan level log, timeout klien Cashi.
- Validasi statis: `caddy validate` lewat container sekali pakai dan `docker compose config` (dilaporkan jujur bila Docker tidak tersedia).
- Mutasi untuk membuktikan test membedakan, seperti paket sebelumnya.

## 7. Risiko

Migration 019 gagal bila data lama melanggar constraint (disengaja, aman karena transaksional; mitigasi: pengecekan DEPLOY.md 4.4). Healthcheck `api` yang salah membuat Caddy tidak pernah start; mitigasi: flag `-healthcheck` memakai config yang sama dan diuji. Header `Cache-Control: no-store` berlaku untuk semua respons API termasuk export CSV (disengaja untuk data keuangan).
