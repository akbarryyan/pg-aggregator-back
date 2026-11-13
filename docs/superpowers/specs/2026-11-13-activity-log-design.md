# Activity Log (Admin) — Desain

Tanggal: 2026-11-13
Status: rancangan disetujui user; menunggu review spec tertulis. Tanpa commit/push (aturan user).

## 1. Latar belakang

Belum ada tabel audit. Aksi penting admin (fee bearer, bayar/tolak payout, adjust saldo) hanya ditulis sebagai baris log server (`logger.InfofCtx("admin %s ...")`), tidak bisa dilihat dari aplikasi. User meminta halaman **Activity Log** di admin berisi aktivitas **seluruh user**, bukan hanya super admin.

## 2. Keputusan (disetujui user)

| # | Keputusan |
|---|---|
| K1 | Pelaku yang dicatat: **admin** dan **user merchant** (dashboard). Aksi lewat API key merchant dan proses otomatis sistem tidak dicatat. |
| K2 | Yang dicatat: **semua aktivitas termasuk melihat** (setiap request terautentikasi admin/merchant), plus login berhasil/gagal dan logout. |
| K3 | Pendekatan: **middleware otomatis** untuk setiap request + **detail tambahan** dari handler untuk aksi penting. |
| K4 | Request otomatis front-end (polling/refresh) **tidak dicatat**: ditandai header `X-Background-Request: 1`. Header ini hanya berlaku untuk GET/HEAD; aksi yang mengubah data (POST/PUT/DELETE) **selalu tercatat** (keputusan user 2026-11-13 setelah review akhir). |
| K5 | Retensi **90 hari** (job harian menghapus yang lebih tua). |
| K6 | Halaman hanya di **admin**; versi merchant di luar cakupan. |

## 3. Data — migrasi 023

`migrations/023_create_activity_logs.sql` (satu transaksi, idempoten dengan `IF NOT EXISTS`):

```sql
CREATE TABLE IF NOT EXISTS activity_logs (
    id           UUID PRIMARY KEY,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    actor_type   VARCHAR(20) NOT NULL CHECK (actor_type IN ('admin', 'merchant_user', 'anonymous')),
    actor_id     UUID,
    actor_email  VARCHAR(255) NOT NULL DEFAULT '',
    merchant_id  UUID,
    action       VARCHAR(100) NOT NULL,
    method       VARCHAR(10) NOT NULL,
    route        VARCHAR(255) NOT NULL,
    target_id    VARCHAR(100) NOT NULL DEFAULT '',
    status_code  INT NOT NULL,
    ip           VARCHAR(64) NOT NULL DEFAULT '',
    user_agent   VARCHAR(500) NOT NULL DEFAULT '',
    duration_ms  INT NOT NULL DEFAULT 0,
    details      JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS idx_activity_logs_created_at ON activity_logs (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_activity_logs_actor ON activity_logs (actor_type, actor_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_activity_logs_merchant ON activity_logs (merchant_id, created_at DESC) WHERE merchant_id IS NOT NULL;
```

- `actor_type = 'anonymous'` hanya untuk **login gagal** (pelaku belum terautentikasi); `actor_email` berisi e-mail yang dicoba (dipotong 255), `actor_id` NULL.
- `merchant_id`: merchant pelaku (user merchant) atau merchant sasaran aksi admin bila route memuat `{id}` merchant; NULL bila tidak ada.
- Tanpa foreign key ke users/merchants (log bertahan walau pelaku dihapus; tabel append-only dari sisi aplikasi).
- Rollback ditulis di `DEPLOY.md` (`DROP TABLE activity_logs`).

## 4. Backend

### 4.1 Paket `internal/activity`
- `Entry` (field sesuai tabel) dan `Recorder` dengan antrian berkanal (buffer 1024) + satu goroutine penulis. `Record(Entry)` tidak pernah memblokir request: bila kanal penuh, entri dibuang dan dihitung, peringatan di-log maksimal sekali per menit. Galat tulis hanya di-log. `Close(ctx)` mengosongkan antrian saat shutdown (masuk ke alur shutdown job yang ada).
- Repository `ActivityLogRepository`: `Insert`, `List(filter, limit, offset)` (keyset/offset seperti list admin lain, urut `created_at DESC, id DESC`), `Count(filter)`, `Stats(days)` (14 hari: total, gagal login, pelaku unik, harian admin vs merchant, breakdown per `action` top 6 + "Other"), `PurgeOlderThan(t)`.
- Detail dari handler: `activity.AddDetails(ctx, map[string]any)` menulis ke wadah di context yang dibuat middleware; tanpa middleware (mis. tes) tidak berbuat apa-apa.

### 4.2 Middleware
`activity.Middleware(recorder, labeler)` dipasang pada subrouter `adminAPI` dan `merchantDash` **sesudah** middleware auth (agar pelaku diketahui), dan dibungkus khusus untuk endpoint auth (login/logout/me/profile/change-password admin dan merchant).
- Lewati bila header `X-Background-Request: 1`, untuk `OPTIONS`, dan untuk route health.
- Ambil template route dari `mux.CurrentRoute(r).GetPathTemplate()`, metode, status (response writer pembungkus), durasi, IP (logika `clientIP` rate limiter yang menghormati `trusted_proxies`, diekspor sebagai fungsi bersama), user agent (dipotong 500).
- Pelaku: `AdminIDFromContext` / `MerchantUserIDFromContext` + `MerchantIDFromContext`; e-mail pelaku di-cache singkat (map + TTL 5 menit) dari repo admin/merchant user agar tidak query per request.
- `target_id`: variabel route pertama yang ada di antara `id`, `keyId`, `reference`, `slug`, `name`.
- Login: handler login menambahkan detail `email` lewat `AddDetails`; middleware memakai e-mail itu bila pelaku belum ada; login gagal (4xx) → `anonymous`; login berhasil → pelaku dari hasil login (handler mengisi `actor_id`/`actor_type` lewat `AddDetails` kunci internal yang tidak disimpan ke `details`).
- **Tidak pernah menyimpan body request/response.** Hanya `details` yang diisi eksplisit oleh handler.

