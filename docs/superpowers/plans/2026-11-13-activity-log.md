# Activity Log Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Record every authenticated admin and merchant-dashboard request (views included), logins/logouts and key-action details in `activity_logs`, and show them on a new admin "Activity Log" page.

**Architecture:** A new `internal/activity` package holds a per-request context holder, an async `Recorder`, a route→label table and an HTTP middleware mounted on the `adminAPI` and `merchantDash` subrouters and around the auth endpoints. Handlers enrich entries with `activity.AddDetails` / `activity.SetActor`. A repository + admin service expose list/stats/export; a daily job purges rows older than 90 days. The front-end marks automatic polling with `X-Background-Request: 1` and adds the admin page.

**Tech Stack:** Go 1.25, gorilla/mux, lib/pq, PostgreSQL 18; Next.js 16 / React 19 / TypeScript / Tailwind v4 / recharts.

Spec: `back/docs/superpowers/specs/2026-11-13-activity-log-design.md`.

## Global Constraints

- **Never `git commit`, `git add`, `git push`, or create branches** (user rule). Leave all changes uncommitted.
- Never read or print secrets: `back/.config.toml`, `back/.env`, `back/db_password`, `front/.env.local`, keys, `.pem`.
- Do not run `docker compose up/down/restart/run/build`. Real-PostgreSQL tests only via the throwaway PG18 recipe in the SDD constraints file; never the system PostgreSQL on 5432.
- Actors: `admin`, `merchant_user`, and `anonymous` (failed logins only). API-key and system actions are NOT logged.
- Every authenticated admin/merchant request is logged, including GETs, except: header `X-Background-Request: 1`, method `OPTIONS`.
- **Never store request or response bodies.** Only `details` explicitly set by handlers. Never store passwords, API key values, webhook secrets, or full bank account numbers (last 4 digits only).
- Writing is async and must never block or fail a request; write errors are only logged.
- Retention: 90 days; purge job every 24 h, deleting in batches of 5000.
- Stats window: 14 days, `DATE(created_at)` / `CURRENT_DATE` convention, arrays never `null`.
- Admin endpoints: `GET /api/v1/admin/activity-logs`, `GET /api/v1/admin/activity-logs/stats`, `GET /api/v1/admin/activity-logs/export`; admin-only.
- Action labels are English; admin UI text is English.
- Backend verification from `back/`: `gofmt -l ./internal ./cmd ./pkg` empty, `go vet ./...`, `go test ./... -race -count=1` (with throwaway PG).
- Front-end: read `front/AGENTS.md` first; verify with `npm run lint` + `npm run build`; lint rule `react-hooks/set-state-in-effect`; no horizontal page scroll at 375 px; untouched pages' desktop layout must not change.

---

### Task 1: Migration 023 + `ActivityLogRepository`

**Files:**
- Create: `back/migrations/023_create_activity_logs.sql`
- Create: `back/internal/activity/entry.go` (domain types only)
- Create: `back/internal/repository/activity_log_repository.go`
- Create: `back/internal/repository/activity_log_repository_pg_test.go`
- Modify: `back/DEPLOY.md` (migration 023 section with rollback `DROP TABLE IF EXISTS activity_logs;`)

**Interfaces — Produces:**

```go
package activity

const (
	ActorAdmin        = "admin"
	ActorMerchantUser = "merchant_user"
	ActorAnonymous    = "anonymous"
)

type Entry struct {
	ID         uuid.UUID              `json:"id"`
	CreatedAt  time.Time              `json:"created_at"`
	ActorType  string                 `json:"actor_type"`
	ActorID    *uuid.UUID             `json:"actor_id,omitempty"`
	ActorEmail string                 `json:"actor_email"`
	MerchantID *uuid.UUID             `json:"merchant_id,omitempty"`
	Action     string                 `json:"action"`
	Method     string                 `json:"method"`
	Route      string                 `json:"route"`
	TargetID   string                 `json:"target_id"`
	StatusCode int                    `json:"status_code"`
	IP         string                 `json:"ip"`
	UserAgent  string                 `json:"user_agent"`
	DurationMS int                    `json:"duration_ms"`
	Details    map[string]interface{} `json:"details"`
}

type Filter struct {
	Search     string     // ILIKE on actor_email, action, target_id
	ActorType  string     // "" = all
	MerchantID *uuid.UUID
	Result     string     // "", "success" (<400), "failed" (>=400)
	DateFrom   *time.Time // inclusive
	DateTo     *time.Time // exclusive
}

type DailyPoint struct {
	Date     string `json:"date"`  // 2006-01-02
	Label    string `json:"label"` // e.g. "Nov 13"
	Admin    int64  `json:"admin"`
	Merchant int64  `json:"merchant"` // merchant_user + anonymous
}

type ActionCount struct {
	Action string `json:"action"`
	Count  int64  `json:"count"`
}

type Stats struct {
	Days         int           `json:"days"`
	Total        int64         `json:"total"`
	FailedLogins int64         `json:"failed_logins"` // actor_type=anonymous
	ActiveActors int64         `json:"active_actors"` // distinct non-null actor_id
	Daily        []DailyPoint  `json:"daily"`
	Breakdown    []ActionCount `json:"breakdown"`  // top 6 actions + "Other"
}
```

