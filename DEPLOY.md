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
docker compose ps                                             # di VPS: caddy, api, postgres harus Up (caddy paling rawan)
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
6. VPS: `docker compose up -d`.
7. Opsional: `docker image prune -f`.

**Peringatan:** tag hanya ada di salinan `docker-compose.yaml` di VPS. Jika
Anda menyalin ulang `docker-compose.yaml` yang lebih baru dari laptop, periksa
lagi baris tag-nya sesudahnya.

### 4.1 Rollback

Kode: kembalikan tag sebelumnya di `x-api-image:`, lalu `docker compose up -d`.
Migration hanya maju: bila migration rilis baru tidak kompatibel dengan kode
lama, pulihkan backup dari langkah 4 (lihat bagian 5).

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
| `api` restart terus | `docker compose logs api`: config tidak valid (`invalid config …`), atau `db.password` ≠ isi `db_password`. |
| `permission denied` membaca config | Kepemilikan file — ulangi `chown` di bagian 2.6. |
| `pull access denied` | Belum `docker login ghcr.io` di VPS, atau PAT tanpa `read:packages`. |
| `seed-admin` menolak | Admin sudah ada — memang disengaja agar password tidak ter-reset. |
| `.config.toml` (atau `db_password`) ternyata berupa folder | File belum ada saat `docker compose up`, sehingga Docker membuat folder pengganti. `docker compose down`, `sudo rm -r .config.toml`, buat ulang file sesuai bagian 2.6, lalu `docker compose up -d`. |
