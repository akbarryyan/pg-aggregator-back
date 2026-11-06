# Saldo Merchant, Pencairan, dan Refund Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merchant punya saldo yang dihitung dari ledger tambah-saja, bisa mengajukan pencairan (diproses manual oleh admin), dan admin bisa mencatat refund serta penyesuaian saldo.

**Architecture:** Tabel `merchant_ledger` (hanya INSERT, dijaga trigger) adalah satu-satunya sumber saldo (`SUM(amount)`). Kredit ditulis atomik bersama perubahan status ke `paid` oleh method repository baru `MarkPaid`; pencairan, refund, dan penyesuaian ditulis dalam transaksi yang mengunci baris merchant atau payment. Spec: `back/docs/superpowers/specs/2026-11-05-merchant-balance-payout-refund-design.md`.

**Tech Stack:** Go 1.25, gorilla/mux, lib/pq, PostgreSQL 18 (migrasi SQL idempoten), Next.js 16 / React 19 / TS / Tailwind (tanpa test runner: verifikasi `npm run lint` + `npm run build`).

## Global Constraints

- **Tanpa commit:** jangan menjalankan `git commit`, `git add`, atau membuat branch. User yang commit. Semua pekerjaan dibiarkan uncommitted.
- **Jangan membaca atau mencetak rahasia:** `back/.config.toml`, `back/db_password`, `back/.env`, `front/.env.local`, kunci Cashi, PAT, `.pem`.
- **Jangan mengganggu proyek/Docker/port lain di mesin ini** (PostgreSQL sistem di 5432; kontainer `core-gate-*`, `pga-backbone-*`). Agent tidak boleh `docker compose up/down/restart/run/build`.
- **Test PostgreSQL:** hanya lewat PG18 sementara (data dir di scratchpad, socket `/tmp/claude-1000/pgsock`, port 54999, `listen_addresses=''`), dihapus dan diverifikasi sesudahnya. Ikuti cara test PG yang sudah ada (`newPGRepo` di `internal/repository/*_pg_test.go`; baca `payment_constraints_pg_test.go` untuk env var dan cara migrasi diterapkan per schema).
- **Uang selalu `int64` rupiah, aritmetika bilangan bulat saja** (tanpa float).
- **Nilai tetap dari spec:** minimum pencairan `50000` (config `payout.min_amount`); `entry_type` ∈ {`payment_credit`, `refund_debit`, `payout_debit`, `payout_reversal`, `adjustment`}; payout `status` ∈ {`requested`, `paid`, `rejected`}; status payment baru `refunded` (hanya `paid → refunded`); event callback `payment.refunded` hanya untuk refund penuh; bank/e-wallet tujuan tersimpan di profil merchant, ubah wajib password.
- **Hanya payment `environment = 'production'` yang dikredit ke ledger.** Sandbox tidak pernah masuk saldo.
- **Pesan galat ke klien tetap/generik**; detail hanya ke log. Teks UI dan galat API dalam bahasa Inggris (seperti yang sudah ada).
- **Penyimpangan dari spec (keputusan controller, diberitahukan ke user):** (1) net merchant ditentukan dari payment itu sendiri, bukan `fee_bearer` merchant saat ini: `fee_amount > 0` → `net = amount`; `fee_amount = 0` → `net = amount - ceil(amount*70/10000)`. Ini menghilangkan risiko bearer yang berubah setelah ada payment, dan membuat backfill tidak perlu asumsi. (2) "Audit log" yang ada adalah feed turunan, bukan tabel; jejak audit di fitur ini = baris `merchant_ledger` (`created_by`, `note`) + baris `payouts`/`payment_refunds` + log aplikasi tiap aksi admin.
- Konstanta 70 bps: pakai konstanta yang sudah ada di adapter Cashi (cari `70` / `bps` di `internal/provider/cashi`); bila tidak diekspor, ekspor satu konstanta bernama `fee.ProviderBasisPoints` di `internal/fee` dan biarkan Cashi memakainya, jangan menduplikasi angka.

---

## File Structure

| File | Tanggung jawab |
|---|---|
| `migrations/021_add_merchant_balance_payouts_refunds.sql` | Skema + trigger + backfill (Task 1) |
| `internal/fee/fee.go` | `MerchantNetBasisPoints` (Task 1) |
| `internal/domain/ledger/` (baru) | Tipe & galat ledger, payout, refund (Task 1) |
| `internal/domain/payment/payment.go`, `errors.go` | `StatusRefunded`, `MerchantNetAmount`, transisi (Task 1, 4) |
| `internal/repository/ledger_repository.go` | Balance, List, Adjust (Task 1, 4) |
| `internal/repository/payment_repository_update.go` | `MarkPaid` atomik (Task 2) |
| `internal/repository/payout_repository.go` | Request/Pay/Reject/List (Task 3) |
| `internal/repository/refund_repository.go` | Refund atomik (Task 4) |
| `internal/service/balance_service.go`, `payout_service.go`, `refund_service.go` | Logika bisnis (Task 2–4) |
| `internal/handler/balance_handler.go` (merchant), `admin_payout_handler.go` | HTTP (Task 3–4) |
| `internal/config/config.go`, `.config.example.toml` | `[payout] min_amount` (Task 3) |
| `cmd/api/main.go` | Wiring & rute |
| `front/...` | Task 5–6 |
| `docs/openapi.yaml`, `docs/payment-state-machine.md`, `DEPLOY.md`, `front/app/dashboard/api-docs/page.tsx` | Task 7 |

---

### Task 1: Migrasi 021, domain, rumus net, dan pembacaan ledger

**Files:**
- Create: `migrations/021_add_merchant_balance_payouts_refunds.sql`
- Create: `internal/domain/ledger/ledger.go`, `internal/domain/ledger/errors.go`
- Create: `internal/repository/ledger_repository.go`
- Modify: `internal/fee/fee.go`, `internal/fee/fee_test.go`
- Modify: `internal/domain/payment/payment.go` (`StatusRefunded`, `MerchantNetAmount`, `IsValidStatus`, `IsTerminalStatus`, `AllowedSourceStatuses`)
- Test: `internal/repository/ledger_pg_test.go`, `internal/domain/payment/payment_test.go`

