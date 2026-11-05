# Deploy ke VPS

Backend berjalan di VPS sebagai tiga container dari `docker-compose.yaml`:
Caddy (HTTPS) → `api` → PostgreSQL. Image `api` di-build di laptop dan
diambil dari GHCR (private). Target saat ini: AWS EC2 `t3.micro` (1 GB RAM),
Ubuntu 26.04, `api.whuzpay.web.id` → `13.48.71.251`.

```
Internet ──80/443──▶ caddy (10.89.0.2, Let's Encrypt) ──▶ api:8080 ──▶ postgres:5432
                                                           (internal)    (internal)
```

## 1. Rilis image (di laptop, dari `back/`)

Sekali saja — emulasi arm64 agar buildx bisa membuat image multi-arch:

```bash
docker run --privileged --rm tonistiigi/binfmt --install arm64
docker buildx ls          # builder default harus mencantumkan linux/arm64
```

Setiap rilis (sudah `docker login ghcr.io` dengan PAT ber-scope `write:packages`):

```bash
make release VERSION=1.0.0
docker buildx imagetools inspect ghcr.io/akbarryyan/whuzpay-backend:1.0.0   # harus ada linux/amd64 dan linux/arm64
```

Setelah push **pertama**, buka
`https://github.com/users/akbarryyan/packages/container/whuzpay-backend/settings`
dan pastikan visibilitas **Private** (keputusan: package private).

Catatan buildx:

- Registrasi `tonistiigi/binfmt` tidak bertahan setelah reboot — jalankan
  ulang sebelum rilis. Paket Ubuntu `qemu-user-binfmt` bersifat permanen.
- Jika `docker buildx ls` tidak menampilkan arm64 meski binfmt sudah
  terpasang, restart Docker: `sudo systemctl restart docker`.

Satu versi = satu tag; tag lama tidak pernah ditimpa.

## 2. Siapkan VPS (sekali)

### 2.1 Security group

- `22/tcp` hanya dari My IP.
- `80/tcp` dan `443/tcp` dari mana saja.
- Tutup port lain (mis. `8090` bila sempat dibuka). **Jangan buka `5432`.**
- Biarkan API hanya IPv4 (jangan buat record AAAA untuk `api.whuzpay.web.id`):
  klien IPv6 masuk ke Caddy lewat docker-proxy sebagai `10.89.0.1`, sehingga
  semuanya berbagi satu bucket rate limit.

### 2.2 Swap 2 GB

```bash
sudo fallocate -l 2G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
free -h
```

### 2.3 Docker, Compose, make, jq

Repo resmi Docker:

```bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl make jq
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
  | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

Jika `apt-get update` gagal karena repo Docker belum mendukung `resolute`
(Ubuntu 26.04), pakai paket Ubuntu:

```bash
sudo rm /etc/apt/sources.list.d/docker.list
sudo apt-get update
sudo apt-get install -y docker.io docker-compose-v2
```

Lalu (logout-login SSH agar grup berlaku):

```bash
sudo usermod -aG docker ubuntu
docker compose version
```

### 2.4 Login GHCR

Buat PAT (classic) **terpisah** dengan scope `read:packages` saja — jangan
pakai PAT push. Token dibaca tanpa tampil dan tanpa masuk history shell:

```bash
read -rs GHCR_PAT && echo "$GHCR_PAT" | docker login ghcr.io -u akbarryyan --password-stdin; unset GHCR_PAT
```

PAT disimpan ter-encode base64 (praktis plain text) di
`~/.docker/config.json` pada VPS; beri tanggal kedaluwarsa pada PAT.

### 2.5 Salin file deploy (dari laptop, folder `back/`)

```bash
ssh ubuntu@13.48.71.251 'mkdir -p ~/whuzpay'
scp docker-compose.yaml Caddyfile Makefile ubuntu@13.48.71.251:~/whuzpay/
```

**Jangan** salin `docker-compose.override.yaml` — keberadaan file itu yang
membuat compose berjalan dalam mode dev.

### 2.6 `.config.toml` dan `db_password` (di VPS)

```bash
umask 077                                          # file baru berizin 600, bukan 664
cd ~/whuzpay
openssl rand -hex 24 | tr -d '\n' > db_password   # password PostgreSQL baru
openssl rand -hex 32                               # salin hasilnya untuk security.jwt_secret
nano .config.toml
```

`db.password` di `.config.toml` harus diisi dari `db_password`
**sebelum** `chown` di bawah (setelahnya hanya root yang bisa membacanya):
jalankan `cat db_password`, lalu tempel hasilnya.

Isi `.config.toml` (semua key wajib; tanda `<…>` diganti nilai asli):

```toml
[app]
name         = "pg-aggregator"
environment  = "production"
port         = 8080
url          = "https://api.whuzpay.web.id"
frontend_url = "https://whuzpay.web.id"      # origin CORS frontend; sesuaikan saat frontend di-deploy

