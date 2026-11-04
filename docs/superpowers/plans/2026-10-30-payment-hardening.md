# Payment Hardening (Paket 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make payment status changes race-safe and provider-verified (atomic transitions, check-status + gross-amount verification, late payments, automatic reconcile), default environment to sandbox, and close the CreatePayment nil-panic / invalid provider-config paths — released as `1.0.1`.

**Architecture:** The state machine gains `expired → paid`; `PaymentRepository.UpdateStatus` becomes one conditional SQL statement that returns the previous status, and only the caller that wins it notifies the merchant. Webhooks become triggers: the payment is marked paid only after the provider's check-status confirms `SETTLED` and the stored gross amount. A new 2-minute `reconcile-payments` job re-checks recent pending/expired production payments with the same verification.

**Tech Stack:** Go 1.25, PostgreSQL (raw SQL, `lib/pq`), go-sqlmock, standard `testing`.

**Spec:** [`back/docs/superpowers/specs/2026-10-30-payment-hardening-design.md`](../specs/2026-10-30-payment-hardening-design.md)

## Global Constraints

- **NO git commits, NO `git add`, NO branches** (explicit user instruction). Each task ends with "report changed files".
- **Never print secrets** (`back/.config.toml`, `back/db_password`, `back/.env`, VPS `.config.toml`).
- No new Go dependencies; no DB migrations.
- All Go commands run from `back/` (module `github.com/akbarryyan/pg-aggregator-back`); verification = `gofmt -l ./cmd ./internal`, `go vet ./...`, `go test ./... -race -count=1`.
- State machine: `paid` ← {`pending`, `expired`}; `expired`/`failed`/`cancelled` ← {`pending`}; `paid`/`failed`/`cancelled` final. Only provider-reported settlements write `paid`.
- Amount rule: provider check-status `amount` (and webhook `amount` when present) must equal `payments.amount` (gross; Cashi's 0.7% fee is taken at settlement).
- Mismatch error text always starts with `amount_mismatch:` — e.g. `amount_mismatch: expected 2000, provider 1500, webhook 1500`.
- Exact strings: webhook event type for late settlement `payment.paid.late`; reconcile mismatch event type `reconcile.amount_mismatch`; verification-failure event status `verification_failed`; merchant callback payload field `late_payment` (boolean, always present; `true` only for late settlements); merchant callback event stays `payment.paid`.
- Constants: provider verification timeout 5s; reconcile every 2 minutes, window 24h, batch 50.
- Environment on create paths: `""` → `sandbox`; `production|prod|live` → `production`; `sandbox|test|dev|development` → `sandbox` (trimmed, case-insensitive); anything else → `ErrInvalidEnvironment` (HTTP 400). `NormalizeEnvironment` (read paths) unchanged.
- `sandbox` is never a production routing candidate; merchant provider configs accept only registered, non-sandbox providers and payment method `qris`.
- Webhook HTTP mapping: success 200 `success`; duplicate / terminal / non-settlement / amount mismatch → 200 `ignored`; provider verification failed → 502; everything else → 400 (unchanged).
- Ignore `back/.superpowers/`. Comments explain why; table-driven tests; no mock libraries beyond sqlmock.

## File Structure

| File | Change |
|---|---|
| `internal/domain/payment/payment.go`, `errors.go`, `payment_test.go` | state machine, `ParseEnvironment`, new errors |
| `internal/domain/paymentlink/paymentlink.go`, `paymentlink_test.go` | use `ParseEnvironment` |
| `internal/domain/provider/provider.go`, `errors.go` | `ProviderSandbox`, `NormalizedPaymentStatus.Amount`, config errors |
| `internal/repository/payment_repository_update.go` | atomic `UpdateStatus` |
| `internal/repository/payment_repository.go` | `ListReconcileCandidates` |
| `internal/repository/payment_repository_test.go` | repo tests |
| `internal/service/interfaces.go` | repo interface changes |
| `internal/service/payment_verify.go` (new), `payment_verify_test.go` (new) | `verifyProviderPayment` |
| `internal/service/payment_webhook_service.go` | verified webhook flow, expire notify |
| `internal/service/payment_service.go` | routing safety, reconcile rewrite, recent-reconcile job |
| `internal/service/merchant_callback_service.go` | `late_payment` |
| `internal/service/admin_merchant_service.go` | provider-config validation |
| `internal/provider/cashi/{client.go,types.go,client_test.go}` | status amount, webhook timestamp |
| `internal/provider/sandbox/{adapter.go,adapter_test.go}` | status amount |
| `internal/handler/{webhook_handler.go,admin_handler.go,payment_link_handler.go,merchant_handler.go}` | error mapping, env |
| test fakes: `internal/service/payment_fakes_test.go`, `internal/handler/fakes_test.go`, `cmd/api/router_test.go` | new signatures |
| `cmd/api/main.go` | provider registry, reconcile job |
| `docs/payment-state-machine.md`, `docs/openapi.yaml`, `README.md` | docs |

---

### Task 1: State machine + atomic `UpdateStatus`

**Files:** `internal/domain/payment/payment.go`, `internal/domain/payment/payment_test.go`, `internal/repository/payment_repository_update.go`, `internal/repository/payment_repository_test.go`, `internal/service/interfaces.go`, `internal/service/payment_service.go`, `internal/service/payment_webhook_service.go`, `internal/service/payment_fakes_test.go`, `internal/handler/fakes_test.go`, `cmd/api/router_test.go`

**Interfaces:**
- Produces: `func payment.AllowedSourceStatuses(newStatus string) []string`; `payment.CanTransitionTo` semantics per Global Constraints; `(*repository.PaymentRepository).UpdateStatus(ctx context.Context, id uuid.UUID, newStatus string, paidAt *time.Time) (previousStatus string, err error)`; service `paymentRepository.UpdateStatus` with the same signature. On a no-op update it returns `payment.ErrPaymentNotFound`, `payment.ErrPaymentAlreadyTerminal` (current status is terminal per `IsTerminalStatus`), or `payment.ErrInvalidStatusTransition`.
- Behavior of services is otherwise unchanged in this task (callers just discard the previous status).

- [ ] **Step 1: Failing domain tests** — in `internal/domain/payment/payment_test.go`, inside `TestCanTransitionTo`, replace the case
  `{"expired is terminal, cannot move to paid", StatusExpired, StatusPaid, false},` with
  ```go
		{"expired to paid allowed (late settlement)", StatusExpired, StatusPaid, true},
		{"expired cannot move to failed", StatusExpired, StatusFailed, false},
		{"paid to paid is not a transition", StatusPaid, StatusPaid, false},
  ```
  and append a new test:
  ```go
func TestAllowedSourceStatuses(t *testing.T) {
	cases := map[string][]string{
		StatusPaid:      {StatusPending, StatusExpired},
		StatusExpired:   {StatusPending},
		StatusFailed:    {StatusPending},
		StatusCancelled: {StatusPending},
		StatusPending:   nil,
		"unknown":       nil,
	}
	for to, want := range cases {
		got := AllowedSourceStatuses(to)
		if len(got) != len(want) {
			t.Fatalf("AllowedSourceStatuses(%q) = %v, want %v", to, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("AllowedSourceStatuses(%q) = %v, want %v", to, got, want)
			}
		}
	}
}
  ```
- [ ] **Step 2: Run** `go test ./internal/domain/payment/ -count=1` → FAIL (`undefined: AllowedSourceStatuses`, expired→paid case).
- [ ] **Step 3: Implement** — in `internal/domain/payment/payment.go` replace the whole `CanTransitionTo` function with:
  ```go
// AllowedSourceStatuses lists the statuses a payment may move to newStatus
// from. pending can go to any outcome; the only way out of an outcome is
// expired → paid, for a settlement the provider reports after the payment
// already expired locally — the customer really paid. Only
// provider-reported settlements (a verified webhook or reconcile) ever write
// paid; there is no manual "mark paid" path, so this cannot revive an
// abandoned payment on its own.
func AllowedSourceStatuses(newStatus string) []string {
	switch newStatus {
	case StatusPaid:
		return []string{StatusPending, StatusExpired}
	case StatusExpired, StatusFailed, StatusCancelled:
		return []string{StatusPending}
	default:
		return nil
	}
}

func CanTransitionTo(currentStatus, newStatus string) bool {
	for _, s := range AllowedSourceStatuses(newStatus) {
		if s == currentStatus {
			return true
		}
	}
	return false
}
  ```
- [ ] **Step 4: Run** `go test ./internal/domain/payment/ -count=1` → PASS.
- [ ] **Step 5: Failing repository tests** — in `internal/repository/payment_repository_test.go` delete the three functions `TestPaymentRepository_UpdateStatus_RejectsInvalidTransition`, `TestPaymentRepository_UpdateStatus_ValidTransition`, `TestPaymentRepository_UpdateStatus_PaymentNotFound` (with their doc comments) and add:
  ```go
const updateStatusSQL = `(?s)WITH prev AS \(SELECT status FROM payments WHERE id = \$4 FOR UPDATE\).*UPDATE payments SET status = \$1, paid_at = \$2, updated_at = \$3.*WHERE id = \$4 AND status IN \(%s\).*RETURNING \(SELECT status FROM prev\)`

func TestPaymentRepository_UpdateStatus_PaidFromPendingOrExpired(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	id := uuid.New()
	paidAt := time.Now()

	mock.ExpectQuery(fmt.Sprintf(updateStatusSQL, `\$5, \$6`)).
		WithArgs(payment.StatusPaid, &paidAt, sqlmock.AnyArg(), id, payment.StatusPending, payment.StatusExpired).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(payment.StatusExpired))

	prev, err := repo.UpdateStatus(context.Background(), id, payment.StatusPaid, &paidAt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prev != payment.StatusExpired {
		t.Errorf("previous status = %q, want expired", prev)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestPaymentRepository_UpdateStatus_ExpireOnlyFromPending(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	id := uuid.New()

	mock.ExpectQuery(fmt.Sprintf(updateStatusSQL, `\$5`)).
		WithArgs(payment.StatusExpired, sqlmock.AnyArg(), sqlmock.AnyArg(), id, payment.StatusPending).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(payment.StatusPending))

	if _, err := repo.UpdateStatus(context.Background(), id, payment.StatusExpired, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// No row matched the guarded UPDATE: someone else moved the payment first
// (or it was never eligible). The repository re-reads it to explain why.
func TestPaymentRepository_UpdateStatus_NoRowsReportsTerminal(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	p := samplePayment()
	p.Status = payment.StatusPaid

	mock.ExpectQuery(fmt.Sprintf(updateStatusSQL, `\$5`)).
		WithArgs(payment.StatusExpired, sqlmock.AnyArg(), sqlmock.AnyArg(), p.ID, payment.StatusPending).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT .* FROM payments WHERE id = \$1`).
		WithArgs(p.ID).
		WillReturnRows(sqlmock.NewRows(paymentCols).AddRow(paymentRow(p)...))

	_, err := repo.UpdateStatus(context.Background(), p.ID, payment.StatusExpired, nil)
	if !errors.Is(err, payment.ErrPaymentAlreadyTerminal) {
		t.Fatalf("expected ErrPaymentAlreadyTerminal, got %v", err)
	}
}

