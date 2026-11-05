# Status HTTP untuk Galat Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Galat provider saat membuat payment dijawab 502 (provider menjawab galat/tidak valid), 504 (timeout), atau 503 (tidak tersedia/tidak terjangkau), bukan 500, di semua jalur pembuatan payment.

**Architecture:** Adapter Cashi mengklasifikasikan setiap kegagalan dengan tepat satu sentinel dari paket `internal/provider` (`ErrProviderAPIError`, `ErrProviderTimeout`, `ErrProviderNotAvailable`); `respondCreatePaymentError` memetakan sentinel ke status HTTP dan semua handler (termasuk admin) memakainya. Pesan ke klien tetap generik.

**Tech Stack:** Go 1.25 (pembungkusan ganda `%w: %w`), gorilla/mux, httptest; Next.js untuk satu tabel dokumentasi.

**Spec:** [`back/docs/superpowers/specs/2026-11-05-provider-error-status-design.md`](../specs/2026-11-05-provider-error-status-design.md)

## Global Constraints

- **NO git commits, NO `git add`, NO branches** sampai user mengizinkan. Tiap task diakhiri "report changed files".
- Jangan pernah membaca/mencetak secret (`back/.config.toml`, `back/db_password`, `back/.env`, `front/.env.local`, kunci Cashi).
- Tanpa dependensi baru, tanpa key config baru, tanpa migration. Tanpa database/Docker di task ini.
- Backend: `gofmt -l ./internal ./cmd ./pkg` kosong, `go vet ./...` bersih, `go test ./... -race -count=1` hijau (jalankan dari `back/`; cek `pwd`). Front-end (Task 3): baca `front/AGENTS.md` dan dokumen Next relevan di `front/node_modules/next/dist/docs/` bila menyentuh komponen/route (perubahan tabel teks saja cukup melaporkan apa yang dibaca); `npm run lint` dan `npm run build` dari `front/` bersih.
- Test baru wajib dibuktikan membedakan lewat **mutasi** (rusakkan logika, test gagal, kembalikan) dan dilaporkan.
- Nilai persis: status/pesan ke klien — **504** `"Payment provider timed out"`; **502** `"Payment provider error"`; **503** `"Payment provider is temporarily unavailable"` (tetap); 503 `"Fee calculation is not available for this payment provider"` (tetap); 403 `"Merchant account is inactive"` (tetap); 400 `"Unsupported payment method"` (tetap); default tetap 500 `"Failed to create payment"`. Isi respons Cashi **tidak pernah** masuk respons ke klien (hanya log). Timeout klien Cashi tetap `requestTimeout` = 10 dtk. Tepat satu sentinel per galat.
- Abaikan `back/.superpowers/`.

## Urutan dan File

| Task | Isi | File utama |
|---|---|---|
| 1 | Sentinel `ErrProviderTimeout` + klasifikasi di adapter Cashi | `internal/provider/errors.go`, `internal/provider/cashi/types.go`, `client.go`, `client_errors_test.go` |
| 2 | Pemetaan bersama + jalur admin + test per jalur | `internal/handler/payment_handler.go`, `admin_handler.go`, tes handler/service |
| 3 | Dokumentasi: openapi + tabel galat API docs | `docs/openapi.yaml`, `front/app/dashboard/api-docs/page.tsx` |

Urutan: 1 → 2 → 3.

---

### Task 1: Sentinel timeout dan klasifikasi di adapter Cashi

**Files:** Modify `internal/provider/errors.go`, `internal/provider/cashi/types.go`, `internal/provider/cashi/client.go`. Create `internal/provider/cashi/client_errors_test.go`.

**Interfaces — Produces:** `providerPkg.ErrProviderTimeout`; `(*CashiError).Unwrap() error` (mengembalikan `providerPkg.ErrProviderAPIError`); `classifyTransportError(err error) error` (privat, paket `cashi`).

- [ ] **Step 1: Tes gagal** `internal/provider/cashi/client_errors_test.go`:

```go
package cashi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
)

var providerSentinels = []error{
	providerPkg.ErrProviderAPIError,
	providerPkg.ErrProviderTimeout,
	providerPkg.ErrProviderNotAvailable,
}

// wantExactlyOne fails unless err matches `want` and none of the other
// provider sentinels: the HTTP mapping depends on there being exactly one.
func wantExactlyOne(t *testing.T, err, want error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error matching %v, got nil", want)
	}
	for _, s := range providerSentinels {
		if got, should := errors.Is(err, s), s == want; got != should {
			t.Errorf("errors.Is(err, %v) = %v, want %v (err: %v)", s, got, should, err)
		}
	}
}

func createReq() *provider.ProviderPaymentRequest {
	return &provider.ProviderPaymentRequest{InternalReference: "PAY-1", Amount: 2000}
}

func TestCreatePayment_ErrorClassification(t *testing.T) {
	respond := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
	}
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"cashi 500", respond(500, `{"message":"boom"}`)},
		{"cashi 400", respond(400, `{"message":"bad"}`)},
		{"cashi 401", respond(401, `{"message":"bad key"}`)},
		{"success false", respond(200, `{"success":false,"message":"nope"}`)},
		{"missing order id", respond(200, `{"success":true,"expires_at":"2030-01-01 10:00:00"}`)},
		{"malformed json", respond(200, `this is not json`)},
		{"unparseable expires_at", respond(200, `{"success":true,"order_id":"X1","expires_at":"garbage"}`)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			adapter := NewCashiAdapter(server.URL, "key", testSecret)

			_, err := adapter.CreatePayment(context.Background(), createReq())
			wantExactlyOne(t, err, providerPkg.ErrProviderAPIError)
		})
	}
}

func TestCreatePayment_TimeoutIsClassifiedAsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select { // return as soon as the client gives up, so server.Close() does not block
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer server.Close()
	adapter := NewCashiAdapter(server.URL, "key", testSecret)
	adapter.httpClient = &http.Client{Timeout: 50 * time.Millisecond}

	_, err := adapter.CreatePayment(context.Background(), createReq())
	wantExactlyOne(t, err, providerPkg.ErrProviderTimeout)
}

func TestCreatePayment_ContextDeadlineIsClassifiedAsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer server.Close()
	adapter := NewCashiAdapter(server.URL, "key", testSecret)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := adapter.CreatePayment(ctx, createReq())
	wantExactlyOne(t, err, providerPkg.ErrProviderTimeout)
}

func TestCreatePayment_ConnectionRefusedIsNotAvailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close() // nothing listens on this address any more
	adapter := NewCashiAdapter(url, "key", testSecret)

	_, err := adapter.CreatePayment(context.Background(), createReq())
	wantExactlyOne(t, err, providerPkg.ErrProviderNotAvailable)
}

func TestCreatePayment_CancelledContextIsNotATimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	adapter := NewCashiAdapter(server.URL, "key", testSecret)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := adapter.CreatePayment(ctx, createReq())
	wantExactlyOne(t, err, providerPkg.ErrProviderNotAvailable)
}

func TestGetPaymentStatus_ErrorClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	}))
	defer server.Close()
	adapter := NewCashiAdapter(server.URL, "key", testSecret)

	_, err := adapter.GetPaymentStatus(context.Background(), "PAY-1")
	wantExactlyOne(t, err, providerPkg.ErrProviderAPIError)
}
```

(`testSecret` sudah didefinisikan di `client_test.go` paket yang sama; bila namanya berbeda, pakai nama yang ada.) Run: `go test ./internal/provider/cashi -run 'Classification|Timeout|Refused|Cancelled' -count=1` → gagal (sentinel belum ada).

- [ ] **Step 2: Implementasi.**

`internal/provider/errors.go`: tambahkan di blok `var (...)`:

```go
	// ErrProviderTimeout means the provider did not answer within the client
	// timeout; callers map it to 504 Gateway Timeout.
	ErrProviderTimeout = errors.New("provider timeout")
```

`internal/provider/cashi/types.go`: tambahkan impor `providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"` dan

```go
// Unwrap lets callers classify every Cashi error response (HTTP 4xx/5xx,
// success:false, missing order id) with errors.Is(err, ErrProviderAPIError):
// the provider answered, but with an error. Cashi's raw response body stays in
// the message for the logs and is never shown to API clients.
func (e *CashiError) Unwrap() error { return providerPkg.ErrProviderAPIError }
```

`internal/provider/cashi/client.go`: tambahkan impor `"context"` (sudah ada), `"errors"` dan `"net"`; tambahkan fungsi

```go
// classifyTransportError maps a failed HTTP round trip to exactly one provider
// sentinel: a timeout (the provider did not answer in time) or "not available"
// (connection refused, DNS failure, cancelled context, ...).
func classifyTransportError(err error) error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return providerPkg.ErrProviderTimeout
	}
	return providerPkg.ErrProviderNotAvailable
}
```

