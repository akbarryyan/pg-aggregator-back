# Design: Migrasi Config Backend dari `.env` ke `.config.toml`

- **Tanggal:** 2026-10-29
- **Status:** Disetujui (brainstorming), menunggu review spec
- **Cakupan repo:** `back/` (utama), `flow-test/`, dokumen di root workspace

## 1. Latar Belakang & Tujuan

Saat ini backend memuat config lewat `godotenv.Load()` + `os.Getenv` di
`internal/config/config.go`. Semua nilai berupa string datar, tanpa
pengelompokan, dan typo nama key (`DB_PASWORD`) diam-diam diabaikan.

**Tujuan:** config dikelompokkan per section (`[app]`, `[db]`, ...) dan
bertipe (integer untuk port), dibaca dari satu file `.config.toml`.

**Kriteria sukses:**

1. Backend, `make db-create`/`migrate`/`seed`, dan `flow-test` berjalan
   tanpa file `.env` dan tanpa `godotenv`.
2. Key yang hilang, key tak dikenal, atau file yang salah sintaks membuat
   backend **dan** script gagal dengan pesan yang menyebut nama key-nya.
3. Seluruh test yang ada tetap lulus; test baru mencakup package `config`
   dan `cmd/configenv`.

## 2. Keputusan

| # | Keputusan | Alasan |
|---|---|---|
| D1 | Hanya ganti format; field config tetap sama | Tujuan = struktur & tipe, bukan memperluas config |
| D2 | **Hanya TOML** — tidak ada override env var untuk nilai apa pun, termasuk secret | Pilihan user: satu sumber kebenaran, paling sederhana |
| D3 | Script bash membaca config lewat helper Go `cmd/configenv` | Satu parser + validasi yang sama dengan backend, tanpa tool eksternal |
| D4 | **Tanpa nilai default** — setiap key wajib ditulis di file | Pilihan user: config eksplisit |
| D5 | Key ditulis dengan nilai `""` tetap sah, kecuali dilarang aturan validasi | Agar `cashi.api_key = ""` di development dan `redis.password = ""` tetap bisa |
| D6 | Key tak dikenal ditolak | Menangkap typo seperti `paswrod` / `[dbb]` |
| D7 | Library `github.com/BurntSushi/toml` | Mapan, zero-dependency, punya `MetaData.Undecoded()` dan `MetaData.IsDefined()` |
| D8 | Lokasi file: `.config.toml` di working directory, bisa diganti lewat flag `-config <path>` | Sama dengan perilaku `.env` sekarang; flag hanya menentukan *lokasi*, bukan nilai |
| D9 | Hard cutover — tidak ada fallback ke `.env` | Konsisten dengan D2 |

## 3. Di Luar Cakupan

- Memindahkan nilai hardcoded di `main.go` (rate limit, interval scheduler,
  pool DB, timeout HTTP) ke config.
- Field bertipe durasi (config saat ini tidak punya).
- Menghapus section `[redis]` (belum dipakai, tapi dipertahankan agar field
  identik dengan sekarang).
- Menulis ulang script migrate/seed/db-create dalam Go.
- `flow-test/.env` (itu config milik script test, bukan config backend) dan
  `front/` (tidak terdampak).
- Mengimplementasikan `LOG_LEVEL` (ada di `.env.example` tapi tidak pernah
  dibaca kode; tidak dibawa ke TOML).

## 4. Format File

`back/.config.example.toml` di-commit sebagai template lengkap. Developer
menyalinnya ke `back/.config.toml` (di-gitignore) dan mengisi nilainya.
Karena D4, template harus memuat **semua** key.

```toml
[app]
name         = "pg-aggregator"
environment  = "development"     # development | production
port         = 8080
url          = "http://localhost:8080"
frontend_url = "http://localhost:3000"

[db]
host     = "localhost"
port     = 5432
user     = "postgres"
password = "postgres"
name     = "pg_aggregator"
sslmode  = "disable"

[redis]   # belum dipakai; dipertahankan agar field tetap sama
host     = "localhost"
port     = 6379
password = ""

[cashi]   # hanya dipakai untuk payment environment=production
base_url   = "https://cashi.id"
api_key    = ""                  # header x-api-key ke Cashi
secret_key = ""                  # validasi HMAC webhook Cashi

[security]
jwt_secret = "change-this-secret"
```

Pemetaan dari env var lama:

| Env var lama | Key TOML | Tipe |
|---|---|---|
| `APP_NAME` | `app.name` | string |
| `APP_ENV` | `app.environment` | string |
| `APP_PORT` | `app.port` | **int** |
| `APP_URL` | `app.url` | string |
| `FRONTEND_URL` | `app.frontend_url` | string |
| `DB_HOST` / `DB_USER` / `DB_PASSWORD` / `DB_NAME` / `DB_SSLMODE` | `db.host` / `db.user` / `db.password` / `db.name` / `db.sslmode` | string |
| `DB_PORT` | `db.port` | **int** |
| `REDIS_HOST` / `REDIS_PASSWORD` | `redis.host` / `redis.password` | string |
| `REDIS_PORT` | `redis.port` | **int** |
| `CASHI_BASE_URL` / `CASHI_API_KEY` / `CASHI_SECRET_KEY` | `cashi.base_url` / `cashi.api_key` / `cashi.secret_key` | string |
| `JWT_SECRET` | `security.jwt_secret` | string |
| `LOG_LEVEL` | — (tidak dibawa, lihat §3) | — |

## 5. Package `internal/config`

### 5.1 Struct

Nama tipe dan field dipertahankan (`Config`, `AppConfig`, `DBConfig`,
`RedisConfig`, `CashiConfig`, `SecurityConfig`). Perubahan:

- Tiap field mendapat tag `toml:"..."` sesuai tabel §4.
- `AppConfig.Port`, `DBConfig.Port`, `RedisConfig.Port`: `string` → `int`.
- `DBConfig.ConnectionString()` memakai `%d` untuk port.
- Helper `getEnv`, `getEnvAsInt`, `getEnvAsBool` dihapus.

### 5.2 API

```go
func Load(path string) (*Config, error)
func (c *Config) Validate() error
```

`Load()` tanpa argumen diganti `Load(path)`. Satu-satunya pemanggil di luar
package adalah `cmd/api/main.go` dan `cmd/configenv/main.go`.

### 5.3 Algoritma `Load`

1. `toml.DecodeFile(path, &cfg)` ke struct kosong (tanpa default, D4).
   - File tidak ada (`errors.Is(err, fs.ErrNotExist)`) →
     `config file not found: <path> (copy .config.example.toml to create it)`.
   - Error sintaks (`toml.ParseError`) → **disaring**, karena pesan asli
     BurntSushi memuat nilai mentah (terverifikasi dengan v1.6.0:
     `expected value but found "SECRETVAL" instead`). Yang dilaporkan hanya
     nomor baris + key terakhir:
     `parse <path>: TOML syntax error at line 2 (near key db.password); details hidden because the file may contain secrets`
     (bagian `(near key ...)` dihilangkan jika `LastKey` kosong).
   - Error lain dari decode (mis. tipe salah: `port = "8080"`) bukan
     `ParseError` dan pesannya tidak memuat nilai (terverifikasi) →
     diteruskan dengan prefix `parse <path>: `.
2. **Key tak dikenal:** `md.Undecoded()` → daftar nama key lengkap
   (`db.paswrod`), diurutkan.
3. **Key hilang:** daftar key wajib diturunkan otomatis dari tag `toml`
   struct lewat walk reflection (rekursif ke field bertipe struct).
   Setiap key dicek dengan `md.IsDefined(section, key)`. Hasil diurutkan.
   Daftar ini **tidak** ditulis manual, supaya field baru otomatis wajib.
4. Kalau langkah 2 atau 3 menemukan masalah → kembalikan satu error berisi
   keduanya (lihat §5.4). Langkah 5 tidak dijalankan.
5. `cfg.Validate()`.

### 5.4 Format Error Struktural

Semua masalah dilaporkan sekaligus; baris yang kosong dihilangkan:

```
invalid config .config.toml:
  unknown keys: db.paswrod, dbb.host
  missing keys: db.password, redis.host, redis.password, redis.port
```

`md.Undecoded()` juga melaporkan table induk dari section tak dikenal
(`[dbb]` → `dbb` **dan** `dbb.host`, terverifikasi v1.6.0). Key yang
merupakan induk dari key lain yang dilaporkan dibuang, sehingga hanya key
daun yang muncul; table kosong tak dikenal (`[dbb]` tanpa isi) tetap
dilaporkan sebagai `dbb`.

**Aturan keamanan:** pesan error apa pun dari package ini hanya boleh
menyebut nama key / path file, **tidak pernah nilai**. File ini berisi
secret dan error startup biasanya masuk ke log aggregator.

### 5.5 Validasi Nilai (`Validate`)

