# Saldo Merchant, Pencairan (Payout), dan Refund — Desain

Tanggal: 2026-11-05
Status: desain disetujui user per bagian (data, aturan, API/layar); menunggu review spec tertulis
Lanjutan dari: butir terbuka "settlement/payout ke merchant dan refund" (belum pernah didesain)

## 1. Latar Belakang

Sistem hanya mencatat payment (`amount`, `fee_amount`, status). Tidak ada saldo merchant, pencairan, ataupun refund. Uang pembeli masuk ke **satu akun Cashi milik operator**; pencairan ke merchant dilakukan manual di luar sistem. Klien Cashi hanya memakai `create-order` dan `check-status` (tidak ada API payout/refund yang diketahui). Operator butuh: (a) tahu berapa yang menjadi hak tiap merchant, (b) alur pengajuan dan pencatatan pencairan, (c) pencatatan refund.

Catatan istilah: "settlement" di kode dan `docs/payment-state-machine.md` berarti konfirmasi bahwa payment sudah dibayar. Dokumen ini memakai "pencairan/payout" untuk pemindahan uang ke merchant.

## 2. Keputusan (disetujui user)

| # | Keputusan |
|---|---|
| S1 | Uang masuk ke akun Cashi operator; operator mentransfer manual ke merchant. |
| S2 | Merchant mengajukan pencairan, admin mentransfer manual lalu menandai selesai (tanpa API payout Cashi). |
| S3 | Saldo = hasil net payment produksi berstatus paid, dikurangi pencairan dan refund. Boleh dicairkan kapan saja (tanpa masa tahan); minimum sekali pencairan **Rp 50.000**. |
| S4 | Net merchant untuk merchant yang menanggung fee dihitung dengan rumus fee Paket 5 (0,7%) dan disimpan di payment saat paid. |
| S5 | Refund hanya dicatat admin (uang dikembalikan manual); saldo merchant dikurangi, boleh negatif. Merchant tidak bisa memicu refund. |
| S6 | Satu rekening tujuan tersimpan di profil merchant; mengubahnya wajib password. |

## 3. Model Data (migrasi 021)

### 3.1 Ledger tambah-saja `merchant_ledger`
Saldo tidak disimpan sebagai angka yang diubah; saldo = `SUM(amount)` ledger milik merchant (production). Tabel hanya INSERT; trigger database menolak UPDATE dan DELETE.

Kolom: `id` UUID, `merchant_id`, `entry_type` (`payment_credit` | `refund_debit` | `payout_debit` | `payout_reversal` | `adjustment`), `amount` BIGINT bertanda (kredit positif, debit negatif), `payment_id`, `payout_id`, `refund_id` (nullable), `note`, `created_by` (admin/merchant user id, nullable untuk sistem), `created_at`.

Constraint: `amount <> 0`; tanda sesuai tipe (`payment_credit`/`payout_reversal` > 0, `refund_debit`/`payout_debit` < 0, `adjustment` bebas); unique parsial `(payment_id) WHERE entry_type = 'payment_credit'` (satu kredit per payment); unique parsial `(payout_id) WHERE entry_type = 'payout_debit'` dan `... = 'payout_reversal'`. Hanya payment `environment = 'production'` yang pernah dikredit.

### 3.2 `payments.merchant_net_amount`
BIGINT NOT NULL DEFAULT 0, `CHECK (>= 0)`. Diisi saat payment menjadi `paid`:
- `fee_bearer = customer`: `amount` (fee sudah dibayar pembeli lewat `fee_amount`).
- `fee_bearer = merchant`: `amount - fee(amount)`, `fee` = rumus Paket 5 (`internal/fee`, 70 bps).
Nilai disimpan agar tidak berubah bila rumus diubah kelak.

### 3.3 `payouts`
`id`, `merchant_id`, `amount` (> 0), `status` (`requested` | `paid` | `rejected`), snapshot rekening (`bank_name`, `account_number`, `account_holder`), `requested_by`, `requested_at`, `processed_by`, `processed_at`, `reference` (nomor referensi transfer, saat paid), `reject_reason`. Unique parsial: satu `requested` per merchant. Status akhir tidak bisa berubah (dijaga di kode dan CHECK konsistensi kolom).

### 3.4 `payment_refunds`
`id`, `payment_id`, `merchant_id`, `amount` (net yang dikurangkan, > 0), `reason` (wajib), `created_by`, `created_at`. Jumlah refund per payment ≤ `merchant_net_amount` (dijaga dalam transaksi dengan penguncian baris payment).

### 3.5 Status payment baru `refunded`
Terminal. Hanya `paid → refunded`, ditulis saat akumulasi refund sama dengan `merchant_net_amount`. Refund sebagian tidak mengubah status. `payment-state-machine.md` dan CHECK status di tabel payments diperbarui; tidak ada jalur lain yang boleh menimpa `refunded`.

### 3.6 Rekening di `merchants`
`payout_bank_name`, `payout_account_number`, `payout_account_holder` (nullable; ketiganya terisi bersama atau kosong bersama, CHECK).

### 3.7 Backfill
Untuk setiap payment produksi `paid` yang sudah ada: hitung dan isi `merchant_net_amount` (fee_bearer merchant saat ini dianggap berlaku untuk payment lama, karena bearer tidak pernah berubah per payment sebelum Paket 5; `fee_amount` lama bernilai 0), lalu tulis ledger `payment_credit`. Dibungkus satu transaksi; idempoten (unique index mencegah ganda bila dijalankan ulang).

## 4. Perilaku

