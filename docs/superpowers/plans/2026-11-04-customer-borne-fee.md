# Fee Provider Ditanggung Customer (Paket 5) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merchant yang diatur `fee_bearer = customer` membuat customer membayar nominal ditambah fee Cashi (0,7%), sehingga merchant menerima nominal penuh setelah potongan Cashi; merchant lain tidak berubah.

**Architecture:** `payments.amount` tetap nominal merchant, kolom baru `payments.fee_amount` menyimpan fee, total ditagih = `amount + fee_amount` (`Payment.TotalAmount()`). Fee dihitung per kandidat provider lewat antarmuka opsional `FeeQuoter` (Cashi dan sandbox; fungsi murni `internal/fee`). Verifikasi nominal Paket 1 membandingkan nominal kotor provider dengan `TotalAmount()`. Pengaturan `merchants.fee_bearer` hanya bisa diubah admin lewat endpoint khusus. Semua perubahan antarmuka bersifat aditif.

**Tech Stack:** Go 1.25, gorilla/mux, lib/pq, PostgreSQL 18; Next.js 16 / React 19 / TypeScript / Tailwind untuk Task 7–8.

**Spec:** [`back/docs/superpowers/specs/2026-11-04-customer-borne-fee-design.md`](../specs/2026-11-04-customer-borne-fee-design.md)

## Global Constraints

- **NO git commits, NO `git add`, NO branches** sampai user mengizinkan (instruksi eksplisit). Tiap task diakhiri "report changed files".
- Jangan pernah membaca/mencetak secret: `back/.config.toml`, `back/db_password`, `back/.env`, `front/.env.local`, kunci Cashi. `.config.example.toml` boleh.
- Jangan mengganggu proyek/Docker/port lain milik user (PostgreSQL sistem di 5432). Test PG hanya di PG18 sementara: data dir di scratchpad, socket `/tmp/claude-1000/pgsock`, port 54999, `listen_addresses=''`, dihapus setelah selesai (verifikasi `ls /tmp/claude-1000/pgsock` hilang dan tak ada proses di 54999). Docker: dilarang `compose up/down/restart/run/build`; hanya `docker compose config -q` bila perlu.
- Gaya kode: ikuti file sekitar (komentar bahasa Inggris yang menjelaskan *why*, table-driven tests, fake in-memory, test PG via `TEST_DATABASE_URL`). Tanpa dependensi baru. Tidak ada key `.config.toml` baru.
- Tiap task backend: `gofmt -l ./internal ./cmd ./pkg` kosong, `go vet ./...` bersih, `go test ./... -race -count=1` hijau (jalankan dari `back/`; cek `pwd`). Tiap task front-end: `npm run lint` dan `npm run build` dari `front/` bersih; wajib membaca `front/AGENTS.md` dan dokumen Next relevan di `front/node_modules/next/dist/docs/` sebelum menulis kode. Test baru wajib dibuktikan membedakan lewat **mutasi** (rusakkan logika, test gagal, kembalikan) dan dilaporkan.
- Nilai persis dari spec: tarif Cashi dan sandbox **70 basis poin (0,7%)**; total = bilangan bulat terkecil `G` dengan `G − ceil(G × bps / 10000) ≥ nominal`; contoh **2.000 → total 2.015, fee 15**; `fee_bearer` ∈ {`merchant`, `customer`}, default `merchant`; `fee_amount` `BIGINT NOT NULL DEFAULT 0 CHECK (>= 0)`; endpoint admin `PUT /api/v1/admin/merchants/{id}/fee-bearer` dengan body `{"fee_bearer":"merchant"|"customer"}` (nilai lain 400, merchant tidak ada 404); field `fee_amount` dan `total_amount` selalu ada pada respons; payload callback `amount` tetap nominal; sandbox tanpa akhiran acak 1–99; provider tanpa `FeeQuoter` + merchant `customer` ditolak; `total < nominal` ditolak.
- Abaikan `back/.superpowers/`.

## Urutan dan File

| Task | Isi | File utama |
|---|---|---|
| 1 | Paket `internal/fee` + antarmuka `FeeQuoter` | `internal/fee/fee.go`, `fee_test.go`, `internal/provider/fee.go` |
| 2 | Migration 020, domain, repository | `migrations/020_*.sql`, `internal/domain/payment/payment.go`, `internal/domain/merchant/merchant.go`, `internal/repository/payment_repository*.go`, `merchant_repository.go` |
| 3 | Adapter Cashi + sandbox | `internal/provider/cashi/client.go`, `internal/provider/sandbox/adapter.go` |
| 4 | `CreatePayment` + verifikasi total | `internal/service/payment_service.go`, `payment_verify.go`, handler error map |
| 5 | Antarmuka: respons, callback, export | `internal/service/merchant_callback_service.go`, `admin_payment_service.go`, `internal/handler/*.go` |
| 6 | Endpoint admin `fee-bearer` | `internal/service/admin_merchant_service.go`, `internal/handler/admin_handler.go`, `cmd/api/main.go` |
| 7 | Front-end admin | `front/lib/admin-api.ts`, halaman admin |
| 8 | Front-end merchant, checkout, API docs | `front/lib/merchant-api.ts`, halaman merchant dan checkout |
| 9 | Dokumentasi | `docs/openapi.yaml`, `README.md`, `DEPLOY.md` |

Task 1 → 3, 4. Task 2 → 4, 5, 6. Task 3 → 4. Task 4 → 5. Task 5, 6 → 7, 8. Task 9 terakhir. Urutan eksekusi: 1, 2, 3, 4, 5, 6, 7, 8, 9.

---

### Task 1: Paket `internal/fee` dan antarmuka `FeeQuoter`

**Files:** Create `internal/fee/fee.go`, `internal/fee/fee_test.go`, `internal/provider/fee.go`.

**Interfaces — Produces:**

```go
package fee
func QuoteTotalBasisPoints(nominal, bps int64) (total, feeAmount int64, err error)
var ErrInvalidNominal, ErrInvalidRate error
const MaxNominal int64
package provider // internal/provider (providerPkg)
type FeeQuoter interface { QuoteTotal(nominal int64) (total, feeAmount int64, err error) }
var ErrFeeNotSupported error
```

- [ ] **Step 1: Tes gagal** `internal/fee/fee_test.go`:

```go
package fee

import (
	"errors"
	"testing"
)

func ceilFee(gross, bps int64) int64 { return (gross*bps + 9999) / 10000 }

func TestQuoteTotalBasisPoints_KnownValues(t *testing.T) {
	tests := []struct {
		nominal, bps, wantTotal, wantFee int64
	}{
		{2000, 70, 2015, 15}, // the real Rp 2.000 case: total 2.014 would net only 1.999
		{1000, 70, 1008, 8},
		{10000, 70, 10071, 71},
		{1, 70, 2, 1},
		{2000, 0, 2000, 0}, // zero rate -> no fee
		{5000, 100, 5051, 51},
	}
	for _, tc := range tests {
		total, fee, err := QuoteTotalBasisPoints(tc.nominal, tc.bps)
		if err != nil {
			t.Fatalf("(%d,%d): unexpected error %v", tc.nominal, tc.bps, err)
		}
		if total != tc.wantTotal || fee != tc.wantFee {
			t.Errorf("(%d,%d) = total %d fee %d, want total %d fee %d", tc.nominal, tc.bps, total, fee, tc.wantTotal, tc.wantFee)
		}
	}
}

// For every nominal the total must leave at least the nominal after the
// (rounded-up) provider fee, and be the smallest total that does.
func TestQuoteTotalBasisPoints_ExactMinimumProperty(t *testing.T) {
	for _, bps := range []int64{70, 100, 250, 1} {
		for nominal := int64(1); nominal <= 200000; nominal++ {
			total, fee, err := QuoteTotalBasisPoints(nominal, bps)
			if err != nil {
				t.Fatalf("(%d,%d): %v", nominal, bps, err)
			}
			if fee != total-nominal || fee < 0 {
				t.Fatalf("(%d,%d): fee %d inconsistent with total %d", nominal, bps, fee, total)
			}
			if total-ceilFee(total, bps) < nominal {
				t.Fatalf("(%d,%d): total %d leaves %d, less than nominal", nominal, bps, total, total-ceilFee(total, bps))
			}
			if total > nominal && (total-1)-ceilFee(total-1, bps) >= nominal {
				t.Fatalf("(%d,%d): total %d is not minimal", nominal, bps, total)
			}
		}
	}
}

func TestQuoteTotalBasisPoints_Invalid(t *testing.T) {
	for _, n := range []int64{0, -5, MaxNominal + 1} {
		if _, _, err := QuoteTotalBasisPoints(n, 70); !errors.Is(err, ErrInvalidNominal) {
			t.Errorf("nominal %d: want ErrInvalidNominal, got %v", n, err)
		}
	}
	for _, bps := range []int64{-1, 10000, 20000} {
		if _, _, err := QuoteTotalBasisPoints(2000, bps); !errors.Is(err, ErrInvalidRate) {
			t.Errorf("bps %d: want ErrInvalidRate, got %v", bps, err)
		}
	}
	if _, _, err := QuoteTotalBasisPoints(MaxNominal, 70); err != nil {
		t.Errorf("MaxNominal itself must not overflow: %v", err)
	}
}
```

Run: `go test ./internal/fee -count=1` → gagal (paket belum ada).

- [ ] **Step 2: Implementasi** `internal/fee/fee.go`:

```go
// Package fee computes the total a customer must pay so that the merchant
// still receives the nominal amount after the payment provider deducts its
// percentage fee. Integer arithmetic only: money never touches a float.
package fee

import "errors"

const (
	bpsDenominator = 10000

	// MaxNominal keeps nominal*10000 well inside int64.
	MaxNominal int64 = 1_000_000_000_000
)

var (
	ErrInvalidNominal = errors.New("fee: nominal must be between 1 and MaxNominal")
	ErrInvalidRate    = errors.New("fee: basis points must be in [0, 10000)")
)

// providerFee is the fee the provider takes from a gross amount. It assumes the
// provider rounds UP (the conservative direction: the merchant is never short).
func providerFee(gross, bps int64) int64 {
	return (gross*bps + bpsDenominator - 1) / bpsDenominator
}

// QuoteTotalBasisPoints returns the smallest total such that
// total - ceil(total*bps/10000) >= nominal, and the fee (total - nominal).
//
// total - providerFee(total) never decreases as total grows (the fee rises by
// at most 1 per rupiah), so starting at the real-valued solution and walking to
// the exact minimum is correct. If the provider actually rounds down, the
// merchant ends up at most 1 rupiah ahead.
func QuoteTotalBasisPoints(nominal, bps int64) (total, feeAmount int64, err error) {
	if nominal < 1 || nominal > MaxNominal {
		return 0, 0, ErrInvalidNominal
	}
	if bps < 0 || bps >= bpsDenominator {
		return 0, 0, ErrInvalidRate
	}
	if bps == 0 {
		return nominal, 0, nil
	}

	denom := bpsDenominator - bps
	g := (nominal*bpsDenominator + denom - 1) / denom // ceil(nominal / (1 - bps/10000))
	for g > nominal && (g-1)-providerFee(g-1, bps) >= nominal {
		g--
	}
	for g-providerFee(g, bps) < nominal {
		g++
	}
	return g, g - nominal, nil
}
```

`internal/provider/fee.go` (paket `provider`; baca file lain di direktori itu untuk gaya dan nama paket):

```go
package provider

import "errors"

// FeeQuoter is implemented by providers that deduct a percentage fee at
// settlement and can tell, for a merchant-facing nominal amount, the total the
// customer must pay so the merchant still nets the nominal.
type FeeQuoter interface {
	QuoteTotal(nominal int64) (total, feeAmount int64, err error)
}

// ErrFeeNotSupported is returned when a merchant is configured so the customer
// bears the fee but the selected provider cannot quote one. Failing closed is
// deliberate: silently charging the bare nominal would leave the merchant short.
var ErrFeeNotSupported = errors.New("provider cannot quote a customer-borne fee")
```

- [ ] **Step 3: Verifikasi** `go test ./internal/fee -race -count=1` hijau; mutasi: ubah pembulatan `providerFee` menjadi floor → test properti gagal; ubah `g--` loop (hapus) → test "minimal" gagal. `go vet ./...`.
- [ ] **Step 4: Report** (tanpa commit).

---

### Task 2: Migration 020, domain, repository

**Files:** Create `migrations/020_add_customer_borne_fee.sql`, `internal/repository/fee_columns_pg_test.go`. Modify `internal/domain/payment/payment.go`, `internal/domain/merchant/merchant.go`, `internal/repository/payment_repository.go`, `payment_repository_update.go`, `merchant_repository.go`; perbarui test/fake yang menyalin struct bila perlu.

**Interfaces — Produces:**

```go
// payment
Payment.FeeAmount int64            // json "fee_amount", db fee_amount
func (p *Payment) TotalAmount() int64 // Amount + FeeAmount
PaymentResponse.FeeAmount, PaymentResponse.TotalAmount int64
PaymentStatusResponse.FeeAmount, PaymentStatusResponse.TotalAmount int64
// merchant
const FeeBearerMerchant = "merchant"; FeeBearerCustomer = "customer"
func IsValidFeeBearer(s string) bool
var ErrInvalidFeeBearer error
Merchant.FeeBearer string; MerchantResponse.FeeBearer string
// repository
func (r *MerchantRepository) SetFeeBearer(ctx context.Context, id uuid.UUID, feeBearer string) error // ErrMerchantNotFound when no row
```

