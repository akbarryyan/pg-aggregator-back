# Sandbox Simulator and Test Payment History Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let merchants drive a sandbox payment to paid / failed / expired (from the public checkout page and the Test Payment page) through the real status + callback paths, and bring old "Dashboard test" payments back into the Test Payment history.

**Architecture:** One data migration renames the old test marker. A new `PaymentService.SimulateSandboxOutcome` reuses `MarkPaid` / `UpdateStatus` / `NotifyMerchantPaymentEvent` and records a `webhook_events` row; two thin endpoints (public by reference, merchant JWT by id) call it. The front-end adds one shared `SandboxSimulator` component used by the checkout and the Test Payment page.

**Tech Stack:** Go 1.25, gorilla/mux, lib/pq, PostgreSQL 18; Next.js 16 / React 19 / TypeScript / Tailwind v4.

Spec: `back/docs/superpowers/specs/2026-11-12-sandbox-simulator-and-test-history-design.md`.

## Global Constraints

- **Never `git commit`, `git add`, `git push`, or create branches** (user rule). The "Commit" step of each task is replaced by "leave the changes uncommitted".
- Never read or print secrets: `back/.config.toml`, `back/.env`, `back/db_password`, `front/.env.local`, keys, `.pem`.
- Do not run `docker compose up/down/restart/run/build`. PostgreSQL tests only against a throwaway PG18 (scratchpad data dir, socket `/tmp/claude-1000/pgsock`, port 54999, `listen_addresses=''`), removed afterwards; never touch the system PostgreSQL on 5432.
- Simulation outcomes are exactly `paid`, `failed`, `expired`.
- Only payments with `environment = "sandbox"` **and** `provider_name = "sandbox"` may be simulated; production is never simulated (403). Only `pending` payments (else 409).
- Simulation uses the real paths: `paid` → `merchantNetFor` + `paymentRepo.MarkPaid`; `failed`/`expired` → `paymentRepo.UpdateStatus`; then `NotifyMerchantPaymentEvent(ctx, p, eventTypeForStatus(status), false)` with a detached context.
- Each simulation records a `webhook_events` row: provider `sandbox`, `event_type` `sandbox.simulate.<outcome>`, `status` = `<outcome>`, `is_processed` true, `payment_id` set. A failure to record is only logged.
- Error codes: 400 invalid outcome/body, 403 not sandbox, 404 not found / other merchant's payment, 409 not pending, 429 public rate limit.
- Routes: `POST /api/v1/public/payments/by-reference/{reference}/simulate` behind `publicRateLimiter`; `POST /api/v1/merchant/payments/{id}/simulate` on the merchant dashboard router (JWT).
- Test marker: `TEST PAYMENT` (new), `Dashboard test` (old).
- Checkout page text is Indonesian; dashboard (Test Payment) text is English.
- Front-end has no test runner: verify with `npm run lint` and `npm run build` (from `front/`). Mobile: no horizontal scroll at 375 px; buttons stack full-width below `sm`, row at `sm`+.
- Backend verification: `gofmt -l` empty, `go vet ./...`, `go test ./... -race`.

---

### Task 1: Migration 022 — rename old test marker

**Files:**
- Create: `back/migrations/022_rename_dashboard_test_customer.sql`
- Create: `back/internal/repository/test_marker_migration_pg_test.go`
- Modify: `back/DEPLOY.md` (append a short section after the migration 021 section)

**Interfaces:**
- Consumes: `newPGRepo(t)` (`internal/repository/payment_repository_pg_test.go`, applies every `migrations/*.sql`), `insertConstraintMerchant(t, db) uuid.UUID`, `insertLedgerPayment(t, db, merchantID, amount, fee, status, env) uuid.UUID` (`ledger_pg_test.go`).
- Produces: nothing used by later tasks.

- [ ] **Step 1: Write the failing PG test**

`back/internal/repository/test_marker_migration_pg_test.go`:

```go
package repository

import (
	"os"
	"path/filepath"
	"testing"
)

// Migration 022 moves dashboard test payments from the old marker to the new
// one, leaves every other customer name alone, and is safe to re-apply.
func TestMigration022_RenamesDashboardTestMarker(t *testing.T) {
	_, db := newPGRepo(t)
	m := insertConstraintMerchant(t, db)
	old := insertLedgerPayment(t, db, m, 2000, 0, "pending", "sandbox")
	other := insertLedgerPayment(t, db, m, 2000, 0, "pending", "sandbox")
	none := insertLedgerPayment(t, db, m, 2000, 0, "pending", "sandbox")
	if _, err := db.Exec(`UPDATE payments SET customer_name = 'Dashboard test' WHERE id = $1`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE payments SET customer_name = 'Budi' WHERE id = $1`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE payments SET customer_name = NULL WHERE id = $1`, none); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", "022_rename_dashboard_test_customer.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("applying migration 022 (run %d): %v", i+1, err)
		}
	}

	get := func(id interface{}) *string {
		var s *string
		if err := db.QueryRow(`SELECT customer_name FROM payments WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := get(old); s == nil || *s != "TEST PAYMENT" {
		t.Errorf("old marker = %v, want TEST PAYMENT", s)
	}
	if s := get(other); s == nil || *s != "Budi" {
		t.Errorf("other customer changed: %v", s)
	}
	if s := get(none); s != nil {
		t.Errorf("NULL customer changed: %v", *s)
	}
}
```

If `insertLedgerPayment` sets `customer_name` itself or has a different signature, adapt the seeding only (keep the assertions).

- [ ] **Step 2: Run it to verify it fails**

Start the throwaway PG18 (see Global Constraints) and run with the env var `newPGRepo` reads (see its doc comment), e.g.:
`go test ./internal/repository -run TestMigration022 -v`
Expected: FAIL — migration file not found.

- [ ] **Step 3: Write the migration**

`back/migrations/022_rename_dashboard_test_customer.sql`:

```sql
-- Migration 022: dashboard test payments marker.
-- The merchant Test Payment page now tags its payments with customer_name
-- 'TEST PAYMENT' (previously 'Dashboard test') and finds its history by that
-- name. Move the old rows to the new marker so they show up again.
-- Data only, idempotent, safe while the api is running.
BEGIN;

UPDATE payments SET customer_name = 'TEST PAYMENT' WHERE customer_name = 'Dashboard test';

COMMIT;
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/repository -run 'TestMigration022|TestMigration020' -v`
Expected: PASS.

- [ ] **Step 5: DEPLOY.md note**

Append to `back/DEPLOY.md` (after the migration 021 section), in Indonesian like the rest of the file:

```markdown
### Migration 022 — penanda Test Payment

Hanya data: `customer_name` 'Dashboard test' diganti 'TEST PAYMENT' agar test
lama muncul lagi di riwayat Test Payment. Idempoten, aman saat api berjalan:
`make migrate`.

Rollback (biasanya tidak perlu; ikut mengganti test baru yang dibuat sesudah
migrasi karena namanya sama):

    UPDATE payments SET customer_name = 'Dashboard test' WHERE customer_name = 'TEST PAYMENT';
```

- [ ] **Step 6: Leave changes uncommitted.**

---

### Task 2: `PaymentService.SimulateSandboxOutcome`

**Files:**
- Create: `back/internal/service/payment_simulate.go`
- Create: `back/internal/service/payment_simulate_test.go`
- Modify: `back/internal/domain/payment/payment.go` (two new sentinel errors next to the existing `Err...` vars)

**Interfaces:**
- Consumes (existing): `s.paymentRepo.MarkPaid(ctx, id, paidAt *time.Time, net int64) (string, error)`, `s.paymentRepo.UpdateStatus(ctx, id, status string, paidAt *time.Time) (string, error)`, `merchantNetFor(p) (int64, error)`, `detached(ctx) (context.Context, context.CancelFunc)`, `s.NotifyMerchantPaymentEvent(ctx, p, eventType string, late bool)`, `eventTypeForStatus(status) string`, `s.webhookEventRepo.Create(ctx, *provider.WebhookEvent) error`, `payment.ErrPaymentAlreadyTerminal`.
- Produces (used by Task 3):
  - `payment.ErrInvalidSimulationOutcome` — `errors.New("outcome must be one of paid, failed, expired")`
  - `payment.ErrSimulationNotAllowed` — `errors.New("only sandbox payments can be simulated")`
  - `func (s *PaymentService) SimulateSandboxOutcome(ctx context.Context, p *payment.Payment, outcome string) (*payment.Payment, error)` — returns the same `p`, updated in place.

- [ ] **Step 1: Add the sentinel errors**

In `back/internal/domain/payment/payment.go`, next to the other payment errors:

```go
// Sandbox simulator (PaymentService.SimulateSandboxOutcome).
var (
	ErrInvalidSimulationOutcome = errors.New("outcome must be one of paid, failed, expired")
	ErrSimulationNotAllowed     = errors.New("only sandbox payments can be simulated")
)
```

- [ ] **Step 2: Write the failing tests**

`back/internal/service/payment_simulate_test.go` (reuses fakes from `payment_fakes_test.go`, `newTestPaymentService` from `payment_service_test.go`, `newCallbackMerchant` from `payment_reconcile_test.go`):

```go
package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/google/uuid"
)