[db]
host     = "postgres"
port     = 5432
user     = "postgres"                         # sama dengan POSTGRES_USER di docker-compose.yaml
password = "<isi persis dari file db_password>"
name     = "pg_aggregator"                    # sama dengan POSTGRES_DB
sslmode  = "disable"                          # hanya lewat network internal compose

[redis]
host     = "localhost"
port     = 6379
password = ""

[cashi]
base_url   = "https://cashi.id"
api_key    = "<CASHI_API_KEY produksi>"
secret_key = "<CASHI_SECRET_KEY produksi>"

[security]
jwt_secret = "<hasil openssl rand -hex 32>"

[rate_limit]
trusted_proxies = ["10.89.0.2/32"]           # hanya Caddy (IP tetap di docker-compose.yaml)

[rate_limit.auth]
requests_per_minute = 5
burst               = 5

[rate_limit.public]
requests_per_minute = 120
burst               = 30

[rate_limit.sensitive]
requests_per_minute = 10
burst               = 5

[rate_limit.api]
requests_per_minute = 120
burst               = 60
```

Kepemilikan file (container `api`/`migrate` berjalan sebagai uid 10001,
`postgres` sebagai uid 70):

```bash
sudo chown 10001:10001 .config.toml && sudo chmod 600 .config.toml
sudo chown 70:70 db_password && sudo chmod 600 db_password
```

Setelah ini, edit config dengan `sudo nano .config.toml`.

## 3. Deploy pertama

Pastikan `api.whuzpay.web.id` sudah mengarah ke IP VPS (`dig +short api.whuzpay.web.id`).

Instalasi baru memakai `docker-compose.yaml` dan `Caddyfile` terbaru dari 2.5, jadi
cek 4.4 (data lama, migration 019) hanya berlaku untuk server yang di-upgrade.
Caddy baru menyala setelah `api` healthy, sehingga `up -d` pertama butuh
beberapa detik.

```bash
cd ~/whuzpay
docker compose up -d postgres
make migrate
make seed-admin
docker compose up -d
```

**Segera ganti password admin** (`Admin123!` tercantum di README publik).
Dari laptop:

```bash
API=https://api.whuzpay.web.id/api/v1
TOKEN=$(curl -fsS -X POST "$API/auth/admin/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@pg-aggregator.local","password":"Admin123!"}' | jq -r .token)
read -rsp 'Password admin baru: ' NEW_PW; echo
# Body lewat pipe agar password tidak muncul di daftar proses.
jq -n --arg p "$NEW_PW" '{current_password:"Admin123!", new_password:$p}' \
  | curl -fsS -X POST "$API/auth/admin/change-password" \
      -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
      --data-binary @-
