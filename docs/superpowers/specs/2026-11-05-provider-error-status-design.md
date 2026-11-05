# Status HTTP untuk Galat Provider saat Membuat Payment — Desain

Tanggal: 2026-11-05
Status: keputusan pemetaan disetujui user (502/503/504 dibedakan); menunggu review spec tertulis
Lanjutan dari: butir parkir Paket 4 dan Paket 5 ("galat provider dijawab 500, bukan 502")

## 1. Latar Belakang

`respondCreatePaymentError` (`internal/handler/payment_handler.go`) sudah memetakan `ErrProviderNotAvailable` → 503 dan `payment.ErrProviderError` → 502, tetapi galat nyata dari Cashi tidak pernah membawa penanda itu:

- respons 4xx/5xx → `*CashiError` (tanpa `Unwrap`), `success:false` → `CashiError{400}`, order id kosong → `CashiError{502}`;
- timeout atau koneksi gagal → `fmt.Errorf("failed to execute request: %w", err)`;
- body tak terbaca atau JSON rusak → galat polos.

Semuanya jatuh ke cabang `default`: **500 "Failed to create payment"**. Hanya kasus "provider menagih kurang dari nominal" yang menghasilkan 502. Jalur admin (`AdminHandler.CreatePayment`) punya `switch` sendiri yang juga memberi 500. Akibatnya merchant tidak bisa membedakan "permintaan saya salah", "provider bermasalah, coba lagi", dan "bug di server kita".

Dicek di kode: tidak ada kode yang bercabang berdasarkan jenis galat provider selain pemetaan di handler (failover di `CreatePayment` hanya melihat `failoverEnabled`; verifikasi webhook/reconcile memperlakukan galat check-status secara generik). Jadi mengklasifikasikan galat di adapter tidak mengubah perilaku selain pemetaan HTTP.

## 2. Keputusan (disetujui user)

| # | Keputusan |
|---|---|
| E1 | Tiga status dibedakan: **502** Cashi menjawab dengan galat atau respons tidak valid; **504** Cashi tidak menjawab dalam batas waktu (10 dtk); **503** tidak ada provider tersedia atau Cashi tidak terjangkau (koneksi ditolak, DNS, dsb). |
| E2 | Pesan ke klien tetap generik; isi mentah respons Cashi hanya masuk log (ber-request-ID). |
| E3 | Satu pemetaan bersama untuk semua jalur pembuatan payment, termasuk admin. |

## 3. Perubahan

### 3.1 Sentinel baru
`internal/provider/errors.go`: `ErrProviderTimeout = errors.New("provider timeout")`.

### 3.2 Klasifikasi di adapter Cashi
- `(*CashiError).Unwrap() error` mengembalikan `providerPkg.ErrProviderAPIError`, sehingga respons 4xx/5xx, `success:false`, dan order id kosong menjadi 502.
- `doRequest`, kegagalan `httpClient.Do`: bila timeout (`errors.Is(err, context.DeadlineExceeded)` atau `net.Error` dengan `Timeout() == true`) → bungkus dengan `ErrProviderTimeout` (`fmt.Errorf("failed to execute request: %w: %w", providerPkg.ErrProviderTimeout, err)`); selain itu (koneksi ditolak, DNS, konteks dibatalkan, dsb) → bungkus dengan `ErrProviderNotAvailable`. Tepat satu sentinel per galat.
- Kegagalan membaca body dan `json.Unmarshal` → bungkus dengan `ErrProviderAPIError` (respons tidak valid).
- `GetPaymentStatus` memakai `doRequest` yang sama; klasifikasinya ikut berlaku tetapi perilaku pemanggil (verifikasi webhook dan reconcile) tidak berubah karena mereka tidak bercabang pada jenis galat (diuji sebagai regresi).

### 3.3 Pemetaan bersama
`respondCreatePaymentError` mendapat cabang, dengan urutan: `ErrProviderTimeout` → 504 "Payment provider timed out"; `ErrProviderAPIError` atau `payment.ErrProviderError` → 502 "Payment provider error"; `ErrProviderNotAvailable` → 503 (tetap). Cabang lain (403 merchant nonaktif, 400 metode tak didukung, 503 `ErrFeeNotSupported`) tetap. Galat yang dibungkus failover ("failed to create payment with available providers: %w") tetap terdeteksi lewat `errors.Is`.

### 3.4 Jalur admin
`switch` di `AdminHandler.CreatePayment`: cabang validasi 400 tetap; `default` memanggil `respondCreatePaymentError` (menggantikan 500 mentah dan cabang `ErrFeeNotSupported` khusus yang kini tercakup).

### 3.5 Dokumentasi
`docs/openapi.yaml`: respons 502/503/504 pada `POST /payments`, `POST /merchant/payments`, `POST /admin/payments` dan checkout payment link, dengan catatan bahwa mengulang dengan `Idempotency-Key` yang sama aman (klaim dilepas saat gagal). Tabel galat di halaman API docs front-end mendapat baris 502/503/504.

## 4. Di Luar Cakupan

Memetakan penolakan 4xx Cashi karena input (mis. nominal di bawah minimum Cashi) menjadi 400 untuk merchant (butuh aturan minimum Cashi yang terverifikasi; saat ini tetap 502 dengan detail di log); retry atau backoff otomatis; metrik/peringatan galat provider (Paket 7).

## 5. Pengujian

- Adapter Cashi: respons 4xx/5xx, `success:false`, order id kosong, body rusak → `errors.Is(err, ErrProviderAPIError)`; server uji yang lebih lambat dari timeout → `ErrProviderTimeout`; server yang ditutup (koneksi ditolak) → `ErrProviderNotAvailable`; tepat satu sentinel per kasus.
- `respondCreatePaymentError`: tabel galat → status dan pesan (termasuk galat yang dibungkus failover dan pesan generik tanpa isi respons Cashi).
- Per jalur: API key, dashboard merchant, admin, checkout payment link menghasilkan 502/503/504 yang benar (service dengan provider palsu yang mengembalikan tiap jenis galat).
- Regresi: kegagalan check-status pada webhook dan reconcile berperilaku persis seperti sebelumnya.
- Mutasi untuk membuktikan test membedakan (hapus `Unwrap`, tukar urutan cabang timeout/API).

## 6. Risiko

Pembungkusan ganda `%w: %w` memerlukan Go ≥ 1.20 (proyek Go 1.25, aman). `CashiError.Error()` memuat isi mentah respons Cashi sehingga ikut ke log; ia tidak pernah dikirim ke klien (pesan respons tetap tetap). Mengubah status 500 menjadi 502/503/504 dapat memengaruhi merchant yang menganggap 500 sebagai satu-satunya status "coba lagi"; ketiganya tetap kelas 5xx sehingga klien yang mengulang pada 5xx tetap berjalan.
