# Pengerasan Data dan Operasional (Paket 4) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Menambah constraint integritas uang di database, health check sungguhan beserta healthcheck container dan hardening Caddy, serta kebersihan kode backend (level log, timeout DB, timeout klien Cashi).

**Architecture:** Satu migration aditif (019) dibungkus transaksi; endpoint `/api/v1/health` dengan ping DB plus flag `-healthcheck` pada binary `api` yang dipakai healthcheck Docker; Caddyfile diperketat (header keamanan, batas body, access log dengan filter header sensitif); konstanta bernama (tanpa key config baru) untuk timeout DB, pool, dan Cashi.

**Tech Stack:** Go 1.25, gorilla/mux, lib/pq, PostgreSQL 18, Caddy 2.11, Docker Compose.

**Spec:** [`back/docs/superpowers/specs/2026-11-04-ops-data-hardening-design.md`](../specs/2026-11-04-ops-data-hardening-design.md)

## Global Constraints

- **NO git commits, NO `git add`, NO branches** sampai user mengizinkan (instruksi eksplisit). Tiap task diakhiri "report changed files".
- Jangan pernah membaca/mencetak secret: `back/.config.toml`, `back/db_password`, `back/.env`, `front/.env.local`, kunci Cashi. `.config.example.toml` boleh.
- Jangan mengganggu proyek/Docker/port lain milik user (PostgreSQL sistem di 5432). Test PG hanya di PG18 sementara: data dir di scratchpad, socket `/tmp/claude-1000/pgsock`, port 54999, `listen_addresses=''`, dihapus setelah selesai (verifikasi `ls /tmp/claude-1000/pgsock` hilang dan tak ada proses di 54999). Docker: hanya container sekali-pakai (`docker run --rm`) untuk validasi statis Caddy; jangan menjalankan/menghentikan stack milik user (`docker compose up/down` dilarang).
- Gaya kode: ikuti file sekitar (komentar bahasa Inggris yang menjelaskan *why*, table-driven tests, fake in-memory, test PG via `TEST_DATABASE_URL`). Tanpa dependensi baru. **Tidak ada key config baru** (loader mewajibkan semua key).
- Tiap task: `gofmt -l ./internal ./cmd ./pkg` kosong, `go vet ./...` bersih, `go test ./... -race -count=1` hijau. Test baru wajib dibuktikan membedakan lewat **mutasi** (rusakkan logika, test gagal, kembalikan) dan dilaporkan.
- Nilai persis dari spec: `statement_timeout=30000`; `idle_in_transaction_session_timeout=60000`; pool 25 open / 5 idle / 5 menit; timeout klien Cashi **10s**; ping health **2s**; `-healthcheck` timeout **3s**; healthcheck compose `interval: 10s`, `timeout: 5s`, `retries: 3`, `start_period: 20s`; Caddy `request_body max_size 1MiB`, `Strict-Transport-Security "max-age=31536000"` (tanpa includeSubDomains/preload), `X-Content-Type-Options nosniff`, `Referrer-Policy no-referrer`, `X-Frame-Options DENY`, `Cache-Control "no-store"`, hapus header `Server`; log akses menghapus `Authorization`, `X-Api-Key`, `Cookie` (request) dan `Set-Cookie` (response); status valid payment: `pending, paid, expired, failed, cancelled`.
- Abaikan `back/.superpowers/`.

## Urutan dan File

| Task | Isi | File utama |
|---|---|---|
| 1 | Migration 019 (constraint) + test PG | `migrations/019_add_payment_integrity_constraints.sql`, `internal/repository/payment_constraints_pg_test.go` |
| 2 | Level log | `pkg/logger/logger.go`, `pkg/logger/logger_test.go`, `cmd/api/loglevel.go`, `cmd/api/main.go` |
| 3 | Timeout DB, konstanta pool, timeout Cashi | `internal/config/config.go`, `config_test.go`, `config_pg_test.go`, `cmd/api/main.go`, `internal/provider/cashi/client.go` |
| 4 | Health endpoint + flag `-healthcheck` | `cmd/api/health.go`, `cmd/api/health_test.go`, `cmd/api/main.go`, `router_test.go`, `session_router_test.go` |
| 5 | Caddyfile + compose + override | `Caddyfile`, `docker-compose.yaml`, `docker-compose.override.yaml` |
| 6 | Dokumentasi | `DEPLOY.md`, `README.md`, `docs/openapi.yaml` |

Task 1, 2, 3 independen satu sama lain; Task 4 setelah 2 dan 3 (sama-sama menyentuh `main.go`); Task 5 setelah 4 (memakai flag `-healthcheck`); Task 6 terakhir.

---

### Task 1: Migration 019 — constraint integritas payments