func seedSimPayment(t *testing.T, repo *fakePaymentRepo, env, providerName, status string, fee int64) *payment.Payment {
	t.Helper()
	ref := "SBX-" + uuid.NewString()[:8]
	p := &payment.Payment{
		ID: uuid.New(), Reference: "PAY-" + ref, MerchantID: uuid.New(),
		Amount: 2000, FeeAmount: fee, Currency: payment.CurrencyIDR,
		PaymentMethod: payment.PaymentMethodQRIS, ProviderName: providerName,
		ProviderReference: &ref, Status: status, Environment: env,
		ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(),
	}
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return p
}

func TestSimulateSandboxOutcome_Outcomes(t *testing.T) {
	for _, tc := range []struct {
		outcome, wantEvent string
		fee, wantNet       int64
	}{
		{"paid", "payment.paid", 0, 1986},
		{"paid", "payment.paid", 14, 2000},
		{"failed", "payment.failed", 0, 0},
		{"expired", "payment.expired", 0, 0},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			svc, repo, _, _ := newTestPaymentService()
			p := seedSimPayment(t, repo, payment.EnvironmentSandbox, "sandbox", payment.StatusPending, tc.fee)
			callbacks := newCallbackMerchant(t, svc, p.MerchantID)

			got, err := svc.SimulateSandboxOutcome(context.Background(), p, tc.outcome)
			if err != nil {
				t.Fatalf("simulate: %v", err)
			}
			if got.Status != tc.outcome {
				t.Fatalf("returned status = %s, want %s", got.Status, tc.outcome)
			}
			stored, _ := repo.GetByID(context.Background(), p.ID)
			if stored.Status != tc.outcome {
				t.Fatalf("stored status = %s, want %s", stored.Status, tc.outcome)
			}
			if tc.outcome == "paid" {
				if stored.PaidAt == nil || stored.MerchantNetAmount != tc.wantNet {
					t.Fatalf("paid: paid_at=%v net=%d, want net %d", stored.PaidAt, stored.MerchantNetAmount, tc.wantNet)
				}
			}
			if len(callbacks.created) != 1 || callbacks.created[0].EventType != tc.wantEvent {
				t.Fatalf("want one %s callback, got %+v", tc.wantEvent, callbacks.created)
			}
		})
	}
}

func TestSimulateSandboxOutcome_RecordsEvent(t *testing.T) {
	svc, repo, _, _ := newTestPaymentService()
	webhookRepo := svc.webhookEventRepo.(*fakeWebhookEventRepo)
	p := seedSimPayment(t, repo, payment.EnvironmentSandbox, "sandbox", payment.StatusPending, 0)

	if _, err := svc.SimulateSandboxOutcome(context.Background(), p, "failed"); err != nil {
		t.Fatalf("simulate: %v", err)
	}
	ev := webhookRepo.last()
	if ev == nil || ev.EventType != "sandbox.simulate.failed" || ev.ProviderName != "sandbox" ||
		ev.Status != "failed" || !ev.IsProcessed || ev.PaymentID == nil || *ev.PaymentID != p.ID {
		t.Fatalf("unexpected simulation event: %+v", ev)
	}
}

func TestSimulateSandboxOutcome_Guards(t *testing.T) {
	for _, tc := range []struct {
		name, env, provider, status, outcome string
		want                                 error
	}{
		{"production rejected", payment.EnvironmentProduction, "cashi", payment.StatusPending, "paid", payment.ErrSimulationNotAllowed},
		{"production with sandbox provider rejected", payment.EnvironmentProduction, "sandbox", payment.StatusPending, "paid", payment.ErrSimulationNotAllowed},
		{"sandbox env but real provider rejected", payment.EnvironmentSandbox, "cashi", payment.StatusPending, "paid", payment.ErrSimulationNotAllowed},
		{"not pending", payment.EnvironmentSandbox, "sandbox", payment.StatusPaid, "failed", payment.ErrPaymentAlreadyTerminal},
		{"invalid outcome", payment.EnvironmentSandbox, "sandbox", payment.StatusPending, "refunded", payment.ErrInvalidSimulationOutcome},
		{"empty outcome", payment.EnvironmentSandbox, "sandbox", payment.StatusPending, "", payment.ErrInvalidSimulationOutcome},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, _, _ := newTestPaymentService()
			p := seedSimPayment(t, repo, tc.env, tc.provider, tc.status, 0)
			callbacks := newCallbackMerchant(t, svc, p.MerchantID)

			_, err := svc.SimulateSandboxOutcome(context.Background(), p, tc.outcome)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			stored, _ := repo.GetByID(context.Background(), p.ID)
			if stored.Status != tc.status {
				t.Fatalf("status changed to %s on a rejected simulation", stored.Status)
			}
			if len(callbacks.created) != 0 {
				t.Fatalf("rejected simulation sent callbacks: %+v", callbacks.created)
			}
		})
	}
}
```

If `svc.webhookEventRepo` is not directly assertable to `*fakeWebhookEventRepo` (e.g. a different field name), build the service with `newWebhookTestService()` (returns the webhook repo) instead and keep the assertions.

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/service -run TestSimulateSandboxOutcome -v`
Expected: FAIL — `SimulateSandboxOutcome` undefined.

