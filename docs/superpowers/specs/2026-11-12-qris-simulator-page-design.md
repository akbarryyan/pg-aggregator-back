# Halaman Simulator QRIS Sandbox — Desain

Tanggal: 2026-11-12
Status: rancangan disetujui user; menunggu review spec tertulis. Tanpa commit/push (aturan user).
Menggantikan bagian UI dari: `2026-11-12-sandbox-simulator-and-test-history-design.md` (§5). Backend simulator (§4, `SimulateSandboxOutcome` dan endpoint publik) dan migrasi 022 dari spec itu tetap berlaku.

## 1. Latar belakang

Simulator sandbox saat ini berupa tiga tombol di halaman checkout dan Test Payment. User ingin alur seperti simulator QRIS Midtrans: **halaman tersendiri**, tempel **URL gambar QR**, klik **Scan QR**, lalu pilih hasil simulasi.

QR sandbox sudah memuat reference payment: isi QR `sandbox:order=<reference payment>;amount=<n>;ref=<SBX-...>` (adapter sandbox). Gambarnya tersimpan sebagai `data:image/png;base64,...` di `payments.qris_data`, jadi belum punya URL.

## 2. Keputusan (disetujui user)

| # | Keputusan |
|---|---|
| K1 | Halaman Simulator **publik**, tanpa login: `{frontend}/simulator`. |
| K2 | Tombol simulasi di checkout dan Test Payment **dihapus**; diganti URL gambar QR yang bisa disalin dan tautan "Buka Simulator". |
| K3 | QR **dibaca di browser** (library `jsqr`); server tidak pernah mengambil URL kiriman pengguna. Selain URL, pengguna boleh **mengunggah file gambar QR**. |
| K4 | Payment sandbox mendapat URL gambar QR publik (`qr_image_url`). |

## 3. Backend

### 3.1 Gambar QR publik
`GET /api/v1/public/payments/by-reference/{reference}/qr.png`, di belakang `publicRateLimiter` (sama dengan GET by-reference).
- Hanya payment `environment = "sandbox"` dengan `qris_data` berawalan `data:image/png;base64,`: badan respons = PNG hasil decode base64, `Content-Type: image/png`, `Cache-Control: public, max-age=300`.
- Selain itu (tidak ditemukan, production, tanpa QR, data bukan PNG base64, base64 rusak) → 404 JSON `Payment QR not found`. Payment production tidak pernah disajikan lewat endpoint ini.
- CORS: route berada di router `/api/v1` yang sudah dibungkus middleware CORS (origin = `frontend_url`), sehingga `fetch` dari halaman Simulator berhasil. Implementasi memverifikasi bahwa GET dari origin front-end mendapat header `Access-Control-Allow-Origin`.

### 3.2 Field `qr_image_url`
- `PaymentService.QRImageURL(p *payment.Payment) string`: `"{app.url}/api/v1/public/payments/by-reference/{url-escaped reference}/qr.png"` untuk payment sandbox yang punya `qris_data` gambar, selain itu `""`. `app.url` adalah `cfg.App.URL` yang sudah dipakai untuk URL webhook provider.
- `payment.PaymentResponse` mendapat `QRImageURL string \`json:"qr_image_url,omitempty"\``; diisi oleh setiap handler yang mengembalikan `ToPaymentResponse` (API key create/get, dashboard merchant create/get/simulate, admin create, payment link checkout).
- Respons publik by-reference (`publicPaymentJSON`) mendapat `qr_image_url` (kosong → tidak dikirim) agar halaman checkout bisa menampilkannya.

### 3.3 Endpoint simulate
- `POST /public/payments/by-reference/{reference}/simulate` **tetap** (dipakai halaman Simulator).
- `POST /merchant/payments/{id}/simulate` **dihapus** (handler, route, tes, openapi), karena tidak ada lagi pemakai.

### 3.4 Tes
Handler: PNG 200 dengan byte yang sama dengan hasil decode untuk payment sandbox; 404 untuk production, tidak ditemukan, `qris_data` kosong, dan base64 rusak. `QRImageURL`: terisi untuk sandbox dengan QR, kosong untuk production/tanpa QR, reference di-escape. `qr_image_url` muncul di respons dashboard/API untuk sandbox dan tidak muncul untuk production. Router: route PNG tercapai tanpa auth; route simulate merchant tidak ada lagi (404/405). `openapi.yaml` diperbarui.

## 4. Front-end

### 4.1 Halaman `/simulator` (publik, teks Bahasa Inggris)
- Judul "QRIS Simulator" dan keterangan singkat: hanya untuk payment sandbox WhuzPay, tidak ada uang sungguhan.
- Input **QR code image URL** + tombol **Scan QR**; pilihan kedua **Upload QR image** (input file `image/*`).
- Proses scan: ambil gambar (`fetch` → `blob`, atau file unggahan) → gambar ke `<canvas>` → `jsQR` → teks QR. Galat yang ditampilkan di halaman (bukan hanya toast):
  - URL kosong/tidak valid → "Enter a valid image URL."
  - Gambar gagal diambil (jaringan/CORS/404) → "Could not load the image. Check the URL."
  - Tidak ada QR terbaca → "No QR code found in this image."
  - Isi QR bukan `sandbox:order=...` → "This is not a WhuzPay sandbox QR code."
