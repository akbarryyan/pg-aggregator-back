# Payment State Machine

Status kanonik payment (`internal/domain/payment/payment.go`) yang semua
provider adapter memetakan status mereka sendiri ke sana
(`PaymentProvider.NormalizeStatus`). Dokumen ini menjelaskan status apa saja
yang valid, transisi mana yang diizinkan, dan kode mana yang menegakkannya —
sebelumnya hanya tersirat dari kode, tidak didokumentasikan di mana pun.

## Status

| Status | Terminal? | Arti |
|---|---|---|
| `pending` | Tidak | Payment dibuat, menunggu pembayaran/settlement dari provider |
| `paid` | Ya | Dibayar & dikonfirmasi provider |
| `expired` | Ya, kecuali pembayaran terlambat (`expired → paid`) | Lewat `expires_at` tanpa dibayar |
| `failed` | Ya | Provider melaporkan gagal (mis. QR dibatalkan) |
| `cancelled` | Ya | Dibatalkan (mis. oleh merchant/admin) |

`IsTerminalStatus(status)` — `paid`, `expired`, `failed`, `cancelled`
(`expired` tetap dianggap terminal oleh fungsi ini; satu-satunya jalan keluar
dari status terminal adalah `expired → paid`, lihat di bawah).

## Diagram Transisi

```
                    ┌─────────┐
        ┌──────────▶│  paid   │  (final)
        │           └─────────┘
        │                ▲
        │                │ pembayaran terlambat
        │           ┌─────────┐
        ├──────────▶│ expired │  (terminal, kecuali ──▶ paid)
        │           └─────────┘
┌───────────┐
│  pending  │
└───────────┘
        │           ┌─────────┐
        ├──────────▶│ failed  │  (final)
        │           └─────────┘
        │
        │           ┌───────────┐
        └──────────▶│ cancelled │  (final)
                    └───────────┘
```

Satu-satunya status non-terminal adalah `pending`. Semua transisi berasal
dari `pending`, dengan satu pengecualian: `expired ──▶ paid` (pembayaran
terlambat — pelanggan benar-benar membayar setelah payment kedaluwarsa
secara lokal). Tidak ada jalan kembali ke `pending`, dan `paid`, `failed`,
`cancelled` benar-benar final.

## Aturan (ditegakkan di kode)

Sumber kebenarannya `payment.AllowedSourceStatuses(newStatus)`
([payment.go](../internal/domain/payment/payment.go)), daftar status asal
yang boleh pindah ke `newStatus`:

| Status tujuan | Boleh dari |
|---|---|
| `paid` | `pending`, `expired` |
| `expired` | `pending` |
| `failed` | `pending` |
| `cancelled` | `pending` |

`payment.CanTransitionTo(currentStatus, newStatus)` hanya mengecek apakah
`currentStatus` ada di daftar itu — jadi `currentStatus == newStatus`
selalu ditolak (ini yang dipakai mendeteksi webhook duplikat), dan status
tujuan tak dikenal tidak punya status asal.

Invarian: **hanya settlement yang dilaporkan provider yang boleh menulis
`paid`** (webhook yang terverifikasi atau reconcile). Tidak ada jalur
"mark paid" manual, sehingga `expired → paid` tidak bisa menghidupkan
payment yang ditinggalkan dengan sendirinya.

Aturan ini ditegakkan **dua kali** secara independen (defense-in-depth):

- **Service layer** — `PaymentService.ProcessWebhook` dan
  `PaymentService.ReconcilePayment`
  ([payment_webhook_service.go](../internal/service/payment_webhook_service.go),
  [payment_service.go](../internal/service/payment_service.go)) mengecek
  sebelum menulis.