- [ ] **Step 4: Implement**

`back/internal/service/payment_simulate.go`:

```go
package service

import (
	"context"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
)

// sandboxProviderName is the sandbox adapter's name (provider/sandbox.ProviderName);
// repeated here so the service does not import the adapter package.
const sandboxProviderName = "sandbox"

// SimulateSandboxOutcome drives a pending sandbox payment to paid, failed or
// expired through the same repository writes and merchant callback as a real
// settlement or expiry, so merchants can test their integration end to end.
// Production payments (and sandbox-environment payments on a real provider)
// are never simulated. p is updated in place and returned.
func (s *PaymentService) SimulateSandboxOutcome(ctx context.Context, p *payment.Payment, outcome string) (*payment.Payment, error) {
	switch outcome {
	case payment.StatusPaid, payment.StatusFailed, payment.StatusExpired:
	default:
		return nil, payment.ErrInvalidSimulationOutcome
	}
	if p.Environment != payment.EnvironmentSandbox || p.ProviderName != sandboxProviderName {
		return nil, payment.ErrSimulationNotAllowed
	}
	if p.Status != payment.StatusPending {
		return nil, payment.ErrPaymentAlreadyTerminal
	}

	// Once the status is written the callback must follow, even if the
	// request is cancelled now (same reasoning as reconcile).
	ctx, cancel := detached(ctx)
	defer cancel()

	if outcome == payment.StatusPaid {
		net, err := merchantNetFor(p)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		if _, err := s.paymentRepo.MarkPaid(ctx, p.ID, &now, net); err != nil {
			return nil, err
		}
		p.PaidAt = &now
		p.MerchantNetAmount = net
	} else {
		if _, err := s.paymentRepo.UpdateStatus(ctx, p.ID, outcome, nil); err != nil {
			return nil, err
		}
	}
	p.Status = outcome

	s.recordSimulation(ctx, p, outcome)
	s.NotifyMerchantPaymentEvent(ctx, p, eventTypeForStatus(outcome), false)
	return p, nil
}

// recordSimulation leaves a webhook_events row so the simulation shows in the
// admin payment timeline and logs. Best effort: a failure is only logged.
func (s *PaymentService) recordSimulation(ctx context.Context, p *payment.Payment, outcome string) {
	pid := p.ID
	ref := ""
	if p.ProviderReference != nil {
		ref = *p.ProviderReference
	}
	ev := &provider.WebhookEvent{
		PaymentID:         &pid,
		ProviderName:      sandboxProviderName,
		ProviderReference: ref,
		EventType:         "sandbox.simulate." + outcome,
		Status:            outcome,
		RawPayload: map[string]interface{}{
			"source":  "sandbox_simulator",
			"outcome": outcome,
		},
		IsProcessed: true,
	}
	if err := s.webhookEventRepo.Create(ctx, ev); err != nil {
		logger.ErrorfCtx(ctx, "Failed to record sandbox simulation for %s: %v", p.Reference, err)
	}
}
```