**Interfaces:**
- Produces (dipakai Task 2–6):
  - `fee.MerchantNetBasisPoints(nominal, bps int64) (net, feeAmount int64, err error)`
  - `ledger.EntryPaymentCredit = "payment_credit"`, `EntryRefundDebit = "refund_debit"`, `EntryPayoutDebit = "payout_debit"`, `EntryPayoutReversal = "payout_reversal"`, `EntryAdjustment = "adjustment"`
  - `ledger.Entry{ID uuid.UUID; MerchantID uuid.UUID; EntryType string; Amount int64; PaymentID, PayoutID, RefundID *uuid.UUID; Note string; CreatedBy *uuid.UUID; CreatedAt time.Time}`
  - `ledger.Summary{Balance, TotalCredited, TotalPaidOut, TotalRefunded int64}` (nilai tidak negatif kecuali `Balance`; `TotalPaidOut` = pencairan berstatus `requested`+`paid` dikurangi reversal)
  - `repository.NewLedgerRepository(db *sql.DB) *LedgerRepository` dengan `Summary(ctx, merchantID) (*ledger.Summary, error)` dan `List(ctx, merchantID uuid.UUID, limit, offset int) ([]*ledger.Entry, int64, error)`
  - `payment.StatusRefunded = "refunded"`; `Payment.MerchantNetAmount int64 \`json:"merchant_net_amount" db:"merchant_net_amount"\``
  - Tabel `payouts`, `payment_refunds`, kolom rekening `merchants.payout_bank_name/payout_account_number/payout_account_holder`

- [ ] **Step 1: Test rumus net (gagal dulu)** — di `internal/fee/fee_test.go`:

```go
func TestMerchantNetBasisPoints(t *testing.T) {
	cases := []struct {
		nominal, bps, net, fee int64
	}{
		{10000, 70, 9930, 70},   // contoh spec: 10.000 -> 9.930
		{2000, 70, 1986, 14},    // 2000*70/10000 = 14.0 -> 14 (pas, tanpa pembulatan)
		{100, 70, 99, 1},        // 0.7 dibulatkan ke atas -> 1
		{1, 70, 0, 1},           // sangat kecil: fee membulat ke atas
		{10000, 0, 10000, 0},    // bps 0: tanpa fee
	}
	for _, c := range cases {
		net, fee, err := MerchantNetBasisPoints(c.nominal, c.bps)
		if err != nil || net != c.net || fee != c.fee {
			t.Errorf("MerchantNetBasisPoints(%d,%d) = (%d,%d,%v), want (%d,%d,nil)", c.nominal, c.bps, net, fee, err, c.net, c.fee)
		}
	}
	if _, _, err := MerchantNetBasisPoints(0, 70); err == nil {
		t.Error("nominal 0 must be rejected")
	}
	if _, _, err := MerchantNetBasisPoints(MaxNominal+1, 70); err == nil {
		t.Error("nominal above MaxNominal must be rejected")
	}
	if _, _, err := MerchantNetBasisPoints(1000, 10000); err == nil {
		t.Error("bps 10000 must be rejected")
	}
}
```

- [ ] **Step 2:** `go test ./internal/fee -run MerchantNet` → FAIL (undefined).
- [ ] **Step 3: Implementasi** di `internal/fee/fee.go`:

```go
// MerchantNetBasisPoints returns what a merchant who bears the provider fee
// keeps from a payment of nominal: nominal - ceil(nominal*bps/10000), and the
// fee itself. The fee rounds UP (the conservative direction, same assumption
// as QuoteTotalBasisPoints). nominal*bps <= 1e12*9999 fits in int64.
func MerchantNetBasisPoints(nominal, bps int64) (net, feeAmount int64, err error) {
	if nominal < 1 || nominal > MaxNominal {
		return 0, 0, ErrInvalidNominal
	}
	if bps < 0 || bps >= bpsDenominator {
		return 0, 0, ErrInvalidRate
	}
	feeAmount = (nominal*bps + bpsDenominator - 1) / bpsDenominator
	return nominal - feeAmount, feeAmount, nil
}
```
Jalankan test → PASS.

- [ ] **Step 4: Migrasi** `migrations/021_add_merchant_balance_payouts_refunds.sql` (ikuti gaya 019/020: satu transaksi, idempoten, pencarian `pg_constraint` selalu dengan `conrelid`):

