# Card Summary + Grafik per Halaman dan Password di Add Merchant Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Admin dapat membuat merchant lengkap dengan password login, dan halaman-halaman admin/merchant yang dipilih mendapat 3 card summary plus grafik dan donut.

**Architecture:** Komponen front-end bersama `StatsOverview` (cards + grafik + donut) mengganti pola yang sudah ada di `MerchantStatsSection`. Data memakai endpoint yang ada bila cukup; selainnya endpoint `GET .../stats` agregat baru (backend Go, satu query per bagian, 14 hari terakhir). Password Add Merchant dibuat di `AdminMerchantService.CreateMerchant` bersama akun pemilik.

**Tech Stack:** Go 1.25 / gorilla/mux / lib/pq / PostgreSQL 18; Next.js 16 / React 19 / TypeScript / Tailwind v4 / recharts. Tes backend: unit + PostgreSQL sementara; front-end: `npm run lint` + `npm run build` (tanpa test runner).

Spec: `back/docs/superpowers/specs/2026-11-11-page-stats-and-admin-merchant-password-design.md` (baca dulu — tabel isi per halaman dan bentuk JSON ada di sana dan berlaku persis).

## Global Constraints

- **Tanpa commit dan push:** jangan `git commit`, `git add`, `git push`, atau membuat branch. Semua pekerjaan dibiarkan uncommitted.
- **Jangan membaca atau mencetak rahasia** (`back/.config.toml`, `back/.env`, `front/.env.local`, `db_password`, kunci Cashi). Jangan menyentuh VPS, Docker lain, atau PostgreSQL sistem (5432). Agen tidak menjalankan `docker compose up/down/restart/run/build`.
- **Tes PostgreSQL** hanya lewat PG18 sementara: `/usr/lib/postgresql/18/bin` (`initdb -A trust`, `pg_ctl -o "-p 54999 -c listen_addresses='' -c unix_socket_directories=/tmp/claude-1000/pgsock"`), data dir di scratchpad, DSN `postgres://postgres@:54999/t?host=/tmp/claude-1000/pgsock&sslmode=disable` (env `TEST_DATABASE_URL`, `createdb t` dulu); hentikan dan hapus data dir + socket sesudahnya, verifikasi tidak ada proses/port 54999 tersisa. Pola tes PG: `newPGRepo(t)` (`internal/repository/payment_repository_pg_test.go`), helper `insertConstraintMerchant`, `insertLedgerPayment`, dst. Merchant uji harus punya `phone` non-NULL bila di-scan lewat `MerchantRepository` (`UPDATE merchants SET phone = ''`).
- **Uang `int64` rupiah**; array JSON tidak pernah `null` (selalu `[]`); hari kosong diisi 0; 14 hari terakhir dengan konvensi `DATE(created_at)` / `CURRENT_DATE` dan pengisian hari di Go seperti `buildMerchantStats` (`internal/service/admin_merchant_service.go`) — gunakan ulang fungsi pengisi hari bila memungkinkan alih-alih menyalin.
- **Pola acuan wajib dibaca:** `internal/repository/merchant_stats.go` + `merchant_stats_pg_test.go`, `AdminMerchantService.Stats`/`buildMerchantStats` dan tesnya, `AdminHandler.GetMerchantStats`, rute `/merchants/stats` di `cmd/api/main.go` (dipasang SEBELUM `/merchants/{id}`), baris rute di `cmd/api/payout_router_test.go`; front: `app/components/admin/MerchantStatsSection.tsx`, `DashboardCharts.tsx` (`ChartTooltipBox`), `app/admin/(panel)/merchants/page.tsx`.
- **Front-end:** baca `front/AGENTS.md` + dokumen Next.js terkait sebelum menulis kode; teks UI bahasa Inggris; aturan lint `react-hooks/set-state-in-effect` aktif; hanya kelas Tailwind yang sudah ada polanya; responsive wajib (1 kolom di HP, grid dua kolom `md:grid-cols-2`, `min-w-0`, tanpa lebar tetap > 320 px, target ketuk ≥ 36 px); perubahan tampilan desktop pada halaman lain tidak boleh terjadi (varian `max-lg:`/`sm:`/`lg:`).
- **Keamanan endpoint:** rute admin hanya di `adminAPI` (RequireAdmin), rute merchant hanya di `merchantDash` (RequireMerchant) dengan ID merchant dari sesi, tidak pernah dari query/body; pesan galat ke klien tetap/generik; log lengkap hanya di server.
- Verifikasi tiap task backend dari `back/`: `gofmt -l ./internal ./cmd ./pkg` kosong, `go vet ./...`, `go test ./... -race -count=1` (dengan PG sementara untuk tes PG). Front-end dari `front/`: `npm run lint` dan `npm run build` bersih. Tampilan tidak bisa dilihat agen: laporkan pemeriksaan kode per butir.

