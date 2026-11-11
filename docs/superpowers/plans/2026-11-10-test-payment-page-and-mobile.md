# Halaman Test Payment dan Responsive Mobile Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Halaman test payment sendiri (form + QR + status langsung + riwayat) menggantikan tombol `+`, dan seluruh dashboard merchant serta panel admin nyaman dipakai di HP.

**Architecture:** Hanya `front/` (Next.js 16, tanpa perubahan backend). Halaman test memakai fungsi API merchant yang sudah ada dan polling. Responsive dimulai dari cangkang bersama: hook `useSidebar` + sidebar menjadi laci di bawah `lg`, lalu audit halaman per halaman dengan aturan kelas Tailwind yang ditetapkan di plan ini.

**Tech Stack:** Next.js 16.2 / React 19 / TypeScript / Tailwind v4 / shadcn (`components/ui`) / `react-hot-toast` / `lucide-react`. Tidak ada test runner front-end: verifikasi = `npm run lint` + `npm run build` + pemeriksaan kode (lihat Global Constraints).

Spec: `back/docs/superpowers/specs/2026-11-10-test-payment-page-and-mobile-design.md`

## Global Constraints

- **Tanpa commit dan tanpa push:** jangan menjalankan `git commit`, `git add`, `git push`, atau membuat branch (aturan tetap dari user). Semua pekerjaan dibiarkan uncommitted.
- **Hanya `front/`.** Jangan mengubah backend, `docker-compose`, atau file di `back/` selain laporan SDD. Jangan membaca atau mencetak rahasia (`front/.env.local`, token, kunci).
- **Wajib membaca `front/AGENTS.md`** dan dokumen terkait di `front/node_modules/next/dist/docs/` sebelum menulis kode (Next.js ini berbeda dari yang dikenal); sebutkan yang dibaca di laporan.
- **Teks UI bahasa Inggris**; gaya, warna, dan komponen mengikuti yang sudah ada (`Card`, `PageHeader`, `TableShell`, `EmptyState`, `LoadingBlock`, `formatIDR`, `formatDateTime` dari `@/app/components/admin/ui`; `Button` dari `@/components/ui/button`; `toast` dari `react-hot-toast`). Jangan membuat helper baru bila sudah ada.
- **Hanya tata letak untuk bagian responsive:** tanpa perubahan fungsi, warna, atau isi; landing page (`app/components/landing`, `app/page.tsx`) tidak disentuh. Perilaku desktop (≥ `lg`, 1024 px) tidak boleh berubah.
- **Aturan lint repo:** `react-hooks/set-state-in-effect` aktif (jangan memanggil `setState` sinkron di badan `useEffect`; boleh di dalam callback/timer/event handler/setelah `await`). Jangan menonaktifkan aturan lint kecuali sudah ada pola serupa di repo.
- **Nominal test default 2.000**, `customer_name` penanda riwayat persis `Dashboard test`, kunci environment dari `useMerchantEnvironment()`.
- Verifikasi tiap task dari `front/`: `npm run lint` dan `npm run build` bersih; tampilan tidak bisa dilihat di browser oleh agen — katakan itu di laporan dan sertakan hasil pemeriksaan kode per butir.

---

## File Structure

| File | Tanggung jawab |
|---|---|
| `front/app/dashboard/test-payment/page.tsx` (baru) | Halaman test payment (Task 1) |
| `front/app/components/merchant/MerchantSidebar.tsx` | Nav "Test Payment" (Task 1); laci mobile (Task 2) |
| `front/app/dashboard/payments/page.tsx` | Hapus tombol `+` (Task 1) |
| `front/lib/use-sidebar.ts` (baru) | Hook state sidebar desktop-ciut + laci mobile (Task 2) |
| `front/app/components/merchant/{MerchantShell,MerchantSidebar,MerchantHeader}.tsx`, `front/app/components/admin/{AdminShell,AdminSidebar,AdminHeader}.tsx` | Cangkang responsive (Task 2) |
| halaman `front/app/dashboard/**`, publik (`login`, `register`, `pay`, `l`) | Audit responsive merchant (Task 3) |
| halaman `front/app/admin/**` | Audit responsive admin (Task 4) |

---

### Task 1: Halaman Test Payment

**Files:**
- Create: `front/app/dashboard/test-payment/page.tsx`
- Modify: `front/app/components/merchant/MerchantSidebar.tsx` (`navItems`)
- Modify: `front/app/dashboard/payments/page.tsx` (hapus tombol `+`)

