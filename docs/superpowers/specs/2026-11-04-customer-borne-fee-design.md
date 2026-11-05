# Fee Provider Ditanggung Customer (Paket 5) — Desain

Tanggal: 2026-11-04
Status: desain disetujui user per bagian; menunggu review spec tertulis
Lanjutan dari: `2026-10-30-payment-hardening-design.md` (aturan verifikasi nominal kotor), `2026-11-04-ops-data-hardening-design.md` (Paket 4)

## 1. Latar Belakang

Pada pembayaran uji nyata Rp 2.000, dashboard Cashi menunjukkan Rp 1.986: Cashi memotong fee 0,7% (Rp 14) saat settlement. Sistem sudah mengantisipasi ini (Paket 1 memverifikasi nominal **kotor** yang dibayar customer, bukan net), tetapi uang yang benar-benar diterima kurang dari nominal transaksi. User ingin customer membayar nominal ditambah fee, sehingga yang diterima setelah potongan Cashi sama dengan nominal merchant.

Ini mengubah arti nominal pada alur pembayaran (pembuatan, verifikasi, callback, tampilan), sehingga dikerjakan sebagai paket tersendiri. Fee tambahan milik WhuzPay sendiri bukan bagian paket ini.

## 2. Keputusan (disetujui user)

| # | Keputusan |
|---|---|
| F1 | Pengaturan per merchant: `merchants.fee_bearer` ∈ {`merchant`, `customer`}, nilai awal `merchant` (perilaku sekarang). Hanya admin yang mengubah, lewat endpoint khusus. |
| F2 | Aturan fee: 0,7% flat dari nominal kotor, dibulatkan konservatif (anggap Cashi membulatkan fee ke atas). Tarif berupa konstanta di adapter Cashi. |
| F3 | `payments.amount` tetap **nominal merchant**; kolom baru `payments.fee_amount` menyimpan fee; total yang dibayar customer = `amount + fee_amount`. |
| F4 | Sandbox memakai aturan fee yang sama dan **akhiran acak 1–99 dihapus** agar identik dengan Cashi nyata. |
| F5 | Provider tanpa kemampuan menghitung fee + merchant `customer` → pembuatan payment ditolak (gagal tertutup). |

## 3. Perubahan

### 3.1 Model data dan perhitungan (migration 020)

- `merchants.fee_bearer VARCHAR(20) NOT NULL DEFAULT 'merchant' CHECK (fee_bearer IN ('merchant','customer'))`.
- `payments.fee_amount BIGINT NOT NULL DEFAULT 0 CHECK (fee_amount >= 0)`. Payment lama otomatis `fee_amount = 0`, sehingga total = `amount` (termasuk payment yang sedang berjalan saat deploy). Migration aditif dan idempoten (`IF NOT EXISTS`, pencarian constraint dibatasi per tabel seperti migration 019).
- `Payment.TotalAmount() int64 { return Amount + FeeAmount }` (tidak disimpan).
- Paket baru `internal/fee`: fungsi murni `QuoteTotalBasisPoints(nominal int64, bps int64) (total, fee int64)`. Total = bilangan bulat terkecil `G` dengan `G − ceil(G × bps / 10000) ≥ nominal`; fee = `G − nominal`. Aritmetika bilangan bulat saja (tanpa float); `ceil(x/10000)` dihitung `(x + 9999) / 10000`. Perkiraan awal `ceil(nominal × 10000 / (10000 − bps))` lalu disesuaikan naik/turun sampai tepat minimal. Contoh: nominal 2.000, bps 70 → total 2.015, fee 15 (total 2.014 → fee 15, net 1.999, tidak memenuhi). Nominal ≤ 0 atau bps di luar [0, 10000) → error.
- Antarmuka opsional `FeeQuoter` di paket provider: `QuoteTotal(nominal int64) (total, fee int64, err error)`. `CashiAdapter` dan sandbox mengimplementasikannya dengan konstanta `feeBasisPoints = 70` (pemanggilan `fee.QuoteTotalBasisPoints`).

### 3.2 Alur payment

`PaymentService.CreatePayment` memuat merchant (sudah dilakukan untuk cek `is_active`) dan membaca `fee_bearer`.

- **`merchant`:** tidak berubah, termasuk penimpaan `p.Amount` dengan angka dari respons provider.
- **`customer`:** pada setiap kandidat provider (failover) — jika provider bukan `FeeQuoter` → kandidat itu gagal (F5); selain itu `total := QuoteTotal(nominal)` dan `ProviderPaymentRequest.Amount = total`. Setelah respons: `charged := respons.Amount` (bila > 0, selain itu total yang diminta); `p.Amount` tetap nominal; `p.FeeAmount = charged − nominal`. Bila `charged < nominal` → error (tidak membuat payment). `fee_amount` dengan demikian mencerminkan apa yang benar-benar ditagih provider.

Verifikasi (`verifyProviderPayment`): nominal kotor yang diharapkan = `p.TotalAmount()`, dibandingkan dengan check-status dan webhook; semua aturan Paket 1 lainnya (`amount_mismatch:` vs `webhook_amount_mismatch:`, gagal tertutup bila nominal provider tidak diketahui, reconcile, pembayaran terlambat) tetap. Payment lama (fee 0) berperilaku identik.