`repository.ActivityLogRepository` (constructor `NewActivityLogRepository(db *sql.DB)`):
- `Insert(ctx, *activity.Entry) error` — sets `ID`/`CreatedAt` when zero; `details` marshalled to JSONB (`{}` when nil).
- `List(ctx, activity.Filter, limit, offset int) ([]activity.Entry, error)` — `ORDER BY created_at DESC, id DESC`; `limit<=0` → 20, cap 200.
- `Count(ctx, activity.Filter) (int64, error)`.
- `Stats(ctx, days int) (*activity.Stats, error)` — single-pass totals over the window; daily rows only for days with data (the service fills gaps); breakdown top 6 by count within the window, the rest summed into `"Other"` when > 0.
- `PurgeOlderThan(ctx, before time.Time, batch int) (int64, error)` — loops `DELETE FROM activity_logs WHERE id IN (SELECT id FROM activity_logs WHERE created_at < $1 LIMIT $2)` until a batch deletes fewer than `batch` rows; returns total deleted.

- [ ] **Step 1:** Write the migration exactly as in spec §3 (`BEGIN; ... COMMIT;`, `IF NOT EXISTS`).
- [ ] **Step 2: PG tests (fail first)** in `activity_log_repository_pg_test.go` using `newPGRepo(t)` for the DB: insert 3 entries (admin GET 200, merchant_user POST 422 with merchant_id, anonymous login 401 with email `x@y.z`), assert `List` order and each filter (search by email, actor_type, merchant_id, result=failed → 2 rows, date range), `Count` matches `List` length, `details` round-trips (`{"amount": 5000}` → float64 5000), nil details stored as `{}`; `Stats(14)` totals (total 3, failed_logins 1, active_actors 2), an entry 20 days old excluded from stats but returned by List without date filter; breakdown with 8 distinct actions yields 6 + `Other`; `PurgeOlderThan(now-90d, 2)` with 5 rows older than 90 days and 1 newer deletes exactly 5 (forces several batches) and keeps the newer one; migration file applied twice is idempotent.
- [ ] **Step 3:** Implement the repository; run the PG tests → PASS.
- [ ] **Step 4:** DEPLOY.md section (Indonesian, like neighbours): table purpose, `make migrate` safe while api runs, rollback statement, retention note.
- [ ] **Step 5:** gofmt/vet/tests. Leave uncommitted.

---

### Task 2: `internal/activity` — context holder, recorder, labels, middleware

**Files:**
- Create: `back/internal/activity/context.go`, `recorder.go`, `labels.go`, `middleware.go` and tests `context_test.go`, `recorder_test.go`, `middleware_test.go`
- Modify: `back/internal/middleware/rate_limit.go` — export the client-IP logic as `func ClientIP(r *http.Request, trusted []netip.Prefix) string` and make `(*IPRateLimiter).clientIP` call it (behaviour unchanged; existing `TestClientIP*` must still pass).

**Interfaces — Consumes:** `activity.Entry` (Task 1); `middleware.AdminIDFromContext`, `middleware.MerchantUserIDFromContext`, `middleware.MerchantIDFromContext`.

**Interfaces — Produces:**