**Interfaces:**
- Consumes (sudah ada di `front/lib/merchant-api.ts`): `createMerchantPayment(payload: {amount:number; description:string; payment_method?:string; customer_name?:string; environment?:string; ...}): Promise<MerchantPayment>`, `fetchMerchantPayment(id: string): Promise<MerchantPayment>` (GET `/api/v1/merchant/payments/{id}`), `fetchMerchantPayments(params?: {search?, status?, environment?, limit?, offset?}): Promise<PaginatedPayments>` (`{items, total, limit, offset}`), tipe `MerchantPayment` (`id, reference, amount, fee_amount, total_amount, currency, status, description, customer_name?, environment?, expires_at, paid_at?, created_at, qris_data?, checkout_url?`); `useMerchantEnvironment()` → `{ environment }` (`"sandbox" | "production"`).
- Produces: rute `/dashboard/test-payment`; tidak ada API baru.

- [ ] **Step 1: Baca pola yang dipakai.** Baca `front/app/pay/[reference]/page.tsx` (render QR gambar vs teks, hitung mundur, kartu status), `front/app/components/admin/ConfirmedActionDialog.tsx` (komposisi `AlertDialog` + footer tombol), `front/app/dashboard/balance/page.tsx` (gaya halaman merchant terbaru, guard `useRef`). Ikuti gaya itu.

- [ ] **Step 2: Buat `front/app/dashboard/test-payment/page.tsx`.** Gunakan struktur dan logika berikut (sesuaikan impor/kelas dengan komponen yang ada; perilaku di bawah wajib persis):

