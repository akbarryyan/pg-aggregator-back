# Front-end Follow-up Paket 1 & 2 — Desain

Tanggal: 2026-11-03
Status: desain disetujui user; menunggu review spec tertulis
Lanjutan dari: `2026-10-30-payment-hardening-design.md` (Paket 1) dan `2026-11-03-callback-ssrf-async-design.md` (Paket 2)

## 1. Latar Belakang

Backend berubah di dua tempat yang terlihat dari front-end, tetapi front-end belum mengikutinya:

1. **Halaman API docs merchant** (`front/app/dashboard/api-docs/page.tsx`) belum menjelaskan: pembayaran terlambat (`expired → paid`, field `late_payment`), callback `payment.expired`, aturan URL `callback_url`/`webhook_url`, pengiriman callback asinkron yang bisa datang lebih dari sekali, dan redirect yang dihitung gagal.
2. **Form "Create payment" di panel admin** (`front/app/components/admin/CreatePaymentDrawer.tsx`) tidak mengirim `environment`. Sejak Paket 1, `environment` kosong berarti **sandbox**, sehingga semua payment dari admin otomatis sandbox dan admin tidak punya cara membuat payment production.

## 2. Keputusan (disetujui user)

| # | Keputusan |
|---|---|
| G1 | API docs tidak menjelaskan field `environment` (ditentukan oleh API key); perubahan hanya pada bagian yang sudah ada, tanpa halaman baru. |
| G2 | Form admin mendapat pilihan **Environment** (Sandbox / Production), nilai awal **Sandbox**, dengan peringatan kecil saat Production dipilih. |
| G3 | Tanpa dependensi baru, tanpa field `callback_url` di form admin, tanpa perubahan di dashboard merchant lain atau payment-links. |

## 3. Perubahan

### 3.1 API docs (`page.tsx`)

- **Create Payment** (tabel parameter): deskripsi `callback_url` menyebut aturan URL (https dan publik; di sandbox `http` boleh; localhost/alamat internal/domain kami ditolak; ditolak = HTTP 400).
- **Error Format**: baris `400` menyebut URL callback tidak valid.
- **Status Lifecycle**: `expired` bukan lagi terminal mutlak. Tambah kalimat tentang pembayaran terlambat: bila pelanggan membayar setelah `expires_at`, payment bisa berubah `expired → paid`, dan callback `payment.paid` membawa `late_payment: true`.
- **Webhooks**:
  - deskripsi: callback dikirim saat status berubah (bukan hanya "terminal"), dan `webhook_url` wajib https publik;
  - contoh payload: tambah `"late_payment": false`;
  - daftar `event`: tambah catatan bahwa `payment.paid` bisa datang setelah `payment.expired`;
  - blok pengiriman: callback tiba paling lama sekitar 5 detik setelah perubahan status; pengiriman at-least-once, jadi lakukan de-duplikasi pada (`payment_id`, `event`); redirect (3xx) dihitung gagal sehingga daftarkan URL akhir; retry tetap 5 kali dengan jeda 5 menit;
  - contoh Node.js: tangani `late_payment` dan idempotensi (balas 200 untuk event yang sudah diproses).

### 3.2 Form admin (`CreatePaymentDrawer.tsx`, `admin-api.ts`)

- `CreatePaymentPayload` mendapat `environment?: "sandbox" | "production"`.
- State `environment` (awal `"sandbox"`), dikirim di payload, di-reset ke `"sandbox"` oleh `resetForm`.
- Kontrol pilihan berupa `<select>` dengan gaya input yang sama seperti field Merchant, dengan label "Environment".
- Saat `production` dipilih, tampil catatan peringatan (warna oranye yang sudah dipakai halaman docs) bahwa payment production memakai provider sungguhan dan memindahkan uang.
- Baris ringkas "Method: QRIS · Currency: IDR" tidak berubah.

## 4. Di Luar Cakupan

Field `callback_url` di form admin, perubahan dashboard merchant lain, halaman payment-links, perubahan backend, test otomatis (front-end tidak punya test runner).

## 5. Verifikasi

`front/AGENTS.md` mewajibkan membaca dokumentasi Next.js di `node_modules/next/dist/docs/` sebelum menulis kode (versi 16.2.10 punya perubahan breaking). Verifikasi: `npm run lint` dan `npm run build` tanpa error baru; pemeriksaan visual melalui dev server bila memungkinkan, dilaporkan jujur bila tidak dijalankan.