func TestPaymentRepository_UpdateStatus_NoRowsPaymentNotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	id := uuid.New()

	mock.ExpectQuery(fmt.Sprintf(updateStatusSQL, `\$5, \$6`)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT .* FROM payments WHERE id = \$1`).WithArgs(id).WillReturnError(sql.ErrNoRows)

	_, err := repo.UpdateStatus(context.Background(), id, payment.StatusPaid, nil)
	if !errors.Is(err, payment.ErrPaymentNotFound) {
		t.Fatalf("expected ErrPaymentNotFound, got %v", err)
	}
}

// A target no status may move to (e.g. back to pending) is refused before
// any SQL is issued.
func TestPaymentRepository_UpdateStatus_InvalidTargetIssuesNoSQL(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)

	_, err := repo.UpdateStatus(context.Background(), uuid.New(), payment.StatusPending, nil)
	if !errors.Is(err, payment.ErrInvalidStatusTransition) {
		t.Fatalf("expected ErrInvalidStatusTransition, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected SQL issued: %v", err)
	}
}
  ```
  Add `"fmt"` to that file's imports if missing.
- [ ] **Step 6: Run** `go test ./internal/repository/ -run UpdateStatus -count=1` → FAIL (signature).
- [ ] **Step 7: Implement repository** — in `internal/repository/payment_repository_update.go` replace the whole `UpdateStatus` function with (add `"database/sql"` and `"strings"` to imports):

  > **Amended during execution (user-approved):** the CTE form below returns NULL for the previous status on PostgreSQL 18 (the `FOR UPDATE` CTE is evaluated lazily, after the row was already modified). The implemented statement is the join form `UPDATE payments p SET … FROM (SELECT id, status FROM payments WHERE id = $4 FOR UPDATE) prev WHERE p.id = prev.id AND p.status IN (…) RETURNING prev.status`, verified by the opt-in real-PostgreSQL tests in `internal/repository/payment_repository_pg_test.go` (run with `TEST_DATABASE_URL`).
  ```go
// UpdateStatus moves a payment to newStatus in one guarded statement and
// returns the status it had just before. The WHERE clause only matches the
// statuses newStatus may come from (payment.AllowedSourceStatuses), so of
// several concurrent callers — webhook, reconcile, expire job — exactly one
// wins and the rest match no row; only the winner should notify the
// merchant. The CTE locks the row and captures the old status for the
// winner (e.g. to recognise a late expired → paid settlement).
func (r *PaymentRepository) UpdateStatus(ctx context.Context, id uuid.UUID, newStatus string, paidAt *time.Time) (string, error) {
	from := payment.AllowedSourceStatuses(newStatus)
	if len(from) == 0 {
		return "", payment.ErrInvalidStatusTransition
	}

	args := []interface{}{newStatus, paidAt, time.Now(), id}
	placeholders := make([]string, len(from))
	for i, s := range from {
		args = append(args, s)
		placeholders[i] = fmt.Sprintf("$%d", len(args))
	}

	query := `
		WITH prev AS (SELECT status FROM payments WHERE id = $4 FOR UPDATE)
		UPDATE payments SET status = $1, paid_at = $2, updated_at = $3
		WHERE id = $4 AND status IN (` + strings.Join(placeholders, ", ") + `)
		RETURNING (SELECT status FROM prev)
	`

	var previous string
	err := r.db.QueryRowContext(ctx, query, args...).Scan(&previous)
	if err == nil {
		return previous, nil
	}
	if err != sql.ErrNoRows {
		return "", fmt.Errorf("failed to update payment status: %w", err)
	}

	current, getErr := r.GetByID(ctx, id)
	if getErr != nil {
		return "", getErr
	}
	if payment.IsTerminalStatus(current.Status) {
		return "", payment.ErrPaymentAlreadyTerminal
	}
	return "", payment.ErrInvalidStatusTransition
}
  ```
- [ ] **Step 8: Update the service interface** — `internal/service/interfaces.go`: change
  `UpdateStatus(ctx context.Context, id uuid.UUID, newStatus string, paidAt *time.Time) error`
  to
  `UpdateStatus(ctx context.Context, id uuid.UUID, newStatus string, paidAt *time.Time) (string, error)`.
- [ ] **Step 9: Update callers (behavior unchanged)** — every `s.paymentRepo.UpdateStatus(` call in `internal/service/payment_service.go` (4 calls) and `internal/service/payment_webhook_service.go` (2 calls) becomes `_, err := …` / `_, updErr := …` in the same `if` form, e.g.
  `if err := s.paymentRepo.UpdateStatus(ctx, p.ID, payment.StatusExpired, nil); err != nil {` →
  `if _, err := s.paymentRepo.UpdateStatus(ctx, p.ID, payment.StatusExpired, nil); err != nil {`.
- [ ] **Step 10: Update the three fakes** — replace `UpdateStatus` in `internal/service/payment_fakes_test.go`, `internal/handler/fakes_test.go`, and `cmd/api/router_test.go` with (same body in all three; each already has `mu`/`byID`):
  ```go
// UpdateStatus mirrors the real repository's guarded update: only allowed
// source statuses move, and a refused move is explained the same way.
func (f *fakePaymentRepo) UpdateStatus(ctx context.Context, id uuid.UUID, newStatus string, paidAt *time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.byID[id]
	if !ok {
		return "", payment.ErrPaymentNotFound
	}
	if !payment.CanTransitionTo(p.Status, newStatus) {
		if payment.IsTerminalStatus(p.Status) {
			return "", payment.ErrPaymentAlreadyTerminal
		}
		return "", payment.ErrInvalidStatusTransition
	}
	previous := p.Status
	p.Status = newStatus
	p.PaidAt = paidAt
	return previous, nil
}
  ```
  (Keep the existing doc comment above the service fake struct.)
- [ ] **Step 11: Verify** — `gofmt -l ./cmd ./internal && go vet ./... && go test ./... -race -count=1` → all pass.
- [ ] **Step 12: Report** changed files (no commit).

---

### Task 2: `ParseEnvironment` on create paths

**Files:** `internal/domain/payment/{payment.go,errors.go,payment_test.go}`, `internal/domain/paymentlink/{paymentlink.go,paymentlink_test.go}`, `internal/handler/{admin_handler.go,payment_link_handler.go,merchant_handler.go}`, `internal/service/payment_service.go`

**Interfaces:** Produces `func payment.ParseEnvironment(raw string) (string, error)`; `payment.ErrInvalidEnvironment`.

- [ ] **Step 1: Failing tests** — append to `internal/domain/payment/payment_test.go`:
  ```go
func TestParseEnvironment(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", EnvironmentSandbox, false},
		{"   ", EnvironmentSandbox, false},
		{"sandbox", EnvironmentSandbox, false},
		{"test", EnvironmentSandbox, false},
		{"dev", EnvironmentSandbox, false},
		{"development", EnvironmentSandbox, false},
		{"production", EnvironmentProduction, false},
		{"prod", EnvironmentProduction, false},
		{"live", EnvironmentProduction, false},
		{" Production ", EnvironmentProduction, false},
		{"prodution", "", true},
		{"garbage", "", true},
	}
	for _, tc := range cases {
		got, err := ParseEnvironment(tc.in)
		if tc.wantErr {
			if !errors.Is(err, ErrInvalidEnvironment) {
				t.Errorf("ParseEnvironment(%q) error = %v, want ErrInvalidEnvironment", tc.in, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ParseEnvironment(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestCreatePaymentRequest_ValidateEnvironment(t *testing.T) {
	base := func() *CreatePaymentRequest {
		return &CreatePaymentRequest{MerchantID: uuid.New(), Amount: 10000, PaymentMethod: PaymentMethodQRIS, Description: "x"}
	}
	r := base()
	if err := r.Validate(); err != nil || r.Environment != EnvironmentSandbox {
		t.Fatalf("empty environment: Validate() = %v, env %q; want nil, sandbox", err, r.Environment)
	}
	r = base()
	r.Environment = "nope"
	if err := r.Validate(); !errors.Is(err, ErrInvalidEnvironment) {
		t.Fatalf("unknown environment: Validate() = %v, want ErrInvalidEnvironment", err)
	}
}
  ```
  (Add `"errors"` and `"github.com/google/uuid"` to the test imports if missing.)
  In `internal/domain/paymentlink/paymentlink_test.go`, add a test that a valid open-link request with `Environment: ""` validates to `payment.EnvironmentSandbox` and one with `Environment: "nope"` returns `payment.ErrInvalidEnvironment` — build the request the same way the existing environment test in that file does (search for `req.Environment = "test"` and copy its request construction).
- [ ] **Step 2: Run** `go test ./internal/domain/... -count=1` → FAIL.
- [ ] **Step 3: Implement**
  - `internal/domain/payment/errors.go`: add `ErrInvalidEnvironment = errors.New("invalid environment: use sandbox or production")`.
  - `internal/domain/payment/payment.go`: add `"strings"` import and, right after `NormalizeEnvironment`:
  ```go
// ParseEnvironment resolves the environment for anything being created
// (payments, payment links). An empty value means sandbox: a missing or
// mistyped environment must never silently charge real money through the
// production provider. Unknown values are rejected rather than guessed.
// NormalizeEnvironment stays for read/filter paths, whose existing
// "empty = production" behavior dashboards rely on.
func ParseEnvironment(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", EnvironmentSandbox, "test", "dev", "development":
		return EnvironmentSandbox, nil
	case EnvironmentProduction, "prod", "live":
		return EnvironmentProduction, nil
	default:
		return "", ErrInvalidEnvironment
	}
}
  ```
  - In `CreatePaymentRequest.Validate`, replace `r.Environment = NormalizeEnvironment(r.Environment)` with:
  ```go
	env, err := ParseEnvironment(r.Environment)
	if err != nil {
		return err
	}
	r.Environment = env
  ```
  - In `internal/domain/paymentlink/paymentlink.go` `Validate`, replace `r.Environment = payment.NormalizeEnvironment(r.Environment)` with the same block using `payment.ParseEnvironment`.
  - `internal/service/payment_service.go` `CreatePayment`: replace `env := payment.NormalizeEnvironment(req.Environment)` with `env := req.Environment // already resolved by req.Validate()`.
  - `internal/handler/merchant_handler.go` `CreatePayment`: delete the line `req.Environment = payment.NormalizeEnvironment(req.Environment)` (Validate resolves it; keep the "Prefer body environment, else query" block above it).
  - `internal/handler/admin_handler.go` `CreatePayment`: add `payment.ErrInvalidEnvironment,` to the 400 case list.
  - `internal/handler/payment_link_handler.go` `respondPaymentLinkValidationError`: add `payment.ErrInvalidEnvironment,` to the 400 case list.
- [ ] **Step 4: Verify** — gofmt/vet/full `-race` suite pass. (Existing tests set `Environment` explicitly; if any test fails because it relied on empty → production, set `Environment: payment.EnvironmentProduction` in that test and note it.)
- [ ] **Step 5: Report** (no commit).

---

### Task 3: Production routing safety + provider-config validation

**Files:** `internal/domain/provider/{provider.go,errors.go}`, `internal/service/payment_service.go`, `internal/service/payment_service_test.go`, `internal/service/admin_merchant_service.go`, `internal/service/admin_merchant_service_test.go`, `internal/handler/admin_handler.go`, `cmd/api/main.go`

**Interfaces:** Produces `domainProvider.ProviderSandbox = "sandbox"`; errors `ErrProviderConfigSandboxNotAllowed`, `ErrProviderConfigUnknownProvider`, `ErrProviderConfigUnsupportedPaymentMethod`; `(*AdminMerchantService).WithProviderRegistry(r providerRegistry) *AdminMerchantService` where `providerRegistry interface{ GetProvider(name string) (providerPkg.PaymentProvider, bool) }`.

- [ ] **Step 1: Failing service tests** — append to `internal/service/payment_service_test.go`:
  ```go
// A merchant config that only lists the sandbox mock must fail cleanly:
// sandbox is never a production route, and an empty candidate list must
// not reach the nil provider-response dereference.
func TestCreatePayment_Production_SandboxOnlyMerchantConfig_NotAvailable(t *testing.T) {
	svc, _, configRepo, router := newTestPaymentService()
	router.RegisterProvider(&fakeProvider{name: "sandbox"})
	merchantID := uuid.New()
	configRepo.configs = []*domainProvider.MerchantProviderConfig{
		{MerchantID: merchantID, ProviderName: "sandbox", PaymentMethod: "qris", IsEnabled: true, FailoverEnabled: true},
	}
	req := baseCreateRequest(merchantID)
	req.Environment = payment.EnvironmentProduction

	_, err := svc.CreatePayment(context.Background(), req)
	if !errors.Is(err, providerPkg.ErrProviderNotAvailable) {
		t.Fatalf("expected ErrProviderNotAvailable, got %v", err)
	}
}

func TestCreatePayment_Production_SandboxOnlyDefaultRoute_NotAvailable(t *testing.T) {
	svc, _, _, router := newTestPaymentService()
	router.RegisterProvider(&fakeProvider{name: "sandbox"})
	router.RegisterPaymentMethodProvider("qris", "sandbox")
	req := baseCreateRequest(uuid.New())
	req.Environment = payment.EnvironmentProduction

	_, err := svc.CreatePayment(context.Background(), req)
	if !errors.Is(err, providerPkg.ErrProviderNotAvailable) {
		t.Fatalf("expected ErrProviderNotAvailable, got %v", err)
	}
}
  ```
  (Add `"errors"` to imports.) Append to `internal/service/admin_merchant_service_test.go` (uses the existing `newAdminMerchantServiceWithMock` helper; each case expects only the merchant lookup query and no INSERT):
  ```go
func TestAdminMerchantService_UpsertMerchantProviderConfig_RejectsInvalid(t *testing.T) {
	cases := []struct {
		name    string
		req     provider.UpsertMerchantProviderConfigRequest
		wantErr error
	}{
		{"sandbox provider", provider.UpsertMerchantProviderConfigRequest{ProviderName: "sandbox", PaymentMethod: "qris"}, provider.ErrProviderConfigSandboxNotAllowed},
		{"unknown provider", provider.UpsertMerchantProviderConfigRequest{ProviderName: "xendit", PaymentMethod: "qris"}, provider.ErrProviderConfigUnknownProvider},
		{"unsupported method", provider.UpsertMerchantProviderConfigRequest{ProviderName: "cashi", PaymentMethod: "va"}, provider.ErrProviderConfigUnsupportedPaymentMethod},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, mock := newAdminMerchantServiceWithMock(t)
			router := providerPkg.NewProviderRouter()
			router.RegisterProvider(&fakeProvider{name: "cashi"})
			svc = svc.WithProviderRegistry(router)
			now := time.Now()
			id := uuid.New()
			mock.ExpectQuery(`SELECT .* FROM merchants\s+WHERE id = \$1`).
				WithArgs(id).
				WillReturnRows(sqlmock.NewRows([]string{
					"id", "name", "email", "phone", "business_name", "webhook_url", "webhook_secret", "is_active", "created_at", "updated_at",
				}).AddRow(id, "Acme", "a@example.com", "", "Acme Biz", nil, nil, true, now, now))

			req := tc.req
			_, err := svc.UpsertMerchantProviderConfig(context.Background(), id, &req)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unexpected SQL: %v", err)
			}
		})
	}
}
  ```
  (Add imports `"errors"` and `providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"` if missing.)
- [ ] **Step 2: Run** `go test ./internal/service/ -run 'SandboxOnly|RejectsInvalid' -count=1` → FAIL.
- [ ] **Step 3: Implement**
  - `internal/domain/provider/provider.go`: add `ProviderSandbox = "sandbox"` to the first `const` block.
  - `internal/domain/provider/errors.go`: add
  ```go
	ErrProviderConfigSandboxNotAllowed        = errors.New("the sandbox provider cannot be configured for production routing")
	ErrProviderConfigUnknownProvider          = errors.New("unknown provider")
	ErrProviderConfigUnsupportedPaymentMethod = errors.New("unsupported payment method")
  ```
  - `internal/service/payment_service.go` `CreatePayment` production branch: delete the block
  ```go
			// Never route production traffic to sandbox mock
			if selectedProvider.GetName() == "sandbox" {
				continue
			}
  ```
  and replace
  ```go
		if lastErr != nil && providerResp == nil {
			return nil, fmt.Errorf("failed to create payment with available providers: %w", lastErr)
		}
  ```
  with
  ```go
		if providerResp == nil {
			// Every candidate failed (or failover stopped after the first
			// error). Never fall through to the nil response below.
			if lastErr == nil {
				lastErr = providerPkg.ErrProviderNotAvailable
			}
			return nil, fmt.Errorf("failed to create payment with available providers: %w", lastErr)
		}
  ```
  - In `resolveProvidersForMerchant`: in the default-routing branch build candidates skipping `defaultProvider.GetName() == provider.ProviderSandbox` (comment: never route production traffic to the sandbox mock) and return `providerPkg.ErrProviderNotAvailable` when none remain; in the merchant-config loop, before the registered check, add
  ```go
		if strings.EqualFold(merchantConfig.ProviderName, provider.ProviderSandbox) {
			logger.WarnfCtx(ctx, "Skipping sandbox provider in production config for merchant %s", merchantConfig.MerchantID)
			continue
		}
  ```
  - `internal/service/admin_merchant_service.go`: add import `providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"` and `"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"` (if missing); add field `providerRegistry providerRegistry` to the struct and:
  ```go
// providerRegistry is the slice of *provider.ProviderRouter needed to check
// that a merchant provider config names a provider this API can route to.
type providerRegistry interface {
	GetProvider(name string) (providerPkg.PaymentProvider, bool)
}

func (s *AdminMerchantService) WithProviderRegistry(r providerRegistry) *AdminMerchantService {
	s.providerRegistry = r
	return s
}

// validateProviderConfig rejects configs CreatePayment could never use: the
// sandbox mock (never a production route), providers this API doesn't
// register, and payment methods it doesn't support.
func (s *AdminMerchantService) validateProviderConfig(req *provider.UpsertMerchantProviderConfigRequest) error {
	req.ProviderName = strings.ToLower(strings.TrimSpace(req.ProviderName))
	req.PaymentMethod = strings.ToLower(strings.TrimSpace(req.PaymentMethod))
	if req.ProviderName == provider.ProviderSandbox {
		return provider.ErrProviderConfigSandboxNotAllowed
	}
	if req.PaymentMethod != payment.PaymentMethodQRIS {
		return provider.ErrProviderConfigUnsupportedPaymentMethod
	}
	if s.providerRegistry != nil {
		if _, ok := s.providerRegistry.GetProvider(req.ProviderName); !ok {
			return provider.ErrProviderConfigUnknownProvider
		}
	}
	return nil
}
  ```
  and in `UpsertMerchantProviderConfig`, right after `req.Validate()` succeeds: `if err := s.validateProviderConfig(req); err != nil { return nil, err }`.
  - `internal/handler/admin_handler.go` `UpsertMerchantProviderConfig`: add the three new errors to the 400 case.
  - `cmd/api/main.go`: `adminMerchantService := service.NewAdminMerchantService(merchantRepo, merchantProviderConfigRepo, adminPaymentService).WithProviderRegistry(providerRouter)`.
- [ ] **Step 4: Verify** — gofmt/vet/full `-race` suite pass.
- [ ] **Step 5: Report** (no commit).

---

### Task 4: Provider status amount + Cashi settlement timestamp

**Files:** `internal/domain/provider/provider.go`, `internal/provider/cashi/{client.go,types.go,client_test.go}`, `internal/provider/sandbox/{adapter.go,adapter_test.go}`

**Interfaces:** Produces `NormalizedPaymentStatus.Amount *int64` (provider-reported gross amount; nil when unknown). Cashi `ParseWebhook` sets `PaidAt` from `data.timestamp` (RFC3339) when valid, else receipt time.

- [ ] **Step 1: Failing tests**
  - `internal/provider/cashi/client_test.go`, in `TestGetPaymentStatus` "success maps normalized status" subtest, add after the status assertion:
  ```go
		if status.Amount == nil || *status.Amount != 50078 {
			t.Errorf("expected amount 50078, got %v", status.Amount)
		}
  ```
  and in `TestParseWebhook` add subtests:
  ```go
	t.Run("settlement timestamp becomes PaidAt", func(t *testing.T) {
		payload := []byte(`{"event":"PAYMENT_SETTLED","data":{"order_id":"INV-3","status":"SETTLED","amount":2000,"timestamp":"2025-12-25T19:46:40Z"}}`)
		got, err := adapter.ParseWebhook(payload)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := time.Date(2025, 12, 25, 19, 46, 40, 0, time.UTC)
		if got.PaidAt == nil || !got.PaidAt.Equal(want) {
			t.Errorf("PaidAt = %v, want %v", got.PaidAt, want)
		}
	})

	t.Run("invalid timestamp falls back to receipt time", func(t *testing.T) {
		before := time.Now()
		payload := []byte(`{"event":"PAYMENT_SETTLED","data":{"order_id":"INV-4","status":"SETTLED","timestamp":"yesterday"}}`)
		got, err := adapter.ParseWebhook(payload)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.PaidAt == nil || got.PaidAt.Before(before) {
			t.Errorf("expected PaidAt to fall back to now, got %v", got.PaidAt)
		}
	})
  ```
  (Add `"time"` import if missing.)
  - `internal/provider/sandbox/adapter_test.go` `TestMarkPaid`, after the `paid` assertion: `if status.Amount == nil || *status.Amount != resp.Amount { t.Errorf("expected amount %d, got %v", resp.Amount, status.Amount) }`.
- [ ] **Step 2: Run** `go test ./internal/provider/... -count=1` → FAIL.
- [ ] **Step 3: Implement**
  - `internal/domain/provider/provider.go` `NormalizedPaymentStatus`: add
  ```go
	// Amount is the gross amount the provider reports for the payment
	// (what the customer paid, before any provider fee); nil if unknown.
	Amount *int64
  ```
  - `internal/provider/cashi/types.go` `WebhookEventData`: add `Timestamp string `json:"timestamp,omitempty"``.
  - `internal/provider/cashi/client.go` `GetPaymentStatus`: return
  ```go
	amount := checkResp.Amount
	return &provider.NormalizedPaymentStatus{
		Status:            a.NormalizeStatus(checkResp.Status),
		ProviderReference: checkResp.OrderID,
		Amount:            &amount,
	}, nil
  ```
  - `ParseWebhook`: replace `now := time.Now(); paidAt = &now` with `paidAt = settledAt(webhook.Data.Timestamp)` and add:
  ```go
// settledAt prefers Cashi's own settlement timestamp (RFC3339, e.g.
// "2025-12-25T19:46:40Z") so paid_at reflects when the customer paid, not
// when the webhook happened to arrive; it falls back to receipt time.
func settledAt(ts string) *time.Time {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return &t
	}
	now := time.Now()
	return &now
}
  ```
  - `internal/provider/sandbox/adapter.go` `GetPaymentStatus`: for a known order return `Amount: &amount` where `amount := st.Amount` (copied while holding the read lock); unknown refs keep `Amount` nil.
- [ ] **Step 4: Verify** — gofmt/vet/full `-race` suite pass.
- [ ] **Step 5: Report** (no commit).

---

### Task 5: Verified webhook flow + late payments

**Files:** `internal/domain/payment/errors.go`, `internal/service/payment_verify.go` (new), `internal/service/payment_verify_test.go` (new), `internal/service/payment_webhook_service.go`, `internal/service/payment_webhook_service_test.go`, `internal/service/merchant_callback_service.go`, `internal/service/merchant_callback_service_test.go`, `internal/service/payment_service.go`, `internal/service/payment_fakes_test.go`, `internal/handler/webhook_handler.go`, `internal/handler/webhook_handler_test.go`, `internal/handler/fakes_test.go`, `cmd/api/router_test.go`

**Interfaces:**
- Consumes: Task 1 `UpdateStatus(...) (string, error)`; Task 4 `NormalizedPaymentStatus.Amount`.
- Produces: errors `payment.ErrAmountMismatch` (`"amount_mismatch"`), `payment.ErrNotSettledAtProvider`, `payment.ErrProviderVerificationFailed`, `payment.ErrWebhookIgnored`; `func verifyProviderPayment(p *payment.Payment, status *domainProvider.NormalizedPaymentStatus, webhookAmount *int64) error`; `func (s *PaymentService) checkProviderStatus(ctx context.Context, prov providerPkg.PaymentProvider, providerReference string) (*domainProvider.NormalizedPaymentStatus, error)`; constant `providerVerifyTimeout = 5 * time.Second`; `NotifyMerchantPaymentEvent(ctx context.Context, p *payment.Payment, eventType string, latePayment bool)`; test helper `ptrInt64(v int64) *int64` in `payment_fakes_test.go`.

- [ ] **Step 1: Errors** — `internal/domain/payment/errors.go` add:
  ```go
	// ErrAmountMismatch's text is the "amount_mismatch" prefix the reconcile
	// job uses to skip payments already flagged for a human.
	ErrAmountMismatch             = errors.New("amount_mismatch")
	ErrNotSettledAtProvider       = errors.New("provider has not settled the payment")
	ErrProviderVerificationFailed = errors.New("could not confirm the payment with the provider")
	ErrWebhookIgnored             = errors.New("webhook event ignored: not a settlement")
  ```
- [ ] **Step 2: Failing verify tests** — create `internal/service/payment_verify_test.go`:
  ```go
package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
)

func TestVerifyProviderPayment(t *testing.T) {
	p := &payment.Payment{Amount: 2000}
	paid := func(amount *int64) *domainProvider.NormalizedPaymentStatus {
		return &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: amount}
	}
	cases := []struct {
		name          string
		status        *domainProvider.NormalizedPaymentStatus
		webhookAmount *int64
		wantErr       error
		wantMsg       string
	}{
		{"settled and amounts match", paid(ptrInt64(2000)), ptrInt64(2000), nil, ""},
		{"settled, webhook has no amount", paid(ptrInt64(2000)), nil, nil, ""},
		{"not settled", &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPending, Amount: ptrInt64(2000)}, nil, payment.ErrNotSettledAtProvider, ""},
		{"nil status", nil, nil, payment.ErrNotSettledAtProvider, ""},
		{"provider amount differs", paid(ptrInt64(1500)), ptrInt64(1500), payment.ErrAmountMismatch, "amount_mismatch: expected 2000, provider 1500, webhook 1500"},
		{"webhook amount differs", paid(ptrInt64(2000)), ptrInt64(1986), payment.ErrAmountMismatch, "amount_mismatch: expected 2000, provider 2000, webhook 1986"},
		{"provider amount unknown", paid(nil), nil, payment.ErrAmountMismatch, "amount_mismatch: expected 2000, provider none, webhook none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyProviderPayment(p, tc.status, tc.webhookAmount)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantMsg != "" && err.Error() != tc.wantMsg {
				t.Errorf("message = %q, want %q", err.Error(), tc.wantMsg)
			}
			if errors.Is(err, payment.ErrAmountMismatch) && !strings.HasPrefix(err.Error(), "amount_mismatch:") {
				t.Errorf("mismatch message must start with amount_mismatch:, got %q", err.Error())
			}
		})
	}
}
  ```
  Add to `internal/service/payment_fakes_test.go`: `func ptrInt64(v int64) *int64 { return &v }`.
- [ ] **Step 3: Implement verify** — create `internal/service/payment_verify.go`:
  ```go
package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
)

// providerVerifyTimeout bounds every check-status call made to confirm a
// settlement (webhook or reconcile): well under the server's 15s
// WriteTimeout, while the Cashi HTTP client itself allows 30s.
const providerVerifyTimeout = 5 * time.Second

// checkProviderStatus asks the provider for the payment's current status
// within providerVerifyTimeout.
func (s *PaymentService) checkProviderStatus(ctx context.Context, prov providerPkg.PaymentProvider, providerReference string) (*domainProvider.NormalizedPaymentStatus, error) {
	callCtx, cancel := context.WithTimeout(ctx, providerVerifyTimeout)
	defer cancel()
	return prov.GetPaymentStatus(callCtx, providerReference)
}

// verifyProviderPayment decides whether a provider status report is enough to
// mark p paid: the provider must report it settled, and its amount — and the
// webhook's, when the webhook carries one — must equal the gross amount we
// stored. Provider fees (e.g. Cashi's 0.7%) are deducted at settlement and
// never change what the customer paid. An unknown provider amount fails
// closed.
func verifyProviderPayment(p *payment.Payment, status *domainProvider.NormalizedPaymentStatus, webhookAmount *int64) error {
	if status == nil || status.Status != payment.StatusPaid {
		return payment.ErrNotSettledAtProvider
	}
	if status.Amount == nil || *status.Amount != p.Amount || (webhookAmount != nil && *webhookAmount != p.Amount) {
		return fmt.Errorf("%w: expected %d, provider %s, webhook %s",
			payment.ErrAmountMismatch, p.Amount, formatAmount(status.Amount), formatAmount(webhookAmount))
	}
	return nil
}

func formatAmount(v *int64) string {
	if v == nil {
		return "none"
	}
	return strconv.FormatInt(*v, 10)
}
  ```
  Run `go test ./internal/service/ -run TestVerifyProviderPayment -count=1` → PASS.
- [ ] **Step 4: `late_payment` in merchant callbacks** — `internal/service/merchant_callback_service.go`: change the signature to `func (s *PaymentService) NotifyMerchantPaymentEvent(ctx context.Context, p *payment.Payment, eventType string, latePayment bool)`, update its doc comment ("latePayment marks a settlement that arrived after the payment had expired; merchants may already have cancelled the order"), and add `"late_payment": latePayment,` to the `payload` map. Update every caller: in `payment_service.go` (4 calls) add `, false`; `merchant_callback_service_test.go` line ~225 add `, false`.
- [ ] **Step 5: Failing webhook tests** — in `internal/service/payment_webhook_service_test.go`:
  - In `TestProcessWebhook_SuccessUpdatesStatus` and `TestProcessWebhook_SuccessTriggersMerchantCallback`, add to the `fakeProvider` literal: `statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)},`.
  - In `TestProcessWebhook_SuccessTriggersMerchantCallback`, after the event-type assertion add: `if delivery.RequestPayload["late_payment"] != false { t.Errorf("expected late_payment=false, got %v", delivery.RequestPayload["late_payment"]) }`.
  - `TestProcessWebhook_TerminalPaymentRejected`: seed `payment.StatusFailed` instead of `payment.StatusPaid` (still expects `ErrPaymentAlreadyTerminal`).
  - Rename `TestProcessWebhook_DuplicateWebhookRejected` → `TestProcessWebhook_NonSettlementIgnored` and expect `payment.ErrWebhookIgnored`.
  - Rename `TestProcessWebhook_InvalidStatusTransitionRejected` → `TestProcessWebhook_UnknownStatusIgnored` and expect `payment.ErrWebhookIgnored`.
  - Append:
  ```go
func paidWebhook(ref string, amount *int64) *domainProvider.ProviderWebhookPayload {
	return &domainProvider.ProviderWebhookPayload{ProviderReference: ref, Status: payment.StatusPaid, Amount: amount}
}

func lastEvent(repo *fakeWebhookEventRepo) *domainProvider.WebhookEvent {
	var found *domainProvider.WebhookEvent
	for _, e := range repo.events {
		found = e
	}
	return found
}

func TestProcessWebhook_AlreadyPaidIsDuplicate(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	seedPayment(t, paymentRepo, payment.StatusPaid, "REF-PAID")
	router.RegisterProvider(&fakeProvider{name: "cashi", parseResp: paidWebhook("REF-PAID", nil)})

	if err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig"); err != payment.ErrDuplicateWebhook {
		t.Fatalf("expected ErrDuplicateWebhook, got %v", err)
	}
}

func TestProcessWebhook_ProviderAmountMismatchLeavesPending(t *testing.T) {
	svc, paymentRepo, webhookRepo, router := newWebhookTestService()
	seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-MM")
	router.RegisterProvider(&fakeProvider{name: "cashi",
		parseResp:  paidWebhook("REF-MM", ptrInt64(1500)),
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(1500)},
	})

	err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
	if !errors.Is(err, payment.ErrAmountMismatch) {
		t.Fatalf("expected ErrAmountMismatch, got %v", err)
	}
	got, _ := paymentRepo.GetByID(context.Background(), seeded.ID)
	if got.Status != payment.StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	e := lastEvent(webhookRepo)
	if e == nil || e.ProcessingError == nil || !strings.HasPrefix(*e.ProcessingError, "amount_mismatch:") {
		t.Fatalf("expected event flagged amount_mismatch, got %+v", e)
	}
}

func TestProcessWebhook_WebhookAmountMismatchRejected(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-WMM")
	router.RegisterProvider(&fakeProvider{name: "cashi",
		parseResp:  paidWebhook("REF-WMM", ptrInt64(1)),
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)},
	})

	if err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig"); !errors.Is(err, payment.ErrAmountMismatch) {
		t.Fatalf("expected ErrAmountMismatch, got %v", err)
	}
	got, _ := paymentRepo.GetByID(context.Background(), seeded.ID)
	if got.Status != payment.StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
}

func TestProcessWebhook_VerificationFailures(t *testing.T) {
	cases := map[string]*fakeProvider{
		"provider call fails": {name: "cashi", parseResp: paidWebhook("REF-VF", nil), statusErr: providerPkg.ErrProviderAPIError},
		"provider not settled": {name: "cashi", parseResp: paidWebhook("REF-VF", nil),
			statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPending, Amount: ptrInt64(15000)}},
	}
	for name, prov := range cases {
		t.Run(name, func(t *testing.T) {
			svc, paymentRepo, webhookRepo, router := newWebhookTestService()
			seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-VF")
			router.RegisterProvider(prov)

			err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
			if !errors.Is(err, payment.ErrProviderVerificationFailed) {
				t.Fatalf("expected ErrProviderVerificationFailed, got %v", err)
			}
			got, _ := paymentRepo.GetByID(context.Background(), seeded.ID)
			if got.Status != payment.StatusPending {
				t.Errorf("status = %q, want pending", got.Status)
			}
			if e := lastEvent(webhookRepo); e == nil || e.Status != "verification_failed" {
				t.Errorf("expected event status verification_failed, got %+v", e)
			}
		})
	}
}

func TestProcessWebhook_ExpiredThenSettledIsLatePayment(t *testing.T) {
	svc, paymentRepo, webhookRepo, router := newWebhookTestService()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	seeded := seedPayment(t, paymentRepo, payment.StatusExpired, "REF-LATE")
	merchantRepo := newFakeMerchantRepo()
	webhookURL := server.URL
	merchantRepo.byID[seeded.MerchantID] = &merchant.Merchant{ID: seeded.MerchantID, WebhookURL: &webhookURL}
	callbackRepo := newFakeMerchantCallbackRepo()
	svc = svc.WithMerchantCallbackDeps(merchantRepo, callbackRepo)
	settled := time.Date(2026, 10, 30, 6, 30, 14, 0, time.UTC)
	router.RegisterProvider(&fakeProvider{name: "cashi",
		parseResp:  &domainProvider.ProviderWebhookPayload{ProviderReference: "REF-LATE", Status: payment.StatusPaid, PaidAt: &settled},
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)},
	})

	if err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := paymentRepo.GetByID(context.Background(), seeded.ID)
	if got.Status != payment.StatusPaid || got.PaidAt == nil || !got.PaidAt.Equal(settled) {
		t.Fatalf("expected paid at %v, got %q at %v", settled, got.Status, got.PaidAt)
	}
	if e := lastEvent(webhookRepo); e == nil || e.EventType != "payment.paid.late" || !e.IsProcessed {
		t.Errorf("expected processed payment.paid.late event, got %+v", e)
	}
	if len(callbackRepo.created) != 1 || callbackRepo.created[0].EventType != "payment.paid" ||
		callbackRepo.created[0].RequestPayload["late_payment"] != true {
		t.Fatalf("expected one payment.paid callback with late_payment=true, got %+v", callbackRepo.created)
	}
}

// Two deliveries of the same settlement racing each other: exactly one
// wins the guarded update, so the merchant hears about it exactly once.
func TestProcessWebhook_ConcurrentDuplicatesNotifyOnce(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	seeded := seedPayment(t, paymentRepo, payment.StatusPending, "REF-RACE")
	merchantRepo := newFakeMerchantRepo()
	webhookURL := server.URL
	merchantRepo.byID[seeded.MerchantID] = &merchant.Merchant{ID: seeded.MerchantID, WebhookURL: &webhookURL}
	callbackRepo := newFakeMerchantCallbackRepo()
	svc = svc.WithMerchantCallbackDeps(merchantRepo, callbackRepo)
	router.RegisterProvider(&fakeProvider{name: "cashi",
		parseResp:  paidWebhook("REF-RACE", nil),
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)},
	})

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
		}(i)
	}
	wg.Wait()

	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else if err != payment.ErrDuplicateWebhook {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one webhook to win, got %d", successes)
	}
	callbackRepo.mu.Lock()
	defer callbackRepo.mu.Unlock()
	if len(callbackRepo.created) != 1 {
		t.Fatalf("expected exactly one merchant callback, got %d", len(callbackRepo.created))
	}
}
  ```
  Add imports `"strings"`, `"sync"` to that test file. In `payment_fakes_test.go`, make `fakeMerchantRepo` race-safe for the concurrent test: add `mu sync.Mutex` to the struct and lock it at the top of `GetByID` and `SetWebhookSecret` (`f.mu.Lock(); defer f.mu.Unlock()`).
- [ ] **Step 6: Run** `go test ./internal/service/ -run 'ProcessWebhook' -count=1` → FAIL.
- [ ] **Step 7: Implement `ProcessWebhook`** — in `internal/service/payment_webhook_service.go`:
  - Add `const webhookEventTypePaidLate = "payment.paid.late"`.
  - Replace everything in `ProcessWebhook` after the line
    `logger.InfofCtx(ctx, "Webhook parsed: provider_reference=%s, status=%s", webhookPayload.ProviderReference, webhookPayload.Status)`
    and after the existing `p, err := s.paymentRepo.GetByProviderReference(...)` error block (keep both), i.e. from `if payment.IsTerminalStatus(p.Status) {` to the end of the function, with:
  ```go
	eventType := webhookEventType(webhookPayload.Status)

	// Only a provider-reported settlement can move a payment; any other event
	// is acknowledged and ignored.
	if webhookPayload.Status != payment.StatusPaid {
		s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, eventType, webhookPayload.Status, payment.ErrWebhookIgnored)
		return payment.ErrWebhookIgnored
	}

	switch p.Status {
	case payment.StatusPaid:
		logger.InfofCtx(ctx, "Payment %s already paid (duplicate webhook)", p.Reference)
		s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, eventType, webhookPayload.Status, payment.ErrDuplicateWebhook)
		return payment.ErrDuplicateWebhook
	case payment.StatusFailed, payment.StatusCancelled:
		logger.WarnfCtx(ctx, "Payment %s already in terminal status: %s", p.Reference, p.Status)
		s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, eventType, webhookPayload.Status, payment.ErrPaymentAlreadyTerminal)
		return payment.ErrPaymentAlreadyTerminal
	}

	// The webhook is only a trigger. Confirm the settlement and its amount
	// with the provider's own status API before trusting it, so a forged or
	// replayed webhook (even one signed with a leaked secret) can't mark a
	// payment paid.
	providerStatus, verifyErr := s.checkProviderStatus(ctx, selectedProvider, webhookPayload.ProviderReference)
	if verifyErr == nil {
		verifyErr = verifyProviderPayment(p, providerStatus, webhookPayload.Amount)
	}
	if verifyErr != nil {
		if errors.Is(verifyErr, payment.ErrAmountMismatch) {
			// Needs a human, not a retry: acknowledge and flag it.
			logger.ErrorfCtx(ctx, "Payment %s: %v", p.Reference, verifyErr)
			s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, eventType, "rejected", verifyErr)
			return verifyErr
		}
		logger.WarnfCtx(ctx, "Payment %s: settlement not confirmed by provider: %v", p.Reference, verifyErr)
		s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, eventType, "verification_failed", verifyErr)
		return fmt.Errorf("%w: %v", payment.ErrProviderVerificationFailed, verifyErr)
	}

	paidAt := webhookPayload.PaidAt
	if paidAt == nil {
		now := time.Now()
		paidAt = &now
	}

	previous, err := s.paymentRepo.UpdateStatus(ctx, p.ID, payment.StatusPaid, paidAt)
	if err != nil {
		if errors.Is(err, payment.ErrPaymentAlreadyTerminal) {
			// Lost the race to a concurrent webhook or reconcile run that
			// already settled it; that caller notified the merchant.
			s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, eventType, webhookPayload.Status, payment.ErrDuplicateWebhook)
			return payment.ErrDuplicateWebhook
		}
		logger.ErrorfCtx(ctx, "Failed to update payment status: %v", err)
		s.failWebhookEvent(ctx, event.ID, &p.ID, webhookPayload.ProviderReference, eventType, webhookPayload.Status, err)
		return err
	}

	late := previous == payment.StatusExpired
	if late {
		eventType = webhookEventTypePaidLate
		logger.WarnfCtx(ctx, "Payment %s settled after it had expired (late payment)", p.Reference)
	}
	p.Status = payment.StatusPaid
	p.PaidAt = paidAt

	s.completeWebhookEvent(ctx, event.ID, p.ID, webhookPayload.ProviderReference, eventType, payment.StatusPaid)

	// Best-effort merchant notify (failures are recorded, not returned to provider).
	s.NotifyMerchantPaymentEvent(ctx, p, eventTypeForStatus(payment.StatusPaid), late)

	logger.InfofCtx(ctx, "Webhook processed successfully for payment: %s", p.Reference)
	return nil
}
  ```
  - Replace `completeWebhookEvent` with:
  ```go
func (s *PaymentService) completeWebhookEvent(ctx context.Context, eventID, paymentID uuid.UUID, providerReference, eventType, status string) {
	pid := paymentID
	if err := s.webhookEventRepo.Finalize(ctx, eventID, &pid, providerReference, eventType, status, true, nil); err != nil {
		logger.ErrorfCtx(ctx, "Failed to finalize webhook event %s: %v", eventID, err)
	}
}
  ```
- [ ] **Step 8: Handler mapping** — `internal/handler/webhook_handler.go`: replace the `if err := h.paymentService.ProcessWebhook(...); err != nil { ... }` block with:
  ```go
	if err := h.paymentService.ProcessWebhook(r.Context(), providerName, rawPayload, signature); err != nil {
		switch {
		case errors.Is(err, payment.ErrDuplicateWebhook),
			errors.Is(err, payment.ErrPaymentAlreadyTerminal),
			errors.Is(err, payment.ErrWebhookIgnored),
			errors.Is(err, payment.ErrAmountMismatch):
			// Acknowledge so the provider doesn't keep retrying: these are
			// duplicates, non-settlements, or a mismatch flagged for a human.
			logger.InfofCtx(r.Context(), "Webhook acknowledged without status change: %v", err)
			respondJSON(w, http.StatusOK, map[string]string{
				"status":  "ignored",
				"message": "Webhook acknowledged without a status change",
			})
		case errors.Is(err, payment.ErrProviderVerificationFailed):
			// The provider couldn't confirm the settlement yet: ask it to
			// retry. The reconcile job is the fallback if it never does.
			logger.WarnfCtx(r.Context(), "Webhook not confirmed by provider: %v", err)
			respondError(w, http.StatusBadGateway, "Payment not yet confirmed by provider; retry later")
		default:
			logger.ErrorfCtx(r.Context(), "Failed to process webhook: %v", err)
			respondError(w, http.StatusBadRequest, "Webhook processing failed")
		}
		return
	}
  ```
- [ ] **Step 9: Handler & router fakes/tests**
  - `internal/handler/fakes_test.go` `fakeProvider`: add fields `statusResp *domainProvider.NormalizedPaymentStatus` and `statusErr error`; `GetPaymentStatus` returns `nil, f.statusErr` if set, a copy of `*f.statusResp` if set, else the current pending default.
  - `internal/handler/webhook_handler_test.go` `TestWebhookHandler_HandleProviderWebhook_Success`: add `statusResp: &domainProvider.NormalizedPaymentStatus{Status: "paid", Amount: &amount},` to `fp` with `amount := int64(50000)` declared before it. Append:
  ```go
func TestWebhookHandler_HandleProviderWebhook_VerificationFailedIs502(t *testing.T) {
	providerRef := "PROV-REF-502"
	fp := &fakeProvider{
		name:         "testprov",
		parsePayload: &domainProvider.ProviderWebhookPayload{ProviderName: "testprov", ProviderReference: providerRef, Status: "paid"},
		statusErr:    errors.New("provider down"),
	}
	svc, paymentRepo := newTestWebhookService(fp)
	p := seedPendingPayment(t, paymentRepo, "testprov", providerRef)

	h := NewWebhookHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/provider-webhooks/testprov", strings.NewReader(`{}`))
	req = mux.SetURLVars(req, map[string]string{"providerName": "testprov"})
	rec := httptest.NewRecorder()
	h.HandleProviderWebhook(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := paymentRepo.GetByID(context.Background(), p.ID)
	if got.Status != payment.StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
}
  ```
  - `cmd/api/router_test.go` `stubProvider`: add field `settledAmount int64`; `GetPaymentStatus` returns `&domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, ProviderReference: providerReference, Amount: &amt}` (with `amt := p.settledAmount`) when `p.settledAmount > 0`, else the current pending value. In `TestRouter_CreatePayment_Webhook_StatusPaid_EndToEnd` set `settledAmount: 75000` on `stub`.
- [ ] **Step 10: Verify** — gofmt/vet/full `-race` suite pass.
- [ ] **Step 11: Report** (no commit).

---

### Task 6: Reconcile job + verified reconcile + expire notifications

**Files:** `internal/repository/payment_repository.go`, `internal/repository/payment_repository_test.go`, `internal/service/interfaces.go`, `internal/service/payment_service.go`, `internal/service/payment_webhook_service.go`, `internal/service/payment_reconcile_test.go`, `internal/service/payment_webhook_service_test.go`, `internal/service/payment_fakes_test.go`, `internal/handler/fakes_test.go`, `cmd/api/router_test.go`, `cmd/api/main.go`

**Interfaces:**
- Consumes: Tasks 1, 4, 5 (`UpdateStatus` returning previous, `verifyProviderPayment`, `checkProviderStatus`, `NotifyMerchantPaymentEvent(..., latePayment bool)`, `ptrInt64`).
- Produces: `(*repository.PaymentRepository).ListReconcileCandidates(ctx context.Context, now time.Time, window time.Duration, limit int) ([]*payment.Payment, error)` (also on the service `paymentRepository` interface); `(*PaymentService).ReconcileRecentPayments(ctx context.Context) error`; exported `service.ReconcileInterval = 2 * time.Minute`.

- [ ] **Step 1: Failing repository test** — append to `internal/repository/payment_repository_test.go`:
  ```go
func TestPaymentRepository_ListReconcileCandidates(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPaymentRepository(db)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	p := samplePayment()

	mock.ExpectQuery(`(?s)FROM payments p.*p.environment = \$1.*COALESCE\(p.provider_reference, ''\) <> ''.*p.status = \$2 AND p.created_at >= \$3.*p.status = \$4 AND p.expires_at >= \$3.*NOT EXISTS.*webhook_events w.*w.processing_error LIKE 'amount_mismatch%'.*ORDER BY p.created_at DESC\s+LIMIT \$5`).
		WithArgs(payment.EnvironmentProduction, payment.StatusPending, now.Add(-24*time.Hour), payment.StatusExpired, 50).
		WillReturnRows(sqlmock.NewRows(paymentCols).AddRow(paymentRow(p)...))

	got, err := repo.ListReconcileCandidates(context.Background(), now, 24*time.Hour, 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != p.ID {
		t.Fatalf("expected the one candidate, got %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
  ```
- [ ] **Step 2: Implement repository** — in `internal/repository/payment_repository.go` after `ListExpiredPending` add:
  ```go
// ListReconcileCandidates returns production payments the reconcile job
// should re-check with their provider: pending ones created within window,
// and expired ones whose deadline passed within window (a late settlement
// may still arrive). Payments already flagged with an amount mismatch are
// left to a human — re-checking them every run would only repeat the flag.
func (r *PaymentRepository) ListReconcileCandidates(ctx context.Context, now time.Time, window time.Duration, limit int) ([]*payment.Payment, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT ` + paymentSelectCols + `
		FROM payments p
		WHERE p.environment = $1
		  AND COALESCE(p.provider_reference, '') <> ''
		  AND ((p.status = $2 AND p.created_at >= $3) OR (p.status = $4 AND p.expires_at >= $3))
		  AND NOT EXISTS (
		      SELECT 1 FROM webhook_events w
		      WHERE w.payment_id = p.id AND w.processing_error LIKE 'amount_mismatch%'
		  )
		ORDER BY p.created_at DESC
		LIMIT $5
	`
	rows, err := r.db.QueryContext(ctx, query,
		payment.EnvironmentProduction, payment.StatusPending, now.Add(-window), payment.StatusExpired, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list reconcile candidates: %w", err)
	}
	defer rows.Close()

	var payments []*payment.Payment
	for rows.Next() {
		p := &payment.Payment{}
		if err := scanPayment(rows, p); err != nil {
			return nil, fmt.Errorf("failed to scan payment: %w", err)
		}
		payments = append(payments, p)
	}
	return payments, nil
}
  ```
  Add to the service `paymentRepository` interface in `internal/service/interfaces.go`:
  `ListReconcileCandidates(ctx context.Context, now time.Time, window time.Duration, limit int) ([]*payment.Payment, error)`.
  Add to `internal/handler/fakes_test.go` and `cmd/api/router_test.go` fakes:
  ```go