- Berhasil: ambil detail lewat `fetchPublicPaymentByReference(reference)` dan tampilkan kartu: merchant, total bayar, reference, status (badge), batas waktu.
- Status `pending` → tiga tombol **Pay**, **Fail**, **Expire** (memanggil `simulatePublicPayment(reference, "paid" | "failed" | "expired")`); selama proses tombol nonaktif. Sesudahnya kartu diperbarui dan tampil pesan "Payment {reference} is now {status}." Status lain → tanpa tombol, dengan keterangan "This payment is no longer pending."
- Tombol **Scan another QR** mengosongkan hasil.
- URL halaman boleh membawa `?qr=<url>` untuk mengisi input otomatis (dipakai tautan dari checkout/Test Payment), tanpa scan otomatis.
- Responsif: satu kolom di HP, lebar maksimum ±560 px, tanpa gulir horizontal di 375 px.
- Dependensi baru: `jsqr` (versi stabil terbaru, dependensi `front/package.json`).

### 4.2 Checkout `/pay/[reference]` (payment sandbox, status pending)
Kotak tombol simulasi dihapus. Diganti kotak kuning kecil: "Mode sandbox · uji pembayaran lewat Simulator", tombol **Salin URL QR** (menyalin `qr_image_url`) dan tautan **Buka Simulator** (`/simulator?qr=<qr_image_url>`, tab baru). Teks Bahasa Indonesia.

### 4.3 Test Payment (mode sandbox)
Tombol simulasi dihapus. Untuk payment sandbox yang tampil: URL gambar QR (teks terpotong + tombol **Copy**) dan tautan **Open Simulator** (`/simulator?qr=...`, tab baru). Riwayat dan polling status tetap seperti sekarang, sehingga hasil simulasi dari tab Simulator tampil otomatis.

### 4.4 Pembersihan
Hapus `front/app/components/SandboxSimulator.tsx` dan `simulateMerchantPayment` di `lib/merchant-api.ts`. `simulatePublicPayment` (dengan pesan galat yang sudah ada) dipakai halaman Simulator; pesan galatnya diubah ke Bahasa Inggris karena halaman Simulator berbahasa Inggris.

## 4.5 Koleksi Bruno (`back/bruno`)
Terakhir diperbarui 2026-10-29; ditambahkan atas permintaan user sebagai task tambahan.
- `environments/Local.bru`: `base_url` → `http://localhost:8090` (port API lokal sekarang); variabel lain tetap (kredensial seed dev). Tambah `environments/Production.bru`: `base_url: https://api.whuzpay.web.id`, kredensial dan API key lewat `vars:secret` (nilai tidak pernah ditulis ke file).
- Susun ulang per folder (setiap folder punya `folder.bru`), request lama dipindah tanpa mengubah perilakunya: **Auth** (admin login/me, merchant login/me), **Merchant API (API key)** (`POST /payments` dengan `X-API-Key` + `Idempotency-Key`, `GET /payments/{id}`, `GET /payments/{id}/status`), **Checkout (public)** (`GET /public/payments/by-reference/{ref}`, `GET .../qr.png`), **Sandbox Simulator** (`POST /public/payments/by-reference/{ref}/simulate` untuk paid/failed/expired), **Payment Links** (request 06–10 yang ada), **Merchant Dashboard** (payments list/detail/create, balance, balance/daily, ledger, payouts list/request, payment-links/stats, dashboard summary), **Admin** (create/get payment yang ada, payments list, merchants list/create + stats, payouts list/stats/pay/reject, adjustments, callbacks/logs stats, reconciliation).
- Setiap request: `docs` singkat (tujuan, auth, variabel yang dipakai/diisi), `tests` minimal untuk kode status, dan `script:post-response` yang menyimpan id/reference/token ke variabel untuk request berikutnya (pola request 01/02 yang ada).
- Tidak ada rahasia sungguhan di file: hanya kredensial seed dev yang sudah ada di `Local.bru`.
- Verifikasi: setiap path dan metode dicocokkan dengan route di `cmd/api/main.go`; bila Bruno CLI (`bru`) tidak tersedia, tidak dijalankan terhadap server.

## 5. Rilis
Backend dulu (versi minor berikutnya, tag `x-api-image` disesuaikan, `make migrate` untuk migrasi 022 bila belum), lalu front-end (versi minor berikutnya). Front-end baru dengan backend lama: Simulator gagal memuat gambar karena endpoint PNG belum ada; halaman lain tidak terpengaruh.

## 6. Di luar cakupan
Membaca QR production, scan lewat kamera, dan simulasi refund.

## 7. Risiko
Siapa pun yang tahu URL gambar QR sandbox dapat mensimulasikannya (sama seperti sebelumnya: hanya sandbox, kena rate limit). URL gambar QR membocorkan reference payment, yang memang sudah publik di URL checkout.
