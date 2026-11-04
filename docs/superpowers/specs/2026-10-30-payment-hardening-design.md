# Design: Payment Hardening — Paket 1 (Status & Nominal)

- **Tanggal:** 2026-10-30
- **Status:** Disetujui (brainstorming), menunggu review spec
- **Cakupan repo:** `back/`
- **Rilis target:** `ghcr.io/akbarryyan/whuzpay-backend:1.0.1`
- **Paket 2 (terpisah, menyusul):** SSRF pada `callback_url`/`webhook_url`
  merchant, dan callback merchant asinkron + penyelarasan timeout.

## 1. Latar Belakang

Backend sudah live (`https://api.whuzpay.web.id`, v1.0.0) dan satu transaksi
produksi Rp 2.000 berhasil end-to-end (Cashi QRIS → webhook → `paid`). Sebelum
menerima transaksi riil secara rutin, beberapa cacat di jalur uang harus
ditutup:

1. Update status tidak atomik (`UpdateStatus` read-then-write, `WHERE id`
   saja) → webhook, job expire, dan reconcile bisa saling timpa dan
   mengirim callback merchant ganda.
2. Nominal di webhook tidak diverifikasi.
3. `CreatePayment` bisa panic `nil` bila semua kandidat provider di-skip;
   nama provider di config merchant tidak divalidasi.
5. `environment` kosong/tak dikenal diam-diam menjadi `production`
   (panel admin tidak mengirim `environment` → selalu Cashi asli).
7. Job expire tidak mengirim callback `payment.expired` ke merchant.

(Nomor mengikuti daftar temuan awal; item 4 dan 6 = Paket 2.)

## 2. Fakta Cashi (dokumentasi + transaksi uji)

Dari dokumentasi `https://cashi.id/documentation` (konten diambil dari bundle
situs) dan transaksi uji `PAY-1793341680-804bb9b5`:

- Webhook: header `X-Gateway-Signature` = HMAC-SHA256 hex atas body mentah.
  Payload terdokumentasi:
  `{"event":"PAYMENT_SETTLED","data":{"order_id":…,"amount":…,"status":"SETTLED","payment_method":…,"timestamp":"2025-12-25T19:46:40Z"}}`.
  Order berawalan `TEST-` = tes koneksi.
- `GET https://cashi.id/api/check-status/:orderId` (header `x-api-key`) →
  `{"success":true,"status":"SETTLED","amount":…,"order_id":…}`; status yang
  terlihat: `WAITING`, `SETTLED`, `EXPIRED`.
- Retry webhook **tidak terdokumentasi** → desain tidak boleh bergantung
  padanya.
- Transaksi uji: create-order mengembalikan `amount = 2000` (sama dengan
  permintaan, tanpa kode unik) dan `expires_at` berupa waktu nyata (bukan
  `"0"` seperti contoh dokumentasi). Pelanggan membayar **tepat Rp 2.000**;
  merchant menerima Rp 1.986 (fee 0,7% dipotong saat settlement).
- **Belum terverifikasi** (syarat rilis §9): `amount` di `raw_payload`
  webhook dan di check-status untuk transaksi uji.

## 3. Keputusan