```tsx
"use client";

import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import toast from "react-hot-toast";
import { CopyIcon, RefreshCwIcon } from "lucide-react";
import {
  Card,
  EmptyState,
  LoadingBlock,
  PageHeader,
  TableShell,
  formatDateTime,
  formatIDR,
} from "@/app/components/admin/ui";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  createMerchantPayment,
  fetchMerchantPayment,
  fetchMerchantPayments,
  type MerchantPayment,
} from "@/lib/merchant-api";
import { useMerchantEnvironment } from "@/lib/use-merchant-environment";

const TEST_CUSTOMER = "Dashboard test"; // history marker (customer_name is searchable)
const DEFAULT_AMOUNT = "2000";
const DEFAULT_DESCRIPTION = "Test payment";
const MAX_AMOUNT = 1_000_000_000_000; // server limit (fee.MaxNominal)
const POLL_MS = 3000;
const FINAL_STATUSES = new Set(["paid", "expired", "failed", "cancelled", "refunded"]);

function statusStyle(status: string) {
  switch (status) {
    case "paid":
      return "bg-[#e8f8ee] text-[#2f9e5a]";
    case "pending":
      return "bg-[#fff7e6] text-[#c27a00]";
    case "expired":
    case "failed":
      return "bg-[#fff1ed] text-[#e85d3b]";
    default:
      return "bg-[#eef2f6] text-[#6b7c93]";
  }
}

// Digits only, 1..MAX_AMOUNT; null when not a valid integer rupiah amount.
function parseAmount(text: string): number | null {
  if (!/^[0-9]{1,13}$/.test(text)) return null;
  const n = Number(text);
  return n >= 1 && n <= MAX_AMOUNT ? n : null;
}

export default function TestPaymentPage() {
  const { environment } = useMerchantEnvironment();
  const isProduction = environment === "production";

  const [amountText, setAmountText] = useState(DEFAULT_AMOUNT);
  const [description, setDescription] = useState(DEFAULT_DESCRIPTION);
  const [creating, setCreating] = useState(false);
  const creatingRef = useRef(false); // double-click guard (state alone is not synchronous)
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [payment, setPayment] = useState<MerchantPayment | null>(null);
  const [now, setNow] = useState(0); // ms; 0 = not started (set from handlers/timer only)

  const [history, setHistory] = useState<MerchantPayment[]>([]);
  const [historyLoading, setHistoryLoading] = useState(true);
  const [historyError, setHistoryError] = useState<string | null>(null);

  const amount = parseAmount(amountText);
  // A payment from the other environment is hidden (not deleted) after a switch.
  const visible =
    payment && (!payment.environment || payment.environment === environment)
      ? payment
      : null;

  const loadHistory = useCallback(async () => {
    try {
      const res = await fetchMerchantPayments({
        search: TEST_CUSTOMER,
        environment,
        limit: 10,
        offset: 0,
      });
      setHistory(res.items ?? []);
      setHistoryError(null);
    } catch (err) {
      setHistoryError(err instanceof Error ? err.message : "Failed to load test payments.");
    } finally {
      setHistoryLoading(false);
    }
  }, [environment]);

  useEffect(() => {
    void loadHistory();
  }, [loadHistory]);

  // Live status: one interval while the shown payment is pending.
  const paymentId = payment?.id;
  const pending = payment?.status === "pending";
  useEffect(() => {
    if (!paymentId || !pending) return;
    let cancelled = false;
    const poll = window.setInterval(() => {
      void (async () => {
        try {
          const p = await fetchMerchantPayment(paymentId);
          if (cancelled) return;
          // Keep the QR if a poll response omits it; ignore a late reply for another payment.
          setPayment((cur) =>
            cur && cur.id === p.id
              ? { ...cur, ...p, qris_data: p.qris_data ?? cur.qris_data }
              : cur,
          );
        } catch {
          // transient error: keep polling
        }
      })();
    }, POLL_MS);
    const clock = window.setInterval(() => setNow(Date.now()), 1000);
    return () => {
      cancelled = true;
      window.clearInterval(poll);
      window.clearInterval(clock);
    };
  }, [paymentId, pending]);

  // Refresh the history once a payment reaches a final status.
  const status = payment?.status;
  useEffect(() => {
    if (status && FINAL_STATUSES.has(status)) void loadHistory();
  }, [status, loadHistory]);

  async function create() {
    if (creatingRef.current || amount === null) return;
    creatingRef.current = true;
    setCreating(true);
    setError(null);
    try {
      const p = await createMerchantPayment({
        amount,
        description: description.trim() || DEFAULT_DESCRIPTION,
        payment_method: "qris",
        customer_name: TEST_CUSTOMER,
        environment,
      });
      setPayment(p);
      setNow(Date.now());
      void loadHistory();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create payment.");
    } finally {
      creatingRef.current = false;
      setCreating(false);
    }
  }

  function onCreateClick() {
    if (amount === null) return;
    if (isProduction) setConfirmOpen(true);
    else void create();
  }

  async function showQr(row: MerchantPayment) {
    try {
      const full = await fetchMerchantPayment(row.id); // list rows may omit qris_data
      setPayment(full);
      setNow(Date.now());
      setError(null);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to load payment.");
    }
  }

  async function copyReference(ref: string) {
    try {
      await navigator.clipboard.writeText(ref);
      toast.success("Reference copied.");
    } catch {
      toast.error("Could not copy.");
    }
  }

  const remaining = (() => {
    if (!visible || visible.status !== "pending" || now === 0) return null;
    const ms = new Date(visible.expires_at).getTime() - now;
    if (ms <= 0) return "Expired";
    const m = Math.floor(ms / 60000);
    const s = Math.floor((ms % 60000) / 1000);
    return `${m}:${String(s).padStart(2, "0")}`;
  })();
  const isImageQr =
    visible?.qris_data?.startsWith("data:image") || visible?.qris_data?.startsWith("http");
  const hasFee = (visible?.fee_amount ?? 0) > 0;

  // JSX: see Step 3 for the required layout and content.
  return null; // REPLACE with the JSX described in Step 3
}
```