- **Repository layer** — `PaymentRepository.UpdateStatus`
  ([payment_repository_update.go](../internal/repository/payment_repository_update.go))
  menjalankan **satu statement `UPDATE` bersyarat** yang mengembalikan
  status sebelumnya:

  ```sql
  UPDATE payments p SET status = $1, paid_at = $2, updated_at = $3
  FROM (SELECT id, status FROM payments WHERE id = $4 FOR UPDATE) prev
  WHERE p.id = prev.id AND p.status IN (<AllowedSourceStatuses>)
  RETURNING prev.status
  ```

  Karena `WHERE` hanya cocok dengan status asal yang sah, caller service
  baru yang lupa mengecek tetap tidak bisa menulis transisi ilegal. Bila
  beberapa caller berlomba (webhook, reconcile, expire job), yang kalah
  **menunggu di row lock** subquery `FOR UPDATE` (READ COMMITTED); setelah
  pemenang commit, ia mengecek ulang guard terhadap versi baris yang baru
  dan tidak cocok dengan baris apa pun. Hasilnya tepat satu pemenang, dan
  **hanya pemenang yang mengantre callback ke merchant**. Yang kalah
  mendapat `ErrPaymentAlreadyTerminal` (atau `ErrInvalidStatusTransition`
  bila status saat ini bukan terminal). Status sebelumnya (`prev.status`)
  dipakai untuk mengenali pembayaran terlambat (`expired → paid`). Bentuk
  CTE + `RETURNING` tidak dipakai karena PostgreSQL mengevaluasinya setelah
  baris berubah sehingga mengembalikan NULL.

## Verifikasi Settlement

Webhook hanya **pemicu**; **check-status ke provider adalah kebenarannya**.
Sebelum menulis `paid`, `ProcessWebhook` (dan reconcile) memanggil
`GetPaymentStatus` provider lalu menjalankan `verifyProviderPayment`
([payment_verify.go](../internal/service/payment_verify.go)). Dengan begitu
webhook palsu atau replay — bahkan yang ditandatangani dengan secret yang
bocor — tidak bisa menandai payment lunas.

- Provider harus melaporkan status `paid` (selain itu `ErrNotSettledAtProvider`).
- **Aturan nominal (gross):** `amount` dari check-status provider — dan
  `amount` dari webhook bila ada — harus sama dengan `payments.amount`.
  Nominal provider yang tidak diketahui ditolak (fail closed). Fee provider
  (mis. potongan 0,7% Cashi) dipotong saat settlement dan tidak mengubah
  nominal yang dibayar pelanggan, jadi yang dibandingkan selalu nominal
  gross.
- **Mismatch** ada dua jenis, dibedakan dari awalan pesannya:
  - **Mismatch di sisi provider** (`ErrAmountMismatch`): `amount`
    check-status tidak diketahui atau berbeda dari `payments.amount`,
    berapa pun nominal webhook-nya. Pesan diawali `amount_mismatch:`, mis.
    `amount_mismatch: expected 2000, provider 1500, webhook 1500`. Payment
    tidak berubah, event webhook dicatat `rejected`, dan webhook dibalas
    200 `ignored` (butuh manusia, bukan retry). Reconcile mencatat event
    `reconcile.amount_mismatch`. **Hanya jenis ini** yang mengeluarkan
    payment dari reconcile otomatis (filter
    `processing_error LIKE 'amount_mismatch%'`).
  - **Mismatch hanya di webhook** (`ErrWebhookAmountMismatch`): provider
    mengonfirmasi nominal yang tersimpan, tapi webhook mengklaim nominal
    lain. Pesan diawali `webhook_amount_mismatch:`, mis.
    `webhook_amount_mismatch: expected 2000, provider 2000, webhook 1986`.
    Webhook-nya ditolak dengan cara yang sama (event `rejected`, payment
    tidak berubah, 200 `ignored`), tetapi payment **tidak** dikeluarkan
    dari reconcile — awalan ini sengaja tidak cocok dengan filter `LIKE`
    di atas, karena check-status adalah kebenarannya dan reconcile masih
    boleh melunasinya.
- **`verification_failed`:** bila check-status gagal/timeout atau provider
  belum melaporkan settlement, event webhook dicatat dengan status
  `verification_failed` dan webhook dibalas **502** supaya provider
  mencoba lagi (reconcile adalah cadangannya).
- Setiap panggilan check-status dibatasi **timeout 5 detik**
  (`providerVerifyTimeout`), di bawah `WriteTimeout` server (15 detik).
  Callback ke merchant **tidak** dikirim di dalam request webhook: request
  hanya mengantre callback (lihat "Callback Merchant"), sehingga endpoint
  merchant yang lambat tidak lagi memperpanjang waktu webhook melewati
  `WriteTimeout`.
- **Pemilik payment:** webhook yang masuk lewat provider A untuk payment
  milik provider B (`payments.provider_name`, dibandingkan tanpa peduli
  huruf besar/kecil) ditolak dengan `ErrProviderMismatch` **sebelum**
  check-status dipanggil, event dicatat `rejected`, dan dibalas **400**.
  Hanya provider pemilik payment yang boleh mengonfirmasi settlement-nya.

## Callback Merchant