Ubah `doRequest`:

```go
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w: %w", classifyTransportError(err), err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w: %w", classifyTransportError(err), err)
	}
	...
	if respBody != nil {
		if err := json.Unmarshal(body, respBody); err != nil {
			return fmt.Errorf("failed to unmarshal response: %w: %w", providerPkg.ErrProviderAPIError, err)
		}
	}
```

Dan di `CreatePayment`, galat parse `expires_at`:

```go
	if err != nil {
		return nil, fmt.Errorf("failed to parse expires_at from cashi: %w: %w", providerPkg.ErrProviderAPIError, err)
	}
```

(Jangan mengubah bagian lain.)

- [ ] **Step 3: Verifikasi** `go test ./internal/provider/cashi -race -count=1` hijau, lalu suite penuh. Mutasi (kembalikan setelah tiap-tiap): hapus `Unwrap` → semua kasus 4xx/5xx/`success false`/`missing order id` gagal; `classifyTransportError` selalu `ErrProviderNotAvailable` → dua test timeout gagal; selalu `ErrProviderTimeout` → test connection refused dan cancelled gagal; hapus `%w` sentinel pada unmarshal → kasus `malformed json` gagal.
- [ ] **Step 4: Report** (tanpa commit).

---

### Task 2: Pemetaan bersama dan jalur admin

**Files:** Modify `internal/handler/payment_handler.go`, `internal/handler/admin_handler.go`. Tes baru/diubah di `internal/handler/*_test.go` dan `internal/service/payment_webhook_service_test.go` (regresi).

**Interfaces — Consumes:** `providerPkg.ErrProviderTimeout`, `providerPkg.ErrProviderAPIError`, `providerPkg.ErrProviderNotAvailable` (Task 1).

- [ ] **Step 1: Tes gagal.**

(a) `internal/handler/provider_error_mapping_test.go` (paket `handler`; ikuti impor tes handler lain):

```go
package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
)

// secretUpstream stands in for Cashi's raw response body, which ends up inside
// the wrapped error (and therefore the logs) but must never reach a client.
const secretUpstream = `{"message":"internal cashi detail 4711"}`

func TestRespondCreatePaymentError_ProviderFailures(t *testing.T) {
	wrap := func(inner error) error {
		return fmt.Errorf("failed to create payment with available providers: %w",
			fmt.Errorf("failed to create QRIS payment: %w: %s", inner, secretUpstream))
	}
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{"provider answered with an error", wrap(providerPkg.ErrProviderAPIError), http.StatusBadGateway, "Payment provider error"},
		{"legacy provider error sentinel", wrap(payment.ErrProviderError), http.StatusBadGateway, "Payment provider error"},
		{"provider timed out", wrap(providerPkg.ErrProviderTimeout), http.StatusGatewayTimeout, "Payment provider timed out"},
		{"provider not available", wrap(providerPkg.ErrProviderNotAvailable), http.StatusServiceUnavailable, "Payment provider is temporarily unavailable"},
		{"fee not supported", wrap(providerPkg.ErrFeeNotSupported), http.StatusServiceUnavailable, "Fee calculation is not available for this payment provider"},
		{"merchant inactive", merchant.ErrMerchantInactive, http.StatusForbidden, "Merchant account is inactive"},
		{"unsupported method", providerPkg.ErrUnsupportedPaymentMethod, http.StatusBadRequest, "Unsupported payment method"},
		{"unknown error stays 500", errors.New("pq: relation does not exist"), http.StatusInternalServerError, "Failed to create payment"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			respondCreatePaymentError(rec, tc.err)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantMsg) {
				t.Errorf("body %q does not contain %q", rec.Body.String(), tc.wantMsg)
			}
			for _, leak := range []string{"4711", "cashi detail", "pq:", "relation"} {
				if strings.Contains(rec.Body.String(), leak) {
					t.Errorf("response leaks internal text %q: %s", leak, rec.Body.String())
				}
			}
		})
	}
}
```