- [ ] **Step 1: Migration** `migrations/020_add_customer_borne_fee.sql` (satu transaksi, idempoten, pencarian constraint dibatasi `conrelid` seperti migration 019):

```sql
-- Migration 020: customer-borne provider fee.
-- merchants.fee_bearer decides who pays the provider's percentage fee; the
-- default keeps today's behaviour for every existing merchant.
-- payments.fee_amount is the fee added on top of payments.amount (the
-- merchant's nominal); the customer pays amount + fee_amount. Existing and
-- in-flight payments get 0, so their total equals amount.
BEGIN;

ALTER TABLE merchants ADD COLUMN IF NOT EXISTS fee_bearer VARCHAR(20) NOT NULL DEFAULT 'merchant';
ALTER TABLE payments ADD COLUMN IF NOT EXISTS fee_amount BIGINT NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'merchants'::regclass AND conname = 'merchants_fee_bearer_valid'
    ) THEN
        ALTER TABLE merchants ADD CONSTRAINT merchants_fee_bearer_valid
            CHECK (fee_bearer IN ('merchant', 'customer'));
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'payments'::regclass AND conname = 'payments_fee_amount_non_negative'
    ) THEN
        ALTER TABLE payments ADD CONSTRAINT payments_fee_amount_non_negative
            CHECK (fee_amount >= 0);
    END IF;
END $$;

COMMENT ON COLUMN merchants.fee_bearer IS 'merchant: customer pays the nominal; customer: customer pays nominal + provider fee';
COMMENT ON COLUMN payments.fee_amount IS 'Provider fee added on top of amount; customer pays amount + fee_amount';

COMMIT;
```

- [ ] **Step 2: Tes PG gagal** `internal/repository/fee_columns_pg_test.go` (pola `newPGRepo(t)` dari `payment_repository_pg_test.go`, skip bila `TEST_DATABASE_URL` kosong; helper `insertConstraintMerchant` dari `payment_constraints_pg_test.go` dapat dipakai ulang di paket yang sama):

```go
package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/google/uuid"
)

func TestFeeColumns_DefaultsKeepTodaysBehaviour(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	var bearer string
	if err := db.QueryRow(`SELECT fee_bearer FROM merchants WHERE id = $1`, m).Scan(&bearer); err != nil || bearer != "merchant" {
		t.Fatalf("new merchant fee_bearer = %q (err %v), want 'merchant'", bearer, err)
	}
	if err := insertConstraintPayment(db, m, 1000, "pending", nil); err != nil {
		t.Fatal(err)
	}
	var fee int64
	if err := db.QueryRow(`SELECT fee_amount FROM payments WHERE merchant_id = $1`, m).Scan(&fee); err != nil || fee != 0 {
		t.Fatalf("new payment fee_amount = %d (err %v), want 0", fee, err)
	}
}

func TestFeeColumns_Constraints(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	if _, err := db.Exec(`UPDATE merchants SET fee_bearer = 'platform' WHERE id = $1`, m); err == nil {
		t.Error("fee_bearer outside {merchant, customer} must be rejected")
	}
	if _, err := db.Exec(`UPDATE merchants SET fee_bearer = 'customer' WHERE id = $1`, m); err != nil {
		t.Errorf("fee_bearer customer must be accepted: %v", err)
	}
	id := uuid.New()
	_, err := db.Exec(
		`INSERT INTO payments (id, reference, merchant_id, amount, fee_amount, payment_method, provider_name, status, description, expires_at)
		 VALUES ($1, $2, $3, 1000, -1, 'qris', 'cashi', 'pending', 'd', now() + interval '1 hour')`,
		id, "REF-"+id.String(), m)
	wantViolation(t, err, "payments_fee_amount_non_negative")
}

func TestFeeColumns_RepositoryRoundTrip(t *testing.T) {
	repo, db := newPGRepo(t)
	ctx := context.Background()
	m := insertConstraintMerchant(t, db)
	p := &payment.Payment{
		ID: uuid.New(), Reference: "REF-RT-" + uuid.NewString(), MerchantID: m,
		Amount: 2000, FeeAmount: 15, Currency: "IDR", PaymentMethod: "qris",
		ProviderName: "cashi", Status: payment.StatusPending, Description: "d",
		Environment: payment.EnvironmentProduction,
		ExpiresAt:   timeNowPlusHour(), CreatedAt: timeNow(), UpdatedAt: timeNow(),
	}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Amount != 2000 || got.FeeAmount != 15 || got.TotalAmount() != 2015 {
		t.Fatalf("round trip: amount=%d fee=%d total=%d", got.Amount, got.FeeAmount, got.TotalAmount())
	}
	rows, err := repo.ListAdmin(ctx, "", "", nil, nil, nil, "", 10, 0)
	if err != nil || len(rows) != 1 || rows[0].Payment.FeeAmount != 15 {
		t.Fatalf("ListAdmin must carry fee_amount: err=%v rows=%v", err, rows)
	}
}

func TestMerchantRepository_FeeBearer(t *testing.T) {
	_, db := newPGRepo(t)
	ctx := context.Background()
	repo := NewMerchantRepository(db)
	m := insertConstraintMerchant(t, db)

	got, err := repo.GetByID(ctx, m)
	if err != nil || got.FeeBearer != merchant.FeeBearerMerchant {
		t.Fatalf("default fee bearer = %q (err %v)", got.FeeBearer, err)
	}
	if err := repo.SetFeeBearer(ctx, m, merchant.FeeBearerCustomer); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetByID(ctx, m)
	if got.FeeBearer != merchant.FeeBearerCustomer {
		t.Fatalf("fee bearer after set = %q", got.FeeBearer)
	}
	if err := repo.SetFeeBearer(ctx, uuid.New(), merchant.FeeBearerCustomer); err != merchant.ErrMerchantNotFound {
		t.Fatalf("unknown merchant: want ErrMerchantNotFound, got %v", err)
	}
}

func TestMigration020_IsIdempotent(t *testing.T) {
	_, db := newPGRepo(t)
	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", "020_add_customer_borne_fee.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("re-applying migration 020 (run %d): %v", i+1, err)
		}
	}
	m := insertConstraintMerchant(t, db)
	if _, err := db.Exec(`UPDATE merchants SET fee_bearer = 'platform' WHERE id = $1`, m); err == nil {
		t.Error("constraint must still reject an invalid fee_bearer after re-applying")
	}
}
```

(`timeNow()`/`timeNowPlusHour()` adalah pembantu kecil yang kamu tulis di file tes itu — `time.Now().UTC()` dan `+ time.Hour`; atau pakai `time` langsung. Sesuaikan tanda tangan `ListAdmin` dengan kode nyata di `payment_repository_update.go`.) Run dengan PG18 sementara → gagal.

- [ ] **Step 3: Domain.**

`payment.go`: tambahkan setelah `Amount` pada struct `Payment`: `FeeAmount int64 \`json:"fee_amount" db:"fee_amount"\``; metode