| # | Keputusan | Alasan |
|---|---|---|
| D1 | Dua paket; ini Paket 1 (item 1, 2, 3, 5, 7) | Perbaikan kritis bisa rilis cepat; item 4/6 berbagi kode callback dan butuh desain sendiri |
| D2 | Transisi `expired → paid` diizinkan; terminal lain tetap terkunci | Uang yang sudah masuk adalah kebenaran (pembayaran terlambat) |
| D3 | Update status atomik via `UPDATE … WHERE id AND status = ANY(<status asal sah>)`; hanya pemenang yang mengirim callback | DB yang menjamin satu pemenang |
| D4 | Webhook = pemicu; **check-status Cashi = kebenaran**; `paid` hanya jika `SETTLED` dan `amount` = `payment.amount` (dan `amount` webhook, bila ada) | Tahan secret bocor; tak bergantung field opsional |
| D5 | Pembanding nominal = **nominal kotor** yang tersimpan (`payments.amount`), bukan nominal bersih setelah fee | Pelanggan membayar nominal kotor (terbukti di transaksi uji) |
| D6 | Mismatch nominal → payment tidak diubah, dicatat `amount_mismatch`, webhook dibalas 200 | Butuh manusia, bukan retry |
| D7 | Verifikasi gagal (error/timeout/belum settled) → payment tidak diubah, webhook dibalas 502 | Sinyal retry; reconcile job sebagai jaring |
| D8 | Job baru `reconcile-payments`: tiap 2 menit, ≤50/putaran, produksi, `pending` (dibuat ≤24 jam) + `expired` (kedaluwarsa ≤24 jam), kecuali yang sudah ber-`amount_mismatch` | Retry Cashi tidak terjamin; tangkap pembayaran terlambat |
| D9 | Konstanta (2 menit, 24 jam, 50, timeout verifikasi 5 detik) di kode, bukan config | Detail internal; YAGNI |
| D10 | `ParseEnvironment` baru untuk jalur pembuatan: kosong → `sandbox`; alias diterima; tak dikenal → 400. `NormalizeEnvironment` tidak diubah (jalur baca) | Default aman; tak mengubah tampilan dashboard |
| D11 | `sandbox` tak pernah jadi kandidat produksi; config merchant hanya menerima provider terdaftar non-sandbox dan metode yang didukung | Tutup panic & konfigurasi tak valid |
| D12 | `ExpirePayments` mengirim `payment.expired` untuk payment yang berhasil di-update | Konsisten dengan reconcile |
| D13 | Tidak ada migration DB | Semua memakai kolom yang ada |

## 4. Di Luar Cakupan

- SSRF `callback_url`/`webhook_url` dan callback asinkron (Paket 2).
- Selector environment di drawer "buat payment" panel admin (frontend,
  tindak lanjut). Sampai itu ada, payment dari admin = sandbox.
- Status `failed`/`cancelled` dari Cashi (tidak terdokumentasi untuk QRIS).
- Refund, partial payment.

## 5. State Machine & Update Atomik (item 1, D2, D3)

```
pending ──▶ paid | expired | failed | cancelled
expired ──▶ paid            (pembayaran terlambat)
paid, failed, cancelled     terminal
```

- `payment.CanTransitionTo` diperbarui sesuai diagram. Invarian (didokumentasi
  di kode + doc + test): satu-satunya jalur yang menulis `paid` adalah laporan
  provider (webhook terverifikasi atau reconcile); tidak ada endpoint admin
  "mark paid".
- Status asal sah per tujuan: `paid` ← `{pending, expired}`;
  `expired`/`failed`/`cancelled` ← `{pending}`.
- `PaymentRepository.UpdateStatus(ctx, id, newStatus, paidAt)` menjadi satu
  query bersyarat:
  `UPDATE payments SET status=$1, paid_at=$2, updated_at=$3 WHERE id=$4 AND status = ANY($5)`.
  `RowsAffected == 0` → baca ulang: tidak ditemukan → `ErrPaymentNotFound`;
  status kini terminal → `ErrPaymentAlreadyTerminal`; selain itu →
  `ErrInvalidStatusTransition`. Status sebelumnya dikembalikan ke pemanggil
  agar bisa menandai pembayaran terlambat (signature persis ditetapkan di
  plan).
- Semua pemanggil (`ProcessWebhook`, `reconcilePaymentData`, `ExpirePayments`)
  hanya mengirim callback merchant bila update berhasil.
- Pembayaran terlambat (`expired → paid`): `webhook_events.event_type =
  "payment.paid.late"`; callback merchant tetap event `payment.paid` dengan
  field tambahan `"late_payment": true` di payload.