### 4.1 Kredit saat paid
Pada transaksi yang sama dengan perubahan status ke `paid` (webhook terverifikasi, reconcile, dan late settlement expired→paid), `UpdateStatus` mengisi `merchant_net_amount` dan menulis ledger `payment_credit` (payment produksi saja). Tidak ada paid tanpa kredit, tidak ada kredit ganda (unique index).

### 4.2 Pencairan
1. Merchant mengajukan `amount` ≥ `payout.min_amount` (config, default 50000) dan ≤ saldo tersedia; rekening harus terisi; tidak boleh ada payout `requested` lain.
2. Dalam satu transaksi dengan `SELECT ... FOR UPDATE` pada baris merchant: hitung saldo, tulis `payouts` (snapshot rekening) dan ledger `payout_debit` (negatif). Saldo tersedia selalu sudah memperhitungkan pengajuan terbuka.
3. Admin `pay` (isi `reference`) → status `paid`, tanpa entri ledger baru. Admin `reject` (isi `reason`) → status `rejected` + ledger `payout_reversal` (positif). Pemrosesan memakai `UPDATE ... WHERE status = 'requested'` sehingga dua admin tidak bisa memproses ganda.
4. Saldo negatif memblokir pengajuan (nominal harus ≤ saldo).
5. Tanpa biaya transfer pencairan di tahap ini.
6. Ubah rekening wajib `password` (seperti regenerate webhook secret), tercatat di audit log; payout memakai snapshot, bukan rekening profil saat ini.

### 4.3 Refund
Admin saja, pada payment produksi `paid`. Input `amount` (≤ sisa net yang belum direfund) dan `reason`. Dalam satu transaksi dengan penguncian baris payment: baris `payment_refunds`, ledger `refund_debit`, dan bila total refund = `merchant_net_amount` maka status `refunded` + event callback `payment.refunded` ke merchant lewat antrian callback yang ada (tanda tangan sama). Refund sebagian tidak mengirim callback. Uang ke pembeli dikembalikan manual di luar sistem.

### 4.4 Penyesuaian saldo
Admin menulis ledger `adjustment` (positif/negatif, alasan wajib), tercatat dengan nama admin dan masuk audit log.

## 5. API

Merchant (JWT dashboard): `GET /merchant/balance` (tersedia, total dikredit/dicairkan/direfund, minimum, kesiapan rekening); `GET /merchant/ledger`; `PUT /merchant/payout-account` (+`password`); `POST /merchant/payouts` (`{amount}`, mendukung `Idempotency-Key`); `GET /merchant/payouts`.

Admin: `GET /admin/balances`; `GET /admin/payouts?status=`; `POST /admin/payouts/{id}/pay` (`{reference}`); `POST /admin/payouts/{id}/reject` (`{reason}`); `POST /admin/payments/{id}/refund` (`{amount, reason}`); `POST /admin/merchants/{id}/adjustments` (`{amount, reason}`); `GET /admin/merchants/{id}/ledger`.

Pengajuan baru memicu notifikasi admin (layanan notifikasi yang ada). Semua aksi admin masuk audit log. `docs/openapi.yaml` diperbarui, termasuk event callback `payment.refunded`.

## 6. Front-end

Merchant: halaman "Balance" (kartu saldo, form pengajuan, riwayat pengajuan, riwayat mutasi) dan bagian "Payout account" di pengaturan. Admin: halaman "Payouts" (antrean dan riwayat, bayar/tolak), kolom saldo di daftar merchant, tombol "Refund" di detail payment paid, "Adjust balance" dan ledger di detail merchant. Halaman API docs mendapat catatan event `payment.refunded`.

## 7. Di Luar Cakupan

Payout otomatis via API Cashi; masa tahan saldo; pengajuan refund oleh merchant; sengketa/chargeback; penagihan otomatis saldo negatif; biaya transfer pencairan; laporan pajak; multi-rekening; refund ke provider.

## 8. Pengujian

Dengan PostgreSQL sementara: kredit tepat sekali per payment walau webhook dan reconcile serentak, termasuk late settlement; net benar untuk kedua `fee_bearer` (10.000 → 10.000 dan 9.930); sandbox tidak pernah masuk saldo; dua pengajuan serentak dari saldo yang sama hanya satu yang lolos; minimum, saldo kurang, rekening kosong, pengajuan terbuka ganda ditolak; tolak mengembalikan saldo; payout final; pemrosesan ganda oleh dua admin; refund tidak melebihi net, hanya untuk payment produksi paid, penuh → `refunded` + callback; refund sebagian tanpa callback; ledger menolak UPDATE/DELETE; saldo = jumlah ledger; backfill idempoten dan cocok dengan total payment paid. Mutasi: hapus unique kredit, hapus penguncian baris, abaikan `environment`, lepas trigger. Front-end: `npm run lint` dan `npm run build`.

## 9. Penerapan dan Risiko

Migrasi 021 + rollback tertulis di `DEPLOY.md`; runbook: cocokkan total backfill dengan dashboard Cashi sebelum fitur dibuka, lalu uji satu siklus penuh (bayar → ajukan → bayar manual) dengan merchant internal. Rumus fee 0,7% dapat berbeda satu-dua rupiah dari potongan nyata Cashi karena pembulatan; selisih ditanggung operator dan dapat dikoreksi dengan adjustment. Saldo negatif tidak ditagih otomatis. Karena payment lama diasumsikan memakai `fee_bearer` merchant saat ini, bearer yang sudah diubah sesudah ada payment paid perlu dicek manual sebelum backfill. Paket ini besar; rencana dipecah per task: (1) migrasi, domain, ledger; (2) kredit saat paid; (3) pencairan; (4) refund, adjustment, callback; (5) front-end merchant; (6) front-end admin; (7) dokumentasi.