(b) Per jalur, end to end dengan service nyata dan provider palsu yang gagal: baca `payment_handler_test.go` (pola `TestPaymentHandler_CreatePayment_UnsupportedProviderInProduction`, ~baris 109-140, dan `newTestPaymentService`) dan `fakes_test.go` (fake provider handler yang punya `createErr`, ~baris 189-203). Tulis tes tabel `TestCreatePayment_ProviderFailureStatusPerPath` yang, untuk tiga galat (`fmt.Errorf("x: %w", providerPkg.ErrProviderAPIError)` → 502, `...ErrProviderTimeout` → 504, `...ErrProviderNotAvailable` → 503) menjalankan: (1) `PaymentHandler.CreatePayment` (jalur API key; konteks merchant/environment seperti `TestPaymentHandler_CreatePayment_APIKeyContextOverridesBody`), (2) `MerchantHandler.CreatePayment` (jalur dashboard; ikuti tes handler merchant yang ada), (3) `AdminHandler.CreatePayment`. Provider palsu dipasang sebagai sandbox provider (`WithSandboxProvider`) dengan `environment: sandbox` supaya tidak perlu routing produksi. Tiap kasus menegaskan status dan bahwa body tidak memuat teks galat internal. Untuk jalur checkout payment link: bila tes handler payment link yang ada sudah menyediakan fixture link, tambahkan kasus yang sama; bila tidak, cukup tunjukkan di laporan (dengan file:baris) bahwa cabang `default` di `payment_link_handler.go` memanggil `respondCreatePaymentError`.

(c) Regresi webhook/reconcile (`internal/service/payment_webhook_service_test.go` dan/atau `payment_reconcile_test.go`): temukan tes tempat check-status provider palsu gagal (`statusErr` di fake provider service, `payment_fakes_test.go` ~baris 500) dan ubah menjadi tabel dengan empat galat — `errors.New("boom")`, `fmt.Errorf("x: %w", providerPkg.ErrProviderTimeout)`, `...ErrProviderNotAvailable`, `...ErrProviderAPIError` — yang semuanya harus menghasilkan hasil identik: error dikembalikan, payment tetap `pending`, event webhook/reconcile tercatat sama seperti sebelumnya.

Run: `go test ./internal/handler ./internal/service -count=1 -run 'ProviderErrors|ProviderFailure|RespondCreatePaymentError'` → gagal (cabang baru belum ada).

- [ ] **Step 2: Implementasi.**

`internal/handler/payment_handler.go` — ganti `respondCreatePaymentError`:

```go
func respondCreatePaymentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, merchant.ErrMerchantInactive):
		respondError(w, http.StatusForbidden, "Merchant account is inactive")
	case errors.Is(err, providerPkg.ErrProviderTimeout):
		// The provider did not answer in time. Checked before the generic
		// "not available" case; the adapter wraps exactly one sentinel.
		respondError(w, http.StatusGatewayTimeout, "Payment provider timed out")
	case errors.Is(err, providerPkg.ErrProviderNotAvailable):
		respondError(w, http.StatusServiceUnavailable, "Payment provider is temporarily unavailable")
	case errors.Is(err, providerPkg.ErrUnsupportedPaymentMethod):
		respondError(w, http.StatusBadRequest, "Unsupported payment method")
	case errors.Is(err, providerPkg.ErrFeeNotSupported):
		respondError(w, http.StatusServiceUnavailable, "Fee calculation is not available for this payment provider")
	case errors.Is(err, providerPkg.ErrProviderAPIError), errors.Is(err, payment.ErrProviderError):
		// The provider answered, but with an error or an invalid response. The
		// raw upstream text stays in the logs; clients get a fixed message.
		respondError(w, http.StatusBadGateway, "Payment provider error")
	default:
		respondError(w, http.StatusInternalServerError, "Failed to create payment")
	}
}
```

`internal/handler/admin_handler.go` `CreatePayment`: hapus pemeriksaan khusus `if errors.Is(err, providerPkg.ErrFeeNotSupported) {...}` dan ganti cabang `default` pada `switch err` menjadi:

```go
		default:
			logger.ErrorfCtx(r.Context(), "Failed to create payment from admin: %v", err)
			respondCreatePaymentError(w, err)
```

Cabang validasi 400 (`payment.ErrInvalidAmount`, dst.) dan `merchant.ErrMerchantInactive` boleh dibiarkan atau dihapus (pemetaan bersama sudah memetakan yang terakhir ke 403); jangan mengubah perilaku keduanya. Hapus impor `providerPkg` dari file itu bila tidak lagi dipakai (cek kompilasi).

