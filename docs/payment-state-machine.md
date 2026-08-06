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
| `expired` | Ya | Lewat `expires_at` tanpa dibayar |
| `failed` | Ya | Provider melaporkan gagal (mis. QR dibatalkan) |
| `cancelled` | Ya | Dibatalkan (mis. oleh merchant/admin) |

`IsTerminalStatus(status)` — `paid`, `expired`, `failed`, `cancelled`.
Begitu payment masuk status terminal, **tidak ada transisi lain yang
diizinkan**, termasuk ke status terminal lain.

## Diagram Transisi

```
                    ┌─────────┐
        ┌──────────▶│  paid   │  (terminal)
        │           └─────────┘
        │
        │           ┌─────────┐
        ├──────────▶│ expired │  (terminal)
        │           └─────────┘
┌───────────┐
│  pending  │
└───────────┘
        │           ┌─────────┐
        ├──────────▶│ failed  │  (terminal)
        │           └─────────┘
        │
        │           ┌───────────┐
        └──────────▶│ cancelled │  (terminal)
                    └───────────┘
```

Satu-satunya status non-terminal adalah `pending`. Semua transisi valid
berasal dari `pending`; tidak ada jalan kembali ke `pending`, dan tidak ada
transisi antar status terminal.

## Aturan (ditegakkan di kode)

Logika ada di `payment.CanTransitionTo(currentStatus, newStatus)`
([payment.go](../internal/domain/payment/payment.go)):

1. `currentStatus == newStatus` → **selalu ditolak** (bukan transisi — ini
   yang dipakai untuk mendeteksi webhook duplikat, lihat di bawah).
2. Jika `currentStatus` sudah terminal → **selalu ditolak**.
3. Jika `currentStatus == pending` → diizinkan hanya ke `paid`, `expired`,
   `failed`, atau `cancelled`.
4. Selain itu → ditolak.

Aturan ini ditegakkan **dua kali** secara independen (defense-in-depth):

- **Service layer** — `PaymentService.ProcessWebhook` dan
  `PaymentService.ReconcilePayment`
  ([payment_webhook_service.go](../internal/service/payment_webhook_service.go),
  [payment_service.go](../internal/service/payment_service.go)) mengecek
  sebelum menulis.
- **Repository layer** — `PaymentRepository.UpdateStatus`
  ([payment_repository_update.go](../internal/repository/payment_repository_update.go))
  mengecek ulang sebelum menjalankan `UPDATE`, sehingga caller service baru
  di masa depan yang lupa mengecek tetap tidak bisa menulis transisi
  ilegal.

## Siapa yang Memicu Transisi

| Trigger | Fungsi | Kapan |
|---|---|---|
| Provider webhook | `PaymentService.ProcessWebhook` | Provider (mis. Cashi) POST ke `/api/v1/provider-webhooks/{providerName}` |
| Reconcile manual/admin | `PaymentService.ReconcilePayment` | Admin poll status ke provider, atau `CheckPaymentStatus` |
| Reconcile batch | `PaymentService.ReconcilePendingPayments` | Admin trigger cek massal payment pending |
| Expire job | `PaymentService.ExpirePayments` (scheduler, tiap 1 menit) | `pending` + `expires_at` sudah lewat → `expired`, tanpa perlu provider |
| Expire lokal saat reconcile | Di dalam `ReconcilePayment` | Kalau `pending` + lewat `expires_at`, expire lokal duluan sebelum/tanpa polling provider |

Catatan: **sandbox tidak pernah menerima webhook eksternal** — adapter
sandbox ([sandbox/adapter.go](../internal/provider/sandbox/adapter.go))
menolak semua `ParseWebhook`. Payment sandbox hanya berubah status lewat
reconcile/expire job, atau lewat helper dev `MarkPaid`/trik referensi
mengandung `"FORCEPAID"` (khusus testing manual, bukan jalur produksi).

## Idempotency Webhook

`ProcessWebhook` membedakan tiga kasus sebelum mengecek `CanTransitionTo`:

1. **Payment sudah terminal** (`IsTerminalStatus(p.Status)`) →
   `ErrPaymentAlreadyTerminal`.
2. **Status webhook sama dengan status payment saat ini**
   (`p.Status == webhookPayload.Status`) → `ErrDuplicateWebhook` (biasanya
   berarti provider mengirim webhook yang sama dua kali).
3. Selain itu → jalankan `CanTransitionTo`; kalau gagal →
   `ErrInvalidStatusTransition`.

Kasus 1 dan 2 **direspons HTTP 200 `"ignored"`** oleh
[webhook_handler.go](../internal/handler/webhook_handler.go), bukan error —
sengaja, supaya provider yang retry webhook (perilaku umum semua payment
gateway) tidak mendapat kode error yang justru memicu retry lebih agresif.
Lihat `TestWebhookHandler_HandleProviderWebhook_DuplicateIsIgnoredNot400`
di [webhook_handler_test.go](../internal/handler/webhook_handler_test.go)
untuk cakupan test-nya.

## Menambah Status Baru

Kalau suatu saat perlu menambah status (mis. `refunded`), checklist supaya
konsisten:

1. Tambah konstanta `Status*` di `payment.go` + masukkan ke
   `IsValidStatus`.
2. Putuskan apakah terminal → update `IsTerminalStatus`.
3. Tambah transisi yang valid ke/dari status baru di `CanTransitionTo`.
4. Cek `webhookEventType`/`eventTypeForStatus` (
   [payment_webhook_service.go](../internal/service/payment_webhook_service.go),
   [merchant_callback_service.go](../internal/service/merchant_callback_service.go))
   kalau status baru perlu memicu notifikasi merchant.
5. Update dokumen ini.
