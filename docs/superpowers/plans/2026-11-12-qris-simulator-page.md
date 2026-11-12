# QRIS Simulator Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the in-page sandbox simulator buttons with a public Midtrans-style `/simulator` page (paste a QR image URL or upload a QR image → Scan QR → Pay / Fail / Expire), give sandbox payments a public QR image URL, and bring the Bruno collection up to date.

**Architecture:** The backend serves each sandbox payment's stored QR (a base64 PNG data URI) as `GET /public/payments/by-reference/{ref}/qr.png` and returns its absolute URL as `qr_image_url`. The front-end `/simulator` page fetches the image in the browser, decodes it with `jsqr`, extracts the payment reference from the sandbox payload `sandbox:order=<ref>;...`, shows the payment, and calls the existing public simulate endpoint. Checkout and Test Payment link to the simulator instead of hosting buttons.

**Tech Stack:** Go 1.25, gorilla/mux; Next.js 16 / React 19 / TypeScript / Tailwind v4; `jsqr`; Bruno `.bru` collection.

Spec: `back/docs/superpowers/specs/2026-11-12-qris-simulator-page-design.md`.

## Global Constraints

- **Never `git commit`, `git add`, `git push`, or create branches** (user rule). Leave all changes uncommitted.
- Never read or print secrets: `back/.config.toml`, `back/.env`, `back/db_password`, `front/.env.local`, keys, `.pem`. Bruno files must contain no real secrets (only the dev seed credentials already in `Local.bru`).
- Do not run `docker compose up/down/restart/run/build`. Real-PostgreSQL tests only via a throwaway PG18 (scratchpad data dir, socket `/tmp/claude-1000/pgsock`, port 54999, `listen_addresses=''`), removed afterwards; never the system PostgreSQL on 5432.
- QR PNG endpoint: `GET /api/v1/public/payments/by-reference/{reference}/qr.png`, behind `publicRateLimiter`; only `environment = "sandbox"` payments whose `qris_data` starts with `data:image/png;base64,`; response `Content-Type: image/png`, `Cache-Control: public, max-age=300`; anything else → 404 JSON message `Payment QR not found`.
- `qr_image_url` = `"{app.url}/api/v1/public/payments/by-reference/{url.PathEscape(reference)}/qr.png"` for those sandbox payments, else omitted (`omitempty`). `app.url` is `cfg.App.URL` (already `PaymentService.appBaseURL`).
- `POST /api/v1/public/payments/by-reference/{reference}/simulate` stays. `POST /api/v1/merchant/payments/{id}/simulate` is removed (route, handler, tests, openapi, front-end helper).
- Sandbox QR payload format (from `internal/provider/sandbox/adapter.go`): `sandbox:order=<payment reference>;amount=<n>;ref=<SBX-...>`.
- Simulator page `/simulator`: public, English UI. Checkout page: Indonesian. Test Payment page: English.
- Simulator error texts (exact): "Enter a valid image URL.", "Could not load the image. Check the URL.", "No QR code found in this image.", "This is not a WhuzPay sandbox QR code."; after a simulation: "Payment {reference} is now {status}."; non-pending: "This payment is no longer pending."
- Front-end: read `front/AGENTS.md` first (Next.js 16 differs from training data); lint rule `react-hooks/set-state-in-effect`; no horizontal scroll at 375 px; desktop layout of untouched pages must not change. No front-end test runner: verify with `npm run lint` and `npm run build` from `front/`.
- Backend verification from `back/`: `gofmt -l ./internal ./cmd ./pkg` empty, `go vet ./...`, `go test ./... -race -count=1`.

---

### Task 1: Backend — QR image endpoint, `qr_image_url`, remove merchant simulate

**Files:**
- Modify: `back/internal/domain/payment/payment.go` (`PaymentResponse.QRImageURL`; helper `SandboxQRImageURL`)
- Modify: `back/internal/service/payment_service.go` (`QRImageURL` method), `back/internal/service/payment_link_service.go` (delegating `QRImageURL`)
- Modify: `back/internal/handler/payment_handler.go` (`GetQRImage`; set `qr_image_url` in `publicPaymentJSON` and both `ToPaymentResponse` call sites)
- Modify: `back/internal/handler/merchant_handler.go` (set `QRImageURL` in GetPayment/CreatePayment; delete `SimulatePayment`)
- Modify: `back/internal/handler/admin_handler.go:182`, `back/internal/handler/payment_link_handler.go:281` (set `QRImageURL`)
- Modify: `back/cmd/api/main.go` (add PNG route, remove merchant simulate route)
- Modify: `back/internal/handler/simulate_handler_test.go` (delete `TestMerchantHandler_SimulatePayment`), `back/cmd/api/payout_router_test.go` (`TestRouter_SandboxSimulateRoutes`: replace the merchant-route cases)
- Create: `back/internal/handler/qr_image_handler_test.go`, `back/internal/domain/payment/qr_image_url_test.go`
- Modify: `back/docs/openapi.yaml`