- [ ] **Step 3: JSX (menggantikan `return null` di atas).** Struktur wajib:
  1. `<PageHeader title="Test Payment" description="Create a QRIS payment from the dashboard and watch it get paid." />`.
  2. Bila `isProduction`: banner merah penuh-lebar `role="alert"` bertuliskan **"PRODUCTION: real money. A payment created here must be paid with a real QRIS scan and will credit your balance."** (kelas merah mengikuti palet `#e85d3b` / `bg-[#fff1ed]`). Bila sandbox: baris kecil abu "Sandbox: no real money."
  3. Grid `grid gap-5 lg:grid-cols-2` berisi dua `Card`:
     - **Kiri (form):** input `Amount (IDR)` (`type="text"`, `inputMode="numeric"`, `autoComplete="off"`, nilai `amountText`, `onChange` menyaring ke digit saja: `setAmountText(e.target.value.replace(/\D/g, "").slice(0, 13))`; pesan galat inline "Enter a whole amount between 1 and 1,000,000,000,000." saat `amount === null` dan teks tidak kosong), input `Description` (maks 255 karakter), tombol **Create payment** (`disabled={creating || amount === null}`, `w-full sm:w-auto`, teks "Creating..." saat `creating`), dan `error` inline merah bila ada.
     - **Kanan (hasil):** bila `visible` null → `EmptyState message="No payment yet. Create one to see its QR here."`. Bila ada: baris atas jumlah (`formatIDR(hasFee ? visible.total_amount : visible.amount, visible.currency)`) + badge status (`statusStyle`), baris "Ref · {reference}" dengan tombol salin (ikon `CopyIcon`, `aria-label="Copy reference"`, target ketuk ≥ 36 px), bila `hasFee` rincian `Amount` / `Service fee` / `Total to pay` (pola `dl` seperti di `/pay/[reference]`), bila `status === "pending"` dan `qris_data`: kotak QR (`<img src=... alt="QRIS" className="mx-auto max-h-56 max-w-full rounded-lg">` bila `isImageQr`, selain itu `<pre className="max-h-40 overflow-auto break-all ...">`; pakai komentar `eslint-disable-next-line @next/next/no-img-element` seperti halaman pay) + teks "Expires in {remaining}" bila `remaining`; bila `paid`: kartu hijau "Payment received" + `formatDateTime(paid_at)` + "Amount {formatIDR(amount)} credited to your balance." (hanya teks kredit saldo bila `visible.environment === "production"`); bila `expired`/`failed`/`cancelled`: kartu merah "This payment is {status}."; tombol **New test payment** (`Button variant="outline"`, mengosongkan `payment` dan `error`) dan tautan **Open checkout page** (`<Link href={`/pay/${visible.reference}`} target="_blank" rel="noopener noreferrer">`).
  4. **Riwayat** `Card` penuh-lebar: judul "Recent test payments", tombol segarkan (`RefreshCwIcon`, memanggil `loadHistory`), `LoadingBlock` saat `historyLoading`, pesan galat bila `historyError`, `EmptyState` bila kosong, selain itu `TableShell` berisi tabel `min-w-130` dengan kolom Created (`formatDateTime`), Amount (`formatIDR(total_amount)`), Status (badge), Reference (`font-mono`, `truncate`), dan Action: tombol **Show QR** hanya untuk `status === "pending"` (`onClick={() => void showQr(row)}`).
  5. Dialog konfirmasi production: `<AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>` dengan `AlertDialogContent` > `AlertDialogHeader` (`AlertDialogTitle` "Create a real payment?", `AlertDialogDescription`: "This creates a PRODUCTION payment of {formatIDR(amount ?? 0)}. The exact fee and total are calculated by the server and shown after it is created. It is paid with a real QRIS scan.") > `AlertDialogFooter` dengan dua `Button`: **Cancel** (`variant="outline"`, `onClick={() => setConfirmOpen(false)}`) dan **Create real payment** (`onClick={() => { setConfirmOpen(false); void create(); }}`). Ikuti komposisi footer di `ConfirmedActionDialog.tsx` (lebar tombol penuh di layar kecil).
  Semua kelas tata letak harus bekerja di 375 px (kolom tunggal di bawah `lg`, `min-w-0` pada anak flex yang berisi teks panjang, tidak ada lebar tetap > 320 px).

- [ ] **Step 4: Navigasi.** Di `MerchantSidebar.tsx` impor `FlaskConicalIcon` dari `lucide-react` dan tambahkan ke `navItems` tepat setelah "Payments": `{ label: "Test Payment", href: "/dashboard/test-payment", icon: FlaskConicalIcon }` (komponen ikon menerima `className`; `AdminSidebar` sudah memakai ikon `lucide-react` dengan cara yang sama).

- [ ] **Step 5: Hapus tombol `+`.** Di `front/app/dashboard/payments/page.tsx` hapus: fungsi `handleQuickCreate`, state `creating`/`setCreating`, blok `<Tooltip>` yang membungkus tombol "New test payment" beserta `PlusIcon`, impor `PlusIcon` dan `createMerchantPayment` yang kini tak terpakai. Pertahankan `TooltipProvider` dan tombol Refresh/Export. Pastikan lint tidak melaporkan impor/variabel tak terpakai.