```sql
-- Migration 021: merchant balance ledger, payouts, refunds.
-- One transaction; idempotent (safe to re-apply, including the backfill).
-- Hard-coded 70 below is the Cashi fee in basis points (internal/fee).
BEGIN;

ALTER TABLE payments ADD COLUMN IF NOT EXISTS merchant_net_amount BIGINT NOT NULL DEFAULT 0;

-- 'refunded' joins the allowed statuses.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint
               WHERE conrelid = 'payments'::regclass AND conname = 'payments_status_valid') THEN
        ALTER TABLE payments DROP CONSTRAINT payments_status_valid;
    END IF;
    ALTER TABLE payments ADD CONSTRAINT payments_status_valid
        CHECK (status IN ('pending', 'paid', 'expired', 'failed', 'cancelled', 'refunded'));
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid = 'payments'::regclass AND conname = 'payments_merchant_net_non_negative') THEN
        ALTER TABLE payments ADD CONSTRAINT payments_merchant_net_non_negative CHECK (merchant_net_amount >= 0);
    END IF;
END $$;

ALTER TABLE merchants
    ADD COLUMN IF NOT EXISTS payout_bank_name VARCHAR(100),
    ADD COLUMN IF NOT EXISTS payout_account_number VARCHAR(50),
    ADD COLUMN IF NOT EXISTS payout_account_holder VARCHAR(150);

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid = 'merchants'::regclass AND conname = 'merchants_payout_account_all_or_none') THEN
        ALTER TABLE merchants ADD CONSTRAINT merchants_payout_account_all_or_none CHECK (
            (payout_bank_name IS NULL AND payout_account_number IS NULL AND payout_account_holder IS NULL)
            OR (payout_bank_name IS NOT NULL AND payout_account_number IS NOT NULL AND payout_account_holder IS NOT NULL));
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS payouts (
    id UUID PRIMARY KEY,
    merchant_id UUID NOT NULL REFERENCES merchants(id) ON DELETE RESTRICT,
    amount BIGINT NOT NULL CHECK (amount > 0),
    status VARCHAR(20) NOT NULL DEFAULT 'requested' CHECK (status IN ('requested', 'paid', 'rejected')),
    bank_name VARCHAR(100) NOT NULL,
    account_number VARCHAR(50) NOT NULL,
    account_holder VARCHAR(150) NOT NULL,
    requested_by UUID,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_by UUID,
    processed_at TIMESTAMPTZ,
    reference VARCHAR(200),
    reject_reason VARCHAR(500),
    CONSTRAINT payouts_processing_consistent CHECK (
        (status = 'requested' AND processed_at IS NULL AND reference IS NULL AND reject_reason IS NULL)
        OR (status = 'paid' AND processed_at IS NOT NULL AND reference IS NOT NULL AND reject_reason IS NULL)
        OR (status = 'rejected' AND processed_at IS NOT NULL AND reject_reason IS NOT NULL AND reference IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_payouts_one_open_per_merchant ON payouts (merchant_id) WHERE status = 'requested';
CREATE INDEX IF NOT EXISTS idx_payouts_status_requested_at ON payouts (status, requested_at);
CREATE INDEX IF NOT EXISTS idx_payouts_merchant ON payouts (merchant_id, requested_at DESC);

CREATE TABLE IF NOT EXISTS payment_refunds (
    id UUID PRIMARY KEY,
    payment_id UUID NOT NULL REFERENCES payments(id) ON DELETE RESTRICT,
    merchant_id UUID NOT NULL REFERENCES merchants(id) ON DELETE RESTRICT,
    amount BIGINT NOT NULL CHECK (amount > 0),
    reason VARCHAR(500) NOT NULL CHECK (length(btrim(reason)) > 0),
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_payment_refunds_payment ON payment_refunds (payment_id);

CREATE TABLE IF NOT EXISTS merchant_ledger (
    id UUID PRIMARY KEY,
    merchant_id UUID NOT NULL REFERENCES merchants(id) ON DELETE RESTRICT,
    entry_type VARCHAR(20) NOT NULL CHECK (entry_type IN
        ('payment_credit', 'refund_debit', 'payout_debit', 'payout_reversal', 'adjustment')),
    amount BIGINT NOT NULL CHECK (amount <> 0),
    payment_id UUID REFERENCES payments(id) ON DELETE RESTRICT,
    payout_id UUID REFERENCES payouts(id) ON DELETE RESTRICT,
    refund_id UUID REFERENCES payment_refunds(id) ON DELETE RESTRICT,
    note VARCHAR(500) NOT NULL DEFAULT '',
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT merchant_ledger_sign_matches_type CHECK (
        (entry_type IN ('payment_credit', 'payout_reversal') AND amount > 0)
        OR (entry_type IN ('refund_debit', 'payout_debit') AND amount < 0)
        OR entry_type = 'adjustment'),
    CONSTRAINT merchant_ledger_reference_matches_type CHECK (
        (entry_type = 'payment_credit' AND payment_id IS NOT NULL)
        OR (entry_type = 'refund_debit' AND payment_id IS NOT NULL AND refund_id IS NOT NULL)
        OR (entry_type IN ('payout_debit', 'payout_reversal') AND payout_id IS NOT NULL)
        OR entry_type = 'adjustment')
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_ledger_credit_per_payment ON merchant_ledger (payment_id) WHERE entry_type = 'payment_credit';
CREATE UNIQUE INDEX IF NOT EXISTS uq_ledger_debit_per_payout ON merchant_ledger (payout_id) WHERE entry_type = 'payout_debit';
CREATE UNIQUE INDEX IF NOT EXISTS uq_ledger_reversal_per_payout ON merchant_ledger (payout_id) WHERE entry_type = 'payout_reversal';
CREATE UNIQUE INDEX IF NOT EXISTS uq_ledger_debit_per_refund ON merchant_ledger (refund_id) WHERE entry_type = 'refund_debit';
CREATE INDEX IF NOT EXISTS idx_ledger_merchant_created ON merchant_ledger (merchant_id, created_at DESC, id DESC);

CREATE OR REPLACE FUNCTION merchant_ledger_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'merchant_ledger is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS merchant_ledger_no_update ON merchant_ledger;
CREATE TRIGGER merchant_ledger_no_update BEFORE UPDATE OR DELETE ON merchant_ledger
    FOR EACH ROW EXECUTE FUNCTION merchant_ledger_append_only();

-- Backfill: production payments already paid. fee_amount > 0 means the
-- customer paid the fee on top (net = amount); otherwise the merchant bears
-- it (net = amount - ceil(amount*70/10000)). Re-runnable: only rows whose net
-- is still 0 are updated and the unique index absorbs repeated credits.
UPDATE payments
   SET merchant_net_amount = CASE WHEN fee_amount > 0 THEN amount
                                  ELSE amount - ((amount * 70 + 9999) / 10000) END
 WHERE status = 'paid' AND environment = 'production' AND merchant_net_amount = 0;

INSERT INTO merchant_ledger (id, merchant_id, entry_type, amount, payment_id, note)
SELECT gen_random_uuid(), p.merchant_id, 'payment_credit', p.merchant_net_amount, p.id, 'backfill'
  FROM payments p
 WHERE p.status = 'paid' AND p.environment = 'production' AND p.merchant_net_amount > 0
ON CONFLICT (payment_id) WHERE entry_type = 'payment_credit' DO NOTHING;

COMMIT;
```
(`gen_random_uuid()` bawaan PG ≥ 13. Bila `amount - fee` untuk nominal kecil menghasilkan 0, baris itu tidak dikredit — dicakup test.)

- [ ] **Step 5: Domain & repo.** Buat `internal/domain/ledger/{ledger.go,errors.go}` sesuai blok Interfaces (galat: `ErrInsufficientBalance`, `ErrBelowMinimumPayout`, `ErrPayoutAccountMissing`, `ErrOpenPayoutExists`, `ErrPayoutNotFound`, `ErrPayoutAlreadyProcessed`, `ErrRefundExceedsNet`, `ErrPaymentNotRefundable`, `ErrInvalidAdjustment`, `ErrReasonRequired`). Di `payment.go`: tambah `StatusRefunded`, masukkan ke `IsValidStatus` dan `IsTerminalStatus`, `AllowedSourceStatuses(StatusRefunded) = []string{StatusPaid}`, field `MerchantNetAmount`; perbarui semua `SELECT`/`Scan` payment di `payment_repository*.go` agar membawa `merchant_net_amount` (cari semua query kolom payment; test sqlmock yang memeriksa daftar kolom ikut diperbarui). `LedgerRepository.Summary` memakai satu query:

```sql
SELECT COALESCE(SUM(amount), 0),
       COALESCE(SUM(amount) FILTER (WHERE entry_type = 'payment_credit'), 0),
       COALESCE(-SUM(amount) FILTER (WHERE entry_type IN ('payout_debit', 'payout_reversal')), 0),
       COALESCE(-SUM(amount) FILTER (WHERE entry_type = 'refund_debit'), 0)
  FROM merchant_ledger WHERE merchant_id = $1
```
`List` mengurutkan `created_at DESC, id DESC` dengan `COUNT(*)` terpisah.

- [ ] **Step 6: Test PG** `internal/repository/ledger_pg_test.go` (pakai `newPGRepo`, `insertConstraintMerchant`, `insertConstraintPayment`). Wajib mencakup, dengan kode nyata:
  - trigger: `UPDATE merchant_ledger SET note='x'` dan `DELETE` keduanya error berisi `append-only`;
  - unique: dua `payment_credit` untuk payment yang sama → pelanggaran `uq_ledger_credit_per_payment`;
  - tanda: `payment_credit` negatif ditolak (`merchant_ledger_sign_matches_type`), `payout_debit` positif ditolak;
  - referensi: `refund_debit` tanpa `refund_id` ditolak;
  - payout: dua `requested` untuk satu merchant ditolak; `status='paid'` tanpa `reference` ditolak;
  - `payments_status_valid` menerima `refunded` dan menolak `bogus`;
  - rekening: hanya sebagian kolom terisi ditolak;
  - `Summary`/`List` pada ledger berisi credit 10000, refund_debit -3000, payout_debit -5000, reversal +5000 → `Balance 7000, TotalCredited 10000, TotalPaidOut 0, TotalRefunded 3000`;
  - **backfill idempoten**: sisipkan payment production `paid` fee_amount 0 amount 10000, `paid` fee_amount 70 amount 10000, `paid` sandbox, `pending`; jalankan ulang blok backfill (ekstrak dua statement backfill dari file migrasi atau jalankan ulang seluruh file) dua kali → net 9930 / 10000 / 0 / 0, tepat dua baris ledger, saldo 19930.
- [ ] **Step 7:** `go test ./internal/fee ./internal/domain/... ./internal/repository/... -race -count=1` (suite PG ikut dengan env test PG sementara) → PASS. `gofmt -l`, `go vet ./...` bersih.
- [ ] **Step 8: Mutasi** (laporkan hasilnya): hapus unique `uq_ledger_credit_per_payment` → test duplikat harus gagal; hapus trigger → test append-only gagal; ubah `+ 9999` menjadi `+ 0` di backfill → test net 9930 gagal; hapus `AND environment = 'production'` → test sandbox gagal. Pulihkan semuanya.

---

### Task 2: Kredit atomik saat paid

**Files:**
- Modify: `internal/repository/payment_repository_update.go` (tambah `MarkPaid`)
- Modify: `internal/service/interfaces.go` (interface `paymentRepository`: ganti `UpdateStatus(... paid ...)` pemakaian paid dengan `MarkPaid`; tetap simpan `UpdateStatus` untuk expired/failed/cancelled)
- Modify: `internal/service/payment_service.go` (~456), `internal/service/payment_webhook_service.go` (~135)
- Modify: `internal/service/payment_fakes_test.go` dan fake lain yang mengimplementasikan `paymentRepository`
- Test: `internal/repository/payment_markpaid_pg_test.go`, tes service

**Interfaces:**
- Consumes: `fee.MerchantNetBasisPoints`, `ledger.EntryPaymentCredit`, tabel Task 1.
- Produces: `PaymentRepository.MarkPaid(ctx context.Context, id uuid.UUID, paidAt *time.Time, merchantNet int64) (previous string, err error)`; helper `func merchantNetFor(p *payment.Payment) (int64, error)` di `internal/service` (aturan: `p.FeeAmount > 0` → `p.Amount`; selain itu `net, _, err := fee.MerchantNetBasisPoints(p.Amount, fee.ProviderBasisPoints)`).

- [ ] **Step 1: Test PG `MarkPaid`** (gagal dulu). Kasus: (a) production pending → paid: `previous == "pending"`, `payments.merchant_net_amount == net`, tepat satu ledger `payment_credit` bernilai net; (b) `expired → paid` (late) mengredit juga, `previous == "expired"`; (c) sandbox → paid: status berubah, net tersimpan, **tidak ada** ledger; (d) panggilan kedua pada payment yang sudah paid → `payment.ErrPaymentAlreadyTerminal`, ledger tetap satu; (e) 20 goroutine memanggil `MarkPaid` bersamaan pada satu payment → tepat 1 sukses, 19 `ErrPaymentAlreadyTerminal`, ledger tepat satu, saldo = net; (f) `merchantNet = 0` (nominal sangat kecil) → payment paid tanpa ledger (jangan melanggar CHECK `amount <> 0`); (g) kegagalan insert ledger membatalkan perubahan status (paksa dengan menyisipkan lebih dulu baris `payment_credit` untuk payment itu secara manual → `MarkPaid` mengembalikan error dan payment tetap `pending`).
- [ ] **Step 2: Implementasi** — satu transaksi:

```go
func (r *PaymentRepository) MarkPaid(ctx context.Context, id uuid.UUID, paidAt *time.Time, merchantNet int64) (string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("failed to begin mark-paid transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	from := payment.AllowedSourceStatuses(payment.StatusPaid)
	args := []interface{}{payment.StatusPaid, paidAt, time.Now(), merchantNet, id}
	placeholders := make([]string, len(from))
	for i, s := range from {
		args = append(args, s)
		placeholders[i] = fmt.Sprintf("$%d", len(args))
	}
	var previous, merchantID, environment string
	err = tx.QueryRowContext(ctx, `
		UPDATE payments p SET status = $1, paid_at = $2, updated_at = $3, merchant_net_amount = $4
		FROM (SELECT id, status FROM payments WHERE id = $5 FOR UPDATE) prev
		WHERE p.id = prev.id AND p.status IN (`+strings.Join(placeholders, ", ")+`)
		RETURNING prev.status, p.merchant_id::text, p.environment`, args...).Scan(&previous, &merchantID, &environment)
	if errors.Is(err, sql.ErrNoRows) {
		// Same classification as UpdateStatus: terminal vs invalid transition.
		current, getErr := r.GetByID(ctx, id)
		if getErr != nil {
			return "", getErr
		}
		if payment.IsTerminalStatus(current.Status) {
			return "", payment.ErrPaymentAlreadyTerminal
		}
		return "", payment.ErrInvalidStatusTransition
	}
	if err != nil {
		return "", fmt.Errorf("failed to mark payment paid: %w", err)
	}
	if environment == payment.EnvironmentProduction && merchantNet > 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO merchant_ledger (id, merchant_id, entry_type, amount, payment_id, note)
			VALUES ($1, $2, 'payment_credit', $3, $4, '')`,
			uuid.New(), merchantID, merchantNet, id); err != nil {
			return "", fmt.Errorf("failed to credit merchant ledger: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("failed to commit mark-paid: %w", err)
	}
	return previous, nil
}
```
(`GetByID` di cabang `ErrNoRows` berjalan di luar tx; transaksi dibatalkan oleh `defer`. Bila `r.db` bukan `*sql.DB` yang mendukung `BeginTx`, ikuti tipe yang dipakai repository.)

- [ ] **Step 3: Wiring service.** Di kedua pemanggil paid (`payment_service.go`, `payment_webhook_service.go`) hitung `net, err := merchantNetFor(p)` lalu panggil `MarkPaid(ctx, p.ID, paidAt, net)` menggantikan `UpdateStatus(..., StatusPaid, paidAt)`; penanganan `ErrPaymentAlreadyTerminal` dan `late := previous == payment.StatusExpired` tidak berubah. Jika `merchantNetFor` gagal (nominal di luar rentang), log error dan jangan menandai paid: kembalikan galat (payment tetap pending sehingga reconcile mencoba lagi; ini tidak boleh terjadi pada nominal valid). Perbarui `p.MerchantNetAmount = net` pada objek in-memory. Perbarui fake repo di test agar mengimplementasikan `MarkPaid` (meniru status + simpan net), dan perbarui semua test yang memeriksa `UpdateStatus` paid.
- [ ] **Step 4: Tes service** (fake repo): webhook paid memanggil `MarkPaid` dengan net 10000 untuk payment `fee_amount=70, amount=10000`, dan 9930 untuk `fee_amount=0`; reconcile paid idem; late settlement (`expired`→paid) memanggil `MarkPaid`; `ErrPaymentAlreadyTerminal` tetap menghasilkan `ErrDuplicateWebhook`/"skipped" seperti sebelumnya (tes yang ada harus tetap lulus).
- [ ] **Step 5:** `go test ./... -race -count=1` PASS; gofmt/vet bersih.
- [ ] **Step 6: Mutasi:** hapus insert ledger → tes (a) gagal; hapus pengecekan `environment` → tes (c) gagal; ganti `FOR UPDATE` subquery dengan update tanpa guard → tes (e) gagal; tukar aturan `FeeAmount > 0` → tes service net gagal.

---

### Task 3: Rekening, pengajuan, dan pemrosesan pencairan

**Files:**
- Create: `internal/repository/payout_repository.go`, `internal/service/payout_service.go`, `internal/handler/balance_handler.go` (merchant), `internal/handler/admin_payout_handler.go`
- Modify: `internal/repository/merchant_repository.go` (kolom rekening + `SetPayoutAccount`), `internal/domain/merchant/merchant.go` (field rekening di struct & response? — lihat bawah), `internal/config/config.go` + `.config.example.toml` + `config_test.go`, `internal/service/admin_notification_service.go`, `cmd/api/main.go`
- Test: `internal/repository/payout_pg_test.go`, `internal/service/payout_service_test.go`, handler tests

**Interfaces:**
- Consumes: Task 1 (tabel, `LedgerRepository`, galat ledger).
- Produces:
  - `ledger.Payout{ID, MerchantID uuid.UUID; Amount int64; Status, BankName, AccountNumber, AccountHolder string; RequestedBy *uuid.UUID; RequestedAt time.Time; ProcessedBy *uuid.UUID; ProcessedAt *time.Time; Reference, RejectReason *string}`
  - `PayoutRepository`: `Request(ctx, merchantID uuid.UUID, amount, minAmount int64, requestedBy *uuid.UUID) (*ledger.Payout, error)`; `MarkPaid(ctx, id uuid.UUID, reference string, adminID *uuid.UUID) (*ledger.Payout, error)`; `Reject(ctx, id uuid.UUID, reason string, adminID *uuid.UUID) (*ledger.Payout, error)`; `ListByMerchant(ctx, merchantID, limit, offset) ([]*ledger.Payout, int64, error)`; `ListAdmin(ctx, status string, limit, offset int) ([]*ledger.Payout, int64, error)`
  - `MerchantRepository.SetPayoutAccount(ctx, id uuid.UUID, bank, number, holder string) error`
  - `PayoutService` membungkus repo + `minAmount int64`, dan `BalanceService` (`Summary`, `Ledger`)
  - Config: `PayoutConfig{MinAmount int64 \`toml:"min_amount"\`}` di `Config.Payout \`toml:"payout"\``; validasi `MinAmount >= 1`

- [ ] **Step 1: Config.** Tambah `[payout] min_amount = 50000` ke `.config.example.toml` dan struct; **config loader menolak kunci yang hilang** (`missingKeys`), jadi `config_test.go` harus diperbarui dan ditambah kasus: kunci `payout.min_amount` hilang → galat, `0` → galat validasi. Catat di runbook (Task 7) bahwa `.config.toml` di server wajib diberi `[payout]` sebelum deploy. Jangan membaca `.config.toml` asli.
- [ ] **Step 2: Test PG `Request`** (gagal dulu). Memakai ledger yang diisi langsung (kredit 100000). Kasus: sukses → baris `payouts` `requested` dengan snapshot rekening dari merchant, ledger `payout_debit` = `-amount`, saldo turun; amount < min → `ErrBelowMinimumPayout`; amount > saldo → `ErrInsufficientBalance`; rekening kosong → `ErrPayoutAccountMissing`; sudah ada `requested` → `ErrOpenPayoutExists`; saldo negatif → `ErrInsufficientBalance`; **20 goroutine** memanggil `Request(60000)` dari saldo 100000 → tepat 1 sukses, sisanya `ErrOpenPayoutExists` atau `ErrInsufficientBalance`, saldo akhir 40000; mengubah rekening merchant sesudah pengajuan tidak mengubah snapshot payout.
- [ ] **Step 3: Implementasi `Request`:** satu transaksi: `SELECT payout_bank_name, payout_account_number, payout_account_holder FROM merchants WHERE id=$1 FOR UPDATE` (mengunci baris merchant untuk menserialkan pengajuan), validasi rekening, `amount >= minAmount`, hitung saldo `SELECT COALESCE(SUM(amount),0) FROM merchant_ledger WHERE merchant_id=$1`, `amount <= saldo`, `INSERT payouts`, `INSERT merchant_ledger (payout_debit, -amount, payout_id)`. Pelanggaran `uq_payouts_one_open_per_merchant` (kode `23505`) dipetakan ke `ErrOpenPayoutExists`.
- [ ] **Step 4: `MarkPaid` / `Reject`:** transaksi: `UPDATE payouts SET status=..., processed_at=now(), processed_by=$, reference/reject_reason WHERE id=$ AND status='requested' RETURNING ...`; bila tidak ada baris → `ErrPayoutNotFound` (id tidak ada) atau `ErrPayoutAlreadyProcessed`. `Reject` menambahkan ledger `payout_reversal` (+amount). `reference` dan `reason` wajib non-kosong setelah trim (`ErrReasonRequired`), maks 200/500 karakter. Tes: pay lalu pay lagi → `ErrPayoutAlreadyProcessed`; reject mengembalikan saldo persis sekali; pay dan reject serentak → tepat satu menang dan ledger konsisten (saldo sesuai pemenang); pay tidak menulis ledger baru.
- [ ] **Step 5: Rekening.** `SetPayoutAccount` + validasi di service (trim; bank 2–100, nomor `^[0-9]{5,30}$`, pemilik 2–150 karakter). Endpoint ubah rekening **wajib `password`** dengan pola yang sama persis seperti `RegenerateWebhookSecret` di `merchant_handler.go` (baca dan ikuti: verifikasi password, rate limiter `sensitiveRateLimiter`, 401 "Invalid password" tidak mengeluarkan sesi). Tes: tanpa/salah password → 401 dan rekening tidak berubah; sukses mengubah ketiga kolom; pemilik kosong → 400.
- [ ] **Step 6: Handler merchant** (`merchantDash`, di belakang `RequireMerchant`; ambil merchant/user ID dari konteks dengan cara yang dipakai `merchant_handler.go`): `GET /balance` → `{balance, total_credited, total_paid_out, total_refunded, min_payout_amount, payout_account: {bank_name, account_number_masked?, account_holder}|null, can_request}` (`can_request` = rekening terisi && saldo ≥ min && tidak ada payout terbuka; nomor rekening ditampilkan penuh kepada pemiliknya); `GET /ledger`; `PUT /payout-account`; `POST /payouts` (body `{amount}`, mendukung `Idempotency-Key` memakai mekanisme idempotensi yang sudah ada **atau**, bila mekanisme itu terikat ke payment, cukup mengandalkan unique "satu payout terbuka" — pilih yang kedua dan catat alasannya di laporan: klik ganda sudah ditolak `ErrOpenPayoutExists` sehingga tidak ada kebutuhan tabel idempotensi baru); `GET /payouts`. Pemetaan galat: 400 (`ErrBelowMinimumPayout`, validasi), 409 (`ErrOpenPayoutExists`), 422 (`ErrInsufficientBalance`, `ErrPayoutAccountMissing`), 500 generik.
- [ ] **Step 7: Handler admin** (`adminAPI`): `GET /balances` (per merchant: id, nama, saldo, payout terbuka; urut saldo menurun; ada filter `negative=true`), `GET /payouts?status=`, `POST /payouts/{id}/pay` (`{reference}`), `POST /payouts/{id}/reject` (`{reason}`), `GET /merchants/{id}/ledger`. Catat tiap aksi admin dengan `logger.InfofCtx` (id admin, payout, jumlah). Tambahkan notifikasi "payout requested" ke `AdminNotificationService` (jumlah payout `requested` + tautan `/admin/payouts`) mengikuti pola item notifikasi yang ada, dengan test.
- [ ] **Step 8:** `go test ./... -race -count=1`, gofmt, vet bersih; tes router (`cmd/api/router_test.go` pola) membuktikan rute merchant menolak tanpa JWT dan rute admin menolak JWT merchant.
- [ ] **Step 9: Mutasi:** hapus `FOR UPDATE` merchant → tes konkuren gagal; hapus `AND status='requested'` → tes proses ganda gagal; hapus ledger reversal → tes saldo reject gagal; longgarkan pengecekan min → tes min gagal.

---

### Task 4: Refund, penyesuaian saldo, dan callback `payment.refunded`

**Files:**
- Create: `internal/repository/refund_repository.go`, `internal/service/refund_service.go`
- Modify: `internal/repository/ledger_repository.go` (`Adjust`), `internal/handler/admin_payout_handler.go` (refund & adjustment handlers), `internal/service/merchant_callback_service.go` + `eventTypeForStatus` (event refunded), `cmd/api/main.go`
- Test: `internal/repository/refund_pg_test.go`, service & handler tests, callback tests

**Interfaces:**
- Consumes: Task 1 & 3.
- Produces: `RefundRepository.Refund(ctx, paymentID uuid.UUID, amount int64, reason string, adminID *uuid.UUID) (refund *ledger.Refund, full bool, p *payment.Payment, err error)`; `LedgerRepository.Adjust(ctx, merchantID uuid.UUID, amount int64, reason string, adminID *uuid.UUID) (*ledger.Entry, error)`; `ledger.Refund{ID, PaymentID, MerchantID uuid.UUID; Amount int64; Reason string; CreatedBy *uuid.UUID; CreatedAt time.Time}`; event callback `payment.refunded`.

- [ ] **Step 1: Test PG `Refund`** (gagal dulu): payment production paid net 9930; refund 3000 → `full=false`, status tetap `paid`, ledger `refund_debit -3000`; refund 6930 berikutnya → `full=true`, status `refunded`; refund melebihi sisa → `ErrRefundExceedsNet` dan tidak ada perubahan; payment `pending` / sandbox / `refunded` → `ErrPaymentNotRefundable`; reason kosong → `ErrReasonRequired`; amount ≤ 0 → `ErrInvalidAdjustment`-sejenis galat validasi (`ErrRefundExceedsNet` tidak dipakai untuk ini; gunakan `ledger.ErrInvalidAmount` baru); **konkuren:** 20 goroutine me-refund 1000 masing-masing dari net 9930 → total berhasil ≤ 9930 dan jumlah ledger refund = jumlah baris `payment_refunds`; refund penuh serentak dua kali (masing-masing = net) → tepat satu sukses.
- [ ] **Step 2: Implementasi `Refund`:** transaksi; `SELECT ... FROM payments WHERE id=$1 FOR UPDATE` (status, environment, merchant_id, merchant_net_amount); status harus `paid` dan environment `production`; `already := SELECT COALESCE(SUM(amount),0) FROM payment_refunds WHERE payment_id=$1`; `amount <= net - already`; insert `payment_refunds`, ledger `refund_debit (-amount, payment_id, refund_id)`; bila `already + amount == net` → `UPDATE payments SET status='refunded', updated_at=now()`. Kembalikan `full`.
- [ ] **Step 3: `Adjust`:** `amount != 0`, `|amount| <= 1_000_000_000_000`, alasan wajib (trim, ≤ 500); satu `INSERT` ledger `adjustment`. Tes: nol/alasan kosong ditolak; positif dan negatif mengubah saldo; `created_by` terisi.
- [ ] **Step 4: Callback.** Baca `merchant_callback_service.go` dan `eventTypeForStatus` lalu tambahkan event `payment.refunded` (payload sama dengan event paid, status `refunded`, ditandatangani sama, lewat antrian yang ada) yang dikirim **hanya jika `full`**. Refund sebagian tidak mengirim callback. Tes: refund penuh meng-enqueue tepat satu callback `payment.refunded`; sebagian tidak; payment tanpa `callback_url`/merchant tanpa webhook mengikuti perilaku notify yang ada.
- [ ] **Step 5: Handler admin:** `POST /payments/{id}/refund` (`{amount, reason}`; 404 payment tidak ada, 409 `ErrPaymentNotRefundable`, 422 `ErrRefundExceedsNet`, 400 validasi) dan `POST /merchants/{id}/adjustments` (`{amount, reason}`, merchant harus ada). Log tiap aksi (admin, payment/merchant, jumlah). Respons refund: `{refund, payment_status, remaining_refundable}`.
- [ ] **Step 6: Regresi state machine:** tes `payment_test.go`: `CanTransitionTo(paid, refunded)` true; `pending/expired/failed/cancelled → refunded` false; `refunded → apa pun` false; `IsTerminalStatus(refunded)` true. Webhook/reconcile pada payment `refunded` tidak mengubahnya (`ErrPaymentAlreadyTerminal`).
- [ ] **Step 7:** `go test ./... -race -count=1`, gofmt, vet bersih.
- [ ] **Step 8: Mutasi:** hapus penguncian baris payment → tes konkuren gagal; hapus batas `net - already` → tes melebihi gagal; kirim callback juga untuk refund sebagian → tes callback gagal; hapus cek `production` → tes sandbox gagal.

---

### Task 5: Front-end merchant (Balance dan rekening pencairan)

**Files:** baca dulu `front/AGENTS.md` (wajib membaca dokumen Next.js terkait di `front/node_modules/next/dist/docs/` sebelum menulis kode), struktur `front/app/dashboard/*`, `front/lib/merchant-auth.ts` (pola `merchantAuthFetch`, `merchantAuthHeaders`).
- Create: `front/lib/merchant-balance.ts` (fungsi API bertipe: `fetchBalance`, `fetchLedger`, `fetchPayouts`, `requestPayout`, `savePayoutAccount`), `front/app/dashboard/balance/page.tsx`
- Modify: navigasi sidebar dashboard (tautan "Balance"), halaman pengaturan (bagian "Payout account") mengikuti pola halaman pengaturan yang ada, `front/app/dashboard/api-docs/page.tsx` bila perlu (Task 7).

- [ ] **Step 1:** Halaman Balance: kartu saldo tersedia, total dikredit/dicairkan/direfund; teks minimum pencairan (dari API, bukan di-hardcode); form pengajuan (input nominal rupiah, tombol dinonaktifkan dan alasan ditampilkan bila `can_request` false: rekening belum diisi / saldo di bawah minimum / ada pengajuan terbuka); daftar pengajuan (status berlabel: Requested/Paid/Rejected + alasan penolakan + referensi transfer); daftar mutasi (tipe berlabel, jumlah bertanda, catatan, waktu) dengan paginasi. Saldo negatif tampil merah dengan keterangan "Negative balance: deducted by refunds".
- [ ] **Step 2:** Bagian "Payout account" di pengaturan: tiga input + input password; galat 401 "Invalid password" ditampilkan inline tanpa logout (pola `merchantAuthFetch` yang sudah menangani ini).
- [ ] **Step 3:** Ikuti gaya komponen, Tailwind, dan format rupiah yang sudah dipakai halaman payments (cari helper format yang ada, jangan membuat yang baru bila sudah ada). Tampilkan juga status payment `refunded` pada daftar/detail payment dan filter status merchant (label "Refunded") bila daftar status di-hardcode di front-end (cari `"cancelled"` di `front/`).
- [ ] **Step 4:** `npm run lint` dan `npm run build` bersih (dijalankan dari `front/`). Tidak ada test runner front-end — nyatakan itu di laporan. Verifikasi visual belum pernah dilakukan di proyek ini; sebutkan bahwa tampilan tidak diuji di browser.

---

### Task 6: Front-end admin (Payouts, saldo, refund, penyesuaian)

**Files:** baca struktur `front/app/admin/*` dan helper fetch admin (pola yang dipakai halaman admin merchants/payments).
- Create: `front/lib/admin-payouts.ts`, `front/app/admin/payouts/page.tsx`
- Modify: navigasi sidebar admin ("Payouts"), daftar merchant admin (kolom "Balance"), halaman detail merchant admin (kartu saldo + ledger + tombol "Adjust balance"), halaman detail payment admin (tombol "Refund" hanya untuk status `paid` production), filter/label status `refunded` di halaman admin.

- [ ] **Step 1:** Halaman Payouts: tab/filter status (default Requested); tiap baris menampilkan merchant, jumlah, rekening snapshot (bank, nomor, pemilik, tombol salin nomor), waktu; aksi "Mark as paid" (dialog meminta nomor referensi transfer wajib) dan "Reject" (dialog alasan wajib). Konfirmasi eksplisit sebelum aksi (dialog), karena aksi final.
- [ ] **Step 2:** Detail merchant: saldo, ledger berpaginasi, dialog "Adjust balance" (nominal bertanda + alasan wajib, dengan peringatan bahwa ini mengubah uang merchant). Daftar merchant: kolom saldo (negatif merah).
- [ ] **Step 3:** Detail payment: dialog "Refund" (nominal default = sisa yang dapat direfund, alasan wajib, teks jelas "Refund is recorded only; return the money to the customer manually").
- [ ] **Step 4:** `npm run lint` dan `npm run build` bersih; laporkan bahwa tidak ada test runner dan tidak ada uji visual.

---

### Task 7: Dokumentasi dan runbook

**Files:** `docs/openapi.yaml`, `docs/payment-state-machine.md`, `DEPLOY.md`, `front/app/dashboard/api-docs/page.tsx`, `README.md` (hanya bila memuat daftar fitur yang relevan).

- [ ] **Step 1: openapi.yaml:** dokumentasikan semua endpoint merchant & admin baru (skema `Balance`, `LedgerEntry`, `Payout`, `Refund`, galat 400/401/404/409/422), field `merchant_net_amount` pada respons payment bila terekspos (periksa apakah `PaymentResponse` memuatnya; jangan mengekspos ke API key merchant kecuali memang ditambahkan ke respons), status `refunded` di enum status, dan event callback `payment.refunded` di bagian callback. Validasi YAML + semua `$ref` terselesaikan.
- [ ] **Step 2: payment-state-machine.md:** tambahkan `refunded` (terminal, hanya dari `paid`, hanya lewat refund penuh admin), kolom tabel status, dan aturan "paid tanpa kredit tidak ada". Perbarui checklist "menambah status" yang sudah ada di akhir dokumen sesuai penambahan ini.
- [ ] **Step 3: DEPLOY.md (bagian baru 4.6, runbook):** (1) tambahkan `[payout] min_amount = 50000` ke `.config.toml` server SEBELUM menjalankan binari baru (loader menolak kunci yang hilang); (2) urutan: backup DB → `make migrate` (migrasi 021) → deploy binari baru; (3) **celah migrasi→deploy:** payment yang jadi `paid` oleh binari lama setelah migrasi tidak terkredit — jalankan ulang file migrasi 021 sesudah binari baru berjalan (idempoten; hanya payment `merchant_net_amount = 0` yang diisi) atau segera deploy; (4) verifikasi backfill: `SELECT COUNT(*), SUM(merchant_net_amount) FROM payments WHERE status='paid' AND environment='production'` harus sama dengan `SELECT COUNT(*), SUM(amount) FROM merchant_ledger WHERE entry_type='payment_credit'` (untuk payment dengan net > 0), lalu cocokkan total dengan dashboard Cashi (selisih ±1–2 rupiah per transaksi karena pembulatan fee wajar); (5) uji satu siklus dengan merchant internal: bayar → saldo bertambah → isi rekening → ajukan ≥ 50.000 → admin tandai paid; (6) rollback: bagian ini hanya aman bila belum ada payout `requested`/`paid` atau refund; rollback skema = `DROP TABLE merchant_ledger, payment_refunds, payouts` + drop kolom baru + kembalikan constraint status — tulis SQL nyatanya dan peringatkan bahwa binari lama tetap bekerja dengan skema baru (tabel/kolom tambahan diabaikan) sehingga **rollback binari tanpa rollback skema adalah opsi yang disarankan**, tetapi payment yang menjadi `paid` selama binari lama berjalan tidak terkredit (jalankan ulang backfill saat naik lagi); (7) saldo negatif tidak ditagih otomatis — admin memantau lewat filter `negative=true`.
- [ ] **Step 4: Halaman API docs front-end:** tambahkan event `payment.refunded` ke bagian callback/webhook dan `refunded` ke daftar status payment; lint + build bersih.
- [ ] **Step 5:** Verifikasi akhir dari `back/`: `gofmt -l ./internal ./cmd ./pkg` kosong, `go vet ./...`, `go test ./... -race -count=1`; dari `front/`: `npm run lint`, `npm run build`; parse YAML openapi.

---

## Self-Review (controller)

- **Cakupan spec:** §3.1–3.7 → Task 1; §4.1 → Task 2; §4.2 → Task 3; §4.3–4.4 → Task 4; §5 → Task 3–4; §6 → Task 5–6; §8 → tes+mutasi di tiap task; §9 → Task 7. Spec §4.2 poin 6 (ubah rekening wajib password) → Task 3 Step 5. Callback `payment.refunded` → Task 4.
- **Penyimpangan dari spec** (net per-payment, audit = ledger+log, idempotensi payout via unique "satu terbuka") tercantum di Global Constraints / Task 3 dan harus dilaporkan ke user.
- **Konsistensi nama:** `MarkPaid` (payment repo, Task 2) berbeda dari `PayoutRepository.MarkPaid` (Task 3) — beda receiver, tidak bentrok; `ledger.Entry*` konsisten di semua task; `fee.ProviderBasisPoints` dipakai Task 1–2.
- **Risiko yang diketahui:** celah migrasi→deploy (Task 7 Step 3); `config` strict-keys (Task 3 Step 1); banyak fake repo di test yang harus ikut diperbarui (Task 2 Step 3); pembulatan fee ±1 rupiah.
