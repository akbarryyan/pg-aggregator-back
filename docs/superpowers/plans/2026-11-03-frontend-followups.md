# Front-end Follow-up Paket 1 & 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Selaraskan front-end dengan perilaku backend Paket 1 & 2: dokumentasi API merchant yang akurat, dan pilihan environment di form "Create payment" admin.

**Architecture:** Dua perubahan terpisah di `front/` (Next.js 16, React 19, TypeScript, Tailwind): konten halaman docs (`api-docs/page.tsx`) dan satu kontrol baru di `CreatePaymentDrawer.tsx` plus type payload di `lib/admin-api.ts`.

**Tech Stack:** Next.js 16.2.10, React 19.2, TypeScript 5, Tailwind. Tanpa test runner; verifikasi dengan `npm run lint` dan `npm run build`.

**Spec:** [`back/docs/superpowers/specs/2026-11-03-frontend-followups-design.md`](../specs/2026-11-03-frontend-followups-design.md)

## Global Constraints

- **NO git commits, NO `git add`, NO branches** (instruksi eksplisit user). Tiap task diakhiri "report changed files".
- Jangan pernah mencetak secret (`back/.config.toml`, `back/db_password`, `back/.env`, `.env*` di `front/`).
- **Wajib** membaca dokumentasi yang relevan di `front/node_modules/next/dist/docs/` sebelum menulis kode (`front/AGENTS.md`: versi Next.js ini punya perubahan breaking). Catat apa yang dibaca di laporan. Bila `node_modules` tidak ada, jalankan `npm ci` di `front/` (jangan ubah `package.json`/`package-lock.json`).
- Tanpa dependensi baru. Ikuti gaya yang ada (kelas Tailwind, komponen `Section`/`ParamTable`/`StatusCodeTable`/`CodeBlock`, bahasa Inggris untuk teks UI dan docs).
- Pesan error backend yang ditampilkan lewat `toast` sudah ada; jangan menambah penanganan error baru.
- Verifikasi akhir: `npm run lint` dan `npm run build` dari `front/` tanpa error baru (laporkan peringatan yang sudah ada sebelumnya apa adanya). Bila dev server dijalankan untuk pemeriksaan visual, pakai port yang belum dipakai dan jangan ganggu proses/port milik proyek lain user; bila tidak dijalankan, katakan jujur.
- Abaikan `back/.superpowers/`.

## File Structure

| File | Perubahan |
|---|---|
| `front/app/dashboard/api-docs/page.tsx` | konten docs: callback_url, 400, lifecycle, webhooks |
| `front/lib/admin-api.ts` | `CreatePaymentPayload.environment` |
| `front/app/components/admin/CreatePaymentDrawer.tsx` | state, kontrol, dan payload environment |

---

### Task 1: Pilihan environment di form "Create payment" admin

**Files:** `front/lib/admin-api.ts`, `front/app/components/admin/CreatePaymentDrawer.tsx`

- [ ] **Step 1: Type payload** — di `front/lib/admin-api.ts`, pada `CreatePaymentPayload` tambahkan setelah `callback_url?: string;`:

```ts
  // Empty means sandbox on the server: a missing value must never create a
  // real-money payment. The admin form always sends it explicitly.
  environment?: "sandbox" | "production";
```

- [ ] **Step 2: State** — di `CreatePaymentDrawer.tsx`, setelah `const [expiresIn, setExpiresIn] = useState("30");` tambahkan:

```tsx
  const [environment, setEnvironment] = useState<"sandbox" | "production">(
    "sandbox",
  );
```

dan di `resetForm()` tambahkan `setEnvironment("sandbox");` setelah `setExpiresIn("30");`.

- [ ] **Step 3: Payload** — pada pemanggilan `createAdminPayment({...})` di `handleSubmit`, tambahkan `environment,` setelah `currency: "IDR",`.

- [ ] **Step 4: Kontrol** — sisipkan blok berikut **tepat sebelum** `<div>` yang berisi label "Expires in (minutes)" (setelah blok "Customer name / Customer email"):

```tsx
            <div>
              <label
                htmlFor="payment-environment"
                className="mb-1.5 block text-[13px] font-medium text-[#3d4b5c]"
              >
                Environment
              </label>
              <select
                id="payment-environment"
                value={environment}
                onChange={(e) =>
                  setEnvironment(e.target.value as "sandbox" | "production")
                }
                className="h-10 w-full rounded-lg border border-[#e8eef4] bg-[#f8fafc] px-3 text-[13.5px] text-[#1f2a37] outline-none focus:border-[#3b9eff] focus:bg-white focus:ring-2 focus:ring-[#3b9eff]/15"
              >
                <option value="sandbox">Sandbox (test, no real money)</option>
                <option value="production">Production (real payment)</option>
              </select>
              {environment === "production" && (
                <p className="mt-2 rounded-lg border border-[#ffe1b3] bg-[#fff8ec] px-3 py-2 text-[12.5px] text-[#8a5a1f]">
                  Production payments go through the real payment provider and
                  move real money.
                </p>
              )}
            </div>
```

- [ ] **Step 5: Verifikasi** — dari `front/`: `npm run lint` dan `npm run build`. Pastikan tidak ada error TypeScript baru. Bila memungkinkan, jalankan dev server di port bebas dan periksa bahwa drawer menampilkan pilihan, default Sandbox, peringatan muncul hanya untuk Production, dan form direset ke Sandbox setelah ditutup; bila tidak dijalankan, katakan jujur.
- [ ] **Step 6: Report** file yang berubah (tanpa commit).

---

