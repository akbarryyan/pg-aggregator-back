# Paket 2: Validasi URL (SSRF) dan Callback Asinkron — Desain

Tanggal: 2026-11-03
Status: menunggu review user
Lanjutan dari: `2026-10-30-payment-hardening-design.md` (Paket 1)

## 1. Latar Belakang

Dua masalah yang tersisa dari Paket 1 (item 4 dan 6 pada daftar hardening):

1. **SSRF.** `merchants.webhook_url` dan `payments.callback_url` tidak pernah divalidasi. Callback dikirim dengan `http.Client` biasa, sehingga merchant bisa mengisi alamat internal (`http://10.89.0.x`, `http://169.254.169.254`, `localhost`) dan server yang memanggilnya. Redirect juga diikuti otomatis.
2. **Callback sinkron.** `NotifyMerchantPaymentEvent` mengirim HTTP (timeout 10 detik) di dalam penanganan webhook Cashi, reconcile, dan expire. Merchant yang lambat menahan semuanya, dan satu webhook bisa melewati `WriteTimeout` 15 detik. Baris delivery dibuat `pending` sebelum dikirim, dan retry hanya mengambil `failed`, sehingga baris `pending` yang yatim (proses mati di tengah jalan) tidak pernah terkirim.

## 2. Keputusan (disetujui user)

| # | Keputusan |
|---|---|
| E1 | URL wajib `https` dan menunjuk alamat publik. Pengecualian: payment/callback berenvironment **sandbox** boleh `http` dan alamat lokal. |
| E2 | Pertahanan dua lapis: validasi saat disimpan (400) dan pengecekan IP saat koneksi dibuat (menutup DNS rebinding). Redirect tidak diikuti. |
| E3 | URL tersimpan yang tidak valid tidak diubah; saat dikirim delivery ditandai `failed` tanpa retry. |
| E4 | Pengiriman asinkron lewat antrean di database + worker. Tanpa migration dan tanpa tabel baru. |
| E5 | Retry manual dari admin tetap sinkron. |

## 3. Di Luar Cakupan

Migration, perubahan front-end (termasuk halaman API docs merchant), perubahan alur verifikasi webhook Cashi, allowlist domain per merchant, pencatatan IP keluar.

## 4. Validasi URL (E1–E3)

### 4.1 Paket baru `internal/netsafe`

Satu paket kecil, hanya pustaka standar:

- `ValidateCallbackURL(raw string, allowInsecure bool) error` — aturan penyimpanan.
- `IsBlockedIP(ip net.IP) bool` — dipakai validator dan dialer.
- `NewSafeClient(allowInsecure bool, timeout time.Duration) *http.Client` — client pengiriman.

`allowInsecure` bernilai true hanya untuk environment sandbox.

### 4.2 Aturan penyimpanan (`ValidateCallbackURL`)

Ditolak dengan error tipe `ErrInvalidCallbackURL` (pesan tidak memuat nilai URL lengkap):

- Skema bukan `https` (kecuali `allowInsecure`, yang juga menerima `http`).
- Mengandung `user:password@`.
- Host kosong, atau `localhost`/`*.localhost`, atau host tanpa titik (kecuali `allowInsecure`).
- Host berupa literal IP yang `IsBlockedIP`, termasuk bentuk desimal/heksadesimal/oktal yang menyamar sebagai IP dan IPv4-mapped IPv6.
- Panjang URL lebih dari 2048 karakter.
- Port selain 443 diizinkan (merchant sering memakai port lain) tetapi tetap tunduk pada aturan IP.

Validasi saat simpan tidak melakukan resolve DNS (tidak ada I/O di jalur request); resolve dicek saat koneksi (4.3).

### 4.3 Dialer aman (`NewSafeClient`)

- `net.Dialer.Control` memeriksa IP tujuan setiap koneksi tepat sebelum connect, dan menolak bila `IsBlockedIP`. Pemeriksaan di level koneksi menutup DNS rebinding dan semua hop DNS.
- `CheckRedirect` mengembalikan `http.ErrUseLastResponse`: respons 3xx tidak diikuti dan diperlakukan sebagai gagal (status non-2xx).
- Proxy dari environment dimatikan (`Transport.Proxy = nil`) supaya pengecekan IP tidak dilewati.
- Timeout total tetap 10 detik.
- Saat `allowInsecure`, pemeriksaan IP dilewati dan `http` diizinkan.

### 4.4 `IsBlockedIP`