Check against the code: the `provider.WebhookEvent` field names must match `recordReconcileMismatch` in `payment_service.go`; `detached` is in `payment_ctx.go`. `WebhookEventRepository.Create` stores `status` and `is_processed` as given, so no `Finalize` call is needed. In the test, `svc.webhookEventRepo` is the interface field `webhookEventRepository`; the type assertion to `*fakeWebhookEventRepo` works because `newTestPaymentService` passes that fake.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/service -run 'TestSimulateSandboxOutcome|TestMerchantReceives|TestMerchantNetFor' -race -v`
Expected: PASS.

- [ ] **Step 6: Leave changes uncommitted.**

---

### Task 3: Endpoints, routes, docs

**Files:**
- Modify: `back/internal/handler/payment_handler.go` (public simulate handler; extract the public JSON map into a helper)
- Modify: `back/internal/handler/merchant_handler.go` (merchant simulate handler)
- Modify: `back/cmd/api/main.go` (two routes)
- Modify: `back/docs/openapi.yaml`, `back/docs/payment-state-machine.md`
- Create: `back/internal/handler/simulate_handler_test.go`
- Modify: `back/cmd/api/router_test.go` (or the file holding `TestRouter_*` route-reach tests)

**Interfaces:**
- Consumes: `SimulateSandboxOutcome`, `payment.ErrInvalidSimulationOutcome`, `payment.ErrSimulationNotAllowed`, `payment.ErrPaymentAlreadyTerminal`, `payment.ErrPaymentNotFound`, `service.MerchantReceives(p) int64`, existing `merchantPaymentDetail` struct in `merchant_handler.go`.
- Produces (used by Task 4): JSON responses — public: the same object `GET /public/payments/by-reference/{reference}` returns; merchant: the same object `GET /merchant/payments/{id}` returns (`ToPaymentResponse` + `merchant_net_amount`). Request body `{"outcome":"paid"|"failed"|"expired"}`.

- [ ] **Step 1: Write the failing handler tests**

`back/internal/handler/simulate_handler_test.go` (uses `newTestPaymentService()` from `payment_handler_test.go`, which registers the real sandbox adapter):

```go
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

func seedHandlerSimPayment(t *testing.T, repo *fakePaymentRepo, merchantID uuid.UUID, env, providerName string) *payment.Payment {
	t.Helper()
	ref := "SBX-" + uuid.NewString()[:8]
	p := &payment.Payment{
		ID: uuid.New(), Reference: "PAY-" + ref, MerchantID: merchantID, Amount: 2000,
		Currency: payment.CurrencyIDR, PaymentMethod: payment.PaymentMethodQRIS,
		ProviderName: providerName, ProviderReference: &ref, Status: payment.StatusPending,
		Environment: env, ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(),
	}
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return p
}

func TestPaymentHandler_SimulateByReference(t *testing.T) {
	for _, tc := range []struct {
		name, env, provider, body string
		wantCode                  int
		wantStatus                string
	}{
		{"paid", payment.EnvironmentSandbox, "sandbox", `{"outcome":"paid"}`, http.StatusOK, "paid"},
		{"expired", payment.EnvironmentSandbox, "sandbox", `{"outcome":"expired"}`, http.StatusOK, "expired"},
		{"production forbidden", payment.EnvironmentProduction, "cashi", `{"outcome":"paid"}`, http.StatusForbidden, ""},
		{"bad outcome", payment.EnvironmentSandbox, "sandbox", `{"outcome":"refunded"}`, http.StatusBadRequest, ""},
		{"bad body", payment.EnvironmentSandbox, "sandbox", `not json`, http.StatusBadRequest, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newTestPaymentService()
			h := NewPaymentHandler(svc, "http://localhost:3000")
			p := seedHandlerSimPayment(t, repo, uuid.New(), tc.env, tc.provider)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/public/payments/by-reference/"+p.Reference+"/simulate", strings.NewReader(tc.body))
			req = mux.SetURLVars(req, map[string]string{"reference": p.Reference})
			rec := httptest.NewRecorder()
			h.SimulateByReference(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantStatus != "" {
				var resp map[string]interface{}
				_ = json.Unmarshal(rec.Body.Bytes(), &resp)
				if resp["status"] != tc.wantStatus || resp["reference"] != p.Reference {
					t.Fatalf("unexpected body: %s", rec.Body.String())
				}
				if _, leaked := resp["merchant_id"]; leaked {
					t.Fatalf("public response must keep the public field set: %s", rec.Body.String())
				}
			}
		})
	}
}

