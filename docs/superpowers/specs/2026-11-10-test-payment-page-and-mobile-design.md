# Halaman Test Payment dan Responsive Mobile — Desain

Tanggal: 2026-11-10
Status: desain disetujui user per bagian (halaman test; responsive mobile); menunggu review spec tertulis
Cakupan: hanya `front/` (tanpa perubahan backend). Tanpa commit/push (aturan user).

## 1. Latar Belakang

Tombol `+` di halaman Payments (`front/app/dashboard/payments/page.tsx`, `handleQuickCreate`) membuat payment tetap Rp 15.000 dan hanya menampilkan toast referensi; QR tidak ditampilkan, sehingga payment production tidak bisa dibayar dari situ. Cashi mengembalikan URL gambar QR (`qris_data`), dan halaman checkout publik `/pay/[reference]` sudah memuat pola tampil QR + polling status. Sidebar dashboard (`MerchantSidebar`, `AdminSidebar`) berlebar tetap (260/72 px) tanpa mode laci di layar kecil, sehingga seluruh panel tidak nyaman di HP.

## 2. Keputusan (disetujui user)

| # | Keputusan |
|---|---|
| T1 | Halaman baru `/dashboard/test-payment` (menu sidebar "Test Payment"): form + QR + status langsung + riwayat test. |
| T2 | Nominal default **2.000** (bukan 1.000). |
| T3 | Tombol `+` di halaman Payments dihapus tanpa pengganti (`handleQuickCreate`, state `creating`, tooltip, impor yang tak terpakai). |
| T4 | Production: banner merah "PRODUCTION: uang sungguhan" + dialog konfirmasi sebelum membuat. Sandbox: tanpa konfirmasi. |
| M1 | Responsive mencakup **dashboard merchant dan panel admin** (cangkang + semua halaman). Landing page tidak disentuh. |

## 3. Halaman Test Payment

**Rute/berkas:** `front/app/dashboard/test-payment/page.tsx`; navigasi di `MerchantSidebar` (`navItems`), ikon dari `app/components/admin/icons`.

**Form:** `amount` (digit saja, bilangan bulat, default `2000`, min 1, maks 1.000.000.000.000 mengikuti batas server), `description` (default "Test payment", maks sesuai batas server). Environment dari `useMerchantEnvironment()`. Pembuatan memakai `createMerchantPayment({amount, description, payment_method: "qris", customer_name: "Dashboard test", environment})`. `customer_name = "Dashboard test"` adalah penanda riwayat (lihat di bawah).

**Production:** banner merah di atas form saat `environment === "production"`. Tombol Create membuka dialog konfirmasi (komponen dialog yang sudah ada di area merchant/admin: `AlertDialog` dari `components/ui`, pola `ConfirmedActionDialog`) berisi nominal, environment, dan catatan bahwa fee/total pasti dihitung server dan tampil setelah dibuat. Sandbox: langsung membuat.

**Hasil (halaman yang sama):** gambar QR (`qris_data`; bila bukan gambar/URL, tampil teks seperti `/pay/[reference]`), referensi + tombol salin, rincian `amount`, `fee_amount`, `total_amount` (dari respons server, tanpa hitung ulang di klien), hitung mundur ke `expires_at`, tautan ke `/pay/[reference]`. Status diperbarui otomatis dengan polling `GET /api/v1/merchant/payments/{id}` (fungsi API yang sudah ada atau tambahan tipis di `lib/merchant-api.ts`) tiap 3 detik sampai status akhir (`paid`, `expired`, `failed`, `cancelled`, `refunded`); polling berhenti di unmount dan pada status akhir, tidak menumpuk (satu timer aktif, guard terhadap respons terlambat setelah ganti payment). `paid` → kartu hijau (nominal, waktu bayar); `expired`/`failed` → pesan + tombol "New test payment". Tombol "New test payment" mengosongkan hasil dan mengembalikan form.

**Riwayat:** 10 payment terakhir environment aktif dengan `search=Dashboard test` lewat `fetchMerchantPayments` yang ada (pencarian mencakup `customer_name`): waktu, nominal, status, referensi; baris `pending` yang belum kedaluwarsa punya tombol "Show QR" yang memuat ulang kartu hasil untuk payment itu. Muat ulang setelah pembuatan dan setelah status akhir. Efek samping yang diterima: nama pelanggan "Dashboard test" tampil juga di halaman Payments.

**Penanganan galat:** pesan server ditampilkan inline (503 provider tak tersedia / 504 timeout / 502 galat provider / 400 validasi) tanpa menghapus isian form; tombol dinonaktifkan selama permintaan berjalan dengan guard `useRef` terhadap klik ganda.

## 4. Responsive Mobile

**Cangkang** (`MerchantShell`, `AdminShell`, `MerchantSidebar`, `AdminSidebar`, `MerchantHeader`, `AdminHeader`): di bawah breakpoint `lg` sidebar menjadi laci overlay (posisi fixed, geser dari kiri, lapisan gelap). Tombol menu header membuka/menutup laci di mobile dan tetap mengciutkan sidebar di desktop. Laci tertutup saat navigasi (perubahan `pathname`), ketuk lapisan gelap, dan tombol Esc; gulir `body` dikunci selama terbuka; fokus dan atribut ARIA dasar (`aria-expanded`, `aria-modal`, label tombol). Pada layar sempit, elemen header sekunder disembunyikan/diringkas agar tidak meluber.

**Halaman:** setiap halaman merchant (13) dan admin (13) serta halaman publik `/login`, `/register`, `/pay/[reference]`, `/l/[slug]` diperiksa pada ±375 px: bar filter/aksi membungkus, tabel lebar tetap di dalam area gulir (`TableShell`) dengan min-width yang pas, grid/kartu/grafik menumpuk satu kolom, dialog dan drawer muat lebar layar, target ketuk memadai. Hanya tata letak: tanpa perubahan fungsi, warna, atau isi; landing page tidak disentuh.

**Verifikasi:** tidak ada browser yang bisa dipakai agen; verifikasi lewat pembacaan kode (kelas breakpoint, lebar tetap, overflow), `npm run lint`, dan `npm run build`. Tampilan akhir dicek user di HP.

## 5. Di Luar Cakupan

Perubahan backend; kolom/tag khusus untuk payment uji; landing page; mode gelap; perubahan desain visual; pengujian otomatis tampilan.

## 6. Risiko

Tidak ada uji visual otomatis, jadi regresi tata letak di desktop mungkin lolos; mitigasi: breakpoint hanya menambah perilaku di bawah `lg` dan tidak mengubah kelas desktop kecuali perlu. Nama pelanggan "Dashboard test" mencemari kolom itu di daftar Payments. Test payment production adalah transaksi nyata yang tercatat di ledger saat dibayar (saldo naik); refund dan pencairan berlaku seperti payment biasa.