**Files:** Create `migrations/019_add_payment_integrity_constraints.sql`, `internal/repository/payment_constraints_pg_test.go`.

- [ ] **Step 1: Tes gagal** — buat `payment_constraints_pg_test.go` (package `repository`; pakai `newPGRepo(t)` dari `payment_repository_pg_test.go`, yang menerapkan semua `migrations/*.sql` berurutan pada schema sementara dan skip bila `TEST_DATABASE_URL` kosong). Bantu dengan helper lokal:

```go
package repository

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func insertConstraintMerchant(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := db.Exec(
		`INSERT INTO merchants (id, name, email, business_name) VALUES ($1, 'm', $2, 'b')`,
		id, id.String()+"@example.com"); err != nil {
		t.Fatalf("insert merchant: %v", err)
	}
	return id
}

// insertConstraintPayment inserts a payment and returns the error so each
// test can assert on a constraint violation.
func insertConstraintPayment(db *sql.DB, merchantID uuid.UUID, amount int64, status string, providerRef *string) error {
	id := uuid.New()
	_, err := db.Exec(
		`INSERT INTO payments (id, reference, merchant_id, amount, payment_method, provider_name, provider_reference, status, description, expires_at)
		 VALUES ($1, $2, $3, $4, 'qris', 'cashi', $5, $6, 'd', now() + interval '1 hour')`,
		id, "REF-"+id.String(), merchantID, amount, providerRef, status)
	return err
}

func wantViolation(t *testing.T, err error, constraint string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s violation, insert succeeded", constraint)
	}
	if !strings.Contains(err.Error(), constraint) {
		t.Fatalf("expected error to name %s, got: %v", constraint, err)
	}
}

func TestPaymentConstraints_AmountMustBePositive(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	for _, amt := range []int64{0, -1} {
		wantViolation(t, insertConstraintPayment(db, m, amt, "pending", nil), "payments_amount_positive")
	}
	if err := insertConstraintPayment(db, m, 1, "pending", nil); err != nil {
		t.Fatalf("amount 1 must be accepted: %v", err)
	}
}

func TestPaymentConstraints_StatusMustBeKnown(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	wantViolation(t, insertConstraintPayment(db, m, 1000, "processing", nil), "payments_status_valid")
	for _, st := range []string{"pending", "paid", "expired", "failed", "cancelled"} {
		if err := insertConstraintPayment(db, m, 1000, st, nil); err != nil {
			t.Fatalf("status %q must be accepted: %v", st, err)
		}
	}
}

func TestPaymentConstraints_ProviderReferenceUniquePerProvider(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	ref := "PROV-1"
	if err := insertConstraintPayment(db, m, 1000, "pending", &ref); err != nil {
		t.Fatal(err)
	}
	wantViolation(t, insertConstraintPayment(db, m, 1000, "pending", &ref), "uq_payments_provider_reference")

	// Same reference under a different provider name is a different payment.
	id := uuid.New()
	if _, err := db.Exec(
		`INSERT INTO payments (id, reference, merchant_id, amount, payment_method, provider_name, provider_reference, status, description, expires_at)
		 VALUES ($1, $2, $3, 1000, 'qris', 'sandbox', $4, 'pending', 'd', now() + interval '1 hour')`,
		id, "REF-"+id.String(), m, ref); err != nil {
		t.Fatalf("same reference under another provider must be accepted: %v", err)
	}

	// Any number of payments may have no provider reference yet.
	for i := 0; i < 3; i++ {
		if err := insertConstraintPayment(db, m, 1000, "pending", nil); err != nil {
			t.Fatalf("NULL provider_reference must not collide: %v", err)
		}
	}
}

func TestPaymentConstraints_MerchantWithPaymentsCannotBeDeleted(t *testing.T) {
	_, db := newPGRepo(t)
	withPayment := insertConstraintMerchant(t, db)
	if err := insertConstraintPayment(db, withPayment, 1000, "paid", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM merchants WHERE id = $1`, withPayment); err == nil {
		t.Fatal("deleting a merchant that has payments must be rejected")
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM payments WHERE merchant_id = $1`, withPayment).Scan(&n); err != nil || n != 1 {
		t.Fatalf("payment history must survive the rejected delete: n=%d err=%v", n, err)
	}

	// A merchant without payments (registration rollback) is still deletable,
	// and its dependent rows keep cascading.
	empty := insertConstraintMerchant(t, db)
	if _, err := db.Exec(`DELETE FROM merchants WHERE id = $1`, empty); err != nil {
		t.Fatalf("deleting a merchant without payments must work: %v", err)
	}
}

func TestMigration019_IsIdempotent(t *testing.T) {
	_, db := newPGRepo(t)
	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", "019_add_payment_integrity_constraints.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("re-applying migration 019 (run %d): %v", i+1, err)
		}
	}
	m := insertConstraintMerchant(t, db)
	wantViolation(t, insertConstraintPayment(db, m, 0, "pending", nil), "payments_amount_positive")
}
```

Run: `TEST_DATABASE_URL=… go test ./internal/repository -run PaymentConstraints -count=1` → gagal (belum ada migration). Gunakan PG18 sementara sesuai Global Constraints.

- [ ] **Step 2: Migration** `migrations/019_add_payment_integrity_constraints.sql`:

```sql
-- Migration 019: money-integrity constraints on payments.
-- Runs in one transaction: if existing data violates a constraint the whole
-- file rolls back and nothing changes (DEPLOY.md 4.4 lists the queries that
-- must return no rows before applying). Idempotent: safe to re-apply.
-- Lookups in pg_constraint are scoped by conrelid, never by name alone: names
-- are only unique per table, and tests apply this file in several schemas.
BEGIN;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'payments'::regclass AND conname = 'payments_amount_positive'
    ) THEN
        ALTER TABLE payments ADD CONSTRAINT payments_amount_positive CHECK (amount > 0);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'payments'::regclass AND conname = 'payments_status_valid'
    ) THEN
        ALTER TABLE payments ADD CONSTRAINT payments_status_valid
            CHECK (status IN ('pending', 'paid', 'expired', 'failed', 'cancelled'));
    END IF;
END $$;

-- Two payments must never share a provider reference: webhooks and reconcile
-- map provider events to payments by (provider_name, provider_reference).
CREATE UNIQUE INDEX IF NOT EXISTS uq_payments_provider_reference
    ON payments (provider_name, provider_reference)
    WHERE provider_reference IS NOT NULL;

-- Deleting a merchant must not silently delete its payment history.
-- confdeltype: 'c' = CASCADE, 'r' = RESTRICT. Other tables (API keys, users,
-- provider configs, payment links, idempotency keys) keep ON DELETE CASCADE.
DO $$
DECLARE
    fk_name text;
BEGIN
    SELECT c.conname INTO fk_name
    FROM pg_constraint c
    WHERE c.conrelid = 'payments'::regclass
      AND c.confrelid = 'merchants'::regclass
      AND c.contype = 'f'
      AND c.confdeltype <> 'r';

    IF fk_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE payments DROP CONSTRAINT %I', fk_name);
        ALTER TABLE payments ADD CONSTRAINT payments_merchant_id_fkey
            FOREIGN KEY (merchant_id) REFERENCES merchants(id) ON DELETE RESTRICT;
    END IF;
END $$;

COMMENT ON CONSTRAINT payments_amount_positive ON payments IS 'Payments are always for a positive amount';
COMMENT ON CONSTRAINT payments_status_valid ON payments IS 'Must match the status constants in internal/domain/payment';

COMMIT;
```

- [ ] **Step 3: Jalankan** seluruh test repository dengan PG: `TEST_DATABASE_URL=… go test ./internal/repository -race -count=1`. Test lama yang menyisipkan payment dengan nominal ≤ 0 atau status di luar lima nilai harus diperbaiki (itu data uji yang tidak valid), bukan constraint-nya. Lalu suite penuh tanpa PG.
- [ ] **Step 4: Mutasi** (a) hapus `CHECK (amount > 0)` → test amount gagal; (b) ubah `confdeltype <> 'r'` menjadi `= 'x'` (FK tidak diganti) → test merchant gagal; (c) hapus `WHERE provider_reference IS NOT NULL` → subtest NULL gagal. Pulihkan.
- [ ] **Step 5: Report** (tanpa commit), termasuk bukti pembersihan PG sementara.

---

### Task 2: Level log

**Files:** Modify `pkg/logger/logger.go`, `pkg/logger/logger_test.go`, `cmd/api/main.go`; Create `cmd/api/loglevel.go`, `cmd/api/loglevel_test.go`.

**Interfaces — Produces:** `logger.Init(level string)` menyaring; `logger.SetLevel(level string)`; `levelForEnvironment(env string) string` (paket `main`).

- [ ] **Step 1: Tes gagal** — di `logger_test.go` (pakai pola `SetOutput` + `Init` yang ada; baca tes lama untuk mereset state `enabled`/level antar tes):

```go
func TestLogger_LevelFiltering(t *testing.T) {
	tests := []struct {
		level string
		want  map[string]bool // log level -> emitted
	}{
		{"debug", map[string]bool{"debug": true, "info": true, "warn": true, "error": true}},
		{"info", map[string]bool{"debug": false, "info": true, "warn": true, "error": true}},
		{"warn", map[string]bool{"debug": false, "info": false, "warn": true, "error": true}},
		{"error", map[string]bool{"debug": false, "info": false, "warn": false, "error": true}},
		{"nonsense", map[string]bool{"debug": false, "info": true, "warn": true, "error": true}}, // unknown -> info
		{"", map[string]bool{"debug": false, "info": true, "warn": true, "error": true}},
	}
	for _, tc := range tests {
		t.Run(tc.level, func(t *testing.T) {
			var out, errOut bytes.Buffer
			SetOutput(&out, &errOut)
			Init(tc.level)
			Debug("d")
			Info("i")
			Warn("w")
			Error("e")
			got := out.String() + errOut.String()
			for lvl, want := range tc.want {
				has := strings.Contains(got, `"level":"`+lvl+`"`)
				if has != want {
					t.Errorf("level %q: %s emitted=%v, want %v\n%s", tc.level, lvl, has, want, got)
				}
			}
		})
	}
}

func TestLogger_SetLevelChangesFilteringAtRuntime(t *testing.T) {
	var out, errOut bytes.Buffer
	SetOutput(&out, &errOut)
	Init("info")
	Debug("hidden")
	SetLevel("debug")
	Debug("shown")
	if strings.Contains(out.String(), "hidden") || !strings.Contains(out.String(), "shown") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}
```

(`cmd/api/loglevel_test.go`:)

```go
package main

import "testing"

func TestLevelForEnvironment(t *testing.T) {
	tests := map[string]string{"development": "debug", "production": "info", "": "info", "staging": "info"}
	for env, want := range tests {
		if got := levelForEnvironment(env); got != want {
			t.Errorf("levelForEnvironment(%q) = %q, want %q", env, got, want)
		}
	}
}
```

Run → gagal.

- [ ] **Step 2: Implementasi.** `logger.go`: tambah impor `strings` dan `sync/atomic`, dan

```go
// Severity order for filtering; a message is written when its rank is at or
// above the configured minimum.
var levelRank = map[string]int32{"debug": 0, "info": 1, "warn": 2, "error": 3}

// minLevel is atomic because SetLevel runs once config is loaded, while
// background goroutines may already be logging.
var minLevel atomic.Int32

func init() { minLevel.Store(levelRank["info"]) }

// Init enables logging at the given level (debug, info, warn, error). An
// unknown or empty level means info.
func Init(level string) {
	SetLevel(level)
	enabled = true
}

// SetLevel changes the minimum level that is written; unknown means info.
func SetLevel(level string) {
	rank, ok := levelRank[strings.ToLower(strings.TrimSpace(level))]
	if !ok {
		rank = levelRank["info"]
	}
	minLevel.Store(rank)
}
```

Hapus `Init` lama (yang mengabaikan `level`), dan di `write` tambahkan setelah cek `enabled`:

```go
	if levelRank[level] < minLevel.Load() {
		return
	}
```

`cmd/api/loglevel.go`:

```go
package main

// levelForEnvironment picks the log level from app.environment, so no new
// (mandatory) config key is needed: development is chatty, everything else
// logs info and above.
func levelForEnvironment(env string) string {
	if env == "development" {
		return "debug"
	}
	return "info"
}
```

`main.go`: setelah `cfg` berhasil dimuat tambahkan `logger.SetLevel(levelForEnvironment(cfg.App.Environment))`.

- [ ] **Step 3: Verifikasi** `go test ./pkg/logger ./cmd/api -race -count=1` lalu suite penuh. Periksa tes yang bergantung pada keluaran `Debug` (jika ada) dan sesuaikan dengan `SetLevel("debug")` di tes itu. Mutasi: hapus cek `minLevel` di `write` → test filtering gagal.
- [ ] **Step 4: Report** (tanpa commit).

---

### Task 3: Timeout DB, konstanta pool, timeout Cashi

**Files:** Modify `internal/config/config.go`, `internal/config/config_test.go`, `cmd/api/main.go`, `internal/provider/cashi/client.go`; Create `internal/config/config_pg_test.go`; tes Cashi di `internal/provider/cashi/client_test.go`.

- [ ] **Step 1: Tes gagal.**

(a) `config_test.go` baris ~104: `wantConn` diperbarui menjadi string lama + ` statement_timeout=30000 idle_in_transaction_session_timeout=60000` (baca nilai lamanya dan tambahkan akhiran itu persis).

(b) `internal/config/config_pg_test.go` (gated; buktikan parameter benar-benar sampai ke server):

```go
package config

import (
	"database/sql"
	"net/url"
	"os"
	"strconv"
	"testing"

	_ "github.com/lib/pq"
)

// The connection string the API builds must make PostgreSQL apply the
// statement and idle-in-transaction timeouts to every session.
func TestConnectionString_AppliesSessionTimeouts(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real PostgreSQL test")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	port := 5432
	if p := u.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}
	pass, _ := u.User.Password()
	cfg := DBConfig{
		Host: u.Hostname(), Port: port, User: u.User.Username(), Password: pass,
		Name: u.Path[1:], SSLMode: "disable",
	}
	if m := u.Query().Get("sslmode"); m != "" {
		cfg.SSLMode = m
	}
	// A unix-socket URL puts the directory in the host query parameter.
	if h := u.Query().Get("host"); h != "" {
		cfg.Host = h
	}

	db, err := sql.Open("postgres", cfg.ConnectionString())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for param, want := range map[string]string{
		"statement_timeout":                   "30s",
		"idle_in_transaction_session_timeout": "1min",
	} {
		var got string
		if err := db.QueryRow("SHOW " + param).Scan(&got); err != nil {
			t.Fatalf("SHOW %s: %v", param, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", param, got, want)
		}
	}
}
```

(Sesuaikan penguraian URL dengan bentuk `TEST_DATABASE_URL` yang dipakai PG sementara — host dari socket dir seperti pada `payment_repository_pg_test.go`; bila bentuknya berbeda, adaptasi minimal dan jelaskan di laporan.)

(c) `cashi/client_test.go`: tes bahwa konstanta `requestTimeout` bernilai 10 detik dan lebih kecil dari 15 detik, dan bahwa klien HTTP adapter memakainya (cara akses: baca `NewAdapter`/struktur di `client.go` ~baris 49).

Run → gagal.

- [ ] **Step 2: Implementasi.**

`config.go`: tambahkan

```go
// Session timeouts applied to every pooled connection of the API (not to
// psql used by make migrate). They stop one stuck query or abandoned
// transaction from holding a connection indefinitely.
const (
	dbStatementTimeoutMS         = 30000
	dbIdleInTxSessionTimeoutMS   = 60000
)
```

dan `ConnectionString()` menjadi:

```go
func (c *DBConfig) ConnectionString() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s statement_timeout=%d idle_in_transaction_session_timeout=%d",
		c.Host, c.Port, c.User, c.Password, c.Name, c.SSLMode,
		dbStatementTimeoutMS, dbIdleInTxSessionTimeoutMS,
	)
}
```

`cmd/api/main.go`: tambahkan blok konstanta dan pakai di `initDB`:

```go
// Connection pool sizing for the single-instance deployment (see DEPLOY.md:
// the API must stay one replica because background jobs have no leader lock).
const (
	dbMaxOpenConns    = 25
	dbMaxIdleConns    = 5
	dbConnMaxLifetime = 5 * time.Minute
)
```

(`db.SetMaxOpenConns(dbMaxOpenConns)` dst.)

`cashi/client.go`: ganti literal `Timeout: 30 * time.Second` dengan konstanta bernama:

```go
// requestTimeout bounds one call to Cashi. It stays below the API server's
// 15s WriteTimeout so a normal attempt finishes inside the merchant's response
// window; a slow Cashi turns into a quick 502 the merchant retries with the
// same Idempotency-Key.
const requestTimeout = 10 * time.Second
```

- [ ] **Step 3: Verifikasi** suite penuh; PG test dengan `TEST_DATABASE_URL` (PG18 sementara, bersihkan). Mutasi: hapus param `statement_timeout` dari DSN → test PG dan `wantConn` gagal; ubah `requestTimeout` ke 30s → test Cashi gagal.
- [ ] **Step 4: Report** (tanpa commit).

---

### Task 4: Health endpoint dan flag `-healthcheck`

**Files:** Create `cmd/api/health.go`, `cmd/api/health_test.go`. Modify `cmd/api/main.go`, `cmd/api/router_test.go`, `cmd/api/session_router_test.go`.

**Interfaces — Produces:**

```go
func newHealthHandler(check func(context.Context) error) http.HandlerFunc // nil check -> 503 (fail closed)
func runHealthcheck(port int, client *http.Client) int                    // 0 healthy, 1 otherwise
// setupRouter gets one new LAST parameter: healthCheck func(context.Context) error
```

- [ ] **Step 1: Tes gagal** `cmd/api/health_test.go`:

```go
package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
)

func TestHealthHandler(t *testing.T) {
	tests := []struct {
		name       string
		check      func(context.Context) error
		wantStatus int
		wantBody   string
	}{
		{"healthy", func(context.Context) error { return nil }, http.StatusOK, `{"status":"ok"}`},
		{"database down", func(context.Context) error { return errors.New("pq: password authentication failed for user \"secret_user\"") }, http.StatusServiceUnavailable, `{"status":"unavailable"}`},
		{"no checker configured fails closed", nil, http.StatusServiceUnavailable, `{"status":"unavailable"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newHealthHandler(tc.check)(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tc.wantBody {
				t.Errorf("body = %s, want %s", got, tc.wantBody)
			}
			if strings.Contains(rec.Body.String(), "secret_user") || strings.Contains(rec.Body.String(), "pq:") {
				t.Error("response must never echo the underlying error")
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
		})
	}
}

