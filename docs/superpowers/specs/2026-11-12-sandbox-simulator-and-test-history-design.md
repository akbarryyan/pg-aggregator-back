# Simulator Sandbox dan Riwayat Test Payment — Desain

Tanggal: 2026-11-12
Status: rancangan disetujui user; menunggu review spec tertulis. Tanpa commit/push (aturan user).

## 1. Latar belakang

- Halaman Test Payment merchant menampilkan "Recent test payments" dengan mencari `customer_name` = penanda test, per environment aktif. Penanda diganti dari `Dashboard test` ke `TEST PAYMENT` (front-end 2026-11-12), sehingga test lama hilang dari riwayat (tetap ada di halaman Payments).
- Payment sandbox tidak pernah bisa lunas: adapter sandbox hanya membuat QR dummy di memori, tidak ada webhook, dan tidak ada jalur yang mengubah statusnya selain kedaluwarsa. Merchant tidak bisa menguji alur paid/failed maupun callback-nya.

## 2. Keputusan (disetujui user)

| # | Keputusan |
|---|---|
| K1 | Test lama dipindah ke penanda baru lewat **migrasi data** (bukan pencarian dua nama di front-end). |
| K2 | Simulator ada di **halaman checkout publik** (payment sandbox) **dan** di **halaman Test Payment** (mode sandbox). |
| K3 | Hasil yang bisa disimulasikan: **dibayar, gagal, kedaluwarsa**. |
| K4 | Simulasi memakai jalur status dan callback yang sama dengan pembayaran sungguhan, sehingga merchant menerima callback `payment.paid` / `payment.failed` / `payment.expired`. |

## 3. Migrasi 022 — penanda test lama

`migrations/022_rename_dashboard_test_customer.sql`, satu transaksi, idempoten:

```sql
UPDATE payments SET customer_name = 'TEST PAYMENT' WHERE customer_name = 'Dashboard test';
```

Tanpa perubahan skema. Rollback (ditulis di `DEPLOY.md`): kebalikan dari UPDATE di atas, dengan catatan bahwa rollback juga mengganti test baru yang dibuat sesudah migrasi (nama `TEST PAYMENT` tidak bisa dibedakan). Rollback tidak diperlukan untuk kebenaran sistem: penanda hanyalah teks tampilan dan kunci pencarian riwayat.

## 4. Backend — simulator

### 4.1 Service
`PaymentService.SimulateSandboxOutcome(ctx, p *payment.Payment, outcome string) (*payment.Payment, error)`:

- `outcome` ∈ {`paid`, `failed`, `expired`}; lainnya → `ErrInvalidSimulationOutcome` (400).
- Penjagaan, berurutan: `p.Environment == "sandbox"` dan `p.ProviderName == "sandbox"`, jika tidak → `ErrSimulationNotAllowed` (403; payment production tidak pernah bisa disimulasikan). `p.Status == "pending"`, jika tidak → `ErrPaymentAlreadyTerminal` yang sudah ada (409).
- `paid`: `merchantNetFor(p)` lalu `paymentRepo.MarkPaid(ctx, id, now, net)` (MarkPaid sudah tidak mengkredit ledger untuk sandbox), lalu set `p.Status/PaidAt/MerchantNetAmount`.
- `failed` / `expired`: `paymentRepo.UpdateStatus(ctx, id, status, nil)`.
- Sesudah status berubah: `NotifyMerchantPaymentEvent(ctx, p, eventTypeForStatus(status), false)` (antrian, tanda tangan, dan retry yang ada). Konteks dilepas (`detached`) seperti jalur reconcile, agar status yang sudah tersimpan tidak kehilangan callback-nya.
- Balapan dengan kedaluwarsa/settlement lain: `ErrPaymentAlreadyTerminal` dari repo diteruskan (409).
- Dicatat sebagai baris `webhook_events` (provider `sandbox`, `event_type` `sandbox.simulate.<outcome>`, `is_processed` true, `payment_id` terisi), sehingga tampil di timeline payment admin dan log. Gagal mencatat event hanya di-log; tidak menggagalkan simulasi.
- Adapter sandbox tidak diubah. Reconcile tidak membalik hasil simulasi: `paid`/`failed` terminal dilewati; `expired` hanya diperiksa untuk settlement terlambat, dan adapter sandbox tidak pernah melaporkan paid.