Pengiriman callback dipisah dari perubahan status payment.
`NotifyMerchantPaymentEvent`
([merchant_callback_service.go](../internal/service/merchant_callback_service.go))
hanya mengantre satu baris delivery berstatus `pending`; tidak ada HTTP
keluar di jalur webhook, reconcile, maupun expire job.

- **Worker:** job `deliver-merchant-callbacks` (tiap 5 detik,
  `CallbackDeliveryInterval`) menjalankan `DeliverMerchantCallbacks`: pertama
  menjadwalkan ulang delivery `failed` yang sudah jatuh tempo
  (`RequeueDueFailed`), lalu mengklaim maksimal 50 baris (`ClaimDue`) dan
  mengirimnya **berurutan**. Batch berhenti lebih awal bila melewati anggaran
  waktunya (`callbackBatchBudget`); baris yang sudah diklaim tetapi belum
  dikirim dikembalikan ke `pending` (`ReleaseClaimed`, juga saat shutdown)
  sehingga run berikutnya langsung mengirimnya. Panic saat
  mengirim satu baris ditangkap per baris dan tidak menghentikan batch.
  Callback sampai ke merchant paling lama ±5 detik setelah perubahan status
  (lebih lama bila antrean panjang atau endpoint merchant lambat).
- **Status delivery:** `pending → sending → success | failed` (`skipped`
  hanya dari pembersihan manual, lihat DEPLOY.md). Hasil dicatat lewat
  `recordDeliveryResult`; bila penulisan lengkap gagal untuk baris itu, ia
  menulis ulang sekali tanpa `response_body`/pesan detail agar baris tidak
  tertinggal `sending`. Body respons merchant disimpan maks. 4096 byte sebagai
  UTF-8 valid tanpa NUL.
- **Gagal memuat payment:** bila payment tidak ditemukan, aturan ketat
  (production) dipakai; error DB lain dianggap sementara: delivery `failed`
  dengan jadwal retry normal ("temporary error, will retry"), tanpa dikirim.
  Bila sudah di batas percobaan (tidak ada jadwal retry lagi), pesannya
  "temporary error, giving up after the last attempt".
- **Retry:** delivery `failed` dijadwalkan ulang 5 menit kemudian dan setiap
  percobaan baru membuat **baris baru** (`attempt_number + 1`), maksimal 5
  percobaan (`maxCallbackAttempts`).
- **Baris macet:** baris `sending` yang lebih dari 2 menit tidak diperbarui
  (mis. proses mati di tengah kirim) diklaim ulang oleh worker.
- **Retry manual admin** (`RetryMerchantCallback`) tetap **sinkron**: menyisipkan
  percobaannya sebagai `sending`, lebih dulu menghapus jadwal retry baris
  sumber, tidak pernah menulis ulang baris sumber, dan masih bisa mengirim
  ulang delivery yang sudah `success`.
- **Retry manual memakai URL terkini:** tujuan percobaan baru dicari ulang dari
  payment: `payments.callback_url` bila terisi, kalau tidak `merchants.webhook_url`.
  Bila payment tidak ditemukan atau keduanya kosong, dipakai `target_url`
  yang tersimpan di delivery. Error pencarian lain (mis. DB) dikembalikan
  tanpa mengubah apa pun (jadwal baris sumber tetap). URL baru tetap melewati
  validator dan klien aman. Dengan begitu merchant yang memperbaiki URL-nya
  bisa memulihkan callback yang gagal karena URL lama. Retry **otomatis**
  tetap menyalin `target_url` yang tersimpan.
- **Aturan URL:** `merchants.webhook_url` dan `payments.callback_url`
  divalidasi saat disimpan (400 bila ditolak): host publik, tanpa
  `user:password@`. `webhook_url` merchant **selalu** `https`; hanya
  `callback_url` sebuah payment yang boleh `http` biasa, dan hanya di sandbox. Aturan alamat (bukan `localhost`, bukan IP privat,
  loopback, link-local, atau metadata) berlaku di **semua** mode; payment
  sandbox hanya melonggarkan skema, yaitu `http` boleh. Alamat tujuan dicek
  lagi **saat koneksi** (mencegah DNS yang dialihkan ke IP internal).
  Redirect (3xx) **tidak diikuti** dan dihitung gagal, jadi merchant harus
  mendaftarkan URL final.
- **Domain kita sendiri ditolak:** host `app.url` dan `app.frontend_url`
  (serta subdomainnya, cocok per label: `evilexample.com` tidak ikut)
  ditolak di semua mode, karena resolve ke IP publik dan tidak tertangkap
  penjaga IP (`netsafe.SetBlockedHosts`, diisi sekali saat startup).