Memblokir: unspecified, loopback, private (RFC 1918 dan `fc00::/7`), link-local unicast/multicast (`169.254.0.0/16`, `fe80::/10`, termasuk metadata `169.254.169.254`), multicast, `100.64.0.0/10` (CGNAT), `192.0.0.0/24`, `192.0.2.0/24`, `198.18.0.0/15`, `198.51.100.0/24`, `203.0.113.0/24`, `240.0.0.0/4`, `255.255.255.255`, serta IPv4-mapped/NAT64 yang membungkus alamat terblokir.

### 4.5 Titik pemasangan validasi

Semua jalur yang menulis `merchants.webhook_url` dan `payments.callback_url` memanggil `ValidateCallbackURL` dan memetakan `ErrInvalidCallbackURL` ke HTTP 400:

- `AdminMerchantService` create dan update merchant (`admin_merchant_service.go`, dua blok trim `WebhookURL`).
- Jalur dashboard/profil merchant yang memperbarui `webhook_url` (tersambung ke `MerchantRepository.Update`) — implementer menelusuri semua penulis lewat `grep WebhookURL` dan mendaftarkannya di laporan.
- `CreatePaymentRequest.Validate` untuk `CallbackURL` (sesudah `ParseEnvironment`, sehingga `allowInsecure = (Environment == sandbox)`).
- Untuk `merchants.webhook_url` (tidak terikat environment), `allowInsecure = false`; pengecualian sandbox hanya berlaku bila payment-nya sandbox, dan diterapkan lagi saat pengiriman (5.4).

### 4.6 Saat pengiriman

Sebelum HTTP, `ValidateCallbackURL(d.TargetURL, allowInsecure)` dijalankan ulang (menangkap URL lama yang tersimpan sebelum Paket 2). Bila gagal: delivery `failed`, `error_message` berisi alasan, `next_retry_at = NULL` (tanpa retry). `allowInsecure` diturunkan dari environment payment (lihat 5.4).

## 5. Callback Asinkron (E4, E5)

### 5.1 Alur

- `NotifyMerchantPaymentEvent` hanya menentukan URL, menyusun payload (isi tidak berubah, termasuk `late_payment`), dan `callbackRepo.Create` baris `pending`. Tidak ada HTTP. Fungsi kembali seketika. Konteks tetap memakai `detached` dari Paket 1 di pemanggil.
- Job baru `deliver-merchant-callbacks` menggantikan `retry-merchant-callbacks`: tiap **5 detik**, maksimal **50** delivery per putaran.

### 5.2 Klaim baris (tanpa dua pengirim)

`MerchantCallbackRepository.ClaimDue(ctx, now, limit, staleAfter)`: satu statement

```sql
UPDATE merchant_callback_deliveries d
SET status = 'sending', updated_at = $1
WHERE d.id IN (
    SELECT id FROM merchant_callback_deliveries
    WHERE status = 'pending'
       OR (status = 'sending' AND updated_at <= $1 - $staleAfter)
       OR (status = 'failed' AND next_retry_at IS NOT NULL AND next_retry_at <= $1)
    ORDER BY created_at
    LIMIT $limit
    FOR UPDATE SKIP LOCKED
)
RETURNING <kolom delivery>
```

- Status baru `CallbackStatusSending = "sending"` (kolom `status` berupa teks; tanpa migration — implementer memastikan tidak ada CHECK constraint pada kolom itu lewat `migrations/008`).
- `staleAfter = 2 menit`: baris `sending` yang macet (proses mati) diambil lagi.
- Baris `failed` yang jatuh tempo dibuat menjadi **percobaan baru** (`attempt_number + 1`, baris `pending` baru) dan jadwal retry baris lama dihapus, mengikuti perilaku `retryMerchantCallbackData` sekarang (riwayat percobaan dipertahankan). Worker kemudian mengirim baris `pending` itu pada putaran yang sama atau berikutnya.

### 5.3 Pengiriman

`executeCallbackDelivery` dipertahankan (HMAC `X-PG-Signature`, batas `maxCallbackAttempts = 5`, jadwal retry 5 menit), dengan perubahan:

- Client dari `netsafe.NewSafeClient` (4.3), bukan `http.Client` polos.
- Validasi ulang URL (4.6).
- Hasil akhir selalu ditulis ke baris (`success`, atau `failed` dengan `next_retry_at`).
- Satu secret-cache per putaran (perilaku sekarang).

### 5.4 Environment untuk pengecualian sandbox