func (f *fakePaymentRepo) ListReconcileCandidates(ctx context.Context, now time.Time, window time.Duration, limit int) ([]*payment.Payment, error) {
	return nil, nil
}
  ```
  Add to `internal/service/payment_fakes_test.go`:
  ```go
// ListReconcileCandidates applies the same environment/status/window rules
// as the real query; the amount-mismatch exclusion is SQL-only and covered
// by the repository test.
func (f *fakePaymentRepo) ListReconcileCandidates(ctx context.Context, now time.Time, window time.Duration, limit int) ([]*payment.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	since := now.Add(-window)
	out := make([]*payment.Payment, 0)
	for _, p := range f.byID {
		if p.Environment != payment.EnvironmentProduction || p.ProviderReference == nil || *p.ProviderReference == "" {
			continue
		}
		pendingRecent := p.Status == payment.StatusPending && !p.CreatedAt.Before(since)
		expiredRecent := p.Status == payment.StatusExpired && !p.ExpiresAt.Before(since)
		if !pendingRecent && !expiredRecent {
			continue
		}
		cp := *p
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
  ```
  (Add `"sort"` import.) In the service fake `seedPayment` helper (in `payment_webhook_service_test.go`) also set `CreatedAt: time.Now()` on the seeded payment.
- [ ] **Step 3: Failing reconcile tests** — in `internal/service/payment_reconcile_test.go`:
  - `TestReconcilePayment_ProviderReportsPaid_UpdatesStatus`: use `statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)}`.
  - Rename `TestReconcilePayment_ProviderReportsInvalidTransition_ReportsError` → `TestReconcilePayment_ProviderReportsUnknownStatus_Unchanged`; expect `result.Action == "unchanged"` and status still pending.
  - Append:
  ```go