```go
// TotalAmount is what the customer pays: the merchant's nominal plus any
// provider fee added on top (zero unless the merchant has the customer bear it).
func (p *Payment) TotalAmount() int64 { return p.Amount + p.FeeAmount }
```

`PaymentResponse` dan `PaymentStatusResponse`: tambahkan `FeeAmount int64 \`json:"fee_amount"\`` dan `TotalAmount int64 \`json:"total_amount"\``, serta isi di `ToPaymentResponse` (`FeeAmount: p.FeeAmount, TotalAmount: p.TotalAmount()`) dan `ToPaymentStatusResponse`.

`merchant.go`: tambahkan (di paket `merchant`, mis. file terpisah `fee_bearer.go`):

```go
const (
	FeeBearerMerchant = "merchant"
	FeeBearerCustomer = "customer"
)

var ErrInvalidFeeBearer = errors.New("fee_bearer must be 'merchant' or 'customer'")

func IsValidFeeBearer(s string) bool { return s == FeeBearerMerchant || s == FeeBearerCustomer }
```

`Merchant`: field `FeeBearer string \`json:"fee_bearer" db:"fee_bearer"\``; `MerchantResponse`: `FeeBearer string \`json:"fee_bearer"\``; isi di `ToMerchantResponse`. **Jangan** menambah field ke `UpdateMerchantRequest` (dipakai juga oleh merchant untuk profil bisnisnya).

- [ ] **Step 4: Repository.**

`payment_repository.go`: tambahkan `fee_amount` di **akhir** `paymentSelectCols` (setelah `updated_at`) dan `&p.FeeAmount` di akhir `scanPayment`; pada `Create` tambahkan kolom `fee_amount` di akhir daftar kolom INSERT, `$20` di VALUES, dan `p.FeeAmount` sebagai argumen terakhir. `payment_repository_update.go`: pada query `ListAdmin` (~baris 305) tambahkan `p.fee_amount` setelah `p.updated_at` dan pada `Scan` tambahkan `&p.FeeAmount` di posisi yang sama (sebelum `&merchantName`). Cari semua query lain yang memilih kolom payment secara eksplisit: `grep -n "p.reference\|reference, merchant_id" internal/repository/*.go`.

`merchant_repository.go`: tambahkan `fee_bearer` ke ketiga SELECT (`GetByID` ~baris 63, `GetByEmail` ~98, `List` ~183) dan ke `Scan` masing-masing (`&m.FeeBearer`, posisi sama), dan metode:

```go
// SetFeeBearer changes who pays the provider fee. Admin-only at the API layer;
// the value is validated by the caller and again by the table's CHECK.
func (r *MerchantRepository) SetFeeBearer(ctx context.Context, id uuid.UUID, feeBearer string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE merchants SET fee_bearer = $2, updated_at = $3 WHERE id = $1`,
		id, feeBearer, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("failed to set fee bearer: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return merchant.ErrMerchantNotFound
	}
	return nil
}
```

(sesuaikan impor `time`/`fmt` dengan file.)

- [ ] **Step 5: Verifikasi** — seluruh suite tanpa PG, lalu repository dengan PG18 sementara (`TEST_DATABASE_URL=… go test ./internal/repository -race -count=1`) dan suite penuh dengan PG. Sqlmock-based tests yang mencocokkan daftar kolom persis harus diperbarui ke kolom baru. Mutasi: hapus `&p.FeeAmount` dari `scanPayment` → test round-trip gagal; hapus `fee_amount` dari INSERT → gagal; hapus `fee_bearer` dari satu SELECT merchant → test gagal. Bersihkan PG. **Report** (tanpa commit).

---

### Task 3: Adapter Cashi dan sandbox

**Files:** Modify `internal/provider/cashi/client.go`, `internal/provider/cashi/client_test.go`, `internal/provider/sandbox/adapter.go`, `internal/provider/sandbox/adapter_test.go`.

**Interfaces — Consumes:** `fee.QuoteTotalBasisPoints`, `provider.FeeQuoter` (Task 1; di paket adapter alias impor mengikuti file: domain provider dipakai sebagai `provider`, jadi impor paket `internal/provider` hati-hati terhadap siklus — pastikan `internal/provider` tidak mengimpor adapter; bila siklus, cukup biarkan adapter mengimplementasikan metode `QuoteTotal` tanpa mengimpor `FeeQuoter` dan tambahkan assertion `var _ providerPkg.FeeQuoter = (*CashiAdapter)(nil)` hanya di file test). **Produces:** `(*CashiAdapter).QuoteTotal`, `(*Adapter).QuoteTotal` (sandbox).

- [ ] **Step 1: Tes gagal.**

Cashi (`client_test.go`):

```go
func TestCashiAdapter_QuoteTotal(t *testing.T) {
	a := &CashiAdapter{}
	total, fee, err := a.QuoteTotal(2000)
	if err != nil || total != 2015 || fee != 15 {
		t.Fatalf("QuoteTotal(2000) = %d,%d,%v; want 2015,15,nil", total, fee, err)
	}
	if _, _, err := a.QuoteTotal(0); err == nil {
		t.Error("a non-positive nominal must be rejected")
	}
}
```

(bangun adapter dengan konstruktor yang dipakai tes lain di file itu bila `&CashiAdapter{}` kurang; metode tidak memakai state.)

Sandbox (`adapter_test.go`): (a) `QuoteTotal(2000)` = 2015/15; (b) `CreatePayment` dengan `Amount: 2015` mengembalikan `Amount == 2015` dan `GetPaymentStatus` melaporkan jumlah yang sama — dan ini berlaku berulang (jalankan 50 kali): tidak ada akhiran acak; (c) tes lama yang mengandalkan akhiran 1–99 diperbarui.

Run → gagal.

- [ ] **Step 2: Implementasi.**

`cashi/client.go`:

```go
// feeBasisPoints is Cashi's percentage fee (0.7%), deducted from the gross
// amount at settlement (seen in production: Rp 2.000 -> Rp 1.986 net).
const feeBasisPoints = 70

// QuoteTotal tells the total a customer must pay so the merchant still nets
// the nominal after Cashi's fee (see internal/fee for the rounding rule).
func (a *CashiAdapter) QuoteTotal(nominal int64) (total, feeAmount int64, err error) {
	return fee.QuoteTotalBasisPoints(nominal, feeBasisPoints)
}
```

(impor `github.com/akbarryyan/pg-aggregator-back/internal/fee`.) `sandbox/adapter.go`: tambah metode yang sama dengan konstanta lokal `feeBasisPoints = 70 // mirrors Cashi so merchants can test with identical numbers`; **hapus** akhiran acak: ganti

```go
	// Mirror Cashi-like unique amount suffix (1–99) without network
	unique := time.Now().UnixNano()%99 + 1
	finalAmount := req.Amount + unique
```