func TestPaymentHandler_SimulateByReference_NotFoundAndConflict(t *testing.T) {
	svc, repo := newTestPaymentService()
	h := NewPaymentHandler(svc, "http://localhost:3000")
	call := func(ref string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"outcome":"paid"}`))
		req = mux.SetURLVars(req, map[string]string{"reference": ref})
		rec := httptest.NewRecorder()
		h.SimulateByReference(rec, req)
		return rec
	}
	if rec := call("PAY-NOPE"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown reference: %d", rec.Code)
	}
	p := seedHandlerSimPayment(t, repo, uuid.New(), payment.EnvironmentSandbox, "sandbox")
	if rec := call(p.Reference); rec.Code != http.StatusOK {
		t.Fatalf("first simulate: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(p.Reference); rec.Code != http.StatusConflict {
		t.Fatalf("second simulate must be 409, got %d", rec.Code)
	}
}

func TestMerchantHandler_SimulatePayment(t *testing.T) {
	svc, repo := newTestPaymentService()
	h := NewMerchantHandler(svc, nil, nil, "http://localhost:3000")
	owner := uuid.New()
	p := seedHandlerSimPayment(t, repo, owner, payment.EnvironmentSandbox, "sandbox")

	call := func(merchantID uuid.UUID, body string) *httptest.ResponseRecorder {
		ctx := context.WithValue(context.Background(), middleware.MerchantIDContextKey, merchantID)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/merchant/payments/"+p.ID.String()+"/simulate", strings.NewReader(body)).WithContext(ctx)
		req = mux.SetURLVars(req, map[string]string{"id": p.ID.String()})
		rec := httptest.NewRecorder()
		h.SimulatePayment(rec, req)
		return rec
	}

	if rec := call(uuid.New(), `{"outcome":"paid"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("other merchant must get 404, got %d", rec.Code)
	}
	if rec := call(owner, `{"outcome":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad outcome must be 400, got %d", rec.Code)
	}
	rec := call(owner, `{"outcome":"paid"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner simulate: %d %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["status"] != "paid" || resp["merchant_net_amount"] != float64(1986) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/handler -run 'Simulate' -v`
Expected: FAIL — `SimulateByReference` / `SimulatePayment` undefined.

- [ ] **Step 3: Implement the handlers**

In `back/internal/handler/payment_handler.go`:

1. Move the `map[string]interface{}{...}` literal from `GetPaymentByReference` into a method and call it from there (behaviour unchanged):

```go
// publicPaymentJSON is the public-safe payment view used by the checkout page
// (GET by reference and the sandbox simulator).
func (h *PaymentHandler) publicPaymentJSON(ctx context.Context, p *payment.Payment) map[string]interface{} {
	return map[string]interface{}{
		// ... the exact fields currently in GetPaymentByReference, with
		// r.Context() replaced by ctx ...
	}
}
```

2. Shared request/error helpers and the public handler:

```go
type simulateRequest struct {
	Outcome string `json:"outcome"`
}

// decodeSimulateOutcome reads {"outcome": "..."}; false means a 400 was sent.
func decodeSimulateOutcome(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req simulateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return "", false
	}
	return strings.TrimSpace(req.Outcome), true
}

// respondSimulateError maps SimulateSandboxOutcome errors to HTTP.
func respondSimulateError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, payment.ErrInvalidSimulationOutcome):
		respondError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, payment.ErrSimulationNotAllowed):
		respondError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, payment.ErrPaymentAlreadyTerminal), errors.Is(err, payment.ErrInvalidStatusTransition):
		respondError(w, http.StatusConflict, "Payment is no longer pending")
	default:
		logger.ErrorfCtx(r.Context(), "sandbox simulate: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to simulate payment")
	}
}