- [ ] **Step 3: Verifikasi** suite penuh (`-race`). Mutasi (kembalikan setelah tiap-tiap): tukar 504 menjadi 503 → tes timeout gagal; hapus cabang `ErrProviderTimeout` → tes timeout gagal (jatuh ke 500); hapus `errors.Is(err, providerPkg.ErrProviderAPIError)` → tes 502 gagal; kembalikan `default` admin menjadi 500 → tes jalur admin gagal. **Report** (tanpa commit), termasuk daftar jalur yang diuji end to end dan bukti jalur payment link.

---

### Task 3: Dokumentasi

**Files:** Modify `docs/openapi.yaml`, `front/app/dashboard/api-docs/page.tsx`.

- [ ] **Step 1: openapi.yaml.** Cari setiap operasi pembuat payment: `grep -n "operationId\|/payments:\|/payment-links/{slug}/pay\|post:" docs/openapi.yaml` dan tentukan operasi `POST /payments`, `POST /merchant/payments`, `POST /admin/payments`, dan `POST /public/payment-links/{slug}/pay` (nama path nyata sesuai file). Pada tiap operasi: pastikan ada respons `'502'`, `'503'`, `'504'` dengan deskripsi berikut (ganti teks `502/503` yang ada: ~baris 270-271 dan ~1033-1034 memuat "Provider error"/"Provider unavailable"; perbarui, jangan duplikasi key):
  - `'502': { description: The payment provider answered with an error or an invalid response. No payment was recorded; retrying with the same Idempotency-Key is safe. }`
  - `'503': { description: No payment provider is available right now, or the provider could not be reached. No payment was recorded; retrying with the same Idempotency-Key is safe. }`
  - `'504': { description: The payment provider did not answer in time. No payment was recorded; retrying with the same Idempotency-Key is safe. }`
  Jangan menyentuh respons 502/503 milik endpoint webhook (~baris 1070-1100: itu makna lain).
- [ ] **Step 2: Halaman API docs.** `front/app/dashboard/api-docs/page.tsx` ~baris 532-541 (`StatusCodeTable` pada bagian Error Format): perbarui baris 502 menjadi `"The payment provider (e.g. Cashi) answered with an error or an invalid response. Safe to retry with the same Idempotency-Key."`, baris 503 menjadi `"No provider is available for this payment method right now, or it could not be reached."`, dan tambahkan baris `{ code: "504", label: "Gateway Timeout", description: "The payment provider did not answer in time. Safe to retry with the same Idempotency-Key." }` tepat setelah 503 (ikuti bentuk objek baris-baris yang ada). Jangan mengubah bagian lain halaman.
- [ ] **Step 3: Verifikasi** — `python3 -c "import yaml;yaml.safe_load(open('docs/openapi.yaml'));print('ok')"` dari `back/`; semua `$ref` masih resolve (skrip kecil di /tmp, bukan di repo); `npm run lint` dan `npm run build` dari `front/`; grep bahwa pesan di dokumentasi sama dengan pesan di kode (`Payment provider timed out`, dst. tidak perlu muncul di docs, tetapi kode status harus cocok). **Report** (tanpa commit).

---

## Self-Review (spec → task)

| Spec | Task |
|---|---|
| 3.1 sentinel `ErrProviderTimeout` | 1 |
| 3.2 `CashiError.Unwrap`, klasifikasi transport/body/parse, tepat satu sentinel | 1 |
| 3.3 pemetaan bersama 504/502/503 dengan urutan, pesan generik | 2 |
| 3.4 jalur admin memakai pemetaan bersama | 2 |
| 3.5 openapi + tabel galat API docs | 3 |
| 5 pengujian (adapter, mapper, per jalur, regresi webhook/reconcile, mutasi) | 1, 2 |

**Konsistensi nama:** `providerPkg.ErrProviderTimeout` / `ErrProviderAPIError` / `ErrProviderNotAvailable`, `classifyTransportError`, `(*CashiError).Unwrap`, `respondCreatePaymentError`; status/pesan: 504 "Payment provider timed out", 502 "Payment provider error", 503 "Payment provider is temporarily unavailable".

**Catatan sengaja:** kegagalan parse `expires_at` dari Cashi ikut diklasifikasikan 502 (respons tidak valid) — sebelumnya jatuh ke 500; pembacaan body yang gagal memakai `classifyTransportError` (timeout membaca body = 504, putus = 503); `context.Canceled` (klien memutus) diklasifikasikan "tidak tersedia" (503), bukan timeout; hanya `respondCreatePaymentError` yang bercabang pada jenis galat sehingga perilaku webhook/reconcile tidak berubah (diuji sebagai regresi).