dengan `finalAmount := req.Amount` dan komentar bahwa pembayaran Cashi nyata tidak menambahkan akhiran. Hapus impor yang tidak terpakai.

- [ ] **Step 3: Verifikasi** — suite penuh; periksa `grep -rn "unique\|suffix" internal/provider/sandbox internal/service/*_test.go internal/handler/*_test.go` untuk tes yang bergantung pada akhiran. Mutasi: ubah `feeBasisPoints` jadi 71 → test Cashi gagal; kembalikan akhiran acak di sandbox → test (b) gagal. **Report** (tanpa commit).

---

### Task 4: `CreatePayment` dan verifikasi nominal total

**Files:** Modify `internal/service/payment_service.go`, `internal/service/payment_verify.go`, `internal/handler/payment_handler.go` (peta error), tes `internal/service/payment_service_test.go` (+ fakes `payment_fakes_test.go`), `internal/service/payment_verify_test.go`, `internal/service/payment_webhook_service_test.go` bila perlu.

**Interfaces — Consumes:** `Merchant.FeeBearer`, `FeeBearerCustomer`, `Payment.FeeAmount/TotalAmount()` (Task 2); `providerPkg.FeeQuoter`, `providerPkg.ErrFeeNotSupported` (Task 1); adapter `QuoteTotal` (Task 3).

- [ ] **Step 1: Tes gagal.**

(a) `payment_verify_test.go` — tabel `verifyProviderPayment` dengan payment ber-fee: `p.Amount=2000, p.FeeAmount=15`; provider amount 2015 → sukses; provider 2000 → `payment.ErrAmountMismatch` (pesan memuat `expected 2015`); webhook 2000 sementara provider 2015 → `payment.ErrWebhookAmountMismatch`; payment tanpa fee tetap berperilaku lama.

(b) `payment_service_test.go` — pola penyiapan dari `TestCreatePayment_Production_DefaultRouting` (router + config repo) dan `TestCreatePayment_Sandbox_UsesSandboxProviderOnly`. Tambahkan fake:

```go
// fakeQuoterProvider is a fakeProvider that can quote a customer-borne fee.
type fakeQuoterProvider struct {
	*fakeProvider
	bps int64
}

func (f *fakeQuoterProvider) QuoteTotal(n int64) (int64, int64, error) {
	return fee.QuoteTotalBasisPoints(n, f.bps)
}
```

Kasus (merchant disimpan di `newFakeMerchantRepo()` lalu `svc.WithMerchantCallbackDeps(merchantRepo, newFakeMerchantCallbackRepo())`):
1. merchant `customer`, sandbox quoter bps 70, nominal 2000, provider mengembalikan `Amount` sama dengan yang diminta → provider menerima `Amount == 2015` (rekam `ProviderPaymentRequest` pada fake — tambahkan field `lastCreateReq` ke `fakeProvider` bila belum ada), payment tersimpan `Amount == 2000`, `FeeAmount == 15`, `TotalAmount() == 2015`.
2. merchant `customer`, provider mengembalikan `Amount` lebih besar dari kuotasi (mis. 2020) → `FeeAmount == 20` (fee mencerminkan apa yang ditagih provider), `Amount == 2000`.
3. merchant `customer`, provider mengembalikan `Amount` < nominal (mis. 1990) → error `errors.Is(err, payment.ErrProviderError)` dan tidak ada payment tersimpan.
4. merchant `customer`, provider tanpa `FeeQuoter` (fakeProvider biasa) → `errors.Is(err, providerPkg.ErrFeeNotSupported)`, provider tidak dipanggil (`createCalls == 0`), tidak ada payment.
5. produksi dengan failover: kandidat pertama quoter bps 70 gagal, kandidat kedua quoter bps 100 → kuotasi dihitung per kandidat (kandidat kedua menerima total untuk 100 bps), `FeeAmount` sesuai kandidat yang berhasil. (Gunakan pola failover test yang sudah ada; bila tidak ada, uji dua kandidat via `configRepo`.)
6. merchant `merchant` (default) dan merchantRepo nil → tidak berubah: provider menerima nominal, `FeeAmount == 0`, `p.Amount` menimpa dengan respons provider seperti sebelumnya.
7. merchant `customer` + `CreatePaymentIdempotent` dua kali dengan kunci sama → payment sama, `FeeAmount` tetap, provider dipanggil sekali.

(c) `payment_handler_test.go`: `respondCreatePaymentError` memetakan `providerPkg.ErrFeeNotSupported` ke 503 dengan pesan "Fee calculation is not available for this payment provider".

Run → gagal.

- [ ] **Step 2: Implementasi.**

`payment_verify.go` — dalam `verifyProviderPayment` ganti kedua pembanding `p.Amount` menjadi `expected := p.TotalAmount()` (dipakai pada perbandingan dan pesan `expected %d`); perbarui komentar fungsi: "nominal kotor yang diharapkan = nominal merchant + fee yang ditambahkan (0 bila merchant menanggung)". Periksa `grep -n "p.Amount\|\.Amount" internal/service/payment_webhook_service.go internal/service/payment_reconcile*.go internal/service/payment_service.go` — setiap pembandingan dengan angka provider harus memakai `TotalAmount()`.

`payment_service.go`:

1. Ganti `ensureMerchantActive` (tetap pertahankan namanya bila tes memanggilnya langsung) agar memuat merchant sekali dan mengembalikan pengaturan fee:

```go
// checkMerchantForCreate is the single choke point for every creation path. It
// rejects inactive merchants and reports who bears the provider fee. An
// unknown merchant is left to the foreign key; with no merchant repo (unit-test
// wiring) the check is skipped and the merchant bears the fee, as before.
func (s *PaymentService) checkMerchantForCreate(ctx context.Context, merchantID uuid.UUID) (feeBearer string, err error) {
	if s.merchantRepo == nil {
		return merchant.FeeBearerMerchant, nil
	}
	m, err := s.merchantRepo.GetByID(ctx, merchantID)
	if err != nil {
		if errors.Is(err, merchant.ErrMerchantNotFound) {
			return merchant.FeeBearerMerchant, nil
		}
		return "", fmt.Errorf("failed to load merchant: %w", err)
	}
	if !m.IsActive {
		return "", merchant.ErrMerchantInactive
	}
	if m.FeeBearer == merchant.FeeBearerCustomer {
		return merchant.FeeBearerCustomer, nil
	}
	return merchant.FeeBearerMerchant, nil
}
```

dan di `CreatePayment`: `feeBearer, err := s.checkMerchantForCreate(ctx, req.MerchantID)` menggantikan pemanggilan lama (hati-hati dengan variabel `err` yang sudah dideklarasikan di fungsi).

2. Tambahkan pembantu:

```go
// chargeFor returns the amount to send to prov for a payment of nominal: the
// bare nominal, or — when the customer bears the fee — the provider's quoted
// total. A provider that cannot quote fails closed (ErrFeeNotSupported).
func chargeFor(prov providerPkg.PaymentProvider, feeBearer string, nominal int64) (int64, error) {
	if feeBearer != merchant.FeeBearerCustomer {
		return nominal, nil
	}
	q, ok := prov.(providerPkg.FeeQuoter)
	if !ok {
		return 0, fmt.Errorf("provider %s: %w", prov.GetName(), providerPkg.ErrFeeNotSupported)
	}
	total, _, err := q.QuoteTotal(nominal)
	if err != nil {
		return 0, fmt.Errorf("provider %s fee quote: %w", prov.GetName(), err)
	}
	return total, nil
}
```

3. Sandbox cabang: sebelum membangun `providerReq`, `charge, err := chargeFor(s.sandboxProvider, feeBearer, req.Amount)`; error → return; `providerReq.Amount = charge`; simpan `requestedCharge = charge`. Cabang produksi, di dalam loop kandidat: `charge, qerr := chargeFor(selectedProvider, feeBearer, req.Amount)`; jika `qerr != nil` → `lastErr = qerr; providerResp = nil; ` lanjut ke kandidat berikutnya mengikuti pola kegagalan yang ada (hormati `candidate.failoverEnabled`); jika berhasil `providerReq.Amount = charge` dan `requestedCharge = charge`. Deklarasikan `var requestedCharge int64 = req.Amount` sebelum percabangan.

4. Ganti blok setelah respons provider:

```go
	charged := providerResp.Amount
	if charged <= 0 {
		charged = requestedCharge
	}
	if feeBearer == merchant.FeeBearerCustomer {
		// The merchant's nominal never changes; the fee is whatever the
		// provider actually charged on top of it.
		if charged < req.Amount {
			return nil, fmt.Errorf("provider %s charged %d, below the nominal %d: %w",
				providerResp.ProviderName, charged, req.Amount, payment.ErrProviderError)
		}
		p.FeeAmount = charged - req.Amount
	} else if providerResp.Amount > 0 {
		p.Amount = providerResp.Amount // unchanged legacy behaviour
	}
```

(Pastikan `p.Amount` untuk `customer` tetap `req.Amount`.)

5. `handler/payment_handler.go` `respondCreatePaymentError`: tambahkan sebelum default `case errors.Is(err, providerPkg.ErrFeeNotSupported): respondError(w, http.StatusServiceUnavailable, "Fee calculation is not available for this payment provider")`. Periksa juga peta error di `admin_handler.go` CreatePayment dan `payment_link_handler.go` agar kasus ini tidak menjadi 500 mentah (pakai `respondCreatePaymentError` bila sudah dipakai di sana).

- [ ] **Step 3: Verifikasi** — suite penuh. Mutasi: pakai `p.Amount` lagi di `verifyProviderPayment` → tes (a) gagal; kirim nominal ke provider untuk merchant `customer` → tes 1 gagal; hapus pengecekan `charged < req.Amount` → tes 3 gagal; hapus pengecekan `FeeQuoter` → tes 4 gagal. **Report** (tanpa commit).

---

### Task 5: Antarmuka — respons, callback, export

**Files:** Modify `internal/service/merchant_callback_service.go`, `internal/service/admin_payment_service.go`, `internal/handler/payment_handler.go` (lookup publik), `internal/handler/merchant_handler.go` dan `admin_handler.go` (export CSV), dan tes yang relevan.

- [ ] **Step 1: Tes gagal.**
  - Callback: payment `Amount=2000, FeeAmount=15` → payload JSON memuat `amount: 2000`, `fee_amount: 15`, `total_amount: 2015`; payment tanpa fee → `fee_amount: 0`, `total_amount` = amount (ikuti tes payload callback yang sudah ada di `merchant_callback_service_test.go`).
  - Lookup publik (`GetPaymentByReference`, `payment_handler.go` ~baris 125): peta respons memuat `fee_amount` dan `total_amount`; tes di `payment_handler_test.go`.
  - DTO admin/merchant (`admin_payment_service.go` struct di ~baris 48 dan pengisian ~baris 146): `FeeAmount` dan `TotalAmount` ada pada JSON; tes di `admin_payment_service_test.go`.
  - CSV: header memuat `fee_amount` dan `total_amount` sebagai dua kolom TERAKHIR (kolom lama tidak bergeser), nilai baris benar; tes untuk export merchant dan admin payments.
  - `ToPaymentResponse` sudah mengisi field baru (Task 2); tambahkan assertion pada tes pembuatan payment (`payment_handler_test.go`): respons 201 memuat `fee_amount` dan `total_amount`.
  Run → gagal.

- [ ] **Step 2: Implementasi.**
  - `merchant_callback_service.go` (payload, ~baris 109): tambahkan `"fee_amount": p.FeeAmount,` dan `"total_amount": p.TotalAmount(),` di samping `"amount": p.Amount,`.
  - `payment_handler.go` lookup publik: tambahkan `"fee_amount": p.FeeAmount, "total_amount": p.TotalAmount(),`.
  - `admin_payment_service.go`: tambahkan `FeeAmount int64 \`json:"fee_amount"\`` dan `TotalAmount int64 \`json:"total_amount"\`` pada DTO, isi dari `p.FeeAmount` dan `p.TotalAmount()`.
  - Export CSV (merchant `ExportPayments` ~baris 215 header dan ~239 baris data; admin `ExportPayments` ~218/256): tambahkan `"fee_amount", "total_amount"` di akhir header dan `strconv.FormatInt(item.FeeAmount, 10)`, `strconv.FormatInt(item.TotalAmount, 10)` di akhir baris (sesuaikan nama field item dengan DTO yang dipakai di sana; bila item berupa `*payment.Payment`, gunakan `item.TotalAmount()`).
  - Periksa `grep -rn "amount" internal/handler/*.go internal/service/*.go | grep -v _test` untuk peta respons lain yang menampilkan payment (mis. detail payment merchant, daftar callback) dan tambahkan field yang sama bila payment dilayani.

- [ ] **Step 3: Verifikasi** — suite penuh; mutasi: hapus `fee_amount` dari payload callback → test gagal; geser kolom CSV baru ke tengah → test urutan kolom gagal. **Report** (tanpa commit).

---

### Task 6: Endpoint admin `fee-bearer`

**Files:** Modify `internal/service/admin_merchant_service.go` (+ antarmuka repository-nya), `internal/handler/admin_handler.go`, `cmd/api/main.go`; tes `admin_merchant_service_test.go`, `admin_handler` test, `cmd/api/router_test.go`.

**Interfaces — Consumes:** `MerchantRepository.SetFeeBearer`, `merchant.IsValidFeeBearer`, `merchant.ErrInvalidFeeBearer` (Task 2). **Produces:** `(*AdminMerchantService).SetFeeBearer(ctx, id uuid.UUID, feeBearer string) (*merchant.MerchantResponse, error)`; `(*AdminHandler).SetMerchantFeeBearer`.