**Interfaces:**
- Produces (used by Tasks 2–4): JSON field `qr_image_url` (string, absent when not applicable) on: `POST /api/v1/payments`, `GET /api/v1/payments/{id}`, `GET /api/v1/merchant/payments/{id}`, `POST /api/v1/merchant/payments`, `POST /api/v1/admin/payments`, `POST /api/v1/public/payment-links/{slug}/pay`, `GET /api/v1/public/payments/by-reference/{reference}` and the public simulate response. PNG endpoint as in Global Constraints.

- [ ] **Step 1: Domain helper test** — `back/internal/domain/payment/qr_image_url_test.go`:

```go
package payment

import "testing"

func TestSandboxQRImageURL(t *testing.T) {
	png := "data:image/png;base64,iVBORw0KGgo="
	text := "00020101021226..."
	cases := []struct {
		name string
		p    Payment
		want string
	}{
		{"sandbox png", Payment{Reference: "PAY-1-abc", Environment: EnvironmentSandbox, QRISData: &png}, "https://api.example.com/api/v1/public/payments/by-reference/PAY-1-abc/qr.png"},
		{"reference escaped", Payment{Reference: "PAY 1/x", Environment: EnvironmentSandbox, QRISData: &png}, "https://api.example.com/api/v1/public/payments/by-reference/PAY%201%2Fx/qr.png"},
		{"production", Payment{Reference: "PAY-1", Environment: EnvironmentProduction, QRISData: &png}, ""},
		{"no qr", Payment{Reference: "PAY-1", Environment: EnvironmentSandbox}, ""},
		{"not a png data uri", Payment{Reference: "PAY-1", Environment: EnvironmentSandbox, QRISData: &text}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SandboxQRImageURL(&tc.p, "https://api.example.com/"); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
```

Run `go test ./internal/domain/payment -run TestSandboxQRImageURL` → FAIL (undefined).

- [ ] **Step 2: Implement the domain helper** in `payment.go`:

```go
// SandboxQRPNGPrefix is how the sandbox adapter stores its QR image.
const SandboxQRPNGPrefix = "data:image/png;base64,"

// SandboxQRImageURL is the public URL of a sandbox payment's QR image (served
// by GET /public/payments/by-reference/{reference}/qr.png), or "" when the
// payment is not sandbox or has no stored PNG QR. apiBaseURL is app.url.
func SandboxQRImageURL(p *Payment, apiBaseURL string) string {
	if p == nil || p.Environment != EnvironmentSandbox || p.QRISData == nil ||
		!strings.HasPrefix(*p.QRISData, SandboxQRPNGPrefix) {
		return ""
	}
	return strings.TrimRight(apiBaseURL, "/") + "/api/v1/public/payments/by-reference/" +
		url.PathEscape(p.Reference) + "/qr.png"
}
```

Add field to `PaymentResponse` (after `QRISData`): `QRImageURL string \`json:"qr_image_url,omitempty"\``. Do **not** change `ToPaymentResponse`'s signature. Run the test → PASS.

- [ ] **Step 3: Service accessors** — `PaymentService.QRImageURL(p *payment.Payment) string { return payment.SandboxQRImageURL(p, s.appBaseURL) }` and `PaymentLinkService.QRImageURL(p) string { return s.paymentSvc.QRImageURL(p) }` (guard `s.paymentSvc == nil` → `""`).

- [ ] **Step 4: Handler tests (fail first)** — `back/internal/handler/qr_image_handler_test.go`, using `newTestPaymentService()` (registers the real sandbox adapter):

```go
// sandbox payment with a real PNG data URI → 200, image/png, Cache-Control,
// body == base64-decoded bytes; production payment with the same data → 404;
// unknown reference → 404; sandbox without qris_data → 404;
// sandbox with "data:image/png;base64,%%%" (corrupt) → 404.
// All 404 bodies carry message "Payment QR not found".
```

