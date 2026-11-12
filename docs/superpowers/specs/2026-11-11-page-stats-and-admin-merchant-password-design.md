# Card Summary + Grafik per Halaman dan Password di Add Merchant — Desain

Tanggal: 2026-11-11
Status: keputusan disetujui user; menunggu review spec tertulis. Tanpa commit/push (aturan user).
Pola acuan: halaman admin Merchants (`MerchantStatsSection`, `GET /admin/merchants/stats`).

## 1. Keputusan (disetujui user)

| # | Keputusan |
|---|---|
| P1 | Drawer **Add Merchant** (admin) mendapat isian **Password, wajib, minimal 8 karakter**. Backend membuat merchant **dan akun pemilik** (`merchant_users`, role `owner`), seperti registrasi mandiri. |
| P2 | Halaman-halaman di bawah mendapat **3 card summary**, lalu di bawahnya **dua card bersebelahan**: grafik (area/batang) dan donut. Layout dan gaya seperti halaman Merchants admin; responsive (1 kolom di HP, 2 kolom ≥ `md`). |
| P3 | Isi per halaman mengikuti tabel §3. Data yang sudah ada dipakai ulang; endpoint baru hanya bila perlu. |
| P4 | Dikerjakan **bertahap, rilis per tahap** (§5). |

## 2. Komponen bersama (front-end)

`front/app/components/admin/StatsOverview.tsx` (dipakai halaman admin **dan** merchant; refactor `MerchantStatsSection` agar memakainya tanpa mengubah tampilannya):

```ts
type StatCard = { label: string; value: string; note?: string; tone?: string; noteTone?: string };
type ChartSeries = { key: string; name: string; color: string };   // satu atau lebih seri
type DonutSlice = { name: string; value: number; color: string };
props = {
  cards: StatCard[];                         // tepat 3
  chart: { title: string; subtitle: string; data: Record<string, number|string>[]; xKey: string;
           series: ChartSeries[]; stacked?: boolean; valueFormat?: "number" | "idr"; emptyText: string };
  donut: { title: string; subtitle: string; slices: DonutSlice[]; emptyText: string };
  loading: boolean; error: boolean; onRetry: () => void;
}
```
Perilaku: memuat sendiri (tabel di bawahnya tidak ikut gagal); skeleton/“Loading summary...”; galat dengan tombol Retry; grafik area (1 seri) atau batang bertumpuk (`stacked`); donut dengan legenda berisi angka; `ChartTooltipBox` dari `DashboardCharts`; tinggi `h-60 sm:h-[280px]`; `min-w-0` agar tidak meluber; tanpa gulir horizontal di 375 px.

## 3. Isi per halaman

| Halaman | 3 card | Grafik | Donut | Sumber data |
|---|---|---|---|---|
| Admin Payments | Total payments · Paid amount · Success rate (paid/total) | Paid amount per hari | Status payment | `GET /admin/dashboard/summary` + `/charts` (ada) |
| Admin Payouts | Menunggu proses (jumlah + nominal) · Sudah dibayar (total) · Ditolak | Nominal pengajuan per hari | Status payout | **baru** `GET /admin/payouts/stats` |
| Admin Callbacks | Total pengiriman · Berhasil (%) · Gagal/menunggu retry | Pengiriman per hari (berhasil vs gagal, bertumpuk) | Status callback | **baru** `GET /admin/callbacks/stats` |
| Admin Reconciliation | Payment menunggu ditinjau · Nominal tertahan · Yang tertua (umur) | Nominal tertahan per hari dibuat | Jenis temuan (`check_status`) | dihitung di klien dari `GET /admin/reconciliation` (ada) |
| Admin Logs | Total event · Terproses · Error/ditolak | Event per hari (terproses vs gagal, bertumpuk) | Status event | **baru** `GET /admin/logs/stats` |
| Merchant Payments | Total payments · Paid amount · Success rate | Paid amount per hari | Status payment | `GET /merchant/dashboard/summary` + `/charts` (ada; ikut environment) |
| Merchant Payment Links | Total link · Link aktif · Payment lewat link (dibayar + nominal) | Payment lewat link per hari (jumlah dibayar) | Aktif vs nonaktif | **baru** `GET /merchant/payment-links/stats?environment=` |
| Merchant Balance | card ringkasan yang sudah ada tetap | Kredit vs debit saldo per hari (bertumpuk) | Ke mana uang masuk pergi: tersedia · dicairkan · direfund | **baru** `GET /merchant/balance/daily?days=14` (donut dari `GET /merchant/balance` yang ada) |

## 4. Backend