unset NEW_PW
```

(Password baru minimal 8 karakter.)

Verifikasi:

```bash
docker compose ps                                             # di VPS: caddy, api, postgres harus Up; api dan postgres juga healthy (caddy paling rawan)
curl -s https://api.whuzpay.web.id/api/v1/health              # {"status":"ok"}
curl -sI http://api.whuzpay.web.id/api/v1/health | head -1    # 308 Permanent Redirect ke https
nc -zv -w 5 13.48.71.251 5432                                 # harus gagal (tertutup)
nc -zv -w 5 13.48.71.251 8080                                 # harus gagal (tertutup)
docker stats --no-stream                                      # di VPS: memori di bawah batas
```

## 4. Rilis berikutnya

1. Laptop: `make release VERSION=1.0.1`.
2. VPS: `cd ~/whuzpay && nano docker-compose.yaml` — ganti tag di baris
   `x-api-image:` menjadi `1.0.1` (`api` dan `migrate` memakai tag yang sama).
3. VPS: `docker compose pull api`.
4. VPS: backup (perintah `pg_dump` di bagian 5) — titik rollback yang
   sebenarnya begitu migration berjalan.
5. VPS: `make migrate` (menjalankan migration dari image BARU; tidak berbuat
   apa-apa bila tidak ada).
6. VPS: `docker compose up -d`. Bila `Caddyfile` ikut berubah pada rilis ini,
   lanjutkan dengan `docker compose restart caddy` (lihat 4.4 langkah 2).
7. Opsional: `docker image prune -f`.

**Peringatan:** tag hanya ada di salinan `docker-compose.yaml` di VPS. Jika
Anda menyalin ulang `docker-compose.yaml` yang lebih baru dari laptop, periksa
lagi baris tag-nya sesudahnya.

### 4.1 Rollback

Kode: kembalikan tag sebelumnya di `x-api-image:`, lalu `docker compose up -d`.
Bila rollback ke versi sebelum Paket 4, kembalikan juga `docker-compose.yaml`
lama (lihat 4.4 langkah 2); constraint database Paket 4 tetap terpasang dan
image lama kompatibel dengannya.
Untuk Paket 5 lihat 4.5 langkah 5.
Migration hanya maju: bila migration rilis baru tidak kompatibel dengan kode
lama, pulihkan backup dari langkah 4 (lihat bagian 5).

### 4.2 Cek sebelum deploy Paket 2

Paket 2 menolak `webhook_url` merchant yang tidak `https` (selalu; hanya
`callback_url` payment sandbox yang boleh `http`) dan URL yang
menunjuk ke alamat internal, dan mengirim callback merchant secara asinkron
lewat antrean di DB. Kerjakan langkah 1-3 sebelum `make migrate` (langkah 4)
dan `docker compose up -d` pada rilis ini; langkah 1-2 di `psql` pada DB
produksi.

1. **Cari URL yang akan ditolak.** Hasilnya adalah daftar merchant/payment
   yang URL-nya akan ditolak:

   ```sql
   -- Skema non-https, atau host yang jelas internal (juga untuk https).
   SELECT id, webhook_url FROM merchants
   WHERE webhook_url IS NOT NULL
     AND (webhook_url !~* '^https://'
          OR webhook_url ~* '://(localhost|127\.|10\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.|169\.254\.|\[)');

   SELECT id, reference, callback_url FROM payments
   WHERE callback_url IS NOT NULL
     AND environment = 'production' AND created_at > now() - interval '30 days'
     AND (callback_url !~* '^https://'
          OR callback_url ~* '://(localhost|127\.|10\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.|169\.254\.|\[)');
   ```

   Ini heuristik: pola host internal hanya menangkap bentuk yang umum (IP
   literal privat/loopback/link-local, `localhost`, IPv6 dalam `[...]`).
   Nama domain yang resolve ke alamat internal tidak tertangkap; itu tetap
   diblokir saat pengiriman (delivery menjadi `failed` tanpa retry).
   Batasan lain yang diketahui: domain IDN/punycode tidak dinormalisasi, dan
   nama domain yang di-CNAME ke domain kami sendiri tidak terdeteksi saat
   validasi (hanya domain kami dan subdomainnya yang ditolak, berdasarkan nama).

2. **Bersihkan callback `pending` lama.** Kode lama bisa meninggalkan baris
   yang macet di `pending` (tidak pernah dikirim ulang). Worker baru
   mengirim **semua** baris `pending` pada tick pertamanya, termasuk callback
   berumur berhari-hari yang tidak lagi diharapkan merchant. Periksa dulu:

   ```sql
   SELECT count(*), min(created_at) FROM merchant_callback_deliveries WHERE status = 'pending';
   ```

   Lalu tandai yang lebih tua dari 1 jam agar tidak dikirim:

   ```sql
   UPDATE merchant_callback_deliveries
   SET status = 'skipped', error_message = 'stale pre-async row, not re-sent', updated_at = now()
   WHERE status = 'pending' AND created_at < now() - interval '1 hour';
   ```

3. **`stop_grace_period`.** `docker-compose.yaml` memberi service `api`
   `stop_grace_period: 50s`. Shutdown bisa memakan hingga 30 detik (drain
   HTTP) ditambah 15 detik (menunggu job latar); default Docker 10 detik akan
   SIGKILL pengiriman callback yang sedang berjalan sebelum hasilnya
   tercatat. Pastikan salinan `docker-compose.yaml` di VPS ikut memuat baris
   ini.

4. **Migration `016_add_callback_queue_indexes.sql`.** Menambah dua indeks
   parsial untuk antrean callback dan komentar kolom `status`. Jalankan
   `make migrate` **sebelum** memulai image API baru. Idempoten
   (`IF NOT EXISTS`); indeks dibuat tanpa `CONCURRENTLY`, tidak masalah untuk
   tabel sekecil ini (penguncian tulis singkat).

5. **Callback yang gagal permanen karena URL lama.** Retry manual admin kini
   memakai URL **terkini**: `callback_url` payment, kalau kosong
   `webhook_url` merchant, dan hanya bila payment tidak ada atau keduanya
   kosong memakai `target_url` yang tersimpan. Jadi setelah merchant
   memperbaiki URL-nya, admin cukup menekan retry. URL baru tetap divalidasi.
   (Retry otomatis tetap memakai `target_url` tersimpan.)

6. **Domain kita sendiri ditolak sebagai tujuan callback.** Host dari
   `app.url` dan `app.frontend_url` di `.config.toml` (beserta subdomainnya)
   ditolak oleh validator. Pastikan keduanya terisi benar. Cek merchant yang
   `webhook_url`-nya menunjuk ke domain kita; mereka akan ditolak saat update
   dan gagal permanen saat kirim.

### 4.3 Cek sebelum deploy Paket 3

Paket 3 memperketat API: validasi config produksi, pencabutan token sesi
(logout / ganti password), idempotensi pembuatan payment (`Idempotency-Key`),
batas ukuran body 1 MiB, dan rate limiter baru `[rate_limit.api]`. Kerjakan
langkah 1-4 sebelum `docker compose up -d` pada rilis ini.

1. **Tambah `[rate_limit.api]` ke `.config.toml` di server.** Section ini
   wajib; tanpanya API gagal start (`docker compose logs api` menyebut key yang
   hilang). Instalasi baru sudah mendapat section ini dari template di 2.6.
   Salin dari `.config.example.toml`, lalu edit dengan `sudo`:

   ```toml
   [rate_limit.api]        # route JWT dashboard/admin + pembuatan payment via API key
   requests_per_minute = 120
   burst               = 60
   ```

   `requests_per_minute` harus > 0 dan `burst` >= 1.

2. **Pastikan `trusted_proxies` benar.** Di `.config.toml` harus ada
   `trusted_proxies = ["10.89.0.2/32"]` (IP Caddy di `docker-compose.yaml`).
   Bila kosong atau salah di belakang Caddy, API melihat semua klien sebagai
   satu IP (IP Caddy), sehingga **semua pengguna berbagi satu bucket rate
   limit**: lockout login (bucket `auth`) menimpa semua orang, dan bucket `api`
   (120/menit, burst 60) dipakai bersama oleh seluruh admin, dashboard, dan
   merchant yang membuat payment. Smoke test setelah deploy: kirim request
   dari dua IP klien berbeda (mis. laptop dan jaringan seluler) ke endpoint
   ber-limit, misalnya `POST /api/v1/auth/login` dengan password salah
   berulang-ulang dari IP pertama sampai `429`; request dari IP kedua harus
   tetap mendapat `401`, bukan `429`.

3. **Aturan config produksi.** Server menolak start bila salah satu dilanggar:
   - `app.environment` harus persis `development` atau `production` (di VPS:
     `production`);
   - `security.jwt_secret` minimal 32 karakter dan bukan placeholder
     `change-this-secret`;
   - `app.url` dan `app.frontend_url` harus berawalan `https://`.

   Periksa ketiganya sebelum menjalankan image baru. **Perhatian:** mengganti
   `jwt_secret` membuat semua token yang ada tidak valid (semua pengguna harus
   login ulang).