```go
// context.go
func AddDetails(ctx context.Context, kv map[string]interface{})            // no-op without holder
func SetActor(ctx context.Context, actorType string, id *uuid.UUID, email string, merchantID *uuid.UUID) // no-op without holder

// recorder.go
type Writer interface{ Insert(ctx context.Context, e *Entry) error }
func NewRecorder(w Writer, buffer int) *Recorder   // starts one writer goroutine
func (r *Recorder) Record(e Entry)                  // never blocks; drops + counts when full
func (r *Recorder) Close(ctx context.Context) error // stops intake, drains, waits or ctx deadline
func (r *Recorder) Dropped() int64

// labels.go
func Label(method, routeTemplate string) string    // known label or "METHOD template"
func Known(method, routeTemplate string) bool

// middleware.go
type EmailLookup func(ctx context.Context, actorType string, id uuid.UUID) string
func Middleware(rec *Recorder, lookup EmailLookup, trusted []netip.Prefix) func(http.Handler) http.Handler
const BackgroundHeader = "X-Background-Request"
```

Behaviour:
- **Holder:** the middleware puts a `*holder{mu sync.Mutex; details map; actorType string; actorID, merchantID *uuid.UUID; actorEmail string}` in the request context before calling `next`; `AddDetails` merges keys; `SetActor` overrides actor fields (used by auth handlers).
- **Recorder:** buffered channel (`buffer` = 1024 in main), one goroutine calling `Insert` with `context.WithTimeout(context.Background(), 5*time.Second)`; on full channel increment an atomic counter and log a warning at most once per minute; `Close` closes intake (later `Record` calls are dropped silently) and drains.
- **Middleware:** skip when `r.Method == OPTIONS` or `r.Header.Get(BackgroundHeader) == "1"`. Otherwise wrap the `ResponseWriter` to capture status (default 200), time the call, then build the Entry:
  - Route: `mux.CurrentRoute(r).GetPathTemplate()` (fallback `r.URL.Path`).
  - Actor: holder `SetActor` values win; else `AdminIDFromContext` → `admin`; else `MerchantUserIDFromContext` → `merchant_user` (+ `MerchantIDFromContext`); else `anonymous` (email from details key `"email"` if present).
  - `actor_email` from holder or `lookup` (lookup may return ""); `merchant_id` from holder/context, else for admin routes a route var `id` that sits under `/merchants/{id}` (template contains `/merchants/{id}`).
  - `target_id`: first non-empty of mux vars `id`, `keyId`, `reference`, `slug`, `name`.
  - IP: `middleware.ClientIP(r, trusted)`; user agent truncated to 500; email truncated to 255.
  - `details`: holder details minus none (internal actor info is held in separate fields, never in the map).
  - Then `rec.Record(entry)`.
- **Labels:** a `map[string]string` keyed `"METHOD template"` covering every admin, merchant-dashboard and auth route in `cmd/api/main.go` (the full `/api/v1/...` template). Use concise past-tense labels, e.g. `"GET /api/v1/admin/payments"`→"Viewed payments", `"GET /api/v1/admin/payments/{id}"`→"Viewed payment", `"POST /api/v1/admin/payouts/{id}/pay"`→"Paid payout", `"POST /api/v1/admin/payouts/{id}/reject"`→"Rejected payout", `"POST /api/v1/admin/merchants/{id}/adjustments"`→"Adjusted balance", `"PUT /api/v1/admin/merchants/{id}/fee-bearer"`→"Changed fee bearer", `"POST /api/v1/auth/admin/login"`→"Admin login", `"POST /api/v1/auth/login"`→"Merchant login", `"POST /api/v1/auth/logout"`→"Merchant logout", `"GET /api/v1/admin/activity-logs"`→"Viewed activity log", `"GET /api/v1/merchant/payments/export"`→"Exported payments". Methods registered with `Methods("PUT","POST")` need an entry per method.

- [ ] **Step 1: Tests first.**
  - `context_test.go`: AddDetails/SetActor are no-ops without a holder; merge semantics with one.
  - `recorder_test.go`: with a fake Writer that blocks, `Record` returns immediately and `Dropped()` grows past the buffer; `Close` drains all buffered entries into a non-blocking fake; `Record` after `Close` doesn't panic.
  - `middleware_test.go` (mux router + fake Writer, flush via `Close`): admin context → actor admin + email from lookup + route template + target_id + status 404 from handler; merchant context → merchant_user + merchant_id; `X-Background-Request: 1` → nothing recorded; OPTIONS → nothing; handler `AddDetails({"amount":1})` appears in details; a POST body containing `"password":"secret"` never appears anywhere in the recorded Entry (marshal entry to JSON and assert substring absent); `SetActor` overrides context actor; X-Forwarded-For honoured only with trusted prefix.