Write these as a table test calling `h.GetQRImage(rec, req)` with `mux.SetURLVars(req, map[string]string{"reference": ref})`. Also add to the existing handler tests: a sandbox `POST /api/v1/payments` (API key path, `TestPaymentHandler_CreatePayment_SandboxSuccess` pattern) response has `qr_image_url` ending in `/qr.png`, and `GetPaymentByReference` for a production payment has no `qr_image_url` key. Delete `TestMerchantHandler_SimulatePayment` from `simulate_handler_test.go`. Run → FAIL (undefined `GetQRImage`).

- [ ] **Step 5: Implement `GetQRImage`** in `payment_handler.go`:

```go
// GetQRImage serves a sandbox payment's QR as PNG for the public QRIS
// simulator. Production payments are never served.
func (h *PaymentHandler) GetQRImage(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(mux.Vars(r)["reference"])
	p, err := h.paymentService.GetPaymentByReference(r.Context(), ref)
	if err != nil || p.Environment != payment.EnvironmentSandbox || p.QRISData == nil ||
		!strings.HasPrefix(*p.QRISData, payment.SandboxQRPNGPrefix) {
		if err != nil && !errors.Is(err, payment.ErrPaymentNotFound) {
			logger.ErrorfCtx(r.Context(), "qr image %s: %v", ref, err)
		}
		respondError(w, http.StatusNotFound, "Payment QR not found")
		return
	}
	png, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(*p.QRISData, payment.SandboxQRPNGPrefix))
	if err != nil {
		respondError(w, http.StatusNotFound, "Payment QR not found")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}
```

- [ ] **Step 6: Fill `qr_image_url` everywhere** — after each `payment.ToPaymentResponse(...)` listed in Files, set `.QRImageURL` via the handler's service (`h.paymentService.QRImageURL(p)`; in `PaymentLinkHandler` use `h.linkService.QRImageURL(p)`; in `AdminHandler` its `paymentService`). In `merchantPaymentDetail` the embedded `*PaymentResponse` carries it. In `publicPaymentJSON` add `"qr_image_url"` only when non-empty. Delete `MerchantHandler.SimulatePayment`.

- [ ] **Step 7: Routes** in `cmd/api/main.go`: add, next to the public by-reference GET,

```go
api.Handle("/public/payments/by-reference/{reference}/qr.png", publicRateLimiter.Limit(http.HandlerFunc(paymentHandler.GetQRImage))).Methods("GET")
```

and delete the `merchantDash ... "/payments/{id}/simulate"` line. In `TestRouter_SandboxSimulateRoutes` replace the merchant cases with: `GET /api/v1/public/payments/by-reference/PAY-NOPE/qr.png` → 404 `Payment QR not found` without auth; `POST /api/v1/merchant/payments/{uuid}/simulate` with a valid merchant JWT → 404 or 405 (route gone, not 200/400). Also assert a CORS preflight-free `GET` of the PNG route from `Origin: <frontend_url>` returns `Access-Control-Allow-Origin` if the router test harness wraps CORS; otherwise note in the report that CORS is applied in `main()` around the whole router (line ~193) and needs no change.

- [ ] **Step 8: openapi** — add the PNG path (200 `image/png`, 404, 429), add `qr_image_url` to the payment response schema(s) and the public payment view, remove the merchant simulate path.

- [ ] **Step 9: Verify** — gofmt/vet/`go test ./... -race -count=1` (throwaway PG). Leave uncommitted.

---

### Task 2: Front-end — public `/simulator` page

**Files:**
- Modify: `front/package.json` / `package-lock.json` (`npm install jsqr@1.4.0`)
- Create: `front/app/simulator/page.tsx` (client component; add a `metadata`-capable server wrapper only if the Next.js 16 docs require it for a title — otherwise a plain client page)
- Create: `front/lib/sandbox-qr.ts`
- Modify: `front/lib/merchant-api.ts` (`PublicPayment.qr_image_url?: string`; English messages in `SIMULATION_ERRORS`/`SimulationError` fallback "Simulation failed.")

**Interfaces:**
- Consumes: `fetchPublicPaymentByReference`, `simulatePublicPayment(reference, outcome)` (throws `SimulationError` with `.status`), PNG endpoint from Task 1.
- Produces: route `/simulator`, query param `?qr=<url>` pre-fills the URL input (no auto-scan).