4. **Migration `017` dan `018`.** Jalankan `make migrate` **sebelum** memulai
   image API baru (setelah backup, lihat bagian 4):
   - `017_create_idempotency_keys.sql` membuat tabel `idempotency_keys`;
   - `018_add_sessions_valid_after.sql` menambah kolom `sessions_valid_after`
     (nullable) di `admins` dan `merchant_users`.

   Keduanya aditif dan idempoten (`IF NOT EXISTS`); `NULL` berarti tidak ada
   token yang dibatalkan saat migration berjalan, jadi sesi yang ada tetap
   berlaku.

5. **Efek yang terlihat pengguna.**
   - Ganti password mencabut **semua** token akun itu, termasuk di perangkat
     lain; pengguna harus login ulang. `POST /auth/logout` dan
     `POST /auth/admin/logout` melakukan hal yang sama.
   - Merchant yang dinonaktifkan langsung ditolak: pembuatan payment `403`,
     token dashboard-nya `401` (tidak menunggu token kedaluwarsa).
   - Regenerate webhook secret kini meminta password merchant
     (`{"password": "..."}`); membaca secret tidak. Front-end versi baru
     mengirim password itu, jadi deploy front-end bersama backend.
   - Body request di atas 1 MiB ditolak: `413` di webhook provider, `400
     Invalid request body` di endpoint JSON lain.