| Aturan | Status |
|---|---|
| `app.name`, `app.url`, `app.frontend_url` tidak boleh kosong | sudah ada |
| `db.host`, `db.name` tidak boleh kosong | sudah ada |
| `app.environment == "production"` → `cashi.api_key` dan `cashi.secret_key` wajib tidak kosong | sudah ada |
| `app.environment == "production"` → `security.jwt_secret` tidak boleh `"change-this-secret"` | sudah ada |
| `security.jwt_secret` tidak boleh kosong di environment apa pun | **baru** |

Aturan baru diperlukan karena sebelumnya default-lah yang mencegah secret
kosong; tanpa default (D4), `jwt_secret = ""` membuat JWT ditandatangani
dengan key kosong sehingga token bisa dipalsukan.

Pesan error `Validate` yang sudah ada dipertahankan maknanya, tapi nama
yang disebut diganti ke nama key TOML (mis. `app.url is required`, bukan
`APP_URL is required`).

### 5.6 Perbedaan Perilaku yang Disengaja

- Sebelumnya `getEnv` memperlakukan nilai `""` sama dengan tidak di-set
  (jatuh ke default). Sekarang key yang ditulis `""` berarti benar-benar
  string kosong, dan key yang tidak ditulis adalah error.
- File config wajib ada; tidak ada mode "hanya dari env var" (sebelumnya
  pun tidak ada, karena `godotenv.Load()` error jika `.env` tidak ada).

## 6. Perubahan `cmd/api/main.go`

- Tambah `flag.String("config", ".config.toml", "path to TOML config file")`
  dan `flag.Parse()` sebelum `config.Load(*configPath)`.
- `Addr: ":" + cfg.App.Port` → `fmt.Sprintf(":%d", cfg.App.Port)`.
- `logger.Infof("Server is running on port %s", ...)` → `%d`.
- Error load tetap: `logger.Errorf("Failed to load config: %v", err)` lalu
  `os.Exit(1)`.

## 7. Helper `cmd/configenv`

`back/cmd/configenv/main.go` — CLI kecil untuk script shell.

- Flag `-config` (default `.config.toml`, sama dengan `cmd/api`).
- Memanggil `config.Load(path)` → validasi identik dengan backend.
- Sukses: mencetak ke stdout, satu baris per variabel, urutan tetap:

  ```
  export DB_HOST='localhost'
  export DB_PORT='5432'
  export DB_USER='postgres'
  export DB_PASSWORD='postgres'
  export DB_NAME='pg_aggregator'
  export CASHI_SECRET_KEY='...'
  ```

  Nama variabel sengaja sama dengan nama env lama supaya badan script tidak
  berubah. Hanya variabel yang dipakai script yang dicetak.
- Quoting: setiap nilai dibungkus petik tunggal, dan `'` di dalam nilai
  ditulis sebagai `'\''`. Logika ini ada di fungsi terpisah
  `shellQuote(string) string` agar bisa di-unit test.
- Gagal: pesan error ke stderr, stdout kosong, exit code 1.

## 8. Perubahan Script

### 8.1 `back/scripts/{migrate,seed,db-create}.sh`

Blok berikut di ketiga script:

```bash
if [[ ! -f .env ]]; then
  echo ".env file not found in $ROOT_DIR"
  exit 1
fi

set -a
. ./.env
set +a
```

diganti dengan:

```bash
# Two steps on purpose: `eval "$(cmd)"` would swallow a configenv failure
# (eval of an empty string returns 0), so set -e would not stop the script.
config_env="$(go run ./cmd/configenv)"
eval "$config_env"
```

Script sudah `cd "$ROOT_DIR"` (= `back/`) sebelumnya, jadi path default
`.config.toml` benar. Baris `: "${DB_*:?...}"` setelahnya dibiarkan apa
adanya. Catatan: karena `:?` juga menolak nilai kosong, `db.password = ""`
diterima backend tapi ditolak script — sama dengan perilaku sekarang.

### 8.2 `flow-test/lib/common.sh`

Blok yang membaca `CASHI_SECRET_KEY` dari `../back/.env` (via `grep`)
diganti dengan pembacaan lewat `configenv`:

- Hanya dijalankan jika `CASHI_SECRET_KEY` belum di-set (nilai di
  `flow-test/.env` tetap menang — prioritas sama dengan sekarang) **dan**
  `../back/.config.toml` ada.
- Dijalankan di subshell (`cd "$SCRIPT_DIR/../back" && go run ./cmd/configenv`)
  dan hanya `CASHI_SECRET_KEY` yang diambil; `DB_*` tidak ikut di-export ke
  environment flow-test.