func TestHealthHandler_PingHasTwoSecondDeadline(t *testing.T) {
	var got time.Duration
	h := newHealthHandler(func(ctx context.Context) error {
		dl, ok := ctx.Deadline()
		if !ok {
			t.Error("ping context must carry a deadline")
		}
		got = time.Until(dl)
		return nil
	})
	h(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if got <= 0 || got > healthPingTimeout {
		t.Errorf("deadline in %v, want within (0, %v]", got, healthPingTimeout)
	}
}

func TestRunHealthcheck(t *testing.T) {
	logger.Init("error") // keep test output quiet

	serve := func(status int) (int, func()) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/health" {
				t.Errorf("unexpected path %q", r.URL.Path)
			}
			w.WriteHeader(status)
		}))
		u := srv.Listener.Addr().String()
		port, _ := strconv.Atoi(u[strings.LastIndex(u, ":")+1:])
		return port, srv.Close
	}

	t.Run("200 is healthy", func(t *testing.T) {
		port, stop := serve(http.StatusOK)
		defer stop()
		if code := runHealthcheck(port, &http.Client{Timeout: time.Second}); code != 0 {
			t.Errorf("exit = %d, want 0", code)
		}
	})
	t.Run("503 is unhealthy", func(t *testing.T) {
		port, stop := serve(http.StatusServiceUnavailable)
		defer stop()
		if code := runHealthcheck(port, &http.Client{Timeout: time.Second}); code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
	})
	t.Run("connection refused is unhealthy", func(t *testing.T) {
		port, stop := serve(http.StatusOK)
		stop() // nothing listens on the port any more
		if code := runHealthcheck(port, &http.Client{Timeout: time.Second}); code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
	})
}
```

Perbarui semua panggilan `setupRouter(` di `router_test.go` (6 tempat termasuk helper) dan `session_router_test.go:160` dengan argumen terakhir `func(context.Context) error { return nil }`, dan tambahkan tes router: `GET /api/v1/health` melalui `setupRouter` dengan checker error → 503, dengan checker nil-error → 200, serta melebihi budget `publicRateLimiter` → 429 (pola tes limiter yang ada). Run → gagal.

- [ ] **Step 2: Implementasi** `cmd/api/health.go`:

```go
package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
)