6. **Untuk merchant yang memakai `Idempotency-Key`.** Header ini opsional
   (1-128 karakter ASCII tercetak, berlaku 24 jam per merchant + environment).
   Gunakan UUID baru per niat bayar dan ulangi kunci yang sama saat retry:
   respons ulang adalah payment dengan status **terkini** plus header
   `Idempotent-Replayed: true`. `409` berarti request pertama dengan kunci itu
   masih diproses (coba lagi sebentar lagi); `422` berarti kunci dipakai untuk
   isi request yang berbeda (jangan pakai ulang kunci untuk payment lain);
   `400` berarti kunci tidak valid. Payment link dan pembuatan oleh admin tidak
   memakainya.

### 4.4 Cek sebelum deploy Paket 4

Paket 4 menambah constraint database (migration 019), health check sungguhan,
healthcheck container, dan header/log di Caddy. Tidak ada key `.config.toml`
baru. Kerjakan langkah 1-3 sebelum `docker compose up -d` pada rilis ini, dan
ambil backup dulu (langkah backup di bagian 4) sebelum `make migrate`.

1. **Cek data lama.** Migration 019 membatalkan diri (satu transaksi, tidak ada
   yang berubah) bila data melanggar constraint baru. Jalankan di `psql` pada
   DB produksi (mis. `docker compose exec postgres psql -U postgres pg_aggregator`);
   ketiganya harus mengembalikan nol baris:

   ```sql
   SELECT id, reference, amount FROM payments WHERE amount <= 0;

   SELECT id, reference, status FROM payments
   WHERE status NOT IN ('pending', 'paid', 'expired', 'failed', 'cancelled');

   SELECT provider_name, provider_reference, count(*) FROM payments
   WHERE provider_reference IS NOT NULL
   GROUP BY 1, 2 HAVING count(*) > 1;
   ```

   Bila ada baris, perbaiki datanya dulu (ini data keuangan; tidak dibersihkan
   otomatis).
2. **Ganti `docker-compose.yaml` dan `Caddyfile` di server bersama image
   baru.** Salin keduanya dari laptop (bagian 2.5), lalu pasang lagi tag image
   yang benar di baris `x-api-image:` (lihat peringatan di bagian 4). Jangan
   menjalankan compose baru dengan image lama: `api` kini punya healthcheck
   (`api -healthcheck`), flag yang tidak dikenal image lama (binary lama keluar
   dengan kode 2), sehingga `api` dinilai tidak sehat. Pada instalasi baru,
   Caddy (yang menunggu `api` sehat) tidak pernah menyala; pada upgrade yang
   Caddy-nya sudah berjalan, `up -d` melaporkan "dependency failed to start
   ... unhealthy" sementara Caddy yang berjalan tetap memproksi. Jaga compose
   dan image selalu sinkron. Hal yang sama berlaku
   bila rollback ke image sebelum Paket 4 (bagian 4.1): kembalikan juga
   `docker-compose.yaml` lama. Caddy kini baru menyala setelah `api` sehat
   (`depends_on` dengan `condition: service_healthy`, `start_period` 20 detik),
   jadi sesudah `docker compose up -d` HTTPS tersedia beberapa detik lebih
   lambat dari sebelumnya.

   Setelah `docker compose up -d`, jalankan **`docker compose restart caddy`**.
   Compose tidak menganggap Caddy berubah (isi `Caddyfile` tidak masuk hash
   service, `depends_on` diabaikan), jadi `up -d` tidak membuat ulang Caddy
   yang sudah berjalan, dan Caddy hanya membaca `Caddyfile` saat start (`scp`
   di 2.5 menimpa file di tempat, sehingga restart membaca isi barunya). Tanpa
   restart, header keamanan, batas body 1 MiB, dan filter log tidak berlaku
   padahal terlihat sudah. Periksa:

   ```bash
   curl -sI https://api.whuzpay.web.id/api/v1/health | grep -i -E 'strict-transport-security|cache-control'
   ```

   Keluarannya harus memuat `strict-transport-security: max-age=31536000` dan
   `cache-control: no-store`. (Instalasi baru tidak perlu restart: Caddy dibuat
   baru.)
3. **`make migrate`** menerapkan 019 sebelum image API baru dijalankan
   (idempoten). Setelah itu menghapus merchant yang masih punya payment
   ditolak database (`ON DELETE RESTRICT`); tabel lain (API key, user, config
   provider, payment link) tetap ikut terhapus bersama merchant.