---

## File Structure

| File | Tanggung jawab |
|---|---|
| `internal/domain/merchant/merchant.go`, `internal/service/admin_merchant_service.go`, `internal/handler/admin_handler.go`, `cmd/api/main.go` | Password + akun pemilik saat admin membuat merchant (Task 1) |
| `front/app/components/admin/CreateMerchantDrawer.tsx`, `front/lib/admin-api.ts` | Isian password (Task 1) |
| `front/app/components/admin/StatsOverview.tsx` (baru), `MerchantStatsSection.tsx` (refactor) | Komponen bersama (Task 2) |
| `front/app/admin/(panel)/payments/page.tsx`, `front/app/dashboard/payments/page.tsx` | Summary + grafik payments (Task 2) |
| `internal/repository/{payout,callback,webhook_event}_stats.go` (baru) + service + handler + rute | Stats payouts/callbacks/logs (Task 3) |
| `front/app/admin/(panel)/{payouts,callbacks,logs,reconciliation}/page.tsx`, `front/lib/admin-api.ts`, `front/lib/admin-payouts.ts` | Halaman admin (Task 4) |
| `internal/repository/{payment_link_stats,ledger_daily}.go` (baru) + service + handler + rute | Stats payment links + balance daily (Task 5) |
| `front/app/dashboard/{payment-links,balance}/page.tsx`, `front/lib/merchant-api.ts`, `front/lib/merchant-balance.ts` | Halaman merchant (Task 6) |
| `docs/openapi.yaml` | Dokumentasi endpoint/field baru (Task 1, 3, 5) |

---

### Task 1: Password di Add Merchant (backend + drawer)

**Files:**
- Modify: `internal/domain/merchant/merchant.go` (`CreateMerchantRequest.Password`), `internal/service/admin_merchant_service.go` (`NewAdminMerchantService` menerima `merchantUserRepo`, `CreateMerchant` membuat akun pemilik), `internal/handler/admin_handler.go` (peta galat), `cmd/api/main.go` (wiring), `docs/openapi.yaml`, `front/lib/admin-api.ts` (`CreateMerchantPayload.password`), `front/app/components/admin/CreateMerchantDrawer.tsx`
- Test: `internal/service/admin_merchant_service_test.go`, handler/router tests yang menyentuh `CreateMerchant` / `NewAdminMerchantService` (perbarui pemanggil), tes PG baru bila perlu

**Interfaces:**
- Consumes: `merchant.User` (`internal/domain/merchant/user.go`), `MerchantUserRepository.Create/GetByEmail` (`internal/repository/merchant_user_repository.go`), galat `ErrMerchantPasswordRequired` dan galat panjang-minimal yang dipakai `RegisterRequest.Validate`, pola `AuthService.RegisterMerchant` (`internal/service/auth_service.go` ~347-402).
- Produces: `POST /admin/merchants` menerima `{name,email,phone,business_name,webhook_url?,password}`; password wajib ≥ 8 karakter dan ≤ 72 byte; membuat merchant + `merchant_users` owner aktif; respons tetap `MerchantResponse` (tanpa password/hash).