- [ ] **Step 1: QR helpers** — `front/lib/sandbox-qr.ts`:

```ts
import jsQR from "jsqr";

/** Reference from a WhuzPay sandbox QR payload, or null. */
export function parseSandboxPayload(text: string): string | null {
  if (!text.startsWith("sandbox:")) return null;
  for (const part of text.slice("sandbox:".length).split(";")) {
    const [k, v] = part.split("=");
    if (k === "order" && v) return v;
  }
  return null;
}

/** Decodes the first QR code in an image blob; null when none is found. */
export async function decodeQrFromBlob(blob: Blob): Promise<string | null> {
  const bitmap = await createImageBitmap(blob);
  const canvas = document.createElement("canvas");
  canvas.width = bitmap.width;
  canvas.height = bitmap.height;
  const ctx = canvas.getContext("2d");
  if (!ctx) return null;
  ctx.drawImage(bitmap, 0, 0);
  const data = ctx.getImageData(0, 0, canvas.width, canvas.height);
  return jsQR(data.data, data.width, data.height)?.data ?? null;
}
```

- [ ] **Step 2: Page** — `front/app/simulator/page.tsx` (`"use client"`), single column, `max-w-[560px]`, same visual language as the checkout (`bg-[#eff4f8]` page, white rounded card, navy `#0c2a5b` header band reading "QRIS Simulator" and a one-line note "For WhuzPay sandbox payments only. No real money moves."). Contents:
  - URL input (`type="url"`, placeholder `https://api.whuzpay.web.id/api/v1/public/payments/by-reference/.../qr.png`) + **Scan QR** button; an **Upload QR image** file input (`accept="image/*"`).
  - Initial URL from `useSearchParams().get("qr")` (read once into the initial state; wrap the client part in `<Suspense>` if Next.js 16 requires it for `useSearchParams` — check the docs in `node_modules/next/dist/docs/`).
  - Scan flow: validate URL (`new URL(...)` with http/https) → `fetch(url)` → `blob` → `decodeQrFromBlob` → `parseSandboxPayload` → `fetchPublicPaymentByReference(ref)`; each failure sets the exact error text from Global Constraints (fetch/network/non-OK → "Could not load the image. Check the URL."; decode null → "No QR code found in this image."; parse null → "This is not a WhuzPay sandbox QR code."; payment lookup failure → show its message). Upload uses the file `Blob` directly.
  - Result card: merchant name, total (`formatIDR(total_amount, currency)` from `@/app/components/admin/ui`), reference (mono, break-all), status badge, expires at (local date-time).
  - If `status === "pending"`: buttons **Pay** (green), **Fail** (red outline), **Expire** (grey outline), stacked full-width below `sm`, row at `sm`+, `min-h-10`; disabled while running; after success replace the payment and show "Payment {reference} is now {status}."; on `SimulationError` show its message inline. If not pending: "This payment is no longer pending." and no buttons.
  - **Scan another QR** clears result, error and file input.
- [ ] **Step 3:** `SIMULATION_ERRORS` → English: 403 "Only sandbox payments can be simulated.", 404 "Payment not found.", 409 "This payment is no longer pending.", 429 "Too many attempts. Try again shortly.", fallback "Simulation failed.".
- [ ] **Step 4: Verify** — `npm run lint`, `npm run build`. Report how each Global Constraint text and responsive rule is satisfied (file:line). Leave uncommitted.

---

### Task 3: Front-end — checkout and Test Payment link to the simulator; cleanup

**Files:**
- Modify: `front/app/pay/[reference]/page.tsx`, `front/app/pay/[reference]/CheckoutPanels.tsx`
- Modify: `front/app/dashboard/test-payment/page.tsx`
- Modify: `front/lib/merchant-api.ts` (delete `simulateMerchantPayment`; add `qr_image_url?: string` to `MerchantPayment`)
- Delete: `front/app/components/SandboxSimulator.tsx`

**Interfaces:**
- Consumes: `qr_image_url` (Task 1), `/simulator?qr=` (Task 2).