func TestReconcilePayment_ExpiredSettledLate_UpdatesToPaid(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusExpired, "REF-RL1")
	router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)}})

	result, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "updated" || !strings.Contains(result.Message, "late payment") {
		t.Fatalf("expected late-payment update, got %q (%s)", result.Action, result.Message)
	}
	got, _ := paymentRepo.GetByID(context.Background(), p.ID)
	if got.Status != payment.StatusPaid {
		t.Errorf("status = %q, want paid", got.Status)
	}
}

func TestReconcilePayment_ExpiredNotSettled_Unchanged(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusExpired, "REF-RL2")
	router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: &domainProvider.NormalizedPaymentStatus{Status: "pending"}})

	result, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "unchanged" {
		t.Fatalf("expected unchanged, got %q", result.Action)
	}
	got, _ := paymentRepo.GetByID(context.Background(), p.ID)
	if got.Status != payment.StatusExpired {
		t.Errorf("status = %q, want expired", got.Status)
	}
}

func TestReconcilePayment_AmountMismatch_FlagsAndLeavesPending(t *testing.T) {
	svc, paymentRepo, webhookRepo, router := newWebhookTestService()
	p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-RMM")
	router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(1)}})

	result, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "error" || !strings.HasPrefix(result.Message, "amount_mismatch:") {
		t.Fatalf("expected amount_mismatch error, got %q (%s)", result.Action, result.Message)
	}
	got, _ := paymentRepo.GetByID(context.Background(), p.ID)
	if got.Status != payment.StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	e := lastEvent(webhookRepo)
	if e == nil || e.EventType != "reconcile.amount_mismatch" || e.PaymentID == nil || *e.PaymentID != p.ID ||
		e.ProcessingError == nil || !strings.HasPrefix(*e.ProcessingError, "amount_mismatch:") {
		t.Fatalf("expected reconcile.amount_mismatch event for the payment, got %+v", e)
	}
}