## 6. Verifikasi Webhook (item 2, D4–D7)

- `NormalizedPaymentStatus` mendapat field `Amount *int64`. Cashi mengisinya
  dari `amount` check-status; sandbox dari nominal order in-memory.
- `ProviderWebhookPayload.PaidAt`: Cashi mem-parse `data.timestamp`
  (RFC3339) bila ada dan valid; selain itu waktu server.
- Fungsi bersama `verifyProviderPayment(p *payment.Payment, status
  *NormalizedPaymentStatus, webhookAmount *int64) error` mengembalikan:
  `nil` (lolos), `ErrNotSettledAtProvider`, atau error yang membungkus
  `ErrAmountMismatch`. Pesan mismatch **selalu diawali `amount_mismatch:`**
  lalu menyebut expected/provider/webhook (nominal bukan secret), mis.
  `amount_mismatch: expected 2000, provider 1500, webhook 1500`. Awalan ini
  dipakai §7 untuk mengecualikan kandidat reconcile, baik mismatch dari
  webhook maupun dari reconcile.
- Alur `ProcessWebhook`:
  1. Simpan raw event; cari provider; validasi signature; parse (seperti
     sekarang).
  2. Cari payment by provider reference (seperti sekarang).
  3. Status webhook ≠ `paid` → abaikan (200), seperti sekarang.
  4. Payment `paid` → `ErrDuplicateWebhook` (200); `failed`/`cancelled` →
     `ErrPaymentAlreadyTerminal` (200).
  5. Payment `pending`/`expired` → `GetPaymentStatus` dengan context timeout
     5 detik → `verifyProviderPayment`:
     - error panggilan atau `ErrNotSettledAtProvider` →
       `ErrProviderVerificationFailed`; event difinalisasi
       `verification_failed`; payment tak berubah.
     - `ErrAmountMismatch` → event difinalisasi dengan `processing_error`
       berisi mismatch; payment tak berubah.
     - lolos → `UpdateStatus(paid)`; kalah race → diperlakukan duplikat
       (200); status sebelumnya `expired` → tandai terlambat; callback.
- `WebhookHandler` memetakan: sukses 200 `success`; duplikat/terminal/
  mismatch/non-settled 200 `ignored`; `ErrProviderVerificationFailed` **502**;
  lainnya 400 (seperti sekarang).

## 7. Job Reconcile (D8, D9)

- Repository baru `ListReconcileCandidates(ctx, now time.Time, window
  time.Duration, limit int) ([]*payment.Payment, error)`:
  `environment = 'production'` dan `provider_reference` tidak kosong; dan
  (`status = 'pending' AND created_at >= now - window`) atau (`status =
  'expired' AND expires_at >= now - window`); dan tidak ada
  `webhook_events` dengan `payment_id` ini dan `processing_error` diawali
  `amount_mismatch`; urut `created_at DESC`; `LIMIT`.
- `PaymentService.ReconcileRecentPayments(ctx) error` dijalankan
  `scheduler.RunPeriodic(bgCtx, 2*time.Minute, "reconcile-payments", …)`.
- `reconcilePaymentData` diperluas: payment `expired` tidak lagi di-skip
  (hanya dicek ke provider; tidak ada expire lokal); verifikasi nominal via
  `verifyProviderPayment`; panggilan provider dengan timeout 5 detik.

| Lokal | Cashi | Tindakan |
|---|---|---|
| pending | SETTLED + nominal cocok | → paid + callback |
| pending | SETTLED + nominal beda | tetap; catat `reconcile.amount_mismatch` (sekali) |
| pending, lewat `expires_at` | belum settled | → expired (lokal, perilaku sekarang) + callback |
| expired | SETTLED + nominal cocok | → paid (`late`) + callback |
| expired | belum settled | tetap |
| apa pun | error/timeout | tetap; dicoba putaran berikutnya |