- [ ] **Step 2:** Run → FAIL. Implement. Run `go test ./internal/activity ./internal/middleware -race` → PASS.
- [ ] **Step 3:** gofmt/vet. Leave uncommitted. (Label coverage against the real router is tested in Task 3.)

---

### Task 3: Wiring, auth actors, key-action details, purge job

**Files:**
- Modify: `back/cmd/api/main.go` — build `activityRepo := repository.NewActivityLogRepository(db)`, `activityRecorder := activity.NewRecorder(activityRepo, 1024)`, email lookup (admin via `AdminRepository.GetByID`, merchant user via `MerchantUserRepository.GetByID`, cached in a `sync.Map` with 5-minute TTL), pass recorder/lookup/trustedProxies into `setupRouter`; `adminAPI.Use(...)` and `merchantDash.Use(...)` the middleware **after** the auth middlewares; wrap each auth endpoint handler (`/auth/login`, `/auth/register`, `/auth/logout`, `/auth/me`, `/auth/profile`, `/auth/change-password`, `/auth/admin/login|logout|me|profile|change-password`) with the middleware inside the existing rate-limiter chain; `startJob(24*time.Hour, "purge-activity-logs", ...)` calling `PurgeOlderThan(ctx, time.Now().AddDate(0,0,-90), 5000)`; on shutdown call `activityRecorder.Close(ctx)` after the HTTP server stops (alongside waiting for jobs).
- Modify: `back/internal/handler/auth_handler.go` — after a successful admin/merchant login call `activity.SetActor(ctx, activity.ActorAdmin|ActorMerchantUser, &id, email, merchantID)`; on every login attempt `activity.AddDetails(ctx, map[string]interface{}{"email": req.Email})`; for `/me`, `/profile`, `/change-password`, `/logout` (which authenticate inside the handler) call `SetActor` once the claims are known.
- Modify handlers for key-action details (`activity.AddDetails` only after success):
  - `admin_handler.go` `SetMerchantFeeBearer`: `{"from": old, "to": new}` (read the old value from the service result or fetch before update — if the service doesn't expose the old value, record `{"to": new}` only and note it).
  - `admin_payout_handler.go` pay/reject: `{"amount": p.Amount, "reference": ref}` / `{"amount": p.Amount, "reason": reason}`.
  - `admin_refund_handler.go` adjust balance: `{"amount": entry.Amount, "reason": reason}`.
  - admin create merchant: `{"email", "business_name"}`; update merchant: `{"fields": [names of non-empty fields in the request]}`; set active: `{"is_active": v}`.
  - API key create/rotate/delete (admin + merchant): `{"environment": env, "key_id": id}` — never the key value.
  - merchant regenerate webhook secret: `{"regenerated": true}`; request payout: `{"amount"}`; update payout account: `{"bank": bank, "account_last4": last4}`; change password (both): no details.
  - Keep the existing `logger.InfofCtx("admin %s ...")` lines.
- Create: `back/cmd/api/activity_router_test.go` — (a) **label coverage**: walk the real router (`router.Walk`) and assert `activity.Known(method, template)` for every route under `/api/v1/admin`, `/api/v1/merchant` and `/api/v1/auth` for each of its methods; (b) end-to-end with a fake Writer: an admin JWT `GET /api/v1/admin/payments` produces one entry with actor admin and label "Viewed payments"; the same with `X-Background-Request: 1` produces none; a failed admin login records `anonymous` with the attempted email and no password anywhere in the entry JSON.
- Modify: `back/internal/handler/*_test.go` where needed for details assertions (at least fee bearer, adjust balance, payout pay, API key create — assert details present and no key value).

- [ ] Steps: tests first → FAIL → implement → `go test ./... -race -count=1` (throwaway PG) → PASS; gofmt/vet. Leave uncommitted.

---

### Task 4: Admin API — list, stats, export

**Files:**
- Create: `back/internal/service/activity_log_service.go` (+ `_test.go`) — `NewActivityLogService(repo)`; `List(ctx, filter, limit, offset) ([]activity.Entry, int64, error)`; `Stats(ctx) (*activity.Stats, error)` filling the 14-day window with zeros via the existing `statsWindow` helper in `internal/service/stats_common.go` (oldest → newest, `Label` like other stats), arrays never nil.
- Create: `back/internal/handler/admin_activity_handler.go` (+ `_test.go`) — `ListActivityLogs`, `GetActivityLogStats`, `ExportActivityLogs` on `AdminHandler` (add a `WithActivityLogs(svc)` setter or constructor param following how other optional services are wired). Query params: `search`, `actor_type` (must be one of the three or empty → else 400), `merchant_id` (uuid or 400), `result` (`success|failed|""` else 400), `date_from`/`date_to` via the existing `parseDateRangeQuery`, `limit`/`offset` via `parseLimitOffset`. Response `{items, total, limit, offset}`; items never null. Export: CSV columns `time,actor_type,actor_email,merchant_id,action,method,route,target_id,status_code,ip,user_agent,duration_ms,details` (details as compact JSON), same filters, capped like other admin exports (reuse their cap constant).
- Modify: `back/cmd/api/main.go` — register `GET /activity-logs`, `/activity-logs/stats`, `/activity-logs/export` on `adminAPI` (stats/export before any `{id}` route); labels for these three already exist from Task 2/3 (add if missing).
- Modify: `back/cmd/api/payout_router_test.go` (or the activity router test) — the three routes reach handlers with an admin JWT (200) and reject a merchant JWT (401/403) and no token (401).
- Modify: `back/docs/openapi.yaml` — the three endpoints and schemas.

- [ ] Steps: tests first (handler validation 400s, service window filling, export header + one row, router auth) → FAIL → implement → full backend verification → PASS. Leave uncommitted.

---

### Task 5: Front-end — background header + Activity Log page

**Files:**
- Modify: `front/app/dashboard/test-payment/page.tsx` — the status poll (`setInterval` ~line 132) sends `X-Background-Request: 1` (extend `fetchMerchantPayment` with an optional `{ background?: boolean }` argument in `front/lib/merchant-api.ts` that adds the header).
- Audit other automatic calls: `grep -rn "setInterval\|setTimeout" front/app front/lib` — any repeating authenticated API call gets the background flag; the public checkout page polling is unauthenticated and not logged (leave it).
- Modify: `front/lib/admin-api.ts` — types mirroring `activity.Entry`/`Stats`; `fetchActivityLogs(params)`, `fetchActivityLogStats()`, `activityLogsExportUrl(params)` or an export helper matching how the Logs page exports CSV.
- Create: `front/app/admin/(panel)/activity-logs/page.tsx` — mirror `front/app/admin/(panel)/logs/page.tsx` structure and controls:
  - `StatsOverview` (from `app/components/admin/StatsOverview.tsx`): cards "Total activity" (14 days), "Failed logins", "Active users"; chart stacked bars Admin (`#06163a`) vs Merchant (`#ff5e16`) per day; donut = breakdown by action.
  - Table columns: Time, Actor (email or "—" + badge Admin/Merchant/Anonymous), Action, Target (link: `/admin/payments/{id}` when route contains `/payments/{id}`, `/admin/merchants/{id}` for `/merchants/{id}`, `/admin/payouts` for payouts; else plain text), Result (badge Success/Failed + status code), IP.
  - Filters/controls exactly like the Logs page: search without a Search button (debounced), actor type select (All/Admin/Merchant/Anonymous), result select (All/Success/Failed), date range popover, export CSV icon, page-size select, pagination (mobile justify-between).
  - Row click opens a drawer (same width/close icon as other drawers) listing all fields, user agent, duration, and `details` as key–value rows.
- Modify: `front/app/components/admin/AdminSidebar.tsx` — add `{ label: "Activity Log", href: "/admin/activity-logs", icon: <history/clock icon> }` after "Logs" (use a lucide icon such as `HistoryIcon`, like `WalletIcon` is used).

- [ ] Steps: implement; `npm run lint` + `npm run build` clean; report per requirement where it is met (file:line) and the polling audit result. Leave uncommitted.