- [ ] **Step 1: Tes gagal.**
  - Service: nilai valid → repo dipanggil dan respons memuat `FeeBearer` baru; nilai tidak valid (`"platform"`, `""`) → `merchant.ErrInvalidFeeBearer` tanpa memanggil repo; merchant tidak ada → `merchant.ErrMerchantNotFound`. (Tambahkan `SetFeeBearer` ke antarmuka repository yang dipakai service dan ke fake-nya.)
  - Handler: `PUT` dengan `{"fee_bearer":"customer"}` → 200 dan body merchant memuat `"fee_bearer":"customer"`; `{"fee_bearer":"x"}` → 400; ID bukan UUID → 400; merchant tidak ada → 404; body rusak → 400; error repo tak dikenal → 500 generik tanpa teks internal.
  - Router (`cmd/api/router_test.go`, pola tes auth/limiter yang ada): tanpa token → 401; dengan JWT **merchant** pada rute admin → 401 (dan bukan 200); rute berada di bawah `apiRateLimiter` (melebihi budget → 429 dengan token palsu).
  Run → gagal.

- [ ] **Step 2: Implementasi.**

Service:

```go
// SetFeeBearer changes who pays the provider fee for a merchant. Admin only:
// it is deliberately NOT part of UpdateMerchantRequest, which merchants also
// use to edit their own business profile.
func (s *AdminMerchantService) SetFeeBearer(ctx context.Context, id uuid.UUID, feeBearer string) (*merchant.MerchantResponse, error) {
	if !merchant.IsValidFeeBearer(feeBearer) {
		return nil, merchant.ErrInvalidFeeBearer
	}
	if err := s.merchantRepo.SetFeeBearer(ctx, id, feeBearer); err != nil {
		return nil, err
	}
	m, err := s.merchantRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return merchant.ToMerchantResponse(m), nil
}
```

(sesuaikan nama field repository di struct service dengan kode nyata.) Handler (pola `SetMerchantActive`):

```go
func (h *AdminHandler) SetMerchantFeeBearer(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid merchant ID")
		return
	}
	var req struct {
		FeeBearer string `json:"fee_bearer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	m, err := h.adminMerchantService.SetFeeBearer(r.Context(), id, req.FeeBearer)
	switch {
	case err == nil:
		respondJSON(w, http.StatusOK, m)
	case errors.Is(err, merchant.ErrInvalidFeeBearer):
		respondError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, merchant.ErrMerchantNotFound):
		respondError(w, http.StatusNotFound, "Merchant not found")
	default:
		logger.ErrorfCtx(r.Context(), "Failed to set merchant fee bearer: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to update fee bearer")
	}
}
```

Rute di `setupRouter` (grup `adminAPI`, dekat `/merchants/{id}/status`): `adminAPI.HandleFunc("/merchants/{id}/fee-bearer", adminHandler.SetMerchantFeeBearer).Methods("PUT")`. Pastikan `MerchantHandler.UpdateBusiness`/`UpdateMerchantRequest` tidak berubah (tes: merchant tidak bisa mengubah `fee_bearer` lewat endpoint bisnisnya — kirim `{"fee_bearer":"customer"}` ke `PUT /merchant/business`, merchant tetap `merchant`).

- [ ] **Step 3: Verifikasi** — suite penuh; mutasi: pindahkan rute ke grup merchant → tes router gagal; hapus validasi → tes 400 gagal. **Report** (tanpa commit).

---

### Task 7: Front-end admin

**Files:** Modify `front/lib/admin-api.ts`, `front/app/admin/(panel)/merchants/[id]/page.tsx`, `front/app/admin/(panel)/payments/page.tsx`, `front/app/admin/(panel)/payments/[id]/page.tsx`, dan (bila menampilkan nominal payment) `front/app/admin/(panel)/page.tsx`, `reconciliation/page.tsx`.

- [ ] **Step 1: Baca** `front/AGENTS.md` dan dokumen Next relevan (catat apa yang dibaca). Baca `admin-api.ts` (tipe payment di ~baris 23, 183, 582; tipe merchant ~baris 47 dan 786; fungsi `setMerchantActive` ~baris 763 sebagai pola) dan halaman yang disebut.
- [ ] **Step 2: Tipe.** Tambahkan `fee_amount: number; total_amount: number;` pada tipe payment admin yang memuat `amount`, dan `fee_bearer: "merchant" | "customer";` pada tipe merchant admin. Tambahkan:

```ts
export type FeeBearer = "merchant" | "customer";