### 4.3 Label aksi
Tabel pemetaan `METHOD + template` → label bahasa Inggris (mis. `GET /api/v1/admin/payments/{id}` → "Viewed payment", `POST /api/v1/admin/payouts/{id}/pay` → "Paid payout", `POST /api/v1/auth/admin/login` → "Admin login", `PUT /api/v1/merchant/business` → "Updated business profile"). Mencakup semua route admin, merchant, dan auth yang ada di `cmd/api/main.go`; tes memastikan setiap route terdaftar punya label (tes membaca route dari router). Route tanpa pemetaan tetap dicatat dengan label `"{METHOD} {template}"`.

### 4.4 Detail aksi penting (handler memanggil `AddDetails`)
Fee bearer (`from`, `to`), bayar payout (`amount`, `reference`), tolak payout (`amount`, `reason`), adjust saldo (`amount`, `reason`), buat/ubah/hapus API key (`environment`, `name`/`key_id`; **tanpa nilai key**), regenerate webhook secret (tanpa nilai secret), buat merchant (`email`, `business_name`; **tanpa password**), ubah merchant (field yang berubah: nama field saja), aktif/nonaktif merchant (`is_active`), ajukan payout merchant (`amount`), ubah rekening payout (`bank`, 4 digit terakhir rekening), ganti password (tanpa nilai), login (`email`).

### 4.5 Endpoint admin
- `GET /api/v1/admin/activity-logs?search=&actor_type=&merchant_id=&result=success|failed&date_from=&date_to=&limit=&offset=` → `{items, total, limit, offset}`; `result=failed` = `status_code >= 400`. `search` mencocokkan `actor_email`, `action`, `target_id` (ILIKE).
- `GET /api/v1/admin/activity-logs/stats` → `{days, total, failed_logins, active_actors, daily:[{date,label,admin,merchant}], breakdown:[{action,count}]}` (14 hari, konvensi `DATE(created_at)`/`CURRENT_DATE`, array tidak pernah null).
- `GET /api/v1/admin/activity-logs/export` → CSV dengan filter yang sama (pola export admin lain).
- Route `stats`/`export` didaftarkan sebelum route berparameter bila ada.
- `openapi.yaml` diperbarui.

### 4.6 Retensi
Job `purge-activity-logs` tiap 24 jam (pola `startJob`) menghapus `created_at < NOW() - 90 days` per batch (mis. 5000 baris per DELETE sampai habis) agar tidak mengunci lama.

### 4.7 Tes
Recorder (tidak memblokir saat penuh, flush saat Close), middleware (pelaku admin/merchant, background dilewati, status & route tercatat, target_id, tidak ada body tersimpan), login berhasil/gagal (anonymous + e-mail, tanpa password), detail aksi penting tanpa rahasia, cakupan label untuk semua route, repo PG (insert/list/filter/stats/purge), router (endpoint admin hanya admin; merchant JWT → 401/403).

## 5. Front-end

- Header `X-Background-Request: 1` ditambahkan pada semua pemanggilan otomatis: polling status Test Payment, polling notifikasi admin/merchant, refresh berkala dashboard/halaman lain bila ada, dan polling apa pun yang memakai `setInterval`. Helper fetch admin/merchant mendapat opsi `{ background: true }`.
- Halaman `/admin/activity-logs` + menu **Activity Log** di sidebar admin (ikon riwayat/jam):
  - `StatsOverview`: 3 card (Total activity 14 hari, Failed logins, Active users), grafik harian bertumpuk Admin vs Merchant, donut per aksi.
  - Tabel: Time, Actor (e-mail + badge Admin/Merchant/Anonymous), Action, Target (tautan ke payment/merchant/payout bila route-nya jelas), Result (badge Success/Failed dengan kode status), IP.
  - Filter dan kontrol sama dengan halaman admin lain: pencarian tanpa tombol Search (debounce), tipe pelaku, hasil, merchant, rentang tanggal, export CSV, batas tampil, pagination (mobile justify-between).
  - Drawer detail per baris (lebar dan tombol tutup seperti drawer lain): semua kolom, user agent, durasi, `details` sebagai daftar key–value.
  - Responsif (tabel bergulir di dalam kartu, tanpa gulir halaman di 375 px). Teks UI bahasa Inggris.

## 6. Rilis
Backend dulu (migrasi 023 via `make migrate`, aman saat api berjalan), lalu front-end. Front-end lama dengan backend baru: polling lama belum membawa header sehingga ikut tercatat sampai front-end baru dirilis (sementara). Front-end baru dengan backend lama: halaman Activity Log menampilkan galat dengan Retry.

## 7. Di luar cakupan
Activity Log untuk merchant, aksi API key dan sistem, peringatan real-time, penyimpanan body request, dan retensi yang bisa diatur dari UI.

## 8. Risiko
Volume baris tinggi karena aktivitas melihat ikut dicatat (mitigasi: polling dilewati, retensi 90 hari, indeks, penulisan async). Entri bisa hilang bila antrian penuh atau server mati mendadak (diterima: log aktivitas bukan sumber kebenaran transaksi). Login gagal mencatat e-mail yang dicoba (data pribadi; hanya terlihat admin dan terhapus setelah 90 hari).