- **URL lama:** URL yang tersimpan sebelum aturan ini dan kini tidak valid,
  atau tujuan yang di-resolve ke alamat terblokir, membuat delivery `failed`
  permanen (tanpa retry).
- **`error_message`** yang tersimpan tidak memuat URL request maupun IP
  internal (lewat `netsafe.DescribeError`).
- **Jaminan pengiriman: at-least-once.** Callback bisa diterima **lebih dari
  sekali**: proses mati di tengah kirim, hasil kirim gagal dicatat, retry
  manual admin, atau delivery yang masuk jendela stale lalu diklaim ulang.
  Merchant harus men-deduplikasi berdasarkan (`payment_id`, `event`).
  Callback hanya hilang bila pengantreannya sendiri gagal, dan itu dicatat di
  log (ERROR) (selain gagal permanen yang tercatat: URL tidak valid/terblokir,
  atau batas 5 percobaan).
- **Indeks antrean:** migration `016_add_callback_queue_indexes.sql` menambah
  indeks parsial untuk retry jatuh tempo (`failed` + `next_retry_at`) dan
  antrean kirim (`pending`/`sending`).

## Context Setelah Commit

Begitu settlement terkonfirmasi (atau expiry akan ditulis), `UpdateStatus`,
pencatatan event webhook, dan pencatatan + pengiriman callback merchant
berjalan dengan context yang **dilepas dari pembatalan pemanggil**
(`detached` di
[payment_ctx.go](../internal/service/payment_ctx.go): nilai context —
mis. request id untuk log — tetap terbawa, tapi tidak ikut batal), dibatasi
`postCommitTimeout` 30 detik. Alasannya: `ProcessWebhook` berjalan di
`r.Context()`; bila provider memutus koneksi tepat setelah `UpdateStatus`
commit, tanpa ini event dan callback gagal tercatat — payment sudah `paid`,
merchant tidak pernah diberi tahu, dan reconcile tidak pernah mengunjungi
payment `paid` lagi, jadi kehilangannya permanen. Hal yang sama berlaku
untuk reconcile (jalur `paid` dan expire lokal) dan expire job (per payment)
saat shutdown.

## Masa Tenggang Expiry

Payment `pending` baru di-expire **5 menit setelah** `expires_at`
(`expireGracePeriod`), baik oleh expire job maupun oleh expire lokal di
reconcile. Alasannya: pelanggan yang membayar di detik-detik terakhir tidak
boleh membuat merchant menerima callback `expired` lalu disusul `paid`
terlambat; reconcile (tiap 2 menit) mendapat kesempatan melihat settlement
lebih dulu.

## Pembayaran Terlambat

Bila provider melaporkan settlement setelah payment sudah `expired`,
transisi `expired → paid` diizinkan (customer sungguh membayar).
`UpdateStatus` mengembalikan status sebelumnya `expired`, sehingga:

- Event webhook dicatat dengan tipe **`payment.paid.late`**
  (`webhookEventTypePaidLate`).
- Callback ke merchant tetap bertipe **`payment.paid`**, dengan field
  payload `late_payment: true`. Field `late_payment` (boolean) selalu ada
  di payload callback; nilainya `true` hanya untuk settlement terlambat.

## Siapa yang Memicu Transisi

| Trigger | Fungsi | Kapan |
|---|---|---|
| Provider webhook | `PaymentService.ProcessWebhook` | Provider (mis. Cashi) POST ke `/api/v1/provider-webhooks/{providerName}`; hanya memicu verifikasi, lihat di atas |
| Reconcile manual/admin | `PaymentService.ReconcilePayment` | Admin poll status ke provider, atau `CheckPaymentStatus` |
| Reconcile batch | `PaymentService.ReconcilePendingPayments` | Admin trigger cek massal payment pending |
| Reconcile otomatis | `PaymentService.ReconcileRecentPayments` (scheduler `reconcile-payments`, tiap 2 menit) | Payment produksi `pending` yang dibuat ≤24 jam lalu (`created_at`, `reconcilePendingWindow`) + `expired` yang deadline-nya lewat ≤6 jam lalu (`expires_at`, `reconcileExpiredWindow` — settlement QRIS lebih lambat dari itu tidak masuk akal; sisanya lewat reconcile admin), kecuali yang punya event ber-awalan `amount_mismatch:` (mismatch sisi provider; `webhook_amount_mismatch:` tidak dikecualikan). Tiap run memeriksa ≤50 kandidat berurutan `created_at DESC, id DESC`; kursor `(created_at, id)` in-memory melanjutkan dari tempat run sebelumnya berhenti dan kembali ke yang terbaru bila run mengembalikan <50. Seluruh kandidat tercakup tiap ⌈N/50⌉ run; restart cukup mulai lagi dari yang terbaru |
| Expire job | `PaymentService.ExpirePayments` (scheduler, tiap 1 menit) | `pending` + `expires_at` sudah lewat lebih dari 5 menit (masa tenggang) → `expired`, tanpa perlu provider; mengantre callback `payment.expired` ke merchant |
| Expire lokal saat reconcile | Di dalam `ReconcilePayment` | Kalau `pending` + lewat `expires_at` lebih dari 5 menit (masa tenggang yang sama) dan provider belum melaporkan settlement (atau tidak bisa ditanya), expire lokal |