### Task 2: Perbarui halaman API docs merchant

**Files:** `front/app/dashboard/api-docs/page.tsx`

Semua string di bawah adalah teks persis. Baca file dulu; ubah hanya yang disebut.

- [ ] **Step 1: `callback_url`** — pada baris tabel parameter `callback_url` (bagian Create Payment) ganti `description` menjadi:

```
"Per-payment webhook override. Falls back to the webhook URL set in Dashboard → Settings if omitted. Must be https and a public host; sandbox payments may also use plain http. localhost, private or internal addresses, credentials in the URL, and our own domains are rejected with 400."
```

- [ ] **Step 2: Error table** — pada `StatusCodeTable` bagian Error Format, ganti `description` baris `400` menjadi:

```
"Invalid request body or validation failure (bad amount, missing description, invalid callback_url, etc)."
```

- [ ] **Step 3: Status Lifecycle** — ganti `description` Section `lifecycle` menjadi:

```
"A payment normally ends in one terminal status. The one exception: if the customer pays after expires_at, an expired payment can still become paid."
```

dan di array baris status ganti dua entri:

```tsx
            { s: "paid", d: "Confirmed paid by the provider. Terminal. Check late_payment in the webhook: true means it was paid after the payment had already expired." },
            { s: "expired", d: "Not paid before expires_at. Terminal, except that a late payment can still turn it into paid." },
```

Setelah `</div>` grid status (di dalam Section lifecycle) tambahkan:

```tsx
        <div className="rounded-lg border border-[#ffe1b3] bg-[#fff8ec] px-3.5 py-3 text-[12.5px] text-[#8a5a1f]">
          <strong>Don&apos;t treat expired as final.</strong> You may receive{" "}
          <code className="font-mono">payment.expired</code> and later{" "}
          <code className="font-mono">payment.paid</code> with{" "}
          <code className="font-mono">late_payment: true</code>. Decide in
          advance whether to fulfil the order or refund the customer.
        </div>
```

- [ ] **Step 4: Webhooks — deskripsi** — ganti `description` Section `webhooks` menjadi:

```
"Set your webhook URL in Dashboard → Settings (or override per-payment with callback_url). We POST here whenever a payment's status changes. The webhook URL must be https and a public host."
```

- [ ] **Step 5: Webhooks — payload** — pada contoh payload JSON tambahkan baris `  "late_payment": false,` setelah `"provider_reference": "INV-9921",`.

- [ ] **Step 6: Webhooks — event & pengiriman** — setelah paragraf daftar `event` (yang berakhir `payment.cancelled`.) tambahkan paragraf:

```tsx
        <p className="text-[13px] text-[#3d4b5c]">
          <code className="font-mono">late_payment</code> is{" "}
          <code className="font-mono">true</code> only on a{" "}
          <code className="font-mono">payment.paid</code> that arrives after
          the payment had expired. A{" "}
          <code className="font-mono">payment.paid</code> can therefore
          follow a <code className="font-mono">payment.expired</code> for
          the same payment.
        </p>
```

Lalu ganti paragraf "Respond with any 2xx ..." dengan:

```tsx
        <p className="text-[13px] text-[#3d4b5c]">
          Respond with any <code className="font-mono">2xx</code> status to
          acknowledge. Failed deliveries (non-2xx, timeout, connection
          refused) retry automatically — 5 attempts total, 5 minutes apart —
          visible any time under{" "}
          <code className="font-mono">Dashboard → Webhook Logs</code>.
        </p>

        <ul className="list-disc space-y-1 pl-5 text-[13px] text-[#3d4b5c]">
          <li>Callbacks are sent shortly after the status change, usually within about 5 seconds, not inline with the payment request.</li>
          <li>Delivery is <strong>at-least-once</strong>: the same event can arrive more than once. De-duplicate on <code className="font-mono">payment_id</code> + <code className="font-mono">event</code> and make your handler idempotent.</li>
          <li>Redirects (3xx) are <strong>not followed</strong> and count as a failed delivery — register the final URL.</li>
          <li>Your endpoint must be reachable on the public internet over https; URLs pointing at localhost or private networks are rejected.</li>
        </ul>
```

- [ ] **Step 7: Webhooks — contoh Node.js** — ganti blok `const { event, status } = req.body; ... }` dalam contoh dengan:

```
  const { event, status, payment_id, late_payment } = req.body;

  // Delivery is at-least-once: skip events you already handled.
  if (await alreadyProcessed(payment_id, event)) return res.status(200).send('OK');

  if (event === 'payment.paid' && status === 'paid') {
    // mark the order paid in your own database; if late_payment is true the
    // payment arrived after it expired — fulfil or refund per your policy
  }

  await markProcessed(payment_id, event);
```

(`alreadyProcessed` dan `markProcessed` adalah pembantu ilustratif; tambahkan satu baris komentar di atasnya: `// alreadyProcessed / markProcessed: your own de-duplication store`.) Pastikan sisa contoh (verifikasi signature, `res.status(200).send('OK')` di akhir) tetap utuh dan template literal tidak rusak.

- [ ] **Step 8: Verifikasi** — `npm run lint` dan `npm run build` dari `front/`. Periksa bahwa halaman `/dashboard/api-docs` tidak error saat dirender (build berhasil; bila dev server dijalankan, buka halaman dan pastikan blok baru tampil). Cari sisa teks usang: `grep -n "terminal state\|Terminal\." app/dashboard/api-docs/page.tsx` dan pastikan tidak ada klaim "never changes again".
- [ ] **Step 9: Report** file yang berubah (tanpa commit).