func TestReconcileRecentPayments_OnlyProductionCandidates(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	prod := seedPayment(t, paymentRepo, payment.StatusPending, "REF-RR1")
	sandbox := seedPayment(t, paymentRepo, payment.StatusPending, "REF-RR2")
	sandbox.Environment = payment.EnvironmentSandbox
	paymentRepo.byID[sandbox.ID] = sandbox
	router.RegisterProvider(&fakeProvider{name: "cashi", statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(15000)}})

	if err := svc.ReconcileRecentPayments(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := paymentRepo.GetByID(context.Background(), prod.ID); got.Status != payment.StatusPaid {
		t.Errorf("production payment status = %q, want paid", got.Status)
	}
	if got, _ := paymentRepo.GetByID(context.Background(), sandbox.ID); got.Status != payment.StatusPending {
		t.Errorf("sandbox payment must be untouched, got %q", got.Status)
	}
}
  ```
  (Add `"strings"` import.) In `internal/service/payment_webhook_service_test.go` append:
  ```go
func TestExpirePayments_NotifiesMerchant(t *testing.T) {
	svc, paymentRepo, _, _ := newWebhookTestService()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	p := seedPayment(t, paymentRepo, payment.StatusPending, "REF-EXP-CB")
	p.ExpiresAt = time.Now().Add(-1 * time.Minute)
	paymentRepo.byID[p.ID] = p
	merchantRepo := newFakeMerchantRepo()
	webhookURL := server.URL
	merchantRepo.byID[p.MerchantID] = &merchant.Merchant{ID: p.MerchantID, WebhookURL: &webhookURL}
	callbackRepo := newFakeMerchantCallbackRepo()
	svc = svc.WithMerchantCallbackDeps(merchantRepo, callbackRepo)

	if err := svc.ExpirePayments(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(callbackRepo.created) != 1 || callbackRepo.created[0].EventType != "payment.expired" {
		t.Fatalf("expected one payment.expired callback, got %+v", callbackRepo.created)
	}
}

// staleExpiredListRepo returns a pending snapshot for a payment that has
// meanwhile been settled — the race the guarded UPDATE resolves.
type staleExpiredListRepo struct {
	*fakePaymentRepo
	stale []*payment.Payment
}

func (r *staleExpiredListRepo) ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]*payment.Payment, error) {
	return r.stale, nil
}