- [ ] **Step 6: Verifikasi.** Dari `front/`: `npm run lint`, `npm run build` (rute `/dashboard/test-payment` muncul di tabel rute). Pemeriksaan kode (tulis hasil per butir di laporan): (a) default amount `2000`; (b) hanya digit yang bisa dimasukkan; (c) `creatingRef` diperiksa sebelum `await` dan di-reset di `finally`; (d) di production tombol Create tidak memanggil `create()` langsung (hanya dialog yang memanggilnya); (e) timer polling dan jam dibersihkan di cleanup dan hanya berjalan saat `pending`; (f) tidak ada `setState` sinkron di badan effect; (g) `customer_name: "Dashboard test"` dan pencarian riwayat memakai konstanta yang sama; (h) `grep -rn "handleQuickCreate\|PlusIcon" front/app` tidak menemukan sisa di halaman Payments.

---

### Task 2: Cangkang responsive (sidebar laci) merchant dan admin

**Files:**
- Create: `front/lib/use-sidebar.ts`
- Modify: `front/app/components/merchant/{MerchantShell,MerchantSidebar,MerchantHeader}.tsx`
- Modify: `front/app/components/admin/{AdminShell,AdminSidebar,AdminHeader}.tsx`

**Interfaces:**
- Produces: `useSidebar(): { collapsed: boolean; mobileOpen: boolean; toggle: () => void; closeMobile: () => void }` — `collapsed` hanya bernilai `true` di desktop (≥ 1024 px); `mobileOpen` hanya `true` di bawah 1024 px. Sidebar menerima props `collapsed: boolean; mobileOpen: boolean; onNavigate: () => void`. Header tetap menerima `{ sidebarCollapsed, onMenuClick }` (tanpa perubahan kontrak).

- [ ] **Step 1: Hook** `front/lib/use-sidebar.ts`:

```ts
"use client";

import { useCallback, useEffect, useState, useSyncExternalStore } from "react";

const DESKTOP_QUERY = "(min-width: 1024px)"; // Tailwind `lg`

function subscribe(onChange: () => void) {
  const mq = window.matchMedia(DESKTOP_QUERY);
  mq.addEventListener("change", onChange);
  return () => mq.removeEventListener("change", onChange);
}

// Server snapshot is `true`: the drawer is hidden by CSS below `lg` regardless,
// so SSR markup stays identical to the first client render.
function useIsDesktop() {
  return useSyncExternalStore(
    subscribe,
    () => window.matchMedia(DESKTOP_QUERY).matches,
    () => true,
  );
}

/**
 * Sidebar state shared by the merchant and admin shells. Desktop (>= lg): the
 * button collapses the sidebar to icons. Below lg: it opens an overlay drawer.
 */
export function useSidebar() {
  const isDesktop = useIsDesktop();
  const [collapsedPref, setCollapsedPref] = useState(false);
  const [mobileOpenPref, setMobileOpenPref] = useState(false);

  // Derived, so resizing across the breakpoint never leaves stale state behind.
  const collapsed = collapsedPref && isDesktop;
  const mobileOpen = mobileOpenPref && !isDesktop;

  const toggle = useCallback(() => {
    if (isDesktop) setCollapsedPref((v) => !v);
    else setMobileOpenPref((v) => !v);
  }, [isDesktop]);
  const closeMobile = useCallback(() => setMobileOpenPref(false), []);

  // While the drawer is open: Escape closes it and the page behind cannot scroll.
  useEffect(() => {
    if (!mobileOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setMobileOpenPref(false);
    };
    document.addEventListener("keydown", onKey);
    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.removeEventListener("keydown", onKey);
      document.body.style.overflow = previous;
    };
  }, [mobileOpen]);

  return { collapsed, mobileOpen, toggle, closeMobile };
}
```

- [ ] **Step 2: Shell (merchant, lalu admin dengan pola identik).** Ganti `useState` collapsed dengan hook dan tambahkan backdrop:

```tsx
"use client";

import { useSidebar } from "@/lib/use-sidebar";
import MerchantHeader from "./MerchantHeader";
import MerchantSidebar from "./MerchantSidebar";

export default function MerchantShell({ children }: { children: React.ReactNode }) {
  const { collapsed, mobileOpen, toggle, closeMobile } = useSidebar();

  return (
    <div className="admin-root flex min-h-screen bg-[#eff4f8] text-[#1f2a37]">
      <MerchantSidebar collapsed={collapsed} mobileOpen={mobileOpen} onNavigate={closeMobile} />
      {mobileOpen && (
        <div
          className="fixed inset-0 z-40 bg-[#06163a]/50 lg:hidden"
          onClick={closeMobile}
          aria-hidden="true"
        />
      )}
      <div className="flex min-w-0 flex-1 flex-col">
        <MerchantHeader sidebarCollapsed={collapsed} onMenuClick={toggle} />
        <main className="min-w-0 flex-1 p-4 sm:p-5 lg:p-6">{children}</main>
      </div>
    </div>
  );
}
```
`AdminShell.tsx` sama persis dengan `AdminSidebar`/`AdminHeader`.

- [ ] **Step 3: Sidebar (kedua berkas).** Props menjadi `{ collapsed: boolean; mobileOpen: boolean; onNavigate: () => void }`. Ganti kelas `<aside>` dengan (laci di bawah `lg`, in-flow sticky di `lg`):

```tsx
<aside
  className={`admin-sidebar fixed inset-y-0 left-0 z-50 flex h-screen w-65 shrink-0 flex-col bg-[#06163a] text-white shadow-xl transition-[transform,width] duration-200 ease-in-out lg:sticky lg:top-0 lg:z-40 lg:translate-x-0 lg:shadow-none ${
    mobileOpen ? "translate-x-0" : "-translate-x-full"
  } ${collapsed ? "lg:w-18" : "lg:w-65"}`}
  aria-label="Sidebar"
>
```
Tambahkan `onClick={onNavigate}` pada setiap `<Link>` item nav (menutup laci saat pindah halaman; di desktop `onNavigate` tidak berefek karena `mobileOpen` selalu `false`), dan `aria-label="Main navigation"` pada `<nav>`. Logika `collapsed` (label disembunyikan, tooltip) tetap, karena hook menjamin `collapsed === false` di mobile. Pastikan tidak ada kelas `w-18`/`w-65` tanpa awalan `lg:` yang tersisa selain `w-65` dasar laci.

- [ ] **Step 4: Header (kedua berkas).** Pada layar sempit pastikan header tidak meluber: baca isi header; (a) tombol menu: `aria-label` tetap, target ketuk `h-9 w-9` (sudah), tambahkan `aria-expanded` bila mudah (boleh dilewati tanpa mengubah props); (b) kelompok kanan: `min-w-0`, jarak `gap-2` di mobile, elemen teks nama/email pengguna tetap `hidden sm:block` (sudah), pemilih environment dan lonceng tidak boleh membuat lebar total header > 100vw pada 375 px (ciutkan label/ikon dengan `hidden sm:inline` bila perlu); (c) dropdown profil dan panel notifikasi: lebar `w-60`/`w-56` dengan `right-0` harus muat (`max-w-[calc(100vw-1rem)]` ditambahkan). Tidak mengubah warna atau fungsi.

- [ ] **Step 5: Verifikasi.** `npm run lint` dan `npm run build` bersih. Pemeriksaan kode (hasil per butir di laporan): (a) di `<aside>` tidak ada kelas tata letak desktop yang berubah (desktop memakai `lg:sticky lg:w-65/lg:w-18` setara dengan sebelumnya); (b) kedua shell memakai `useSidebar` dan tidak ada `useState` collapsed tersisa; (c) backdrop `lg:hidden` dan `z-40` di bawah sidebar `z-50`, header `z-30`; (d) tidak ada `setState` sinkron di effect hook; (e) `grep -rn "sidebarCollapsed" front/app` tetap konsisten dengan prop header; (f) laci menutup lewat: klik tautan, klik backdrop, Escape; (g) scroll lock dipulihkan di cleanup.

---

### Task 3: Audit responsive halaman merchant dan publik

**Files:** semua `front/app/dashboard/**/page.tsx` (api-docs, api-keys, balance, dashboard utama, payment-links + `new` + `[id]`, payments + `[id]`, profile, reports, settings, test-payment, webhooks), `front/app/login`, `front/app/register`, `front/app/pay/[reference]`, `front/app/l/[slug]`, serta komponen yang dipakainya (`front/app/components/merchant/*`, `front/app/components/admin/{ui.tsx,FilterDropdown.tsx,PagerBar.tsx,SignOutConfirmModal.tsx}` bila perlu).