Catatan: **sandbox tidak pernah menerima webhook eksternal** — adapter
sandbox ([sandbox/adapter.go](../internal/provider/sandbox/adapter.go))
menolak semua `ParseWebhook`. Payment sandbox hanya berubah status lewat
reconcile/expire job, atau lewat helper dev `MarkPaid`/trik referensi
mengandung `"FORCEPAID"` (khusus testing manual, bukan jalur produksi).
Reconcile otomatis hanya menyentuh payment `production`.

## Idempotency Webhook

`ProcessWebhook` mengevaluasi dengan urutan ini sebelum memverifikasi dan
menulis:

1. **Provider bukan pemilik payment** → `ErrProviderMismatch`.
2. **Bukan settlement** (`webhookPayload.Status != paid`) →
   `ErrWebhookIgnored`.
3. **Payment sudah `paid`** → `ErrDuplicateWebhook`.
4. **Payment `failed`/`cancelled`** → `ErrPaymentAlreadyTerminal`.
   (`pending` dan `expired` lanjut ke verifikasi.)
5. Verifikasi settlement (lihat di atas): mismatch sisi provider →
   `ErrAmountMismatch`; mismatch hanya di webhook →
   `ErrWebhookAmountMismatch`; gagal konfirmasi →
   `ErrProviderVerificationFailed`.
6. `UpdateStatus` ke `paid` (dengan context yang dilepas, lihat "Context
   Setelah Commit"). Kalah balapan (`ErrPaymentAlreadyTerminal` dari
   repository) → `ErrDuplicateWebhook`; pemenang yang mengantre callback.

Pemetaan HTTP oleh
[webhook_handler.go](../internal/handler/webhook_handler.go):

| Hasil | HTTP |
|---|---|
| Sukses | 200 `"success"` |
| Duplikat / terminal / bukan settlement / amount mismatch (kedua jenis) | 200 `"ignored"` |
| Verifikasi provider gagal (`ErrProviderVerificationFailed`) | 502 |
| Lainnya (signature gagal, provider tak terdaftar, payload/order tidak dikenal, provider bukan pemilik payment) | 400 |

Kasus "ignored" sengaja 200, bukan error, supaya provider yang retry
webhook (perilaku umum semua payment gateway) tidak mendapat kode error
yang justru memicu retry lebih agresif; 502 sengaja agar provider mencoba
lagi. Lihat `TestWebhookHandler_HandleProviderWebhook_AcknowledgedAsIgnored`
dan `TestWebhookHandler_HandleProviderWebhook_ProviderMismatchIs400` di
[webhook_handler_test.go](../internal/handler/webhook_handler_test.go)
untuk cakupan test-nya.

## Menambah Status Baru

Kalau suatu saat perlu menambah status (mis. `refunded`), checklist supaya
konsisten:

1. Tambah konstanta `Status*` di `payment.go` + masukkan ke
   `IsValidStatus`.
2. Putuskan apakah terminal → update `IsTerminalStatus`.
3. Update `AllowedSourceStatuses` (status asal yang boleh pindah ke status
   baru, dan tambahkan status baru ke daftar asal status lain bila perlu).
4. Cek `webhookEventType`/`eventTypeForStatus` (
   [payment_webhook_service.go](../internal/service/payment_webhook_service.go),
   [merchant_callback_service.go](../internal/service/merchant_callback_service.go))
   kalau status baru perlu memicu notifikasi merchant.
5. Update dokumen ini.