### 4.2 Endpoint
- `POST /api/v1/public/payments/by-reference/{reference}/simulate` body `{"outcome":"paid"|"failed"|"expired"}`. Tanpa login, di belakang `publicRateLimiter` yang sama dengan `GET .../by-reference/{reference}`. Respons: payment publik yang sama bentuknya dengan GET by-reference (halaman checkout langsung memakai hasilnya). Payment tidak ditemukan → 404.
- `POST /api/v1/merchant/payments/{id}/simulate` body sama. JWT merchant; payment milik merchant lain → 404 (pola kepemilikan yang ada). Respons: `ToPaymentResponse` (ditambah `merchant_net_amount` seperti detail dashboard).
- Kode galat: 400 outcome/body tidak valid, 403 bukan sandbox, 404 tidak ditemukan, 409 sudah tidak pending, 429 rate limit.
- `docs/openapi.yaml` dan `docs/payment-state-machine.md` (transisi pending → paid/failed/expired oleh simulator, sandbox saja) diperbarui.

### 4.3 Tes
Service (fake repo): tiap outcome mengubah status dan memicu satu callback dengan event yang benar; production ditolak; provider bukan sandbox ditolak; status non-pending → 409; outcome tidak valid → 400; `paid` menyimpan net. PostgreSQL sementara: simulasi `paid` sandbox tidak menulis `merchant_ledger`. Handler: kepemilikan merchant (404), body tidak valid (400). Router: kedua route tercapai; route merchant butuh JWT (401).

## 5. Front-end

- `lib/merchant-api.ts`: `simulatePublicPayment(reference, outcome)` dan `simulateMerchantPayment(id, outcome)`.
- Komponen bersama kecil `SandboxSimulator` (props: `onSimulate(outcome)`, `busy`): kotak kuning berjudul "Simulator sandbox · bukan uang sungguhan" dengan tiga tombol "Simulasikan dibayar", "Simulasikan gagal", "Simulasikan kedaluwarsa"; tombol nonaktif selama proses; galat lewat toast.
- **Checkout `/pay/[reference]`**: tampil hanya bila `environment === "sandbox"` dan status `pending`, di bawah tombol "Saya sudah bayar". Hasil simulasi langsung dipasang ke state payment, sehingga layar hasil muncul tanpa menunggu polling. Teks Bahasa Indonesia (halaman pelanggan).
- **Test Payment** (`/dashboard/test-payment`): tampil hanya pada mode sandbox untuk payment yang sedang ditampilkan dan masih pending, di bawah QR. Sesudah berhasil: payment yang tampil diperbarui dan riwayat dimuat ulang. Label mengikuti bahasa UI aplikasi (Inggris): "Sandbox simulator", "Simulate paid", "Simulate failed", "Simulate expired".
- Mode production: komponen tidak dirender sama sekali.
- Responsif: tombol bertumpuk penuh di layar sempit, berjajar ≥ `sm`; tanpa gulir horizontal di 375 px.

## 6. Rilis
Backend dulu (versi minor berikutnya; tag `x-api-image` di `docker-compose.yaml` disesuaikan), lalu di VPS `make migrate` (migrasi 022, aman saat api berjalan) dan `docker compose up -d`. Front-end sesudahnya (versi minor berikutnya). Front-end baru dengan backend lama: tombol simulator menampilkan galat 404/405 lewat toast; tidak ada bagian lain yang rusak.

## 7. Di luar cakupan
Simulasi refund, simulasi nominal salah/pembayaran sebagian, simulator untuk production, dan perubahan adapter sandbox.

## 8. Risiko
Endpoint publik memungkinkan siapa pun yang tahu reference sandbox mengubah statusnya; dampaknya terbatas pada data uji dan callback sandbox merchant itu sendiri (tidak ada uang, tidak masuk saldo), dan dibatasi rate limit. Callback simulasi bertanda tangan sama dengan callback sungguhan; merchant membedakannya lewat field `provider` (`"sandbox"`) pada payload callback yang sudah ada (payload tidak punya field `environment`; tidak ditambah dalam paket ini).