- [ ] **Step 1: Checkout** — remove the `simulator` prop, `simulate()` handler and `simulating` state; for a pending sandbox payment with `qr_image_url`, render below "Saya sudah bayar" a small yellow box (`border-[#f5d48a] bg-[#fff7e6]`): text "Mode sandbox · uji pembayaran lewat Simulator", button **Salin URL QR** (copies `qr_image_url`, toast "URL QR disalin."), link **Buka Simulator** → `/simulator?qr=${encodeURIComponent(qr_image_url)}` (`target="_blank" rel="noopener noreferrer"`). Buttons stack below `sm`.
- [ ] **Step 2: Test Payment** — remove the simulator component, handler and state; when `!isProduction && visible && visible.qr_image_url`, show under the QR: a truncated, selectable URL (`min-w-0 truncate font-mono text-[12px]`), **Copy** button (toast "QR URL copied."), and **Open Simulator** link (same href rule, new tab). Keep polling/history unchanged so a simulation done in the simulator tab shows up automatically.
- [ ] **Step 3: Cleanup** — delete `SandboxSimulator.tsx` and `simulateMerchantPayment`; `grep -rn "SandboxSimulator\|simulateMerchantPayment" front/app front/lib` must be empty. `simulatePublicPayment`/`SimulationError` stay (used by `/simulator`).
- [ ] **Step 4: Verify** — `npm run lint`, `npm run build`. Leave uncommitted.

---

### Task 4: Bruno collection

**Files:** everything under `back/bruno/` (existing 10 requests moved into folders, new requests, `environments/Local.bru`, new `environments/Production.bru`).

**Interfaces:**
- Consumes: routes in `back/cmd/api/main.go` (source of truth for every path/method), request/response shapes from handlers and `docs/openapi.yaml`.

- [ ] **Step 1: Environments** — `Local.bru`: `base_url: http://localhost:8090`, keep the other vars. `Production.bru`:

```
vars {
  base_url: https://api.whuzpay.web.id
  merchant_id:
}
vars:secret [
  admin_email,
  admin_password,
  merchant_email,
  merchant_password,
  merchant_api_key
]
```

Add `merchant_api_key:` (empty) to `Local.bru` too, with a `docs`-style comment in a request explaining where to create one (merchant dashboard → API keys).

- [ ] **Step 2: Folders** (each with `folder.bru` `meta { name: ... }`), requests numbered by `seq` inside each folder; every request has `docs` (purpose, auth, vars used/set), `tests` asserting the expected status, and `script:post-response` saving ids/tokens like the existing 01/02:
  - **Auth**: Admin Login (existing 01), Admin Me, Merchant Login (existing 05), Merchant Me.
  - **Merchant API (API key)**: Create Payment (sandbox) `POST /api/v1/payments` with headers `X-API-Key: {{merchant_api_key}}`, `Idempotency-Key: {{$guid}}` (or a script-generated key if `$guid` is unsupported — check Bruno docs), body `{amount, description, payment_method: "qris", customer_name, customer_email}`; saves `payment_id`, `payment_reference`, `qr_image_url`. Get Payment `GET /api/v1/payments/{{payment_id}}`. Get Payment Status `GET /api/v1/payments/{{payment_id}}/status`.
  - **Checkout (public)**: Get Payment by Reference; Get QR Image (`.../qr.png`, test status 200 for sandbox).
  - **Sandbox Simulator**: Simulate Paid / Failed / Expired (`POST .../simulate` body `{"outcome": ...}`), docs noting each needs a fresh pending sandbox payment and that `/simulator` in the web app does the same.
  - **Payment Links**: existing 06–10 unchanged except folder/seq.
  - **Merchant Dashboard** (bearer `{{merchant_token}}`): List Payments, Payment Detail, Create Payment (dashboard), Dashboard Summary, Balance, Balance Daily (`?days=14`), Ledger, List Payouts, Request Payout (`{amount}`; docs: min Rp 50.000, needs payout account), Payment Link Stats (`?environment=sandbox`).
  - **Admin** (bearer `{{admin_token}}`): Create Payment (existing 02, 03), Get Payment (existing 04), List Payments, List Merchants, Create Merchant (with `password`), Merchant Stats, List Payouts, Payout Stats, Pay Payout, Reject Payout, Adjust Balance, Callback Stats, Log Stats, Reconciliation.
- [ ] **Step 3: Verify** — for every `.bru`, check method + path against `cmd/api/main.go` (list the mapping in the report). If `bru` CLI is installed, run `bru run --env Local` only if a local API is already running on 8090 — never start one; otherwise state it was not run. Confirm no secret values in any file (`grep -rn` for the dev seed creds shows only `Local.bru`). Leave uncommitted.
