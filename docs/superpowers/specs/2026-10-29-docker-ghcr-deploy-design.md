# Design: Docker (Lokal + VPS), GHCR, HTTPS via Caddy, Rate Limit di Config

- **Tanggal:** 2026-10-29
- **Status:** Disetujui (brainstorming), menunggu review spec
- **Cakupan repo:** `back/` (utama), dokumen root workspace (`README.md`, `RUNNING.md`)
- **Bergantung pada:** [2026-10-29-toml-config-design.md](2026-10-29-toml-config-design.md)
  (config TOML-only, tanpa default, key tak dikenal ditolak, `cmd/configenv`)

## 1. Latar Belakang & Tujuan

Backend belum memakai Docker. Development lokal berjalan native (`make run` /
`make dev` dengan `air`, Postgres native), dan belum ada jalur deploy.

**Tujuan:**

1. Backend berjalan di Docker **di lokal dan di VPS dengan perintah yang sama**:
   `docker compose up -d`. Hot reload tetap ada di lokal.
2. Image produksi multi-arch (amd64 + arm64) di-build dan di-push dari laptop ke
   GHCR private: `ghcr.io/akbarryyan/whuzpay-backend:<versi>`.
3. VPS (AWS EC2 `t3.micro`, Ubuntu 26.04, IP `13.48.71.251`) menjalankan
   Caddy (HTTPS otomatis untuk `api.whuzpay.web.id`) + API + Postgres.
4. Pengaturan rate limiter dipindah ke `.config.toml`, termasuk daftar proxy
   tepercaya sehingga rate limit tetap per-pengunjung di belakang Caddy.

**Kriteria sukses:**

1. Lokal: `docker compose up -d` → API di `http://localhost:8090`, perubahan
   file `.go` memicu rebuild otomatis; `make migrate`, `make seed`,
   `flow-test/run_all.sh` (sandbox) lulus.
2. `make release VERSION=1.0.0` menghasilkan satu tag multi-arch di GHCR.
3. VPS: `https://api.whuzpay.web.id/api/v1/health` → `{"status":"ok"}`,
   `http://` di-redirect ke `https://`, port 5432/8080 tidak terjangkau dari
   internet.
4. Dua pengunjung berbeda di belakang Caddy mendapat bucket rate limit
   masing-masing; header `X-Forwarded-For` palsu tidak bisa mengakali limiter.
5. Seluruh test Go tetap lulus; test baru mencakup rate limiter dan
   `[rate_limit]`.

## 2. Keputusan