4. **Header dan log Caddy.** Caddy menambah header keamanan (HSTS
   `max-age=31536000`, `nosniff`, `Referrer-Policy: no-referrer`,
   `X-Frame-Options: DENY`, `Cache-Control: no-store`), membatasi body 1 MiB
   di proxy, dan menulis log akses ke stdout (`docker compose logs caddy`).
   Log akses membuang header `Authorization`, `X-Api-Key`, `Cookie`,
   `Set-Cookie`, ditambah `X-Gateway-Signature` dan `Proxy-Authorization`;
   **jangan hapus blok filter itu** dari `Caddyfile`, karena
   tanpanya bearer token dan API key merchant tertulis ke log container.
5. **Health check.** `GET /api/v1/health` kini 200 `{"status":"ok"}` hanya
   bila database menjawab ping (batas 2 detik); 503 `{"status":"unavailable"}`
   bila tidak (detail error hanya ada di `docker compose logs api`). Pantau
   dengan `docker compose ps` (kolom `STATUS` harus `healthy`).

   Healthcheck container sengaja **senyap**: `api -healthcheck` tidak mencetak
   apa pun, hanya keluar dengan kode 0 (sehat) atau 1, dan tidak membedakan
   config gagal dimuat dari koneksi ditolak. Bila `api` `unhealthy`:

   ```bash
   docker compose ps                  # api: unhealthy / restarting?
   docker compose logs --tail 50 api  # config tidak valid, DB tidak terjangkau, panic
   docker compose exec api wget -O- http://127.0.0.1:8080/api/v1/health
   ```

   Perintah `wget` memakai busybox bawaan image alpine (tanpa `-q`, sehingga
   galat HTTP seperti `503` ikut tercetak; badan jawaban 503 sendiri tidak
   ditampilkan busybox); ganti `8080` bila
   `app.port` di `.config.toml` berbeda. Jawaban `503` berarti server hidup
   tetapi database tidak menjawab (cek `docker compose ps postgres` dan
   `db.password`); koneksi ditolak berarti proses `api` belum/tidak
   mendengarkan (lihat log).
6. **`api` harus satu replika.** Job latar (expire, kirim callback,
   reconcile) berjalan di dalam proses `api` tanpa kunci leader; menjalankan
   lebih dari satu instance (mis. `docker compose up --scale api=2`) membuat
   job berjalan ganda. Ukuran pool koneksi DB (maks 25 koneksi terbuka) juga
   dihitung untuk satu instance.
7. **Timeout.** Koneksi DB kini memakai `statement_timeout` 30 detik dan
   `idle_in_transaction_session_timeout` 60 detik; panggilan ke Cashi
   di-timeout 10 detik. Level log mengikuti `app.environment`
   (`development` → debug, selain itu info; di VPS `production` → info).

### 4.5 Cek sebelum deploy Paket 5

Paket 5 menambah opsi fee provider ditanggung customer (migration 020). Tidak
ada key `.config.toml` baru, dan tidak ada merchant yang berubah perilaku
sampai admin mengaktifkannya. Ambil backup dulu (langkah 4 pada daftar rilis
di awal bagian 4, perintah `pg_dump` di bagian 5) sebelum `make migrate`.

1. **`make migrate`** menerapkan 020 sebelum image API baru dijalankan:
   kolom `merchants.fee_bearer` (default `merchant`) dan `payments.fee_amount`
   (default 0), beserta dua constraint. Aditif dan idempoten; tidak ada
   perubahan perilaku: semua merchant tetap `merchant` dan payment lama punya
   `fee_amount` 0, sehingga totalnya sama dengan nominalnya.
2. **Deploy backend dan front-end bersamaan.** Dashboard dan halaman checkout
   kini membaca `fee_amount` dan `total_amount`, dan panel admin punya
   pengaturan baru. Field itu aditif, jadi front-end lama dengan backend baru
   tetap berjalan, tetapi hanya menampilkan nominal (`amount`). **Bila
   pengaturan diaktifkan sebelum front-end baru tayang, checkout dan dashboard
   menampilkan nominal sementara QR menagih `total_amount`** (mis. Rp 2.015
   pada payment Rp 2.000). Jangan aktifkan sebelum keduanya terpasang.
3. **Cara mengaktifkan.** Panel admin -> Merchants -> detail merchant -> kartu
   **Fee borne by** -> pilih **Customer** -> simpan (ada dialog konfirmasi).
   Atau `PUT /api/v1/admin/merchants/{id}/fee-bearer` dengan
   `{"fee_bearer":"customer"}`. Hanya admin yang bisa; berlaku untuk payment
   **baru** saja. Sejak diaktifkan, customer merchant itu langsung ditagih
   fee, jadi **jangan aktifkan merchant yang melayani customer sungguhan
   sebelum verifikasi di langkah 4 lulus.**