- [ ] **Step 1: Tes gagal dulu (service, sqlmock atau fake yang sudah ada di file tes):** `CreateMerchant` tanpa password → `ErrMerchantPasswordRequired`; password 7 karakter → galat panjang minimal yang sama dengan registrasi; password 73 byte → galat validasi (tambahkan galat domain bernama jelas, mis. `ErrMerchantPasswordTooLong`, bila belum ada); e-mail yang sudah dipakai oleh `merchant_users` (tanpa merchant) → `ErrMerchantAlreadyExists`; sukses → merchant dibuat DAN user owner dibuat dengan `bcrypt.CompareHashAndPassword(hash, password) == nil`, `Role=="owner"`, `IsActive`, e-mail dinormalisasi (trim + lowercase) sama dengan merchant; kegagalan pembuatan user → merchant dihapus (assert `Delete` terpanggil) dan galat generik; respons tidak memuat password/hash (marshal JSON respons dan periksa tidak ada substring password).
- [ ] **Step 2: Implementasi** sesuai Interfaces. Urutan: validasi password → normalisasi → cek e-mail di merchants dan merchant_users → bcrypt → buat merchant → buat user (rollback delete bila gagal). Password tidak boleh dicatat di log maupun galat. Perbarui `NewAdminMerchantService(...)` dan semua pemanggil (main.go, tes). Handler: petakan galat password (kosong/pendek/panjang) ke 400 dengan pesan galat domain yang sudah fixed.
- [ ] **Step 3: Tes PG** (opsional jika service test sudah memakai fake repo): satu tes yang memanggil service dengan repo PG nyata dan memastikan baris `merchants` dan `merchant_users` terbentuk atomik dan login hash cocok.
- [ ] **Step 4: Front-end.** `CreateMerchantPayload` + `password: string`; di `CreateMerchantDrawer.tsx` tambahkan isian **Password** (`type="password"`, `autoComplete="new-password"`, `minLength=8`, `maxLength=72`, label "Password", bantuan "Min. 8 characters. Share it with the merchant owner; they can change it in their profile."), validasi klien (kosong/<8 → pesan inline, tombol submit tidak mengirim), password dikosongkan di `finally` setelah tiap percobaan dan tidak disimpan di tempat lain; galat server tampil inline. Ikuti gaya isian yang ada di drawer itu.
- [ ] **Step 5: openapi** (`password` wajib, `minLength: 8`, `maxLength: 72` di skema create merchant admin).
- [ ] **Step 6: Verifikasi** sesuai Global Constraints. **Mutasi** (laporkan): hapus pemeriksaan panjang minimal → tes gagal; hapus rollback delete → tes gagal; hapus bcrypt (simpan plaintext) → tes gagal.

---

### Task 2: `StatsOverview` + Admin Payments + Merchant Payments

**Files:**
- Create: `front/app/components/admin/StatsOverview.tsx`
- Modify: `front/app/components/admin/MerchantStatsSection.tsx` (pakai `StatsOverview`; tampilan halaman Merchants tetap sama), `front/app/admin/(panel)/payments/page.tsx`, `front/app/dashboard/payments/page.tsx`, `front/lib/admin-api.ts` / `front/lib/merchant-api.ts` bila butuh tipe tambahan

**Interfaces:**
- Produces (dipakai Task 4 dan 6): komponen `StatsOverview` dengan props persis seperti spec §2 (`cards`, `chart`, `donut`, `loading`, `error`, `onRetry`) dan helper kecil yang diekspor dari file yang sama: `formatCompactNumber(v: number): string` (sumbu Y: ≥1.000.000 → "1,5jt", ≥1.000 → "12rb") dan `statusColor(status: string): string` (palet status payment/callback/payout yang konsisten dengan `STATUS_COLORS` di `DashboardCharts.tsx`).
- Consumes: `fetchDashboardSummary()`, `fetchDashboardCharts(days)` (`lib/admin-api.ts`), `fetchMerchantDashboardSummary(env)`, `fetchMerchantDashboardCharts(days, env)` (`lib/merchant-api.ts`), `useMerchantEnvironment()`.