Delivery tidak menyimpan environment. `executeCallbackDelivery` memuat payment (`paymentRepo.GetByID(d.PaymentID)`) dan memakai `Environment == sandbox` sebagai `allowInsecure`. Bila payment tidak ditemukan, perlakukan sebagai production (ketat). Satu lookup per delivery; dengan batas 50 per 5 detik ini dapat diterima.

### 5.5 Retry manual admin (E5)

`RetryMerchantCallback` dari admin tetap sinkron: membuat percobaan baru dan langsung `executeCallbackDelivery` (sekarang melalui client aman), supaya admin melihat hasilnya langsung.

### 5.6 Shutdown

Worker berhenti mengklaim baris baru saat konteks dibatalkan; pengiriman yang sedang berjalan memakai `detached` (Paket 1) agar penulisan hasil tidak hilang. `main` menunggu job selesai dengan batas waktu singkat (WaitGroup) sebelum keluar, sehingga menutup catatan "shutdown tidak menunggu goroutine job" dari review akhir Paket 1. Baris yang tak sempat selesai otomatis diambil lagi lewat aturan `staleAfter`.

## 6. Pengujian

- **netsafe unit:** tabel `ValidateCallbackURL` (skema, user:password, IPv4/IPv6 privat, loopback, link-local, metadata, IPv4-mapped IPv6, `localhost`, host tanpa titik, desimal/heksadesimal, panjang, kasus `allowInsecure`); tabel `IsBlockedIP` per rentang.
- **Dialer:** server `httptest` di loopback ditolak untuk client ketat dan diterima untuk `allowInsecure`; respons 3xx tidak diikuti; resolver palsu yang mengembalikan IP internal untuk domain publik ditolak saat connect.
- **Service:** `NotifyMerchantPaymentEvent` hanya membuat baris `pending` dan tidak melakukan HTTP (fake client/server menghitung 0 request); worker mengirim dan menandai `success`; gagal dijadwalkan retry dan membuat percobaan baru; URL tidak valid ditandai `failed` tanpa retry; payment sandbox boleh `http` ke server lokal; payment production tidak.
- **Repository:** sqlmock untuk bentuk SQL `ClaimDue`; **test PostgreSQL asli opsional** (`TEST_DATABASE_URL`, pola Paket 1) yang membuktikan: dua klaim bersamaan tidak mengembalikan baris yang sama, baris `sending` yang macet diambil lagi, `failed` yang belum jatuh tempo tidak diambil.
- **Handler:** URL tidak valid di setiap pintu masuk (4.5) menghasilkan 400.
- **Shutdown:** job yang sedang berjalan selesai (atau habis waktu) sebelum `main` keluar.

## 7. Dokumentasi

`docs/payment-state-machine.md` (alur callback asinkron dan status `sending`), `docs/openapi.yaml` (aturan URL dan respons 400 di endpoint terkait), `README.md` (Background Jobs: `deliver-merchant-callbacks`).

## 8. Rilis dan Perubahan Perilaku

- Tanpa migration. Dirilis bersama atau setelah `1.0.1`; keputusan waktu rilis menyusul setelah urusan VPS.
- Catatan rilis: URL non-https atau internal untuk payment production sekarang ditolak (400); callback datang paling lama sekitar 5 detik setelah perubahan status; merchant dengan `webhook_url` lama yang tidak valid akan melihat delivery `failed` dengan pesan alasan, dan perlu memperbarui URL.
- Sebelum deploy: query pengecekan `webhook_url` dan `callback_url` yang tersimpan dan tidak lolos aturan baru, supaya merchant terdampak bisa diberi tahu.

## 9. Amandemen Saat Eksekusi (disetujui user)

| # | Tanggal | Mengganti | Keputusan |
|---|---|---|---|
| A8 | 2026-11-03 | E1, §4.2, §4.3 | Pengecualian sandbox hanya melonggarkan **skema** (`http` boleh). Aturan IP/host (privat, loopback, link-local, metadata, `localhost`) berlaku di semua mode, di validator **dan** dialer; `NewSafeClient` tidak lagi punya mode "insecure". Alasan: `localhost` merchant tidak terjangkau dari server kita, sedangkan pengecualian IP membuka SSRF lewat payment sandbox. Test lokal memakai server `httptest` lewat seam internal, bukan lewat pelonggaran produksi. |
| A9 | 2026-11-03 | §5.3 | `error_message` delivery disimpan lewat `netsafe.DescribeError`: URL permintaan (bisa memuat token) dan IP internal hasil resolve tidak ikut tersimpan. |