4. **Verifikasi wajib dengan satu pembayaran uji nyata, lebih dulu pada
   merchant internal/uji milik operator**, tidak pernah pada merchant yang
   dipakai customer. Jalankan payment uji dengan opsi yang **sama** dengan
   yang akan dipakai merchant yang hendak diaktifkan (terutama
   `use_custom_merchant_name` / QRIS Custom: mode custom Cashi mengembalikan
   `expected_net` dan perilaku fee-nya bisa berbeda):
   1. Aktifkan **Customer** pada merchant uji itu saja. Buat payment
      `production` nominal Rp 2.000. Respons harus memuat `amount: 2000`,
      `fee_amount: 15`, `total_amount: 2015`.
   2. Bayar QR-nya (nominal di QR/aplikasi bayar harus Rp 2.015).
   3. Bandingkan dengan dashboard Cashi: net yang diterima harus Rp 2.000 (atau
      Rp 2.001 bila Cashi membulatkan fee-nya ke bawah).
   4. Pastikan nominal yang dilaporkan Cashi pada check-status/webhook adalah
      2015 dan payment berubah `paid`. Ini sekaligus menuntaskan pengecekan
      nominal Cashi yang tertunda dari Paket 1.
   5. **Hanya bila net dan nominal Cashi cocok**, aktifkan merchant
      customer-facing satu per satu. Bila net Cashi **kurang** dari nominal
      (atau payment tidak menjadi `paid` karena nominal tidak cocok),
      kembalikan merchant uji ke `merchant` dan laporkan; jangan aktifkan
      merchant lain. Mengembalikan ke `merchant` hanya mencegah payment
      **berikutnya** terdampak; payment yang sudah dibuat dengan fee tidak
      ikut berubah.

   Batas atas: berdasarkan informasi yang kami terima, Cashi membatasi total
   kotor sekitar Rp 10.000.000 (belum diverifikasi; konfirmasikan ke Cashi).
   Total yang melebihi batas Cashi dapat ditolak provider sehingga pembuatan
   payment gagal; uji nominal besar plus fee dulu. Backend tidak menerapkan
   batas ini sendiri.
5. **Rollback, urutannya wajib.** Image lama tidak mengenal `fee_amount` dan
   memverifikasi terhadap **nominal**. Payment fee-customer yang settle
   setelah rollback ditolak sebagai nominal tidak cocok dan harus ditangani
   manual (uang customer sudah masuk). Urutan berikut **mengurangi** risikonya:
   1. Kembalikan **setiap** merchant yang `customer` ke `merchant` lebih dulu.
   2. **Tunggu** sampai daftar Payments admin dengan filter status `pending`
      tidak lagi memuat baris yang ber-fee (`fee_amount` > 0), **dan**
      sedikitnya **6 jam** (jendela reconcile payment `expired`,
      `reconcileExpiredWindow`) sudah lewat sejak `expires_at` terakhir di
      antara payment fee-customer, ditambah 5 menit masa tenggang
      (`expireGracePeriod`). Selama 6 jam itu payment `expired` masih dicek
      ulang tiap 2 menit dan Cashi masih bisa mengirim webhook; image lama
      yang menerimanya memverifikasi terhadap nominal dan mencatat
      `amount_mismatch`. Export CSV memuat kolom `expires_at` (halaman
      daftar tidak menampilkannya) dan `fee_amount` serta `total_amount`
      sebagai dua kolom terakhir. Jangan berpegang pada TTL 30 menit: itu
      hanya default; `expires_in_minutes` bisa sampai 1440 dan untuk payment
      produksi `expires_at` yang berlaku adalah milik Cashi (menimpa nilai
      lokal). Payment sandbox milik merchant `customer` juga tampak ber-fee
      di daftar `pending` (tidak berbahaya, hanya berlebih).
   3. Baru setelah itu rollback image (dan compose bila perlu, lihat 4.1).

   Payment `expired` masih bisa settle terlambat (jalur late payment), jadi
   menunggu `paid` atau `expired` saja tidak menutup jendelanya sepenuhnya.
   Payment fee-customer yang settle terlambat setelah rollback tetap ditolak
   image lama sebagai nominal tidak cocok dan harus ditangani manual.

   Payment dengan event `amount_mismatch` **keluar permanen dari reconcile
   otomatis**, juga setelah roll forward lagi. Setelah roll forward, setiap
   payment fee-customer (`fee_amount` > 0) yang terkena harus direkonsiliasi
   manual dengan memanggil `POST /api/v1/admin/reconciliation/{payment_id}/check`
   memakai token admin, satu kali untuk tiap payment id (endpoint ini
   memverifikasi terhadap total dan bisa menyelesaikan payment `expired`
   menjadi `paid` sebagai pembayaran terlambat). Halaman Reconciliation di
   panel hanya memuat payment `pending`, jadi payment `expired` yang terkena
   **tidak akan tampil di sana**; jangan menganggap pekerjaan selesai hanya
   karena halaman itu kosong. Cara menemukan payment yang terkena: (a) filter
   daftar Payments admin (atau export CSV) untuk `fee_amount` > 0 berstatus
   `expired` (atau masih `pending`) dengan `expires_at` dalam 6 jam sebelum
   rollback; dan/atau (b) buka halaman detail payment dan cari event webhook
   dengan `processing_error` berawalan `amount_mismatch`. Payment yang settle
   terlambat tanpa webhook sama sekali tidak meninggalkan event, sehingga
   hanya bisa ditemukan lewat filter (a). Reconcile otomatis berjalan tiap 2
   menit dalam batch dan bergiliran ke semua kandidat, jadi tiap payment
   dicek ulang secara berkala, belum tentu tiap 2 menit.

   Mengganti merchant ke `merchant` tanpa rollback image aman: payment lama
   menyimpan `fee_amount`-nya sendiri sehingga verifikasi tetap benar.