- [ ] **Step 1: `StatsOverview`.** Ekstrak tata letak dan grafik dari `MerchantStatsSection` (grid 3 card `sm:grid-cols-3`; dua card `md:grid-cols-2`; area chart satu seri atau `BarChart` bertumpuk bila `stacked`; donut dengan legenda berisi angka; kondisi kosong per grafik; kondisi `loading`/`error`+Retry yang sama). Generalisasikan sumbu Y (`valueFormat`), seri, dan tooltip (`ChartTooltipBox` dengan `valueFormatter`). `MerchantStatsSection` menjadi pemetaan tipis `MerchantStats` → props `StatsOverview` dan keluaran visualnya tidak berubah.
- [ ] **Step 2: Admin Payments.** Di atas filter/tabel: `StatsOverview` dengan cards Total payments (`total_payments`), Paid amount (`formatIDR(paid_amount)`), Success rate (`paid_payments / total_payments` dibulatkan, "0%" bila total 0; catatan "N paid"); grafik "Paid amount per day" dari `charts.daily` (`paid_amount`, `valueFormat:"idr"`); donut dari `charts.status_breakdown` (warna `statusColor`). Muat dengan `fetchDashboardSummary()` + `fetchDashboardCharts(14)`, galat/Retry sendiri, dan segarkan bersama tombol Refresh yang sudah ada.
- [ ] **Step 3: Merchant Payments.** Sama untuk `fetchMerchantDashboardSummary(environment)` + `fetchMerchantDashboardCharts(14, environment)`; muat ulang saat `environment` berubah.
- [ ] **Step 4: Verifikasi front-end** + pemeriksaan kode: halaman Merchants tidak berubah tampilan, 375 px tanpa luber, tidak ada `setState` sinkron di effect, daftar/paginasi/filter lama tidak berubah.

---

### Task 3: Backend stats payouts, callbacks, logs

**Files:**
- Create: `internal/repository/payout_stats.go`, `callback_stats.go`, `webhook_event_stats.go` (+ `_pg_test.go` masing-masing)
- Modify: service (`internal/service/payout_service.go`, `admin_callback_service.go`, `admin_log_service.go` — satu method `Stats` tiap service) + tes unit pengisi hari, handler (`admin_payout_handler.go`, `admin_handler.go`), `cmd/api/main.go` (rute SEBELUM rute berparameter: `/payouts/stats` sebelum `/payouts/{id}/...`, `/callbacks/stats` sebelum `/callbacks/{id}/retry`, `/logs/stats` sebelum `/logs/{id}`), `cmd/api/payout_router_test.go` (baris rute + 401), `docs/openapi.yaml`

**Interfaces:**
- Produces JSON persis spec §4.2: `GET /admin/payouts/stats`, `GET /admin/callbacks/stats`, `GET /admin/logs/stats`; tipe service `PayoutStats`, `CallbackStats`, `WebhookEventStats` + `...DailyPoint`, `BreakdownPoint{Status string; Count int64}` (satu tipe `BreakdownPoint` bersama di paket service).
- Query (acuan): payouts total/status: `SELECT status, COUNT(*), COALESCE(SUM(amount),0) FROM payouts GROUP BY status`; harian: `SELECT DATE(requested_at), COUNT(*), COALESCE(SUM(amount),0) FROM payouts WHERE requested_at >= CURRENT_DATE - ($1::int-1)*INTERVAL '1 day' GROUP BY 1`. Callbacks: `SELECT status, COUNT(*) FROM merchant_callback_deliveries GROUP BY status` dan harian per `created_at` dengan `COUNT(*) FILTER (WHERE status='success')`, `FILTER (WHERE status='failed')`, sisanya `other`. Logs: `webhook_events` `COUNT(*)`, `COUNT(*) FILTER (WHERE is_processed)`, `FILTER (WHERE processing_error IS NOT NULL OR status IN ('rejected','failed'))` (pastikan nama nilai status sebenarnya dengan membaca `webhook_event_repository.go`/layanan webhook), harian per `created_at`, breakdown `GROUP BY status`.