const (
	healthPingTimeout        = 2 * time.Second
	healthcheckClientTimeout = 3 * time.Second
)

// newHealthHandler reports readiness: 200 only when the database answers a
// ping. Failures are logged (with the request id) but never echoed, so the
// public endpoint cannot leak connection details. A nil checker fails closed.
func newHealthHandler(check func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if check == nil {
			logger.ErrorCtx(r.Context(), "health: no readiness checker configured")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), healthPingTimeout)
		defer cancel()
		if err := check(ctx); err != nil {
			logger.ErrorfCtx(r.Context(), "health: database check failed: %v", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

// runHealthcheck backs the `api -healthcheck` flag used by the Docker
// healthcheck: it asks the local server for /api/v1/health and returns the
// process exit code (0 healthy, 1 otherwise).
func runHealthcheck(port int, client *http.Client) int {
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/health", port))
	if err != nil {
		logger.Errorf("healthcheck: %v", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.Errorf("healthcheck: status %d", resp.StatusCode)
		return 1
	}
	return 0
}
```

(`logger` belum di-`Init` pada jalur healthcheck, sehingga tidak mencetak apa pun — disengaja agar log container tidak penuh tiap 10 detik; exit code-lah sinyalnya.)

`main.go`:
1. Tambahkan flag dan ubah awal `main` agar `flag.Parse()` dan cabang healthcheck berjalan **sebelum** `logger.Init` dan log "Starting …":

```go
func main() {
	configPath := flag.String("config", ".config.toml", "path to the TOML config file")
	healthcheck := flag.Bool("healthcheck", false, "probe the local /api/v1/health endpoint and exit 0 (healthy) or 1; used by the Docker healthcheck")
	flag.Parse()

	if *healthcheck {
		cfg, err := config.Load(*configPath)
		if err != nil {
			os.Exit(1) // logger is not initialised: stay silent, the exit code is the signal
		}
		os.Exit(runHealthcheck(cfg.App.Port, &http.Client{Timeout: healthcheckClientTimeout}))
	}

	logger.Init("info")
	logger.Info("Starting Payment Gateway Aggregator API...")

	cfg, err := config.Load(*configPath)
	...
```

(hapus pemanggilan `flag.String`/`flag.Parse()` lama di bawah.)
2. `setupRouter(...)` mendapat parameter terakhir `healthCheck func(context.Context) error`; ganti rute health lama dengan `api.Handle("/health", publicRateLimiter.Limit(newHealthHandler(healthCheck))).Methods("GET")`; pemanggilan di `main` meneruskan `db.PingContext`.

- [ ] **Step 3: Verifikasi** `go test ./cmd/api -race -count=1`, suite penuh. Mutasi: ubah `StatusServiceUnavailable` jadi 200 pada jalur error → tes gagal; hapus `publicRateLimiter.Limit` → tes 429 gagal; hapus pemeriksaan `StatusCode` di `runHealthcheck` → subtest 503 gagal.
- [ ] **Step 4: Report** (tanpa commit).

---

### Task 5: Caddyfile, compose, override

**Files:** Modify `Caddyfile`, `docker-compose.yaml`, `docker-compose.override.yaml`.

- [ ] **Step 1: Caddyfile** — ganti isi blok situs menjadi (pertahankan komentar kepala dan blok global yang ada):

```
api.whuzpay.web.id {
	encode zstd gzip

	# Same cap as the API itself (middleware.LimitBody, 1 MiB), but rejected at
	# the proxy before the body is forwarded.
	request_body {
		max_size 1MiB
	}

	header {
		Strict-Transport-Security "max-age=31536000"
		X-Content-Type-Options nosniff
		Referrer-Policy no-referrer
		X-Frame-Options DENY
		# Financial data: nothing from this API may be cached by clients or proxies.
		Cache-Control "no-store"
		-Server
	}

	# Access log. By default Caddy records every request header, which would
	# write bearer tokens and merchant API keys into the container log; the
	# filter below removes them. Do not remove it.
	log {
		output stdout
		format filter {
			wrap json
			fields {
				request>headers>Authorization delete
				request>headers>X-Api-Key delete
				request>headers>Cookie delete
				resp_headers>Set-Cookie delete
			}
		}
	}

	reverse_proxy api:8080
}
```

Jika `caddy validate` menolak bentuk `fields { … }`, gunakan bentuk langsung (`request>headers>Authorization delete` tanpa blok `fields`) yang diterima versi 2.11, dan laporkan bentuk mana yang valid.

- [ ] **Step 2: docker-compose.yaml** — pada service `api` tambahkan (setelah `mem_limit`):

```yaml
    healthcheck:
      # The binary probes its own /api/v1/health (port read from the same
      # .config.toml), so no port is hard-coded here and no wget is needed.
      test: ["CMD", "/app/bin/api", "-healthcheck"]
      interval: 10s
      timeout: 5s
      retries: 3
      start_period: 20s
```

dan ubah `depends_on` service `caddy` dari daftar menjadi:

```yaml
    depends_on:
      api:
        condition: service_healthy
```

- [ ] **Step 3: docker-compose.override.yaml** (hanya lokal) — pada service `api` tambahkan:

```yaml
    # The dev image runs air from /src; /app/bin/api (used by the production
    # healthcheck) does not exist there.
    healthcheck:
      disable: true
```

- [ ] **Step 4: Validasi statis** (tidak menyentuh stack user):
  - `docker run --rm -v "$PWD/Caddyfile:/etc/caddy/Caddyfile:ro" caddy:2.11-alpine caddy validate --config /etc/caddy/Caddyfile` → "Valid configuration".
  - `docker compose config -q` (dengan override, dan juga `docker compose -f docker-compose.yaml config -q` tanpa override) → tanpa error.
  - Bukti log: jalankan `caddy adapt --config /etc/caddy/Caddyfile` (container sekali-pakai) dan tunjukkan bahwa JSON hasil adaptasi memuat keempat filter `delete`.
  - Bila Docker/jaringan tidak tersedia, katakan jujur apa yang tidak bisa diverifikasi.
- [ ] **Step 5: Report** (tanpa commit).

---

### Task 6: Dokumentasi

**Files:** Modify `DEPLOY.md`, `README.md`, `docs/openapi.yaml`.

- [ ] **Step 1: DEPLOY.md** — tambahkan setelah 4.3 bagian baru (gaya sama dengan 4.2/4.3, bahasa Indonesia):

````markdown
### 4.4 Cek sebelum deploy Paket 4

Paket 4 menambah constraint database (migration 019), health check sungguhan,
healthcheck container, dan header/log di Caddy. Tidak ada key `.config.toml`
baru.

1. **Cek data lama.** Migration 019 membatalkan diri (satu transaksi, tidak ada
   yang berubah) bila data melanggar constraint baru. Jalankan di `psql` pada
   DB produksi; ketiganya harus mengembalikan nol baris:

   ```sql
   SELECT id, reference, amount FROM payments WHERE amount <= 0;

   SELECT id, reference, status FROM payments
   WHERE status NOT IN ('pending', 'paid', 'expired', 'failed', 'cancelled');

   SELECT provider_name, provider_reference, count(*) FROM payments
   WHERE provider_reference IS NOT NULL
   GROUP BY 1, 2 HAVING count(*) > 1;
   ```

   Bila ada baris, perbaiki datanya dulu (ini data keuangan; tidak dibersihkan
   otomatis).
2. **Ganti `docker-compose.yaml` dan `Caddyfile` di server** dengan versi baru
   bersama image baru: `api` kini punya healthcheck (`api -healthcheck`) dan
   Caddy baru menyala setelah `api` sehat; Caddy menambah header keamanan
   (HSTS `max-age=31536000`), batas body 1 MiB, dan log akses. Log akses sudah
   membuang header `Authorization`, `X-Api-Key`, `Cookie`, dan `Set-Cookie`;
   jangan hapus filter itu.
3. **`make migrate`** menerapkan 019 sebelum image API baru dijalankan
   (idempoten). Setelah itu menghapus merchant yang masih punya payment
   ditolak database (`ON DELETE RESTRICT`).
4. **Health check.** `GET /api/v1/health` kini 200 hanya bila database
   menjawab; 503 `{"status":"unavailable"}` bila tidak. Pantau dengan
   `docker compose ps` (kolom `STATUS` harus `healthy`).
5. **`api` harus satu replika.** Job latar (expire, kirim callback,
   reconcile) berjalan di dalam proses `api` tanpa kunci leader; menjalankan
   lebih dari satu instance membuat job berjalan ganda.
6. **Timeout.** Koneksi DB kini memakai `statement_timeout` 30 detik dan
   `idle_in_transaction_session_timeout` 60 detik; panggilan ke Cashi
   di-timeout 10 detik. Level log mengikuti `app.environment`
   (`development` → debug, `production` → info).
````

Perbaiki juga ringkasan/daftar isi DEPLOY.md bila ada yang merujuk bagian 4.x.

- [ ] **Step 2: README.md** — di bagian API/operasional tambahkan catatan singkat: `/api/v1/health` (200/503), flag `-healthcheck`, dan satu kalimat tentang constraint integritas payments.
- [ ] **Step 3: openapi.yaml** — perbarui entri `/health`: respons `200 {"status":"ok"}` dan `503 {"status":"unavailable"}` (skema objek dengan `status` string), deskripsi "readiness: database ping, 2s".
- [ ] **Step 4: Verifikasi** — `python3 -c "import yaml;yaml.safe_load(open('docs/openapi.yaml'))"` bila PyYAML ada; `grep` bahwa tiap nama berkas/flag/konstanta/angka yang ditulis ada di kode/konfigurasi (019, `-healthcheck`, 30000/60000, 10s, 1MiB). **Report** (tanpa commit).

---

## Self-Review (spec → task)

| Spec | Task |
|---|---|
| 3.1 constraint (CHECK amount/status, unique parsial, FK RESTRICT, idempoten, transaksi) + pengecekan data | 1 (+6 untuk DEPLOY) |
| 3.2 health endpoint, `-healthcheck`, compose, override, Caddyfile | 4, 5 |
| 3.3 level log | 2 |
| 3.3 timeout DB + konstanta pool | 3 |
| 3.3 timeout Cashi | 3 |
| 5 dokumentasi (DEPLOY 4.4, README, satu replika) | 6 |
| 6 pengujian (PG, unit, validasi statis, mutasi) | 1–5 |

**Konsistensi nama:** `payments_amount_positive`, `payments_status_valid`, `uq_payments_provider_reference`, `payments_merchant_id_fkey`; `newHealthHandler`, `runHealthcheck`, `healthPingTimeout`, `healthcheckClientTimeout`; `levelForEnvironment`, `logger.SetLevel`; `dbStatementTimeoutMS`, `dbIdleInTxSessionTimeoutMS`, `dbMaxOpenConns`, `dbMaxIdleConns`, `dbConnMaxLifetime`; `requestTimeout` (Cashi).

**Catatan sengaja:** jalur `-healthcheck` memanggil `flag.Parse()` sebelum `logger.Init` agar senyap; `setupRouter` mendapat satu parameter terakhir sehingga semua pemanggil tes ikut diperbarui; `conrelid` membatasi pencarian constraint karena nama constraint hanya unik per tabel (tes menerapkan migration di banyak schema).