// Admin only: who pays the provider's percentage fee for this merchant.
export async function setMerchantFeeBearer(id: string, feeBearer: FeeBearer): Promise<AdminMerchant> {
  const res = await adminAuthFetch(`${API_URL}/api/v1/admin/merchants/${id}/fee-bearer`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ fee_bearer: feeBearer }),
  });
  // ikuti penanganan error yang dipakai setMerchantActive di file yang sama
  ...
}
```

(salin pola header/penanganan error persis dari `setMerchantActive`; `AdminMerchant` = nama tipe merchant yang ada.)
- [ ] **Step 3: UI.**
  - Halaman detail merchant: blok "Fee ditanggung" dengan `<select>` (Merchant / Customer) dan tombol Save, memanggil `setMerchantFeeBearer`, memakai `toast` yang sudah ada untuk hasil; teks penjelasan: "Merchant: customer membayar sebesar nominal; fee Cashi dipotong dari yang diterima. Customer: customer membayar nominal + fee Cashi (0,7%), merchant menerima nominal penuh. Contoh: nominal Rp 2.000 → customer membayar Rp 2.015." Perubahan ke Customer meminta konfirmasi (dialog atau `confirm` mengikuti pola yang ada) karena memengaruhi nominal yang ditagih ke customer.
  - Daftar dan detail payment: pada detail tampilkan baris Nominal / Fee / Total (baris Fee dan Total hanya bila `fee_amount > 0`); pada tabel daftar biarkan kolom Amount (nominal) dan tambahkan teks kecil redup `+ <formatIDR(fee)> fee` di bawah angka bila `fee_amount > 0`.
- [ ] **Step 4: Verifikasi** — `npm run lint` dan `npm run build` bersih; katakan jujur bila dev server tidak dijalankan. **Report** (tanpa commit).

---

### Task 8: Front-end merchant, checkout, API docs

**Files:** Modify `front/lib/merchant-api.ts`, `front/app/dashboard/payments/page.tsx`, `front/app/dashboard/payments/[id]/page.tsx`, `front/app/dashboard/settings/page.tsx` (atau halaman bisnis — pengaturan fee hanya baca), `front/app/pay/[reference]/page.tsx`, `front/app/l/[slug]/page.tsx`, `front/app/dashboard/api-docs/page.tsx`.

- [ ] **Step 1: Baca** `front/AGENTS.md` dan dokumen Next relevan (catat). Baca `merchant-api.ts` (tipe payment ~baris 59, 139, 221), halaman checkout `pay/[reference]/page.tsx` (baris ~103 menampilkan `payment.amount`), halaman link `l/[slug]/page.tsx`, halaman bisnis/settings yang memuat profil merchant, dan `api-docs/page.tsx` (bagian Create Payment, Status, Webhooks).
- [ ] **Step 2: Tipe.** Tambahkan `fee_amount: number; total_amount: number;` pada tipe payment merchant (termasuk hasil lookup publik) dan `fee_bearer` pada tipe profil bisnis merchant bila responsnya memuatnya.
- [ ] **Step 3: UI.**
  - Daftar/detail payment merchant: sama seperti admin (Nominal / Fee / Total di detail bila fee > 0; teks redup `+ fee` di tabel).
  - **Checkout** `/pay/[reference]`: bila `fee_amount > 0` tampilkan blok rincian "Nominal Rp X · Biaya layanan Rp F · Total yang dibayar Rp T" dan angka utama yang menonjol adalah `total_amount`; bila fee 0 tampilan lama persis (tidak ada perubahan visual). Halaman `/l/[slug]`: setelah customer memilih/mengisi nominal tidak perlu pratinjau fee (fee dihitung server); cukup pastikan halaman pembayaran yang dibuka setelahnya (checkout) menampilkan rincian.
  - Halaman bisnis/settings merchant: teks hanya-baca "Biaya layanan ditanggung: Merchant | Customer" dengan penjelasan satu kalimat.
  - **API docs**: tambahkan `fee_amount` dan `total_amount` pada contoh respons Create Payment, tabel respons, dan payload webhook; paragraf yang menjelaskan: "`amount` adalah nominal Anda. Bila akun Anda diatur agar customer menanggung fee, customer membayar `total_amount = amount + fee_amount` dan Anda menerima `amount` setelah potongan provider. Bila tidak, `fee_amount` bernilai 0 dan `total_amount` sama dengan `amount`." (Teks UI dan docs berbahasa Inggris mengikuti file; sesuaikan penjelasan di atas ke bahasa Inggris dengan makna sama.)
- [ ] **Step 4: Verifikasi** — `npm run lint` dan `npm run build` bersih. **Report** (tanpa commit).

---

### Task 9: Dokumentasi

**Files:** Modify `docs/openapi.yaml`, `README.md`, `DEPLOY.md`.

- [ ] **Step 1: openapi.yaml** — tambahkan `fee_amount` dan `total_amount` pada skema respons payment, status payment, lookup publik, payload callback, dan baris export; `fee_bearer` pada skema merchant; endpoint baru `PUT /admin/merchants/{id}/fee-bearer` (body, 200/400/404/401); deskripsi: `amount` = nominal merchant, total = `amount + fee_amount`.
- [ ] **Step 2: README.md** — bagian singkat "Biaya provider (fee)": pengaturan per merchant, rumus total, contoh 2.000 → 2.015, dan bahwa defaultnya `merchant`.
- [ ] **Step 3: DEPLOY.md** — bagian baru **4.5 "Cek sebelum deploy Paket 5"** (gaya 4.2–4.4, bahasa Indonesia) berisi: (1) `make migrate` menerapkan 020 (aditif, tanpa perubahan perilaku; semua merchant tetap `merchant`); (2) backend dan front-end di-deploy bersamaan; (3) cara mengaktifkan: admin → detail merchant → Fee ditanggung = Customer; (4) **verifikasi dengan satu pembayaran uji nyata** pada merchant yang diaktifkan: buat payment nominal Rp 2.000 → respons `fee_amount: 15`, `total_amount: 2015`; bayar; bandingkan dengan dashboard Cashi (net seharusnya Rp 2.000, atau Rp 2.001 bila Cashi membulatkan fee ke bawah) dan konfirmasi nominal check-status/webhook = 2015 (pengecekan nominal Cashi yang tertunda dari Paket 1); bila net kurang dari nominal, matikan kembali pengaturan fee merchant itu dan laporkan; (5) catatan batas atas Cashi (total kotor maksimum 10.000.000) dan bahwa mengganti `fee_bearer` hanya memengaruhi payment baru.
- [ ] **Step 4: Verifikasi** — `python3 -c "import yaml;yaml.safe_load(open('docs/openapi.yaml'))"`; grep bahwa tiap field/endpoint/angka yang ditulis ada di kode. **Report** (tanpa commit).

---

## Self-Review (spec → task)

| Spec | Task |
|---|---|
| 3.1 model data + migration 020, `Payment.TotalAmount()`, `merchants.fee_bearer` | 2 |
| 3.1 perhitungan fee murni `internal/fee`, `FeeQuoter`, konstanta 70 bps pada Cashi/sandbox | 1, 3 |
| 3.2 alur `CreatePayment` (merchant vs customer, per kandidat, gagal tertutup, `charged < nominal`) | 4 |
| 3.2 verifikasi total, reconcile, terlambat, idempotensi | 4 |
| 3.2 sandbox tanpa akhiran acak | 3 |
| 3.3 antarmuka aditif (respons, status, lookup publik, admin, CSV, callback), payment link | 2, 4, 5, 8 |
| 3.4 endpoint admin khusus + `UpdateMerchantRequest` tidak berubah | 6 |
| 3.5 front-end admin / merchant / checkout / API docs | 7, 8 |
| 5 rilis + pembayaran uji nyata + 6 pengujian | 9 (dan tes di tiap task) |

**Konsistensi nama:** `fee.QuoteTotalBasisPoints`, `fee.MaxNominal`, `fee.ErrInvalidNominal/ErrInvalidRate`, `provider.FeeQuoter` / `ErrFeeNotSupported` (paket `internal/provider`), `QuoteTotal`, `feeBasisPoints`, `Payment.FeeAmount` / `TotalAmount()`, `merchant.FeeBearerMerchant/FeeBearerCustomer`, `IsValidFeeBearer`, `ErrInvalidFeeBearer`, `MerchantRepository.SetFeeBearer`, `AdminMerchantService.SetFeeBearer`, `AdminHandler.SetMerchantFeeBearer`, `checkMerchantForCreate`, `chargeFor`, `setMerchantFeeBearer` (front-end), kolom DB `fee_amount` / `fee_bearer`, constraint `payments_fee_amount_non_negative` / `merchants_fee_bearer_valid`.

**Catatan sengaja:** `UpdateMerchantRequest` tidak disentuh agar merchant tidak bisa mengubah `fee_bearer` sendiri (diuji di Task 6); kolom `fee_amount` ditambahkan di akhir daftar kolom SELECT/INSERT agar penomoran parameter yang ada tidak bergeser; kolom CSV baru ditaruh di akhir agar konsumen yang membaca berdasarkan posisi tidak rusak; pengaturan `customer` tanpa `FeeQuoter` gagal tertutup (503) alih-alih menagih nominal polos.