**Interfaces:** Consumes: cangkang dari Task 2. Produces: tidak ada API baru.

**Aturan responsive (berlaku untuk Task 3 dan 4).** Terapkan hanya bila halaman melanggar; jangan mengubah yang sudah benar:
- **R1 Tidak ada gulir horizontal halaman pada 375 px.** Anak flex/grid yang memuat teks panjang diberi `min-w-0`; teks panjang (referensi, email, URL, kunci) `truncate` atau `break-all`/`break-words`.
- **R2 Bar filter/aksi:** `flex flex-col gap-3 sm:flex-row sm:flex-wrap`; tiap kontrol `w-full sm:w-<n>` (pola yang sudah dipakai di Payments, pertahankan).
- **R3 Kelompok tombol:** `flex flex-wrap gap-2`; tombol utama di form/dialog `w-full sm:w-auto`; aksi ikon-saja minimal `size-9` (36 px).
- **R4 Tabel:** selalu di dalam `TableShell` (`overflow-x-auto`) dengan `min-w-*` sesuai jumlah kolom; jangan memaksa tabel mengecil.
- **R5 Grid:** kolom tunggal dulu: `grid-cols-1 sm:grid-cols-2 lg:grid-cols-N`; grid dengan `grid-cols-2`/`grid-cols-3` tanpa awalan hanya boleh bila isinya pendek (angka/tombol) dan terbukti muat.
- **R6 Dialog/drawer/popover:** lebar `w-full max-w-[calc(100vw-2rem)]` (atau `max-w-md` dengan margin), konten panjang `max-h-[85dvh] overflow-y-auto`, tombol footer ditumpuk di layar sempit (`flex-col-reverse sm:flex-row`).
- **R7 Target ketuk ≥ 36 px** untuk tombol/tautan interaktif (baris tabel yang bisa diketuk, ikon aksi, tab).
- **R8 Padding halaman** tetap `p-4 sm:p-5 lg:p-6` (shell); kartu `p-4 sm:p-5`.
- **R9 Elemen dekoratif** absolut yang keluar viewport (blur/lingkaran) harus berada di induk `overflow-hidden`.

**Daftar awal temuan (hasil pemindaian; audit tetap menyeluruh):**
- `dashboard/payment-links/new/page.tsx:157` `grid grid-cols-2 gap-3` → `grid-cols-1 sm:grid-cols-2` bila isinya input teks.
- `dashboard/page.tsx:182` tabel `min-w-180`, `dashboard/payment-links/[id]/page.tsx:379` `min-w-175`, `dashboard/api-keys/page.tsx:203` `min-w-160`, `dashboard/balance/page.tsx:390,475` `min-w-150`, `dashboard/api-docs/page.tsx:119,160`: pastikan semuanya berada di dalam `TableShell`/`overflow-x-auto`.
- `dashboard/balance/page.tsx:341` `sm:w-64` label: pastikan `w-full` di mobile.
- `dashboard/reports/page.tsx`: tabel harian 9 kolom (sudah `min-w-225` dalam `TableShell`), batang status dan kartu ringkasan.
- `components/admin/dashboard/TrafficChart.tsx:26` `grid-cols-3`: periksa teks muat.
- `components/admin/SignOutConfirmModal.tsx:71` footer `grid-cols-2`: dua tombol pendek, periksa muat.

- [ ] **Step 1: Audit dan perbaiki per halaman.** Untuk setiap halaman dalam daftar Files: baca berkasnya penuh, cocokkan dengan R1–R9 pada lebar 375 px (bayangkan `min-width` 320 px), perbaiki pelanggaran dengan perubahan kelas Tailwind minimal. Catat di laporan satu baris per halaman: "OK, tidak ada perubahan" atau daftar perubahan (kelas sebelum → sesudah, `berkas:baris`).
- [ ] **Step 2: Halaman publik.** `login`, `register`: kartu form `w-full max-w-md` dengan padding horizontal; `pay/[reference]`: sudah `max-w-md` — periksa teks panjang referensi (`break-all`), QR `max-w-full`; `l/[slug]`: form checkout (input/tombol penuh-lebar, grid input kolom tunggal di mobile).
- [ ] **Step 3: Dialog dan drawer yang dipakai merchant** (`components/ui/{alert-dialog,drawer,popover,dropdown-menu,date-picker}.tsx`, `MerchantNotificationsDrawer`, `SignOutConfirmModal`): terapkan R6 (lebar maks viewport, scroll internal) bila belum; perubahan kecil dan hanya kelas.
- [ ] **Step 4: Verifikasi.** `npm run lint` dan `npm run build` bersih; `git diff --stat` (hanya baca, tanpa `git add`) menunjukkan hanya berkas di bawah `front/app` dan `front/components`; laporan memuat tabel audit per halaman dan pernyataan bahwa perubahan hanya kelas tata letak. Jalankan `grep -rn "grid-cols-[2-9]" front/app/dashboard front/app/login front/app/register front/app/pay front/app/l` dan jelaskan setiap sisa.