// SimulateByReference is the public sandbox simulator used by the checkout
// page: POST /public/payments/by-reference/{reference}/simulate.
func (h *PaymentHandler) SimulateByReference(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(mux.Vars(r)["reference"])
	if ref == "" {
		respondError(w, http.StatusBadRequest, "Reference is required")
		return
	}
	outcome, ok := decodeSimulateOutcome(w, r)
	if !ok {
		return
	}
	p, err := h.paymentService.GetPaymentByReference(r.Context(), ref)
	if err != nil {
		if errors.Is(err, payment.ErrPaymentNotFound) {
			respondError(w, http.StatusNotFound, "Payment not found")
			return
		}
		respondError(w, http.StatusInternalServerError, "Failed to load payment")
		return
	}
	p, err = h.paymentService.SimulateSandboxOutcome(r.Context(), p, outcome)
	if err != nil {
		respondSimulateError(w, r, err)
		return
	}
	respondJSON(w, http.StatusOK, h.publicPaymentJSON(r.Context(), p))
}
```

In `back/internal/handler/merchant_handler.go`:

```go
// SimulatePayment is the dashboard sandbox simulator (Test Payment page):
// POST /merchant/payments/{id}/simulate. Another merchant's payment is 404.
func (h *MerchantHandler) SimulatePayment(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payment ID")
		return
	}
	outcome, ok := decodeSimulateOutcome(w, r)
	if !ok {
		return
	}
	p, err := h.paymentService.GetPayment(r.Context(), id)
	if err != nil || p.MerchantID != merchantID {
		if err == nil || errors.Is(err, payment.ErrPaymentNotFound) {
			respondError(w, http.StatusNotFound, "Payment not found")
			return
		}
		respondError(w, http.StatusInternalServerError, "Failed to get payment")
		return
	}
	p, err = h.paymentService.SimulateSandboxOutcome(r.Context(), p, outcome)
	if err != nil {
		respondSimulateError(w, r, err)
		return
	}
	respondJSON(w, http.StatusOK, merchantPaymentDetail{
		PaymentResponse:   payment.ToPaymentResponse(p, h.frontendURL),
		MerchantNetAmount: service.MerchantReceives(p),
	})
}
```

Add any missing imports (`errors`, `context`, `logger`) following each file's existing import block.

- [ ] **Step 4: Register the routes**

In `back/cmd/api/main.go`:
- Next to the existing public GET by-reference route:

```go
api.Handle("/public/payments/by-reference/{reference}/simulate", publicRateLimiter.Limit(http.HandlerFunc(paymentHandler.SimulateByReference))).Methods("POST")
```

- Next to `merchantDash.HandleFunc("/payments/{id}", merchantHandler.GetPayment).Methods("GET")`:

```go
merchantDash.HandleFunc("/payments/{id}/simulate", merchantHandler.SimulatePayment).Methods("POST")
```

- [ ] **Step 5: Router tests**

In the router test file that holds `TestRouter_MerchantPageStatsRoutes_ReachHandlers` (`cmd/api/payout_router_test.go`) add `TestRouter_SandboxSimulateRoutes`, following that test's setup exactly:
- `POST /api/v1/merchant/payments/{random uuid}/simulate` without a token → 401.
- Same with a valid merchant JWT → 404 (`Payment not found`, proves the route reaches `SimulatePayment`).
- `POST /api/v1/public/payments/by-reference/PAY-NOPE/simulate` with `{"outcome":"paid"}` → 404 with message `Payment not found` (proves the public route is wired; no auth needed).

- [ ] **Step 6: Docs**

- `back/docs/openapi.yaml`: add both paths (`post`, request body schema `{outcome: enum [paid, failed, expired]}`, responses 200/400/403/404/409, plus 429 for the public one and 401 for the merchant one), mirroring the neighbouring `/public/payments/by-reference/{reference}` and `/merchant/payments/{id}` entries. Describe: sandbox only, uses the real status path, sends the merchant callback.
- `back/docs/payment-state-machine.md`: in the transitions section, add the sandbox simulator as a source for `pending → paid`, `pending → failed`, `pending → expired` (sandbox environment + sandbox provider only; same guards and callbacks as the real paths).

- [ ] **Step 7: Run all backend checks**

Run: `gofmt -l ./internal ./cmd` (empty), `go vet ./...`, `go test ./... -race` (with the throwaway PG for PG tests).
Expected: all PASS.

- [ ] **Step 8: Leave changes uncommitted.**

---

### Task 4: Front-end simulator (checkout + Test Payment)

**Files:**
- Modify: `front/lib/merchant-api.ts`
- Create: `front/app/components/SandboxSimulator.tsx`
- Modify: `front/app/pay/[reference]/page.tsx`, `front/app/pay/[reference]/CheckoutPanels.tsx`
- Modify: `front/app/dashboard/test-payment/page.tsx`

**Interfaces:**
- Consumes: Task 3 endpoints and responses.
- Produces: nothing for later tasks.

- [ ] **Step 1: API helpers** in `front/lib/merchant-api.ts` (follow the file's existing fetch/auth helpers for the merchant call — reuse whatever `fetchMerchantPayment` uses for the JWT header and error parsing):

```ts
export type SimulationOutcome = "paid" | "failed" | "expired";

// Public sandbox simulator used by the checkout page. Returns the same
// public payment view as fetchPublicPaymentByReference.
export async function simulatePublicPayment(
  reference: string,
  outcome: SimulationOutcome,
): Promise<PublicPayment> {
  const res = await fetch(
    `${API_URL}/api/v1/public/payments/by-reference/${encodeURIComponent(reference)}/simulate`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ outcome }),
    },
  );
  const body = await res.json().catch(() => null);
  if (!res.ok) {
    throw new Error(body?.message ?? "Simulasi gagal.");
  }
  return body as PublicPayment;
}

// Dashboard sandbox simulator (Test Payment page).
export function simulateMerchantPayment(
  id: string,
  outcome: SimulationOutcome,
): Promise<MerchantPayment> {
  // POST `/api/v1/merchant/payments/${encodeURIComponent(id)}/simulate`
  // with JSON body { outcome }, using the same authenticated request helper
  // and error handling as createMerchantPayment; fallback message
  // "Simulation failed.".
}
```

Implement `simulateMerchantPayment`'s body with the existing helper (no new fetch wrapper).

- [ ] **Step 2: Shared component** `front/app/components/SandboxSimulator.tsx`:

```tsx
"use client";

import type { SimulationOutcome } from "@/lib/merchant-api";

type Labels = { title: string; paid: string; failed: string; expired: string };