- [ ] **Step 1: Tes PG gagal dulu** per repo: data uji mencakup semua status, hari di dalam dan di luar jendela 14 hari, database kosong (semua nol, `breakdown` dan `daily` non-nil), dan boundary hari ini. **Step 2:** repo + service (pengisi hari) + tes unit pengisi hari (hari kosong = 0, urutan lama→baru, baris di luar jendela diabaikan). **Step 3:** handler + rute + tes router (401 tanpa token dan dengan JWT merchant). **Step 4:** openapi. **Step 5:** verifikasi + **mutasi** (laporkan): ubah `FILTER (WHERE status='success')` menjadi status lain → tes gagal; hapus filter jendela hari → tes jendela gagal; tukar pengisian `processed`/`failed` → tes gagal.

---

### Task 4: Halaman admin Payouts, Callbacks, Logs, Reconciliation

**Files:** `front/app/admin/(panel)/{payouts,callbacks,logs,reconciliation}/page.tsx`, `front/lib/admin-payouts.ts` (tipe + `fetchPayoutStats`), `front/lib/admin-api.ts` (tipe + `fetchCallbackStats`, `fetchLogStats`)

**Interfaces:** Consumes `StatsOverview` (Task 2) dan JSON Task 3 (tipe TS mencerminkan JSON persis, array bisa kosong). Reconciliation memakai `fetchAdminReconciliation(limit)` yang ada (tanpa endpoint baru).

- [ ] **Step 1 per halaman** (isi persis tabel spec §3): Payouts — cards: "Awaiting processing" (`requested_count`, catatan `formatIDR(requested_amount)`), "Paid out" (`formatIDR(paid_amount)`, catatan `paid_count` payouts), "Rejected" (`rejected_count`); grafik "Payout requests per day" (`amount`, idr); donut `breakdown` (`statusColor`). Callbacks — cards: "Total deliveries", "Delivered" (success + persentase), "Failed / pending" (`failed + pending`); grafik bertumpuk `success` vs `failed` (+ `other` abu); donut `breakdown`. Logs — cards: "Total events", "Processed", "Errors / rejected"; grafik bertumpuk `processed` vs `failed`; donut `breakdown`. Reconciliation — dihitung dari `items`: cards "Awaiting review" (`items.length` / `total`), "Amount on hold" (`formatIDR(sum total_amount)`), "Oldest" (umur dari `created_at` terlama, mis. "3 h 12 m" atau "—"); grafik "Amount on hold per day created" (14 hari, dari `created_at`, tanpa fetch baru); donut dari hitungan `check_status` (label ramah: Possibly expired, Missing provider ref, Pending review). Catatan jujur di Reconciliation bila `total > items.length` ("Showing the oldest N of TOTAL").
- [ ] **Step 2:** tiap halaman memuat statistik sendiri (galat/Retry terpisah dari tabel), menyegarkan bersama tombol Refresh yang ada, dan tidak mengubah filter/paginasi/tabel lama. **Step 3:** verifikasi front-end + pemeriksaan kode (375 px, desktop tidak berubah).

---

### Task 5: Backend stats payment links dan balance daily

**Files:**
- Create: `internal/repository/payment_link_stats.go`, `ledger_daily.go` (+ tes PG), service method (`payment_link_service.go` `Stats(ctx, merchantID, environment)`, `balance_service.go` `Daily(ctx, merchantID, days)`), handler (`payment_link_handler.go`, `balance_handler.go`), `cmd/api/main.go` (rute di `merchantDash`: `/payment-links/stats` SEBELUM `/payment-links/{id}`; `/balance/daily`), router test (401 tanpa token dan dengan JWT admin), `docs/openapi.yaml`

**Interfaces:** JSON persis spec §4.2 (`GET /merchant/payment-links/stats?environment=` dan `GET /merchant/balance/daily?days=14`). Merchant ID selalu dari sesi (`MerchantIDFromContext`). `environment` divalidasi dengan konvensi endpoint merchant yang ada (baca `merchant_handler.go` list payments; nilai tak dikenal → default seperti di sana). `days` di luar 1..90 dijepit (default 14).
Query acuan: links `SELECT COUNT(*), COUNT(*) FILTER (WHERE is_active), COUNT(*) FILTER (WHERE amount_type='fixed'), COUNT(*) FILTER (WHERE amount_type='open') FROM payment_links WHERE merchant_id=$1`; paid via link `SELECT COUNT(*), COALESCE(SUM(amount),0) FROM payments WHERE merchant_id=$1 AND payment_link_id IS NOT NULL AND status='paid' AND environment=$2`; harian `DATE(paid_at)` (bukan `created_at`) dengan jendela 14 hari; ledger harian `SELECT DATE(created_at), COALESCE(SUM(amount) FILTER (WHERE amount>0),0), COALESCE(-SUM(amount) FILTER (WHERE amount<0),0) FROM merchant_ledger WHERE merchant_id=$1 AND created_at >= ... GROUP BY 1`.