- Jika `configenv` gagal, `common.sh` **tidak** menghentikan flow-test
  (mode sandbox tidak butuh secret ini); cukup cetak peringatan ke stderr.
  `05_webhook_settle.sh` sudah punya guard sendiri yang gagal dengan pesan
  jelas jika `CASHI_SECRET_KEY` kosong — pesannya diperbarui menyebut
  `back/.config.toml`.

## 9. File & Dokumentasi

| File | Perubahan |
|---|---|
| `back/.env.example` | Dihapus |
| `back/.config.example.toml` | Baru (isi §4) |
| `back/.gitignore` | Tambah `.config.toml` di bawah komentar `# Environment variables`; baris `.env`, `.env.local`, `.env.production` dipertahankan agar `.env` lama tidak ter-commit |
| `back/go.mod`, `back/go.sum` | Tambah `github.com/BurntSushi/toml`, hapus `github.com/joho/godotenv` (`go mod tidy`) |
| `README.md` (root) | Perintah quick start `cp .env.example .env` → `cp .config.example.toml .config.toml` |
| `RUNNING.md` | Bagian setup, catatan port (tabel tiga file), tabel env var wajib, troubleshooting (`.env file not found`) |
| `back/README.md` | Setup, tabel "Environment Variables Penting" → "Config Penting" dengan nama key TOML |
| `flow-test/README.md` | Referensi `back/.env` (baris 29 dan 50) → `back/.config.toml`; instruksi `cp .env.example .env` milik flow-test sendiri tidak berubah |
| `flow-test/05_webhook_settle.sh` | Pesan guard `expected it in back/.env` → `expected it in back/.config.toml` |
| `.air.toml`, CI workflow | Tidak berubah (air tidak memantau file config; CI tidak membaca config) |

## 10. Testing

Gaya mengikuti test yang ada: `go test` standar, table-driven, tanpa mock
library, hermetic (file sementara via `t.TempDir()`).

### 10.1 `internal/config/config_test.go`

| Kasus | Ekspektasi |
|---|---|
| File lengkap & valid | Semua field terisi, port bertipe `int` dengan nilai benar |
| File tidak ada | Error menyebut path dan `copy .config.example.toml` |
| Sintaks TOML salah | Error berprefix `parse <path>:` |
| Beberapa key tak dikenal | Semua disebut, terurut |
| Beberapa key hilang | Semua disebut, terurut |
| Key tak dikenal + key hilang bersamaan | Keduanya dalam satu error |
| Key ditulis `""` (mis. `cashi.api_key` saat development) | Diterima |
| `app.url` kosong / `jwt_secret` kosong | Ditolak |
| Production tanpa kredensial Cashi | Ditolak |
| Production dengan `jwt_secret = "change-this-secret"` | Ditolak |
| Secret tidak bocor | Pasang nilai secret unik, picu error validasi & struktural, assert nilai tidak muncul di pesan |
| `.config.example.toml` asli bisa di-load | `Load("../../.config.example.toml")` sukses |

Test terakhir menjaga template tetap sinkron dengan struct: karena semua key
wajib (D4) dan key tak dikenal ditolak (D6), menambah field tanpa
memperbarui template (atau sebaliknya) langsung membuat test gagal. Test ini
juga memvalidasi walk reflection.

### 10.2 `cmd/configenv`

- Unit test `shellQuote` untuk nilai berisi `'`, `$`, spasi, `"`, `\`, dan
  string kosong.
- Test bolak-balik: output di-`eval` oleh `bash -c`, lalu nilai yang dibaca
  kembali dibandingkan dengan aslinya. Di-skip (`t.Skip`) jika `bash` tidak
  ada di PATH.

### 10.3 Verifikasi Manual

1. `go vet ./...` dan `go test ./... -race` — seluruh test lama tetap lulus.
2. `make db-create`, `make migrate`, `make seed`, `make run` memakai
   `.config.toml`; `curl /api/v1/health` → `{"status":"ok"}`.
3. `flow-test/run_all.sh` (sandbox).
4. Negatif: typo nama key → `make run` dan `make migrate` sama-sama gagal
   dengan pesan menyebut key tersebut.

## 11. Langkah Migrasi Lokal

`back/.env` developer yang ada (di workspace ini: `APP_PORT=8090` dan
kredensial Cashi) harus dipindahkan manual satu kali ke `back/.config.toml`.
Saat implementasi, user ditanya dulu apakah file ini dibuat otomatis dari
`.env` yang ada atau dibuat sendiri, karena berisi secret. `back/.env` lama
tidak dihapus otomatis.