export const SIMULATOR_LABELS_ID: Labels = {
  title: "Simulator sandbox · bukan uang sungguhan",
  paid: "Simulasikan dibayar",
  failed: "Simulasikan gagal",
  expired: "Simulasikan kedaluwarsa",
};

export const SIMULATOR_LABELS_EN: Labels = {
  title: "Sandbox simulator · no real money",
  paid: "Simulate paid",
  failed: "Simulate failed",
  expired: "Simulate expired",
};

/** Yellow box with three buttons that drive a pending sandbox payment. */
export default function SandboxSimulator({
  labels,
  busy,
  onSimulate,
}: {
  labels: Labels;
  /** The outcome being simulated, or null when idle. */
  busy: SimulationOutcome | null;
  onSimulate: (outcome: SimulationOutcome) => void;
}) {
  const buttons: { outcome: SimulationOutcome; label: string; className: string }[] = [
    { outcome: "paid", label: labels.paid, className: "bg-[#2f9e5a] text-white hover:bg-[#278a4e]" },
    { outcome: "failed", label: labels.failed, className: "bg-white text-[#e85d3b] border border-[#f3c9bd] hover:bg-[#fff1ed]" },
    { outcome: "expired", label: labels.expired, className: "bg-white text-[#6b7c93] border border-[#e8eef4] hover:bg-[#f4f7fb]" },
  ];
  return (
    <div className="rounded-xl border border-[#f5d48a] bg-[#fff7e6] p-3">
      <p className="text-[12px] font-semibold text-[#c27a00]">{labels.title}</p>
      <div className="mt-2 flex flex-col gap-2 sm:flex-row">
        {buttons.map((b) => (
          <button
            key={b.outcome}
            type="button"
            disabled={busy !== null}
            onClick={() => onSimulate(b.outcome)}
            className={`h-10 min-w-0 flex-1 rounded-lg px-3 text-[12.5px] font-semibold transition disabled:cursor-wait disabled:opacity-60 ${b.className}`}
          >
            {busy === b.outcome ? "…" : b.label}
          </button>
        ))}
      </div>
    </div>
  );
}
```

- [ ] **Step 3: Checkout page**

In `front/app/pay/[reference]/page.tsx`:
- State `const [simulating, setSimulating] = useState<SimulationOutcome | null>(null);`
- Handler:

```ts
async function simulate(outcome: SimulationOutcome) {
  if (!reference || simulating) return;
  setSimulating(outcome);
  try {
    const data = await simulatePublicPayment(reference, outcome);
    setPayment(data);
    setError(null);
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "Simulasi gagal.");
  } finally {
    setSimulating(null);
  }
}
```

- Pass `simulator={sandbox ? { busy: simulating, onSimulate: (o) => void simulate(o) } : undefined}` to `QrisPanel` (only rendered while pending, so the simulator is only shown for pending sandbox payments).

In `CheckoutPanels.tsx` `QrisPanel`: add the optional prop `simulator?: { busy: SimulationOutcome | null; onSimulate: (o: SimulationOutcome) => void }` and, directly below the "Saya sudah bayar" button, render

```tsx
{simulator ? (
  <div className="mt-3">
    <SandboxSimulator labels={SIMULATOR_LABELS_ID} busy={simulator.busy} onSimulate={simulator.onSimulate} />
  </div>
) : null}
```

- [ ] **Step 4: Test Payment page** (`front/app/dashboard/test-payment/page.tsx`)

- State `const [simulating, setSimulating] = useState<SimulationOutcome | null>(null);`
- Handler:

```ts
async function simulate(outcome: SimulationOutcome) {
  if (!visible || simulating) return;
  setSimulating(outcome);
  try {
    const p = await simulateMerchantPayment(visible.id, outcome);
    setPayment((cur) =>
      cur && cur.id === p.id ? { ...cur, ...p, qris_data: p.qris_data ?? cur.qris_data } : cur,
    );
    loadHistory();
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "Simulation failed.");
  } finally {
    setSimulating(null);
  }
}
```

  (Import `toast` from `react-hot-toast` if the page does not already.)
- Render `<SandboxSimulator labels={SIMULATOR_LABELS_EN} busy={simulating} onSimulate={(o) => void simulate(o)} />` directly below the shown payment's QR, only when `!isProduction && visible && visible.status === "pending"`.
- Production mode never renders it.

- [ ] **Step 5: Verify**

Run from `front/`: `npm run lint` (no issues) and `npm run build` (succeeds).
Check by reading the JSX: no unprefixed utility changes desktop layout elsewhere; the simulator box uses `min-w-0`/`flex-1` and stacks below `sm`.

- [ ] **Step 6: Leave changes uncommitted.**