- Catatan mismatch dari reconcile: baris `webhook_events` baru
  (`provider_name` = provider payment, `provider_reference`, `payment_id`,
  `event_type = "reconcile.amount_mismatch"`, `status = "rejected"`,
  `is_processed = false`, `processing_error = "amount_mismatch: …"`,
  `raw_payload` = ringkasan status provider). Plan memastikan notifikasi/log
  admin menampilkannya.
- Reconcile manual admin (`/admin/reconciliation/*`) memakai logika yang sama.

## 8. Environment, Provider, Expire (item 5, 3, 7)

- `payment.ParseEnvironment(raw string) (string, error)`: trim + lowercase;
  `""` → `sandbox`; `production|prod|live` → `production`;
  `sandbox|test|dev|development` → `sandbox`; lain → `ErrInvalidEnvironment`.
  Dipakai di `CreatePaymentRequest.Validate` dan validasi pembuatan payment
  link; `MerchantHandler.CreatePayment` tidak lagi memanggil
  `NormalizeEnvironment`. Handler terkait memetakan `ErrInvalidEnvironment` →
  400. `NormalizeEnvironment` tidak diubah.
- `resolveProvidersForMerchant` menyaring `sandbox` dari kandidat (default
  router maupun config merchant); kandidat kosong → `ErrProviderNotAvailable`.
  Setelah loop `CreatePayment`, bila tidak ada response: kembalikan `lastErr`
  (dibungkus) bila ada, selain itu `ErrProviderNotAvailable` (503). Tidak ada
  jalur dereference `nil`.
- `AdminMerchantService.UpsertMerchantProviderConfig`: `provider_name` harus
  terdaftar di router dan bukan `sandbox`; `payment_method` harus didukung
  (`qris`); jika tidak → error baru → 400. Dependensi via builder
  `WithProviderRegistry(...)` (interface kecil `GetProvider(name)`), dipasang di
  `main.go`.
- `ExpirePayments`: setelah `UpdateStatus(expired)` berhasil →
  `NotifyMerchantPaymentEvent(payment.expired)`. Keterbatasan diketahui:
  callback masih sinkron (timeout 10 detik) sampai Paket 2.

## 9. Syarat Rilis, Rilis, dan Verifikasi Produksi

**Syarat sebelum rilis (wajib):**
1. Akses SSH ke VPS dipulihkan (security group "My IP").
2. Untuk `PAY-1793341680-804bb9b5`: `amount` di `webhook_events.raw_payload`
   **dan** di check-status Cashi = `2000`. Jika salah satunya berbeda (mis.
   `1986`), aturan D5 direvisi sebelum rilis.

**Rilis:** `make release VERSION=1.0.1` → VPS sesuai `DEPLOY.md` §4 (ganti
tag, `docker compose pull api`, backup, `make migrate` (no-op), `docker compose
up -d`).

**Verifikasi produksi setelah rilis:**
1. Transaksi Rp 2.000 → `paid`; `webhook_events` terbaru `is_processed=true`.
2. Webhook palsu bertanda tangan valid (dikirim di VPS) untuk payment belum
   dibayar → HTTP 502; payment tetap `pending`.
3. Log `reconcile-payments` muncul tiap 2 menit; payment uji yang tidak dibayar
   menjadi `expired`.
4. `docker stats` memori aman.

## 10. Dokumentasi

- `docs/payment-state-machine.md`: diagram, update atomik, verifikasi
  check-status, nominal kotor vs fee, `late_payment`, job reconcile.
- `back/README.md`: Background Jobs + `reconcile-payments`.
- `docs/openapi.yaml`: webhook 502, `late_payment` di payload callback, 400
  `invalid environment` — sejauh bagian itu terdokumentasi (dicek di plan).

## 11. Testing

Gaya yang ada: `go test`, table-driven, fake manual, sqlmock untuk repository,
`httptest` untuk handler, `router_test.go` untuk integrasi.