---

### Task 4: Audit responsive halaman admin

**Files:** semua `front/app/admin/**` (`login`, `(panel)/page.tsx`, `callbacks`, `logs` + `[id]`, `merchants` + `[id]`, `payments` + `[id]`, `payouts`, `profile`, `providers` + `[name]`, `reconciliation`, `routing`, `settings`), dan komponen admin yang dipakai (`front/app/components/admin/{AdminHeader,CreateMerchantDrawer,CreatePaymentDrawer,EditMerchantDrawer,FilterDropdown,MerchantAPIKeysSection,MerchantProviderConfigSection,NotificationsDrawer,PagerBar,ConfirmedActionDialog,DashboardCharts,dashboard/*}.tsx`).

**Interfaces:** Consumes: cangkang Task 2 dan aturan R1–R9 dari Task 3. Produces: tidak ada API baru.

**Daftar awal temuan (pemindaian):**
- `admin/login/page.tsx:156,160` lingkaran dekoratif absolut `h-72 w-72` / `h-80 w-80` → pastikan induk `overflow-hidden` (R9).
- `admin/(panel)/profile/page.tsx:409` tombol `min-w-42` → `w-full sm:w-auto` di mobile.
- `admin/(panel)/{payments,merchants,logs,callbacks}/page.tsx` bar filter `w-full sm:w-*`: sudah baik, verifikasi bar tombol aksi dan pager.
- Drawer buat merchant/payment/edit, `ConfirmedActionDialog` (tombol footer), `NotificationsDrawer`, `MerchantAPIKeysSection` (tabel kunci), `MerchantProviderConfigSection` (tabel konfigurasi).
- Halaman detail (`merchants/[id]`, `payments/[id]`, `logs/[id]`, `providers/[name]`): grid dua kolom → satu kolom di mobile; blok kode/JSON `overflow-x-auto`.

- [ ] **Step 1: Audit dan perbaiki per halaman** dengan prosedur Task 3 Step 1 (aturan R1–R9; perubahan kelas minimal; satu baris laporan per halaman).
- [ ] **Step 2: Drawer/dialog admin** menerapkan R6 (lebar maks viewport, scroll internal, footer ditumpuk).
- [ ] **Step 3: Verifikasi.** `npm run lint`, `npm run build` bersih; laporan memuat tabel audit per halaman; `git diff --stat` (hanya baca) hanya memuat berkas `front/app` dan `front/components`; setiap sisa `grid-cols-[2-9]` tanpa awalan dijelaskan.

---

## Self-Review (controller)

- **Cakupan spec:** T1–T3 → Task 1 (halaman, default 2.000, hapus `+`, nav); T4 → Task 1 Step 2–3 (banner + dialog production); M1 → Task 2–4 (cangkang merchant dan admin, semua halaman, publik); verifikasi tanpa browser → Global Constraints + Step verifikasi tiap task; "tanpa commit" → Global Constraints.
- **Konsistensi nama:** `useSidebar` (Task 2) dipakai kedua shell; props sidebar `collapsed/mobileOpen/onNavigate` konsisten antar Merchant/Admin; `TEST_CUSTOMER` dipakai untuk pembuatan dan pencarian riwayat; aturan R1–R9 didefinisikan di Task 3 dan dirujuk Task 4.
- **Risiko:** tidak ada uji visual otomatis (hasil akhir dicek user di HP); Task 3 dan 4 adalah audit sehingga hasilnya bergantung pada ketelitian pembacaan kode — reviewer diminta memeriksa setiap halaman terhadap R1–R9; tombol `+` dihapus sehingga satu-satunya jalur uji payment dari dashboard adalah halaman baru.