| # | Keputusan | Alasan |
|---|---|---|
| D1 | VPS: docker compose; Postgres juga container di compose yang sama | Pilihan user |
| D2 | Migration dijalankan manual dengan `make migrate` (lokal & VPS), bukan otomatis saat start | Pilihan user |
| D3 | Image multi-arch `linux/amd64,linux/arm64`, satu tag versi, tanpa `latest` | VPS bisa x86 atau ARM; tag diganti eksplisit di VPS |
| D4 | Package GHCR **private**; VPS login dengan PAT `read:packages` | Pilihan user; image berisi binary, skema, dan seed demo |
| D5 | HTTPS sekarang via **Caddy** di compose (Let's Encrypt otomatis), hanya Caddy yang membuka port 80/443 | Webhook Cashi & frontend HTTPS butuh API HTTPS |
| D6 | **Satu image** untuk `api` dan `migrate` (runtime alpine + bash + psql) | Satu tag yang diganti saat deploy |
| D7 | Proxy tepercaya dikonfigurasi eksplisit (CIDR) di config; XFF hanya dipercaya dari proxy itu | Tidak ada kepercayaan implisit ke jaringan privat |
| D8 | Semua pengaturan rate limiter (`trusted_proxies`, rpm, burst per kelas) ada di `.config.toml` | Pilihan user |
| D9 | Semua file Docker langsung di `back/` (tanpa folder `deploy/`); nama file `docker-compose.yaml` | Pilihan user |
| D10 | Lokal juga pakai `docker compose up -d`; `make run`/`make dev` dihapus; hot reload via `air` di dalam container | Pilihan user |
| D11 | Perbedaan lokal vs VPS lewat `docker-compose.override.yaml` (otomatis dibaca Compose, hanya ada di repo/laptop, tidak disalin ke VPS) | Satu perintah yang sama di dua tempat |
| D12 | Admin pertama via `make seed-admin` (`seeds/002_admin_user.sql`, password `Admin123!`), **menolak jalan jika tabel `admins` sudah berisi**; password wajib langsung diganti | Pilihan user; guard mencegah reset password karena seed itu upsert |
| D13 | Seed demo (`make seed`) hanya boleh di lokal; ditolak di VPS | 50 merchant palsu + kredensial publik tidak boleh masuk produksi |
| D14 | Alur rilis: `make release VERSION=x.y.z` di laptop → edit tag di `docker-compose.yaml` VPS → `docker compose up -d` (+ `make migrate` bila perlu) | Alur user |

## 3. Di Luar Cakupan

- Deploy frontend (`front/`) dan record DNS untuk frontend.
- CI/CD build & push otomatis (GitHub Actions); build dilakukan manual di laptop.
- Backup terjadwal / restore otomatis Postgres (hanya perintah `pg_dump` manual di `DEPLOY.md`).
- HTTP/3 (UDP 443), monitoring, alerting.
- Interval/idle cleanup bucket rate limiter (tetap hardcoded 10 menit / 30 menit).
- Membuat repo/akun AWS/DNS (sudah dilakukan user).
- Masalah SERVFAIL DNS `whuzpay.web.id` (aktivasi zona di idwebhost — urusan user/registrar).

## 4. File

```
back/
├── Dockerfile                  # baru — 3 stage: build, runtime, dev
├── .dockerignore               # baru
├── docker-compose.yaml         # baru — dipakai VPS (dan lokal sebagai base)
├── docker-compose.override.yaml# baru — hanya lokal (otomatis dibaca Compose)
├── Caddyfile                   # baru
├── DEPLOY.md                   # baru — panduan VPS
├── scripts/seed-admin.sh       # baru
├── scripts/lib/db.sh           # baru — load_config (fallback configenv) + psql_cmd, dipakai 3 script
├── scripts/migrate.sh          # ubah — pakai scripts/lib/db.sh
├── scripts/seed.sh             # ubah — pakai scripts/lib/db.sh
├── scripts/db-create.sh        # HAPUS — DB dibuat container Postgres (POSTGRES_DB)
├── Makefile                    # tulis ulang — semua target lewat Docker
├── .air.toml                   # TIDAK diubah — dipakai apa adanya oleh air di container (cwd /src)
├── .gitignore                  # tambah db_password
├── .config.example.toml        # ubah — template lokal Docker + [rate_limit]
├── internal/config/config.go   # ubah — RateLimitConfig + validasi
├── internal/middleware/rate_limit.go  # ubah — trusted proxies
├── cmd/api/main.go             # ubah — limiter dari config
└── README.md                   # ubah
README.md, RUNNING.md (root)    # ubah — alur lokal Docker
```

File yang hanya ada di VPS (`~/whuzpay/`): salinan `docker-compose.yaml`,
`Caddyfile`, `Makefile`, plus `.config.toml` dan `db_password` yang dibuat di
VPS (tidak pernah masuk git).

## 5. Dockerfile (3 stage)

1. **`build`** — `FROM --platform=$BUILDPLATFORM golang:1.25-alpine`;
   `go mod download`; build `./cmd/api` dan `./cmd/configenv` dengan
   `CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH -trimpath -ldflags="-s -w"`.
   Kompilasi selalu native di mesin build (tanpa emulasi Go).
2. **`runtime`** (target default, yang di-push) — `alpine` versi di-pin;
   paket `bash`, `postgresql<MAJOR>-client`, `ca-certificates`, `tzdata`;
   user non-root `app` uid/gid **10001**; `WORKDIR /app`; isi:
   `/app/bin/api`, `/app/bin/configenv` (`/app/bin` di `PATH`),
   `/app/migrations`, `/app/seeds`, `/app/scripts`. `EXPOSE 8080`.
   `ENTRYPOINT ["/app/bin/api"]` (default `-config .config.toml` relatif ke
   `/app`). Label OCI `org.opencontainers.image.source=https://github.com/akbarryyan/pg-aggregator-back`
   dan `org.opencontainers.image.version` dari build-arg `VERSION`.
3. **`dev`** (hanya lokal, tidak di-push) — `golang:1.25-alpine` + `air`
   (versi di-pin) + `bash` + `postgresql<MAJOR>-client`; `WORKDIR /src`;
   tidak menyalin source (source di-mount).

**Versi Postgres:** major **18** untuk server (`postgres:18-alpine`) dan
client (`postgresql18-client`). Plan memverifikasi paket client tersedia di
versi Alpine yang di-pin; jika `postgresql18-client` tidak tersedia, server
dan client **sama-sama** turun ke major 17 (tidak boleh beda major). Image
Postgres 18 menyimpan data di bawah `/var/lib/postgresql` — volume di-mount
ke path itu (plan memverifikasi path sesuai dokumentasi image).

**`.dockerignore`:** `.git`, `.github`, `.env*`, `.config.toml`, `db_password`,
`tmp/`, `.superpowers/`, `docs/`, `bruno/`, `docker-compose*.yaml`,
`Caddyfile`, `DEPLOY.md`, `*.md`. Secret tidak pernah masuk build context.

## 6. `docker-compose.yaml` (base = konfigurasi VPS)

- **`caddy`** — `caddy:2-alpine` (minor di-pin di plan); `ports: 80:80, 443:443`;
  volume `./Caddyfile:/etc/caddy/Caddyfile:ro`, `caddy_data:/data`,
  `caddy_config:/config`; IP tetap **`10.89.0.2`** di network `whuzpay`;
  `depends_on: api`.
- **`api`** — `image: ghcr.io/akbarryyan/whuzpay-backend:1.0.0` (tag yang
  diedit saat deploy); volume `./.config.toml:/app/.config.toml:ro`;
  `depends_on: postgres (service_healthy)`; tidak membuka port ke host.
- **`postgres`** — `postgres:18-alpine`; env `POSTGRES_USER=postgres`,
  `POSTGRES_DB=pg_aggregator` (nilai non-secret, sama dengan `db.user`/`db.name`
  di template dan config lokal; config VPS wajib memakai nilai yang sama),
  `POSTGRES_PASSWORD_FILE=/run/secrets/db_password`; `secrets: [db_password]`;
  volume `pgdata:/var/lib/postgresql`; healthcheck `pg_isready`; tidak membuka
  port ke host.
- **`migrate`** — image sama dengan `api`; `profiles: [tools]` (tidak ikut
  `up`); `entrypoint: [bash]`, `command: [scripts/migrate.sh]`; volume config
  yang sama; `depends_on: postgres (service_healthy)`; `restart: "no"`.
- **Semua service** (kecuali `migrate`): `restart: unless-stopped`; logging
  `json-file` `max-size: 10m`, `max-file: 3`; batas memori: postgres 384m,
  api 192m, caddy 128m.
- `secrets.db_password.file: ./db_password`.
- `networks.whuzpay` dengan subnet tetap **`10.89.0.0/24`** (menghindari
  `172.31.0.0/16` milik VPC AWS dan rentang default Docker).
- Volume bernama: `pgdata`, `caddy_data`, `caddy_config`.

**Kepemilikan file secret:** entrypoint Postgres membaca ulang
`POSTGRES_PASSWORD_FILE` setelah turun hak ke user `postgres` (uid **70** di
`postgres:18-alpine`, terverifikasi), jadi di VPS `db_password` harus
`chown 70:70` + `chmod 600`; `.config.toml` `chown 10001:10001` + `chmod 600`
(dibaca container `api`/`migrate`). Di lokal `db_password` `chmod 644`
(password DB dev lokal saja; port Postgres hanya bind ke 127.0.0.1).

**Password Postgres ada di dua tempat** (`db_password` dan `db.password` di
`.config.toml`) dan harus sama. Postgres hanya membaca `db_password` saat
volume pertama kali diinisialisasi; mengganti password sesudahnya butuh
`ALTER USER` (didokumentasikan).

## 7. `docker-compose.override.yaml` (lokal saja)

- **`caddy`** — diberi profile nonaktif (`profiles: [prod-only]`) sehingga
  tidak jalan di lokal.
- **`api`** — `build: {context: ., target: dev}`, `image: whuzpay-backend:dev`;
  `user: "${HOST_UID:-1000}:${HOST_GID:-1000}"`; volume `.:/src`;
  `working_dir: /src`; `command: [air]`; `ports: 8090:8080`;
  `pull_policy: build` (Dockerfile berubah → otomatis ter-build; tanpa
  perubahan, cache layer membuatnya instan); env
  `GOCACHE=/src/tmp/go-build`, `GOMODCACHE=/src/tmp/go-mod` (cache di `tmp/`
  yang sudah di-gitignore, menghindari masalah izin dan download ulang),
  `HOME=/tmp`, dan `GOFLAGS=-buildvcs=false` (image `golang:alpine` tidak
  punya `git`, terverifikasi; tanpa ini Go gagal menyematkan info VCS dari
  `.git` yang ter-mount).
  Server membaca `/src/.config.toml` (default path, cwd `/src`).
- **`migrate`** — image `whuzpay-backend:dev` dengan user, volume,
  `working_dir`, dan env cache yang sama seperti `api` lokal.
- **`postgres`** — tambahan `ports: 127.0.0.1:5433:5432` (5432 dipakai
  Postgres native user; hanya bind ke localhost).
- Image `dev` hanya perlu di-build ulang jika `Dockerfile`/versi Go/air
  berubah; perubahan kode cukup disimpan (air rebuild otomatis).

## 8. `Caddyfile`

```
api.whuzpay.web.id {
	encode zstd gzip
	reverse_proxy api:8080
}
```

Caddy secara default mengabaikan `X-Forwarded-For` kiriman klien dan
menulis IP koneksi sebenarnya ke XFF saat meneruskan ke `api`.

## 9. Config `[rate_limit]` dan Middleware

### 9.1 Config

```toml
[rate_limit]
trusted_proxies = []          # lokal; VPS: ["10.89.0.2/32"]

[rate_limit.auth]
requests_per_minute = 5
burst               = 5

[rate_limit.public]
requests_per_minute = 120
burst               = 30

[rate_limit.sensitive]
requests_per_minute = 10
burst               = 5
```

- `Config.RateLimit RateLimitConfig \`toml:"rate_limit"\``;
  `RateLimitConfig{TrustedProxies []string; Auth, Public, Sensitive RateLimitRule}`;
  `RateLimitRule{RequestsPerMinute int; Burst int}`. Semua key wajib (walk
  reflection yang ada sudah mendukung sub-table).
- Nilai template/lokal **identik** dengan angka hardcoded sekarang.
- `Validate()` menambah (pesan menyebut key, tidak pernah nilai):
  - `rate_limit.<kelas>.requests_per_minute must be greater than 0`
  - `rate_limit.<kelas>.burst must be at least 1`
  - `rate_limit.trusted_proxies[<i>] is not a valid CIDR` (`netip.ParsePrefix`)
- Helper `(RateLimitConfig) TrustedProxyPrefixes() ([]netip.Prefix, error)`.

### 9.2 Middleware (`internal/middleware/rate_limit.go`)

- `func (l *IPRateLimiter) WithTrustedProxies(p []netip.Prefix) *IPRateLimiter`.
  Tanpa pemanggilan ini perilaku identik dengan sekarang.
- Penentuan IP klien:
  1. `peer` = host dari `RemoteAddr`.
  2. `peer` bukan proxy tepercaya → pakai `peer`, abaikan header.
  3. `peer` tepercaya → gabungkan semua header `X-Forwarded-For`, baca
     **kanan ke kiri**, kembalikan entri pertama yang valid dan bukan proxy
     tepercaya.
  4. Header kosong, entri tidak valid ditemui sebelum ketemu klien, atau
     semua entri tepercaya → pakai `peer`.
- Alamat IPv4-mapped-IPv6 dinormalisasi (`Unmap`) sebelum dicocokkan.

### 9.3 `cmd/api/main.go`

Ketiga limiter dibuat dari `cfg.RateLimit.{Auth,Public,Sensitive}`
(`NewIPRateLimiter(float64(rpm), burst)`) lalu `.WithTrustedProxies(prefixes)`
dengan `prefixes` dari `TrustedProxyPrefixes()` (error → log + exit 1,
walau seharusnya sudah tertangkap `Validate`).

## 10. Script & Makefile

### 10.1 Script

- `migrate.sh`, `seed.sh`: blok load config menjadi
  `go run ./cmd/configenv` bila `go` tersedia (container dev), selain itu
  binary `configenv` (image runtime). Pola dua langkah
  (`config_env="$(...)"`; `eval "$config_env"`) dipertahankan.
- `seed-admin.sh` (baru): load config dengan cara yang sama; cek
  `SELECT count(*) FROM admins`; jika > 0 → pesan
  "admins table is not empty — seed-admin only runs on a fresh database" dan
  exit 1; jika 0 → jalankan `seeds/002_admin_user.sql` lalu cetak pengingat
  untuk segera mengganti password.
- `db-create.sh` dihapus.

### 10.2 Makefile (ditulis ulang)

| Target | Perilaku |
|---|---|
| `migrate` | `docker compose run --rm migrate` |
| `seed-admin` | `docker compose run --rm migrate scripts/seed-admin.sh` |
| `seed` | Jika `docker-compose.override.yaml` ada → `docker compose run --rm migrate scripts/seed.sh`; jika tidak → error "demo seed is local-only" |
| `release` | Wajib `VERSION`; `docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=$(VERSION) -t ghcr.io/akbarryyan/whuzpay-backend:$(VERSION) --push .` |

`run`, `dev`, `db-create` dihapus. Makefile yang sama dipakai di VPS
(`release` dan `seed` tidak relevan di sana).

## 11. Dokumentasi

- **`DEPLOY.md`** (VPS): security group (22 dari My IP, 80, 443; tutup 8090
  bila sempat dibuka); swap 2 GB; instal Docker + Compose + `make` di Ubuntu
  26.04 (repo resmi Docker, cadangan `docker.io` + `docker-compose-v2`);
  `docker login ghcr.io` dengan PAT `read:packages` via `--password-stdin`;
  `scp` tiga file; tabel nilai `.config.toml` produksi (environment
  production, port 8080, url `https://api.whuzpay.web.id`, `db.host =
  "postgres"`, JWT secret baru `openssl rand -hex 32`, `trusted_proxies =
  ["10.89.0.2/32"]`, kredensial Cashi); `db_password`; `.config.toml`
  `chown 10001:10001`, `db_password` `chown 70:70`, keduanya `chmod 600`; urutan deploy pertama (`up -d postgres` → `make
  migrate` → `make seed-admin` → `up -d` → ganti password admin); alur update
  rilis; log, `ps`, `pg_dump`; cara ganti password Postgres (`ALTER USER`).
- **`RUNNING.md`, `README.md`, `back/README.md`**: alur lokal Docker
  (`docker compose up -d`, `logs -f api`, `make migrate`/`make seed`, port
  8090, Postgres container di 5433), hapus instruksi `make run`/`make dev`/
  `make db-create`/`air` native, dokumentasikan `[rate_limit]`.
- **`.config.example.toml`**: template lokal Docker (`db.host = "postgres"`,
  `db.port = 5432`, `app.port = 8080`, `app.url = "http://localhost:8090"`,
  `[rate_limit]`).

## 12. Migrasi Lokal (saat implementasi, dengan izin user)

- `back/.config.toml` diubah: `db.host = "postgres"`, `db.port = 5432`,
  `app.port = 8080`, tambah `[rate_limit]` (nilai sama dengan template),
  tanpa membaca/mencetak secret lain.
- `back/db_password` dibuat dari `db.password` config yang sama
  (via `configenv` di subshell, tanpa mencetak), `chmod 600`.
- Data mulai kosong di Postgres container → `make migrate && make seed`.
  Postgres native tidak disentuh.

## 13. Build & Push

- Satu kali di laptop: pastikan builder buildx mendukung `linux/arm64`.
  Builder default (containerd image store) saat ini hanya melaporkan
  `linux/amd64`; emulasi arm64 dipasang dengan `tonistiigi/binfmt` —
  **mengubah pengaturan sistem, minta izin user dulu**.
- `make release VERSION=1.0.0`. **Push hanya setelah konfirmasi user.**

## 14. Verifikasi

| Lapisan | Cara |
|---|---|
| Go | `go vet ./...`, `go test ./... -race -count=1` (test baru: limiter tanpa trusted → XFF diabaikan; trusted + XFF → klien; spoof dari peer tak tepercaya; spoof kiri lewat proxy; entri tidak valid; semua tepercaya; IPv6; dua klien di belakang proxy dapat bucket terpisah. Config: `[rate_limit]` valid; rpm 0 / burst 0 / CIDR salah ditolak tanpa nilai; sub-table hilang terdeteksi; template tetap valid) |
| Image | build `runtime` amd64 `--load`: `id` → uid 10001; `/app` tanpa `.config.toml`/`.env`/`db_password`; `configenv`, `psql`, `bash` ada; tanpa config → exit 1 "config file not found". Build multi-arch kedua platform sukses |
| Compose | `docker compose config` (lokal) dan `docker compose -f docker-compose.yaml config` (VPS) valid |
| Lokal | `docker compose up -d`; `make migrate`; `make seed`; `curl localhost:8090/api/v1/health`; ubah file `.go` → log `air` rebuild & restart; `make seed-admin` ditolak (admin sudah ada); `flow-test/run_all.sh` lulus |
| VPS (setelah DNS aktif, dijalankan user mengikuti `DEPLOY.md`) | health via HTTPS; redirect `http`; `nc -zv 13.48.71.251 5432` dan `8080` gagal; login 6× dari satu IP → 429 tanpa memblokir IP lain; `docker stats --no-stream` di bawah batas |