| Area | Kasus |
|---|---|
| State machine | `expired→paid` ok; `paid→expired`, `failed→paid`, `cancelled→paid`, `paid→paid` ditolak |
| Repository `UpdateStatus` | query bersyarat + `ANY` status asal (sqlmock); 0 baris → error yang tepat |
| Race | dua updater bersaing → satu berhasil, satu callback |
| Webhook | settled+cocok → paid; mismatch check-status → pending+mismatch+200; mismatch webhook-only → ditolak; check-status error/timeout → pending+502; check-status `WAITING` → 502; expired+settled → paid late; paid → 200 ignored tanpa callback; `paid_at` dari timestamp |
| Reconcile | query kandidat (sqlmock: env, jendela, pengecualian mismatch, urutan, limit); setiap baris tabel §7; mismatch dicatat sekali; sandbox tak disentuh |
| Environment | `ParseEnvironment` tabel; admin create tanpa env → sandbox; env tak dikenal → 400 |
| Provider | config hanya sandbox → 503 (bukan panic); upsert `sandbox`/tak dikenal/metode tak didukung → 400 |
| Expire | callback per payment yang berhasil di-update; tidak ada callback bila kalah race |
| Integrasi | `router_test.go` create → webhook → paid dengan fake provider yang menjawab check-status |

## 12. Amandemen Saat Eksekusi (disetujui user)

Perubahan berikut diputuskan selama implementasi atau review, dan menggantikan teks di atas bila bertentangan:

| # | Tanggal | Mengganti | Keputusan |
|---|---|---|---|
| A1 | 2026-10-30 | §5 (bentuk SQL `UpdateStatus`) | Bentuk CTE `WITH prev … FOR UPDATE … RETURNING (SELECT status FROM prev)` mengembalikan NULL di PostgreSQL 18. Diganti bentuk join `UPDATE payments p … FROM (SELECT id, status FROM payments WHERE id = $4 FOR UPDATE) prev WHERE p.id = prev.id AND p.status IN (…) RETURNING prev.status`. Ada test PostgreSQL asli yang opsional (`TEST_DATABASE_URL`). |
| A2 | 2026-11-02 | §7 (batch 50) | Reconcile memakai cursor keyset bergilir `(created_at, id)` di memori, sehingga seluruh jendela tercakup tiap ⌈N/50⌉ putaran, tidak hanya 50 payment terbaru. |
| A3 | 2026-11-02 | D6/D8 (mismatch) | Hanya mismatch **dari provider** (nominal check-status beda atau tidak ada) yang memakai awalan `amount_mismatch:` dan mengeluarkan payment dari reconcile. Mismatch yang **hanya di webhook** (check-status cocok) memakai awalan `webhook_amount_mismatch:`. Webhook-nya tetap ditolak (dijawab 200 ignored, dicatat), tapi reconcile tetap boleh menandai paid berdasarkan check-status. |
| A4 | 2026-11-02 | §8 (expire) | Masa tenggang 5 menit: `expire-payments` dan expire lokal oleh reconcile baru berlaku setelah `expires_at + 5 menit`. |
| A5 | 2026-11-02 | D8 (jendela) | Jendela reconcile: pending ≤24 jam menurut `created_at`; expired ≤**6 jam** menurut `expires_at`. Ini mengurangi volume panggilan check-status ke Cashi. |
| A6 | 2026-11-02 | §6 (tambahan) | Webhook ditolak (400) bila provider di URL bukan provider pemilik payment (`p.ProviderName`). |
| A7 | 2026-11-02 | §6/§7 (tambahan) | Setelah verifikasi lolos, update status, pencatatan event, dan pencatatan callback merchant memakai context yang tidak ikut batal (`context.WithoutCancel`, maksimal 30 detik). Dengan begitu, payment yang sudah paid tidak kehilangan callback kalau koneksi provider putus. |