6. **Sandbox.** Nominal sandbox tidak lagi diberi akhiran acak 1-99; sandbox
   memakai aturan fee yang sama dengan Cashi (0,7%), sehingga angkanya sama
   dengan produksi.

## 5. Operasional

```bash
docker compose logs -f api              # log aplikasi (JSON per baris)
docker compose logs -f caddy            # sertifikat / akses
docker compose restart api

# Backup database
docker compose exec -T postgres pg_dump -U postgres -d pg_aggregator | gzip > backup-$(date +%F).sql.gz

# Restore (hentikan api dulu agar tidak ada koneksi aktif)
docker compose stop api
docker compose exec -T postgres psql -U postgres -d postgres -c 'DROP DATABASE pg_aggregator' -c 'CREATE DATABASE pg_aggregator'
gunzip -c backup-YYYY-MM-DD.sql.gz | docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U postgres -d pg_aggregator
docker compose start api
```

Salin backup keluar dari VPS, dari laptop:
`scp ubuntu@13.48.71.251:~/whuzpay/backup-*.sql.gz .`

**Peringatan:** jangan pernah menjalankan `docker compose down -v` di VPS —
perintah itu menghapus `pgdata` DAN `caddy_data` (sertifikat harus diterbitkan
ulang dan bisa terkena rate limit Let's Encrypt).

**Ganti password PostgreSQL** (`db_password` hanya dibaca saat volume
pertama kali dibuat):

```bash
docker compose exec postgres psql -U postgres -c '\password postgres'   # prompt interaktif, tidak masuk history
# lalu samakan isi db_password dan db.password di .config.toml, kemudian:
docker compose up -d --force-recreate api
```

Mengedit `db_password` / `.config.toml` memerlukan `sudo` (kepemilikan bukan
user `ubuntu`).

`make seed` (data demo) sengaja ditolak di server.

## 6. Troubleshooting

| Gejala | Penyebab & solusi |
|---|---|
| Caddy gagal mendapat sertifikat | DNS `api.whuzpay.web.id` belum ke IP VPS, atau port 80/443 tertutup di security group. Cek `docker compose logs caddy`. |
| `api` `unhealthy` (Caddy tidak menyala) | Healthcheck senyap; ikuti diagnosis di 4.4 langkah 5. Cek juga image `api` memang versi Paket 4 (image lama tidak mengenal `-healthcheck`). |
| `api` restart terus | `docker compose logs api`: config tidak valid (`invalid config …`), atau `db.password` ≠ isi `db_password`. |
| `permission denied` membaca config | Kepemilikan file — ulangi `chown` di bagian 2.6. |
| `pull access denied` | Belum `docker login ghcr.io` di VPS, atau PAT tanpa `read:packages`. |
| `seed-admin` menolak | Admin sudah ada — memang disengaja agar password tidak ter-reset. |
| `.config.toml` (atau `db_password`) ternyata berupa folder | File belum ada saat `docker compose up`, sehingga Docker membuat folder pengganti. `docker compose down`, `sudo rm -r .config.toml`, buat ulang file sesuai bagian 2.6, lalu `docker compose up -d`. |