- [ ] **Step 1: Tes PG gagal dulu:** isolasi antar merchant (data merchant lain tidak masuk hitungan — wajib), environment filter, hari di dalam/luar jendela, kosong → nol dan array non-nil, kredit/debit harian termasuk entri `adjustment` positif/negatif. **Step 2:** repo + service + handler + rute + tes router. **Step 3:** openapi. **Step 4:** verifikasi + **mutasi** (laporkan): hapus `merchant_id = $1` → tes isolasi gagal; hapus filter environment → tes gagal; tukar kredit/debit → tes gagal.

---

### Task 6: Halaman merchant Payment Links dan Balance

**Files:** `front/app/dashboard/payment-links/page.tsx`, `front/app/dashboard/balance/page.tsx`, `front/lib/merchant-api.ts` (`fetchPaymentLinkStats(environment)`), `front/lib/merchant-balance.ts` (`fetchBalanceDaily(days)`)

**Interfaces:** Consumes `StatsOverview` (Task 2), JSON Task 5, `GET /merchant/balance` yang ada (`BalanceOverview`: `balance`, `total_credited`, `total_paid_out`, `total_refunded`).

- [ ] **Step 1: Payment Links** — cards: "Total links", "Active links" (catatan "N inactive"), "Paid via links" (`paid_count` + catatan `formatIDR(paid_amount)`); grafik "Paid via links per day" (`paid_count`, number); donut `breakdown` Aktif (hijau) vs Nonaktif (abu). Muat ulang saat `environment` berubah dan saat link dibuat/diubah/diaktifkan (ikuti mekanisme refresh halaman itu).
- [ ] **Step 2: Balance** — kartu ringkasan yang sudah ada TETAP (jangan diganti); tambahkan di bawahnya (di atas form/tabel) dua card bersebelahan: grafik bertumpuk "Credits vs debits per day" (`credited` hijau, `debited` merah/oranye, idr) dari `fetchBalanceDaily(14)` dan donut "Where your earnings went" dari `BalanceOverview` yang sudah dimuat: Available (`max(balance,0)`), Paid out (`total_paid_out`), Refunded (`total_refunded`) — bila semuanya 0, teks kosong; saldo negatif dijelaskan dengan catatan kecil. Gunakan `StatsOverview` dengan `cards` tidak dipakai (tambahkan dukungan `cards` opsional ke komponen, bila perlu, tanpa mengubah pemakai lain) atau komponen `StatsCharts` yang diekstrak darinya — pilih yang paling kecil perubahannya dan jelaskan di laporan.
- [ ] **Step 3:** verifikasi front-end + pemeriksaan kode (375 px, tidak ada regresi pada form pencairan/tabel).

---

## Self-Review (controller)

- **Cakupan spec:** P1 → Task 1; P2/P3 → Task 2, 4, 6 (tabel §3 satu baris per halaman); endpoint baru §4.2 → Task 3 dan 5; tahap rilis §5 → pemetaan: Tahap 1 = Task 1, Tahap 2 = Task 2, Tahap 3 = Task 3+4, Tahap 4 = Task 5+6.
- **Konsistensi nama:** `StatsOverview` props sama di Task 2/4/6; `BreakdownPoint{Status,Count}` bersama untuk Task 3 dan 5; rute stats dipasang sebelum rute berparameter (dicatat di tiap task).
- **Risiko:** tidak ada uji visual (verifikasi manual user); Task 6 Balance membutuhkan penyesuaian kecil `StatsOverview` (cards opsional) — ditetapkan di Task 6 agar Task 2 tetap sederhana; nilai `status` pada `webhook_events` harus dibaca dari kode saat Task 3.