Idempotensi: hash request dari nominal; replay mengembalikan payment tersimpan dengan fee aslinya, sehingga mengganti `fee_bearer` di antara dua retry tidak mengubah hasil.

Sandbox: `CreatePayment` tidak lagi menambah akhiran acak 1–99 ke nominal (sebelumnya meniru perilaku Cashi lama yang tidak terbukti pada pembayaran nyata).

### 3.3 Antarmuka (aditif)

`fee_amount` dan `total_amount` (selalu ada; 0 dan sama dengan `amount` bila merchant yang menanggung) ditambahkan pada: respons pembuatan/ambil/daftar payment merchant, lookup publik untuk checkout, daftar/detail payment admin, kolom export CSV (merchant dan admin), dan payload callback merchant (`amount` tetap nominal). Respons merchant dan detail admin juga memuat `fee_bearer` (hanya baca). `paid_amount` di dashboard tetap menjumlahkan nominal. Payment link: batas dan nominal berlaku pada nominal; checkout menampilkan total; payment dari link memakai `CreatePayment` sehingga mengikuti pengaturan merchant pemilik link. `docs/openapi.yaml` dan halaman API docs diperbarui.

### 3.4 Pengaturan admin

`PUT /api/v1/admin/merchants/{id}/fee-bearer` dengan body `{"fee_bearer":"merchant"|"customer"}` hanya di grup rute `adminAPI` (di belakang `RequireAdmin` dan `apiRateLimiter`). Nilai lain → 400; merchant tidak ada → 404. **Tidak** menambahkan field ini ke `UpdateMerchantRequest`, karena struct itu juga dipakai endpoint bisnis milik merchant sendiri (merchant akan bisa mengubahnya sendiri).

### 3.5 Front-end

- Admin: pilihan "Fee ditanggung" (Merchant/Customer) beserta penjelasan dan contoh angka di halaman detail merchant (memanggil endpoint 3.4); daftar dan detail payment menampilkan `fee_amount` dan `total_amount`.
- Merchant: daftar dan detail payment menampilkan nominal, fee, total; halaman bisnis menampilkan pengaturan fee (hanya baca).
- Checkout publik (`/pay/[reference]`, `/l/[slug]`): rincian nominal + fee = total yang harus dibayar bila fee > 0; tampilan lama bila fee 0.

## 4. Di Luar Cakupan

Fee tambahan WhuzPay, tarif yang dapat diubah dari panel (tetap konstanta, berubah lewat rilis), laporan "total fee terkumpul", penyesuaian batas maksimum Cashi (total kotor Cashi dibatasi 10.000.000; nominal mendekati batas itu + fee bisa ditolak Cashi sebagai error provider), serta fee berbeda per metode pembayaran.

## 5. Rilis

Migration 020 aditif tanpa perubahan perilaku; semua merchant tetap `merchant` sampai admin mengubahnya. Backend dan front-end di-deploy bersamaan. Verifikasi sesudah mengaktifkan satu merchant: satu pembayaran uji nyata (mis. nominal Rp 2.000 → total Rp 2.015) dan bandingkan angka net di dashboard Cashi — seharusnya Rp 2.000 (atau 2.001 bila Cashi membulatkan fee ke bawah); ini mengonfirmasi arah pembulatan dan sekaligus menuntaskan pengecekan nominal Cashi yang tertunda dari Paket 1. Langkah ini ditulis di DEPLOY.md bagian baru.

## 6. Pengujian

- `internal/fee`: untuk nominal 1…200.000 dengan bps 70, `total − ceil(total×70/10000) ≥ nominal` dan minimal (total−1 tidak memenuhi); contoh 2.000 → 2.015/15; input tidak valid; bps 0 → fee 0.
- Service: `customer` (nominal, fee, total benar; kuotasi per kandidat saat failover; provider tanpa `FeeQuoter` ditolak; `charged < nominal` ditolak; respons provider yang berbeda dari kuotasi tercermin di `fee_amount`); `merchant` tidak berubah; verifikasi dengan fee (check-status, webhook, reconcile, terlambat); idempotensi replay.
- Handler/antarmuka: field respons dan payload callback; endpoint admin (400/404/200, ditolak untuk JWT merchant, rate limit).
- Sandbox: tanpa akhiran acak, fee sesuai aturan.
- Migration 020: test PostgreSQL asli (default, CHECK, idempoten, payment lama tetap fee 0).
- Front-end: `npm run lint` dan `npm run build`.
- Mutasi untuk membuktikan test membedakan.

## 7. Risiko

Arah pembulatan Cashi belum terbukti (diasumsikan ke atas; salah paling banyak Rp 1 ke arah merchant menerima sedikit lebih) — diverifikasi pada pembayaran uji. Perubahan arti total memengaruhi verifikasi uang, sehingga seluruh jalur verifikasi harus lolos test dan review ketat. Menghapus akhiran sandbox mengubah nominal yang tampil di sandbox (test yang bergantung padanya diperbarui).