### 4.1 Password di Add Merchant
- `CreateMerchantRequest` mendapat `Password string \`json:"password"\``. `Validate()` TIDAK mengubah aturan registrasi mandiri (yang membuat request tanpa password); aturan password dipasang di `AdminMerchantService.CreateMerchant`: kosong → `ErrMerchantPasswordRequired`, < 8 karakter → galat panjang minimal (gunakan galat yang sama dengan `RegisterRequest.Validate`), > 72 byte ditolak (batas bcrypt) dengan galat validasi yang jelas.
- Setelah merchant dibuat (e-mail belum dipakai merchant maupun user), hash bcrypt (cost default) dan buat `merchant_users` (`role: owner`, `is_active: true`, nama dan e-mail merchant). Gagal membuat user → hapus merchant yang baru dibuat (pola `RegisterMerchant`) dan kembalikan galat generik.
- `AdminMerchantService` menerima `merchantUserRepo`; `main.go` diperbarui. Handler memetakan galat password ke 400. Password tidak pernah masuk log maupun respons. `openapi.yaml` diperbarui (`password` wajib, minLength 8).

### 4.2 Endpoint statistik baru
Semua hanya admin (`/admin/...`) atau JWT merchant (`/merchant/...`, ID dari sesi), `GET`, tanpa body. Pengelompokan hari: `DATE(created_at)` / `CURRENT_DATE` dan 14 hari terakhir (hari kosong = 0), sama dengan statistik lain. Bentuk umum: `{ days, <total...>, daily: [{date, label, ...angka}], breakdown: [{status, count}] }`; semua angka `int64`, array tidak pernah `null`.

- `GET /admin/payouts/stats` → `{ days, requested_count, requested_amount, paid_count, paid_amount, rejected_count, rejected_amount, daily:[{date,label,count,amount}] (pengajuan per `requested_at`), breakdown:[{status,count}] }`.
- `GET /admin/callbacks/stats` → `{ days, total, success, failed, pending, skipped, daily:[{date,label,success,failed,other}] (per `created_at`), breakdown:[{status,count}] }` (`pending` = pending + sending).
- `GET /admin/logs/stats` → `{ days, total, processed, failed, ignored, daily:[{date,label,processed,failed}], breakdown:[{status,count}] }` dari `webhook_events`. **Definisi (direvisi setelah review Task 3):** `processed` = `is_processed`; `ignored` = event yang sengaja tidak diproses tanpa kesalahan (pesan `processing_error` sama persis dengan salah satu sentinel: webhook bukan settlement, webhook duplikat, payment sudah terminal, event uji — string diambil dari sentinel di kode, bukan di-hardcode di SQL); `failed` = event yang ditolak/gagal sungguhan (`processing_error IS NOT NULL` dan bukan `ignored`, atau `status` rejected/failed). `ignored` hanya di total (bukan di deret harian).
- `GET /merchant/payment-links/stats?environment=` → `{ days, total, active, inactive, fixed, open, paid_count, paid_amount, daily:[{date,label,paid_count,paid_amount}], breakdown:[{status:"active"|"inactive",count}] }`; "paid via link" = payments dengan `payment_link_id` milik merchant itu, status `paid`, environment yang diminta (default sesuai konvensi endpoint merchant lain).
- `GET /merchant/balance/daily?days=14` → `{ days, daily:[{date,label,credited,debited}] }` dari `merchant_ledger` (credited = jumlah entri positif, debited = nilai absolut entri negatif per hari); `days` dibatasi 1..90.
- Semua query agregat tunggal-pass dengan indeks yang ada; tes PostgreSQL untuk kebenaran hitungan dan batas hari; tes router (401 tanpa token / JWT peran salah); `openapi.yaml` diperbarui.

## 5. Tahapan dan rilis
1. **Tahap 1** — Password Add Merchant (backend + drawer).
2. **Tahap 2** — `StatsOverview` + Admin Payments + Merchant Payments (front-end saja).
3. **Tahap 3** — Backend payouts/callbacks/logs stats + halaman Admin Payouts, Callbacks, Logs, Reconciliation.
4. **Tahap 4** — Backend payment-links stats + balance daily + halaman Merchant Payment Links dan Balance.
Setiap tahap: tes backend lolos (`go test ./... -race`, PG sementara), `npm run lint` + `npm run build` bersih, siap dirilis sendiri (urutan rilis: backend dulu, front-end sesudahnya; front-end baru dengan backend lama hanya membuat bagian statistik menampilkan pesan galat dengan Retry).

## 6. Di Luar Cakupan
Filter tanggal/periode pada card dan grafik (tetap 14 hari), ekspor, notifikasi, perubahan skema database, dan perubahan halaman lain.

## 7. Risiko
Admin kini menetapkan password merchant dan menyampaikannya sendiri (tidak ada alur undangan/reset); statistik ledger/payment yang berat bila data sangat besar (mitigasi: satu query agregat tiap bagian, hanya 14 hari untuk deret harian); tampilan tidak bisa diuji otomatis di browser (verifikasi lewat lint/build dan pengecekan manual oleh user).