func TestExpirePayments_LostRaceSendsNoCallback(t *testing.T) {
	base := newFakePaymentRepo()
	p := seedPayment(t, base, payment.StatusPaid, "REF-EXP-RACE")
	stale := *p
	stale.Status = payment.StatusPending
	repo := &staleExpiredListRepo{fakePaymentRepo: base, stale: []*payment.Payment{&stale}}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	svc := NewPaymentService(repo, &fakeMerchantProviderConfigRepo{}, newFakeWebhookEventRepo(), providerPkg.NewProviderRouter(), "http://localhost:8080")
	// The merchant has a webhook URL, so a callback WOULD be recorded if
	// ExpirePayments notified after losing the race — keeps the test honest.
	merchantRepo := newFakeMerchantRepo()
	webhookURL := server.URL
	merchantRepo.byID[p.MerchantID] = &merchant.Merchant{ID: p.MerchantID, WebhookURL: &webhookURL}
	callbackRepo := newFakeMerchantCallbackRepo()
	svc = svc.WithMerchantCallbackDeps(merchantRepo, callbackRepo)

	if err := svc.ExpirePayments(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(callbackRepo.created) != 0 {
		t.Fatalf("expected no callback for a payment another caller settled, got %d", len(callbackRepo.created))
	}
	if got, _ := base.GetByID(context.Background(), p.ID); got.Status != payment.StatusPaid {
		t.Errorf("status = %q, want paid", got.Status)
	}
}
  ```
- [ ] **Step 4: Run** `go test ./internal/service/ ./internal/repository/ -count=1` → FAIL.
- [ ] **Step 5: Implement reconcile** — in `internal/service/payment_service.go` (add `"errors"` import):
  - Replace the whole `reconcilePaymentData` function (keep its doc comment) with:
  ```go
func (s *PaymentService) reconcilePaymentData(ctx context.Context, p *payment.Payment) (*ReconcileResult, error) {
	result := &ReconcileResult{
		PaymentID:      p.ID,
		Reference:      p.Reference,
		PreviousStatus: p.Status,
		CurrentStatus:  p.Status,
		Action:         "unchanged",
		Message:        "Status unchanged",
	}

	switch p.Status {
	case payment.StatusPaid, payment.StatusFailed, payment.StatusCancelled:
		result.Action = "skipped"
		result.Message = "Payment already in terminal status"
		return result, nil
	}

	// A pending payment past its deadline may be expired locally; an expired
	// one is only re-checked for a late settlement and never changed locally.
	pastExpiry := p.Status == payment.StatusPending && time.Now().After(p.ExpiresAt)
	hasRef := p.ProviderReference != nil && *p.ProviderReference != ""

	if !hasRef {
		if pastExpiry {
			return s.expireLocally(ctx, p, result, "Marked expired locally (no provider reference; expires_at passed)")
		}
		result.Action = "skipped"
		result.Message = "No provider reference — cannot poll provider"
		return result, nil
	}

	selectedProvider, err := s.getProviderByName(p.ProviderName)
	if err != nil {
		result.Action = "error"
		result.Message = err.Error()
		return result, nil
	}

	logger.InfofCtx(ctx, "Reconciling payment %s with provider %s ref %s", p.Reference, p.ProviderName, *p.ProviderReference)

	providerStatus, err := s.checkProviderStatus(ctx, selectedProvider, *p.ProviderReference)
	if err != nil {
		logger.WarnfCtx(ctx, "Failed to check provider status for %s: %v", p.Reference, err)
		if pastExpiry {
			return s.expireLocally(ctx, p, result, fmt.Sprintf("Provider check failed (%v); marked expired locally (expires_at passed)", err))
		}
		result.Action = "error"
		result.Message = fmt.Sprintf("Provider check failed: %v", err)
		return result, nil
	}

	ps := providerStatus.Status
	result.ProviderStatus = &ps

	if providerStatus.Status != payment.StatusPaid {
		if pastExpiry {
			return s.expireLocally(ctx, p, result, "Provider has not settled; marked expired locally (expires_at passed)")
		}
		result.Message = "Provider has not settled the payment"
		return result, nil
	}

	if verifyErr := verifyProviderPayment(p, providerStatus, nil); verifyErr != nil {
		logger.ErrorfCtx(ctx, "Payment %s: %v", p.Reference, verifyErr)
		s.recordReconcileMismatch(ctx, p, providerStatus, verifyErr)
		result.Action = "error"
		result.Message = verifyErr.Error()
		return result, nil
	}

	paidAt := providerStatus.PaidAt
	if paidAt == nil {
		now := time.Now()
		paidAt = &now
	}
	previous, err := s.paymentRepo.UpdateStatus(ctx, p.ID, payment.StatusPaid, paidAt)
	if err != nil {
		if errors.Is(err, payment.ErrPaymentAlreadyTerminal) {
			result.Action = "skipped"
			result.Message = "Payment was settled concurrently"
			return result, nil
		}
		return nil, err
	}

	late := previous == payment.StatusExpired
	p.Status = payment.StatusPaid
	p.PaidAt = paidAt
	result.CurrentStatus = payment.StatusPaid
	result.Action = "updated"
	result.Message = fmt.Sprintf("Status updated from %s to paid via provider", previous)
	if late {
		result.Message += " (late payment)"
	}
	s.NotifyMerchantPaymentEvent(ctx, p, eventTypeForStatus(payment.StatusPaid), late)
	result.MerchantNotified = true
	return result, nil
}

// expireLocally marks a pending payment expired after its deadline. Losing a
// race to a concurrent settlement is reported in the result, not an error.
func (s *PaymentService) expireLocally(ctx context.Context, p *payment.Payment, result *ReconcileResult, message string) (*ReconcileResult, error) {
	if _, err := s.paymentRepo.UpdateStatus(ctx, p.ID, payment.StatusExpired, nil); err != nil {
		if errors.Is(err, payment.ErrPaymentAlreadyTerminal) {
			result.Action = "skipped"
			result.Message = "Payment changed concurrently"
			return result, nil
		}
		return nil, err
	}
	p.Status = payment.StatusExpired
	result.CurrentStatus = payment.StatusExpired
	result.Action = "expired_local"
	result.Message = message
	s.NotifyMerchantPaymentEvent(ctx, p, eventTypeForStatus(payment.StatusExpired), false)
	result.MerchantNotified = true
	return result, nil
}

// recordReconcileMismatch stores an amount mismatch found by reconcile as a
// webhook_events row, so it shows up in the admin logs/notifications like a
// webhook mismatch and excludes the payment from later automatic runs.
func (s *PaymentService) recordReconcileMismatch(ctx context.Context, p *payment.Payment, status *provider.NormalizedPaymentStatus, cause error) {
	msg := cause.Error()
	pid := p.ID
	var providerAmount interface{}
	if status.Amount != nil {
		providerAmount = *status.Amount
	}
	ev := &provider.WebhookEvent{
		PaymentID:         &pid,
		ProviderName:      p.ProviderName,
		ProviderReference: *p.ProviderReference,
		EventType:         "reconcile.amount_mismatch",
		Status:            "rejected",
		RawPayload: map[string]interface{}{
			"source":          "reconcile",
			"provider_status": status.Status,
			"provider_amount": providerAmount,
			"expected_amount": p.Amount,
		},
		IsProcessed:     false,
		ProcessingError: &msg,
	}
	if err := s.webhookEventRepo.Create(ctx, ev); err != nil {
		logger.ErrorfCtx(ctx, "Failed to record reconcile amount mismatch for %s: %v", p.Reference, err)
	}
}

// Automatic reconcile (scheduler job "reconcile-payments"). Cashi doesn't
// document webhook retries, so this is the safety net that settles a payment
// whose webhook was lost or failed verification, and catches late
// settlements of payments that had already expired.
const (
	ReconcileInterval  = 2 * time.Minute
	reconcileWindow    = 24 * time.Hour
	reconcileBatchSize = 50
)

// ReconcileRecentPayments re-checks recent pending/expired production
// payments with their provider. Per-payment problems are logged and skipped
// so one bad payment never stops the batch.
func (s *PaymentService) ReconcileRecentPayments(ctx context.Context) error {
	candidates, err := s.paymentRepo.ListReconcileCandidates(ctx, time.Now(), reconcileWindow, reconcileBatchSize)
	if err != nil {
		return fmt.Errorf("failed to list reconcile candidates: %w", err)
	}
	changed := 0
	for _, p := range candidates {
		r, err := s.reconcilePaymentData(ctx, p)
		if err != nil {
			logger.ErrorfCtx(ctx, "reconcile-payments: payment %s: %v", p.Reference, err)
			continue
		}
		if r.Action == "updated" || r.Action == "expired_local" {
			changed++
		}
	}
	if len(candidates) > 0 {
		logger.InfofCtx(ctx, "reconcile-payments: checked %d, changed %d", len(candidates), changed)
	}
	return nil
}
  ```
  - In `internal/service/payment_webhook_service.go` `ExpirePayments`, replace the loop body with:
  ```go
		logger.InfofCtx(ctx, "Expiring payment: %s", p.Reference)
		if _, err := s.paymentRepo.UpdateStatus(ctx, p.ID, payment.StatusExpired, nil); err != nil {
			// Includes losing a race to a concurrent settlement: that caller
			// owns the merchant notification.
			logger.ErrorfCtx(ctx, "Failed to expire payment %s: %v", p.Reference, err)
			continue
		}
		p.Status = payment.StatusExpired
		expiredCount++
		s.NotifyMerchantPaymentEvent(ctx, p, eventTypeForStatus(payment.StatusExpired), false)
  ```
- [ ] **Step 6: Schedule** — `cmd/api/main.go`: after the `retry-merchant-callbacks` job add
  `go scheduler.RunPeriodic(bgCtx, service.ReconcileInterval, "reconcile-payments", paymentService.ReconcileRecentPayments)`
  and extend the comment above the jobs: "…, re-check recent pending/expired production payments with the provider (reconcile-payments), …".
- [ ] **Step 7: Verify** — gofmt/vet/full `-race` suite pass.
- [ ] **Step 8: Report** (no commit).

---

### Task 7: Documentation

**Files:** `docs/payment-state-machine.md`, `docs/openapi.yaml`, `README.md` (all under `back/`)

- [ ] **Step 1: `docs/payment-state-machine.md`** — rewrite to reflect the code from Tasks 1–6, keeping the existing section structure and Indonesian prose:
  - **Status** table: `expired` "Terminal? → Ya, kecuali pembayaran terlambat (`expired → paid`)".
  - **Diagram**: add the `expired ──▶ paid (pembayaran terlambat)` edge.
  - **Aturan**: replace the 4 rules with `AllowedSourceStatuses` (`paid` ← pending/expired; others ← pending) and the invariant that only provider-reported settlements write `paid`.
  - **Ditegakkan di mana**: the guarded single-statement `UpdateStatus` (CTE + `WHERE status IN (…)` + `RETURNING` previous) and "hanya pemenang yang mengirim callback".
  - New section **Verifikasi Settlement**: webhook = trigger, check-status = truth, gross amount rule incl. the Cashi 0.7% fee note, `amount_mismatch:` prefix, `verification_failed` → 502, 5-second timeout.
  - New section **Pembayaran Terlambat**: `payment.paid.late` event type; callback stays `payment.paid` with `late_payment: true`.
  - **Siapa yang Memicu Transisi** table: add `reconcile-payments` (scheduler, tiap 2 menit, ≤50, pending ≤24 jam + expired ≤24 jam, kecuali yang ber-`amount_mismatch`) and note `ExpirePayments` now sends `payment.expired`.
  - **Idempotency Webhook**: update to the new order (non-settlement → ignored; paid → duplicate; failed/cancelled → terminal; lost race → duplicate) and the HTTP mapping table from Global Constraints.
  - **Menambah Status Baru**: step 3 becomes "update `AllowedSourceStatuses`".
- [ ] **Step 2: `docs/openapi.yaml`** — in `/provider-webhooks/{providerName}` replace the description and responses with:
  ```yaml
      description: |
        No auth — signature-verified per provider adapter (`x-gateway-signature`,
        HMAC-SHA256 of the raw body). A webhook only triggers processing: the
        payment is marked paid after the provider's check-status confirms it is
        settled and its gross amount equals the stored amount (see
        docs/payment-state-machine.md). Duplicates, non-settlement events,
        already-final payments and amount mismatches respond 200
        `{"status":"ignored"}`; if the provider cannot confirm the settlement
        yet the response is 502 so the provider may retry (the reconcile job
        is the fallback). Rate limited 120 req/min per IP.
      parameters: [{ name: providerName, in: path, required: true, schema: { type: string } }]
      requestBody:
        content: { application/json: { schema: { type: object } } }
      responses:
        '200': { description: Processed, or acknowledged without a status change (ignored) }
        '400': { description: Invalid payload / signature validation failed / unregistered provider / unknown order }
        '429': { $ref: '#/components/responses/TooManyRequests' }
        '502': { description: Settlement not yet confirmed by the provider — retry later }
  ```
  Then `grep -n "environment" docs/openapi.yaml` and, for every request-body `environment` property on create-payment and create-payment-link schemas, set its description to: `sandbox | production (aliases: prod, live / test, dev, development). Empty means sandbox; any other value is rejected with 400.`
- [ ] **Step 3: `back/README.md`** — in **Background Jobs** add `- \`reconcile-payments\` (tiap 2 menit) — cek ulang payment produksi \`pending\`/\`expired\` ≤24 jam ke check-status provider (verifikasi nominal); jaring pengaman bila webhook hilang` and change the `expire-payments` line to mention it now sends `payment.expired` to the merchant.
- [ ] **Step 4: Verify** — `grep -n "expired is terminal\|CanTransitionTo(currentStatus" docs/payment-state-machine.md` shows nothing stale; YAML still parses: `python3 -I -c "import yaml,sys; yaml.safe_load(open('docs/openapi.yaml'))" && echo ok` (skip if PyYAML missing and say so).
- [ ] **Step 5: Report** (no commit).

---

### Task 8: Release gate, `1.0.1` release, production verification

**Files:** none. Run by the controller with the user; every VPS command is run by the user (or by the controller over SSH only if the user asks), and nothing secret is printed.

- [ ] **Step 1 (gate): Restore SSH** — user updates the security-group SSH rule to "My IP"; `ssh -i ~/.ssh/whuzpay-vps.pem ubuntu@13.48.71.251 true` succeeds.
- [ ] **Step 2 (gate): Confirm Cashi amounts for the test payment** — on the VPS:
  ```bash
  cd ~/whuzpay
  docker compose exec -T postgres psql -U postgres -d pg_aggregator -c \
   "SELECT event_type, status, is_processed, raw_payload->'data'->>'amount' AS webhook_amount FROM webhook_events WHERE provider_reference = 'PAY-1793341680-804bb9b5' ORDER BY created_at;"
  KEY=$(sudo sed -n 's/^api_key *= *"\(.*\)"/\1/p' .config.toml)
  curl -fsS -H "x-api-key: $KEY" "https://cashi.id/api/check-status/PAY-1793341680-804bb9b5"; echo; unset KEY
  ```
  **Proceed only if** `webhook_amount = 2000` and check-status `amount = 2000`. Otherwise STOP and revisit the amount rule with the user.
- [ ] **Step 3: Release** — with user approval: from `back/`, `make release VERSION=1.0.1`; then `docker buildx imagetools inspect ghcr.io/akbarryyan/whuzpay-backend:1.0.1` lists `linux/amd64` + `linux/arm64`.
- [ ] **Step 4: Deploy** — user, on the VPS, per `DEPLOY.md` §4: set `x-api-image` to `1.0.1`, `docker compose pull api`, backup (`pg_dump` per §5), `make migrate` (expect `applied: 0`), `docker compose up -d`, `docker compose ps`.
- [ ] **Step 5: Verify a real settlement** — user creates and pays a Rp 2.000 payment (same commands as the earlier test, steps A–C); status becomes `paid`; latest `webhook_events` row for it has `is_processed = true` and event type `payment.paid`.
- [ ] **Step 6: Forged-but-signed webhook is refused** — user creates an unpaid payment (step B only), notes its `reference` as `REF`, then on the VPS:
  ```bash
  cd ~/whuzpay
  REF=PAY-...   # the unpaid payment
  BODY=$(printf '{"event":"PAYMENT_SETTLED","data":{"order_id":"%s","amount":2000,"status":"SETTLED"}}' "$REF")
  SECRET=$(sudo sed -n 's/^secret_key *= *"\(.*\)"/\1/p' .config.toml)
  SIG=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$SECRET" | awk '{print $2}'); unset SECRET
  curl -s -o /dev/null -w '%{http_code}\n' -X POST https://api.whuzpay.web.id/api/v1/provider-webhooks/cashi \
    -H "x-gateway-signature: $SIG" -H 'Content-Type: application/json' -d "$BODY"
  ```
  Expected: `502`; the payment stays `pending`.
- [ ] **Step 7: Reconcile + expiry** — after the unpaid payment's deadline: `docker compose logs api | grep reconcile-payments` shows runs, and the payment becomes `expired`.
- [ ] **Step 8:** `docker stats --no-stream` — memory within limits. Report results (no commit).
