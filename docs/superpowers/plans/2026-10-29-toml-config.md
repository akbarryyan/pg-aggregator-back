# TOML Config Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the backend's `.env`/godotenv config with a typed, sectioned `.config.toml` that has no defaults and rejects unknown keys, and move every `.env` consumer (API, bash scripts, flow-test, docs) onto it.

**Architecture:** `internal/config` decodes `.config.toml` with BurntSushi/toml into the existing `Config` structs (now tagged, ports as `int`), then checks unknown keys (`MetaData.Undecoded`), missing keys (reflection over `toml` tags + `MetaData.IsDefined`) and value rules (`Validate`). The API reads the path from a `-config` flag. A tiny `cmd/configenv` CLI reuses the same loader to print `export NAME='value'` lines that bash scripts `eval`.

**Tech Stack:** Go 1.25, `github.com/BurntSushi/toml` v1.6.0, bash.

**Spec:** [`back/docs/superpowers/specs/2026-10-29-toml-config-design.md`](../specs/2026-10-29-toml-config-design.md)

## Global Constraints

- **NO git commits, NO `git add`, NO branches — ever — in this work (explicit user instruction).** Every task ends with "report changed files", not a commit.
- Go module path: `github.com/akbarryyan/pg-aggregator-back`. All Go commands run from `back/`.
- Only new dependency: `github.com/BurntSushi/toml v1.6.0`. Remove `github.com/joho/godotenv`.
- `internal/config` must not read environment variables (no `os.Getenv`, no godotenv).
- No default values: every key declared on `Config` must be present in the file; an explicit `""` is allowed unless `Validate` forbids it; unknown keys are an error.
- Error messages from `internal/config` and `cmd/configenv` name keys/paths/line numbers only — **never config values**.
- Default config path is `.config.toml` (relative to working directory), overridable with `-config <path>` in both `cmd/api` and `cmd/configenv`.
- `cmd/configenv` prints exactly these variables, in this order: `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`, `CASHI_SECRET_KEY`.
- Do not touch `front/` or `flow-test/.env`.
- Match surrounding code style: comments explain *why*, not *what*; table-driven tests with the standard `testing` package, no mock libraries.

## File Structure

| File | Action | Responsibility |
|---|---|---|
| `back/internal/config/config.go` | Rewrite | Config structs, `Load(path)`, structural checks, `Validate` |
| `back/internal/config/config_test.go` | Create | Tests for loading, structural errors, validation, secret non-leakage, example file |
| `back/.config.example.toml` | Create | Complete committed template (all keys) |
| `back/cmd/api/main.go` | Modify | `-config` flag, `Load(path)`, int port formatting |
| `back/go.mod`, `back/go.sum` | Modify | Add toml, drop godotenv |
| `back/cmd/configenv/main.go` | Create | CLI printing shell `export` lines |
| `back/cmd/configenv/main_test.go` | Create | `shellQuote`, `render` format, bash round-trip |
| `back/scripts/migrate.sh`, `seed.sh`, `db-create.sh` | Modify | Load DB vars via `configenv` |
| `flow-test/lib/common.sh` | Modify | Read `CASHI_SECRET_KEY` via `configenv` |
| `flow-test/05_webhook_settle.sh` | Modify | Guard message points at `back/.config.toml` |
| `back/.gitignore` | Modify | Ignore `.config.toml` |
| `back/.env.example` | Delete | Replaced by `.config.example.toml` |
| `README.md`, `RUNNING.md`, `back/README.md`, `flow-test/README.md` | Modify | Setup/troubleshooting docs |

---

### Task 1: TOML loader in `internal/config` + API wiring

**Files:**
- Rewrite: `back/internal/config/config.go`
- Create: `back/internal/config/config_test.go`
- Create: `back/.config.example.toml`
- Modify: `back/cmd/api/main.go` (imports; `config.Load()` call near line 33; `Addr:` near line 148; `logger.Infof("Server is running on port %s"...)` near line 155)
- Modify: `back/go.mod`, `back/go.sum`

**Interfaces:**
- Consumes: nothing.
- Produces (used by Task 2):
  - `func Load(path string) (*Config, error)`
  - `func (c *Config) Validate() error`
  - `type Config struct { App AppConfig; DB DBConfig; Redis RedisConfig; Cashi CashiConfig; Security SecurityConfig }`
  - `DBConfig{Host string; Port int; User string; Password string; Name string; SSLMode string}`
  - `CashiConfig{BaseURL string; APIKey string; SecretKey string}`
  - `AppConfig{Name string; Environment string; Port int; URL string; FrontendURL string}`
  - `RedisConfig{Host string; Port int; Password string}`, `SecurityConfig{JWTSecret string}`

- [ ] **Step 1: Add the TOML dependency**

Run (from `back/`):
```bash
go get github.com/BurntSushi/toml@v1.6.0
```
Expected: `go: added github.com/BurntSushi/toml v1.6.0`. (It is marked `// indirect` until code imports it; `go mod tidy` in Step 8 fixes that.)

- [ ] **Step 2: Create the example config `back/.config.example.toml`**

```toml
# Backend config. Copy to .config.toml (gitignored) and fill in values:
#   cp .config.example.toml .config.toml
# Every key below is required — there are no defaults — and unknown keys
# are rejected, so a typo fails startup instead of being silently ignored.
# An empty string ("") is allowed unless noted otherwise.

[app]
name         = "pg-aggregator"           # must not be empty
environment  = "development"             # development | production
port         = 8080
url          = "http://localhost:8080"   # must not be empty
frontend_url = "http://localhost:3000"   # must not be empty; also the CORS origin

[db]
host     = "localhost"                   # must not be empty
port     = 5432
user     = "postgres"
password = "postgres"
name     = "pg_aggregator"               # must not be empty
sslmode  = "disable"

[redis]  # not used yet; kept so the config fields stay unchanged
host     = "localhost"
port     = 6379
password = ""

[cashi]  # real QRIS provider, used only for environment=production payments
base_url   = "https://cashi.id"
api_key    = ""                          # x-api-key header; required when app.environment = "production"
secret_key = ""                          # webhook HMAC secret; required when app.environment = "production"

[security]
jwt_secret = "change-this-secret"        # must not be empty; must be changed in production
```

- [ ] **Step 3: Write the failing tests `back/internal/config/config_test.go`**

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validTOML is a complete, valid development config. Values that tests
// replace are unique strings so replaceOnce hits exactly one line.
const validTOML = `
[app]
name         = "pg-aggregator"
environment  = "development"
port         = 8090
url          = "http://localhost:8090"
frontend_url = "http://localhost:3000"

[db]
host     = "db.local"
port     = 5433
user     = "pguser"
password = "db-pass-123"
name     = "pg_aggregator"
sslmode  = "disable"

[redis]
host     = "redis.local"
port     = 6380
password = ""

[cashi]
base_url   = "https://cashi.id"
api_key    = ""
secret_key = ""

[security]
jwt_secret = "dev-jwt-secret"
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// replaceOnce swaps exactly one occurrence of old, failing the test if old
// isn't in s exactly once — guards against a fixture edit that silently
// mutates nothing.
func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if n := strings.Count(s, old); n != 1 {
		t.Fatalf("expected exactly one %q in fixture, found %d", old, n)
	}
	return strings.Replace(s, old, new, 1)
}

func TestLoad_ValidFile(t *testing.T) {
	cfg, err := Load(writeConfig(t, validTOML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.App.Port != 8090 || cfg.DB.Port != 5433 || cfg.Redis.Port != 6380 {
		t.Errorf("ports = app %d, db %d, redis %d; want 8090, 5433, 6380", cfg.App.Port, cfg.DB.Port, cfg.Redis.Port)
	}
	if cfg.App.URL != "http://localhost:8090" || cfg.App.FrontendURL != "http://localhost:3000" {
		t.Errorf("app urls = %q, %q", cfg.App.URL, cfg.App.FrontendURL)
	}
	if cfg.Security.JWTSecret != "dev-jwt-secret" {
		t.Errorf("jwt secret = %q", cfg.Security.JWTSecret)
	}
	// An explicit "" is a present key, not a missing one (cashi creds are
	// optional outside production).
	if cfg.Cashi.APIKey != "" || cfg.Cashi.SecretKey != "" {
		t.Errorf("cashi creds = %q, %q; want empty", cfg.Cashi.APIKey, cfg.Cashi.SecretKey)
	}

	wantConn := "host=db.local port=5433 user=pguser password=db-pass-123 dbname=pg_aggregator sslmode=disable"
	if got := cfg.DB.ConnectionString(); got != wantConn {
		t.Errorf("ConnectionString() = %q, want %q", got, wantConn)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.toml")
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	for _, want := range []string{"config file not found: " + path, "copy .config.example.toml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestLoad_SyntaxErrorHidesValue(t *testing.T) {
	content := replaceOnce(t, validTOML, `password = "db-pass-123"`, `password = s3cr3t-unquoted`)
	_, err := Load(writeConfig(t, content))
	if err == nil {
		t.Fatal("expected syntax error")
	}
	msg := err.Error()
	for _, want := range []string{"TOML syntax error at line 13", "near key db.password"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "s3cr3t-unquoted") {
		t.Errorf("error leaks the raw value: %q", msg)
	}
}

func TestLoad_TypeMismatch(t *testing.T) {
	content := replaceOnce(t, validTOML, `port         = 8090`, `port         = "8090"`)
	_, err := Load(writeConfig(t, content))
	if err == nil {
		t.Fatal("expected type error")
	}
	if !strings.Contains(err.Error(), "app.port") {
		t.Errorf("error %q does not name app.port", err)
	}
}

func TestLoad_UnknownKeys(t *testing.T) {
	content := replaceOnce(t, validTOML, `sslmode  = "disable"`, "sslmode  = \"disable\"\npaswrod  = \"typo-secret\"")
	content += "\n[dbb]\nhost = \"x\"\n"

	_, err := Load(writeConfig(t, content))
	if err == nil {
		t.Fatal("expected unknown-key error")
	}
	msg := err.Error()
	// md.Undecoded reports both "dbb" and "dbb.host"; only the leaf is shown.
	if !strings.Contains(msg, "unknown keys: db.paswrod, dbb.host\n") && !strings.HasSuffix(msg, "unknown keys: db.paswrod, dbb.host") {
		t.Errorf("error %q does not list exactly the unknown leaf keys", msg)
	}
	if strings.Contains(msg, "missing keys") {
		t.Errorf("error %q reports missing keys for a complete file", msg)
	}
	if strings.Contains(msg, "typo-secret") {
		t.Errorf("error leaks a value: %q", msg)
	}
}

func TestLoad_MissingKeys(t *testing.T) {
	content := replaceOnce(t, validTOML, "password = \"db-pass-123\"\n", "")
	content = replaceOnce(t, content, "[redis]\nhost     = \"redis.local\"\nport     = 6380\npassword = \"\"\n", "")

	_, err := Load(writeConfig(t, content))
	if err == nil {
		t.Fatal("expected missing-key error")
	}
	want := "missing keys: db.password, redis.host, redis.password, redis.port"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err, want)
	}
}

func TestLoad_UnknownAndMissingReportedTogether(t *testing.T) {
	content := replaceOnce(t, validTOML, `password = "db-pass-123"`, `paswrod = "db-pass-123"`)
	path := writeConfig(t, content)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected structural error")
	}
	want := "invalid config " + path + ":\n  unknown keys: db.paswrod\n  missing keys: db.password"
	if err.Error() != want {
		t.Errorf("error =\n%s\nwant\n%s", err, want)
	}
}

func TestLoad_Validation(t *testing.T) {
	type edit struct{ old, new string }
	tests := []struct {
		name    string
		edits   []edit
		wantErr string // "" means Load must succeed
	}{
		{
			name:    "empty app.name",
			edits:   []edit{{`name         = "pg-aggregator"`, `name         = ""`}},
			wantErr: "app.name is required",
		},
		{
			name:    "empty app.url",
			edits:   []edit{{`url          = "http://localhost:8090"`, `url          = ""`}},
			wantErr: "app.url is required",
		},
		{
			name:    "empty app.frontend_url",
			edits:   []edit{{`frontend_url = "http://localhost:3000"`, `frontend_url = ""`}},
			wantErr: "app.frontend_url is required",
		},
		{
			name:    "empty db.host",
			edits:   []edit{{`host     = "db.local"`, `host     = ""`}},
			wantErr: "db.host and db.name are required",
		},
		{
			name:    "empty jwt_secret in development",
			edits:   []edit{{`jwt_secret = "dev-jwt-secret"`, `jwt_secret = ""`}},
			wantErr: "security.jwt_secret is required",
		},
		{
			name:    "production without cashi credentials",
			edits:   []edit{{`environment  = "development"`, `environment  = "production"`}},
			wantErr: "cashi.api_key and cashi.secret_key are required in production",
		},
		{
			name: "production with placeholder jwt_secret",
			edits: []edit{
				{`environment  = "development"`, `environment  = "production"`},
				{`api_key    = ""`, `api_key    = "k"`},
				{`secret_key = ""`, `secret_key = "s"`},
				{`jwt_secret = "dev-jwt-secret"`, `jwt_secret = "change-this-secret"`},
			},
			wantErr: "security.jwt_secret must be changed in production",
		},
		{
			name: "valid production",
			edits: []edit{
				{`environment  = "development"`, `environment  = "production"`},
				{`api_key    = ""`, `api_key    = "k"`},
				{`secret_key = ""`, `secret_key = "s"`},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := validTOML
			for _, e := range tt.edits {
				content = replaceOnce(t, content, e.old, e.new)
			}
			_, err := Load(writeConfig(t, content))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Load: unexpected error %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("Load error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_ErrorsNeverContainSecretValues(t *testing.T) {
	const marker = "LEAKCHECK"
	secrets := validTOML
	secrets = replaceOnce(t, secrets, `password = "db-pass-123"`, `password = "LEAKCHECK-db"`)
	secrets = replaceOnce(t, secrets, `jwt_secret = "dev-jwt-secret"`, `jwt_secret = "LEAKCHECK-jwt"`)

	cases := map[string]string{
		// structural error with a secret-looking value on the unknown key
		"structural": secrets + "\n[extra]\ntoken = \"LEAKCHECK-unknown\"\n",
		// validation error while secrets are set
		"validation": replaceOnce(t, secrets, `environment  = "development"`, `environment  = "production"`),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeConfig(t, content))
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), marker) {
				t.Errorf("error leaks a config value: %q", err)
			}
		})
	}
}

// The committed template must always load: since every key is required
// and unknown keys are rejected, this fails as soon as Config and
// .config.example.toml drift apart.
func TestLoad_ExampleFileIsValid(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", ".config.example.toml"))
	if err != nil {
		t.Fatalf("Load(.config.example.toml): %v", err)
	}
	if cfg.App.Port != 8080 || cfg.DB.Port != 5432 {
		t.Errorf("example ports = app %d, db %d; want 8080, 5432", cfg.App.Port, cfg.DB.Port)
	}
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `go test ./internal/config/ -count=1`
Expected: FAIL — compile errors such as `too many arguments in call to Load` / `cannot use ... (untyped int constant) ... Port` because `Load` still takes no argument and ports are strings.

- [ ] **Step 5: Rewrite `back/internal/config/config.go`**

Replace the whole file with:

```go
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	App      AppConfig      `toml:"app"`
	DB       DBConfig       `toml:"db"`
	Redis    RedisConfig    `toml:"redis"`
	Cashi    CashiConfig    `toml:"cashi"`
	Security SecurityConfig `toml:"security"`
}

type AppConfig struct {
	Name        string `toml:"name"`
	Environment string `toml:"environment"`
	Port        int    `toml:"port"`
	URL         string `toml:"url"`
	FrontendURL string `toml:"frontend_url"`
}

type DBConfig struct {
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	User     string `toml:"user"`
	Password string `toml:"password"`
	Name     string `toml:"name"`
	SSLMode  string `toml:"sslmode"`
}

type RedisConfig struct {
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	Password string `toml:"password"`
}

type CashiConfig struct {
	// BaseURL e.g. https://cashi.id (see docs/cashi-api.md)
	BaseURL string `toml:"base_url"`
	// APIKey sent as x-api-key header on create-order / check-status
	APIKey string `toml:"api_key"`
	// SecretKey used only to verify webhook HMAC signatures
	SecretKey string `toml:"secret_key"`
}

type SecurityConfig struct {
	JWTSecret string `toml:"jwt_secret"`
}

// placeholderJWTSecret is the value shipped in .config.example.toml;
// Validate rejects it in production.
const placeholderJWTSecret = "change-this-secret"

// Load reads the TOML config at path. There are no defaults: every key
// declared on Config must be present in the file (an explicit "" is fine
// unless Validate forbids it), and keys Config doesn't declare are
// rejected so a typo fails startup instead of being silently ignored.
//
// Errors name keys and line numbers only, never values — the file holds
// secrets, and startup errors usually end up in a log aggregator.
func Load(path string) (*Config, error) {
	var cfg Config
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return nil, decodeError(path, err)
	}

	unknown := unknownKeys(md)
	missing := missingKeys(md, reflect.TypeOf(cfg), nil)
	if len(unknown) > 0 || len(missing) > 0 {
		return nil, structuralError(path, unknown, missing)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func decodeError(path string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("config file not found: %s (copy .config.example.toml to create it)", path)
	}
	// BurntSushi's syntax-error message quotes the offending raw text
	// (e.g. `expected value but found "s3cret" instead`), which may be a
	// secret — report only where it happened.
	var pe toml.ParseError
	if errors.As(err, &pe) {
		near := ""
		if pe.LastKey != "" {
			near = fmt.Sprintf(" (near key %s)", pe.LastKey)
		}
		return fmt.Errorf("parse %s: TOML syntax error at line %d%s; details hidden because the file may contain secrets", path, pe.Position.Line, near)
	}
	// Remaining decode errors (e.g. a string where an int is expected) name
	// the key and the types involved, not the value.
	return fmt.Errorf("parse %s: %w", path, err)
}

// unknownKeys lists the keys present in the file that Config doesn't
// declare. md.Undecoded also reports the enclosing table of an unknown
// section (both "dbb" and "dbb.host"), so a key that is the parent of
// another reported key is dropped — each problem is reported once.
func unknownKeys(md toml.MetaData) []string {
	var all []string
	for _, k := range md.Undecoded() {
		all = append(all, k.String())
	}

	var leaves []string
	for _, k := range all {
		isParent := false
		for _, other := range all {
			if strings.HasPrefix(other, k+".") {
				isParent = true
				break
			}
		}
		if !isParent {
			leaves = append(leaves, k)
		}
	}
	sort.Strings(leaves)
	return leaves
}

// missingKeys lists every key declared on t via `toml` tags (recursing
// into nested structs) that the file doesn't define. Deriving the list from
// the struct instead of hand-maintaining it means a newly added field is
// required automatically.
func missingKeys(md toml.MetaData, t reflect.Type, prefix []string) []string {
	var missing []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
		if name == "" || name == "-" {
			continue
		}
		path := append(append([]string{}, prefix...), name)
		if f.Type.Kind() == reflect.Struct {
			missing = append(missing, missingKeys(md, f.Type, path)...)
			continue
		}
		if !md.IsDefined(path...) {
			missing = append(missing, strings.Join(path, "."))
		}
	}
	sort.Strings(missing)
	return missing
}

// structuralError reports every unknown and missing key at once so the
// file can be fixed in a single pass.
func structuralError(path string, unknown, missing []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "invalid config %s:", path)
	if len(unknown) > 0 {
		fmt.Fprintf(&b, "\n  unknown keys: %s", strings.Join(unknown, ", "))
	}
	if len(missing) > 0 {
		fmt.Fprintf(&b, "\n  missing keys: %s", strings.Join(missing, ", "))
	}
	return errors.New(b.String())
}

func (c *Config) Validate() error {
	if c.App.Name == "" {
		return errors.New("app.name is required")
	}

	if c.App.URL == "" {
		return errors.New("app.url is required")
	}

	if c.App.FrontendURL == "" {
		return errors.New("app.frontend_url is required")
	}

	if c.DB.Host == "" || c.DB.Name == "" {
		return errors.New("db.host and db.name are required")
	}

	// Previously a default kept this non-empty; without defaults an empty
	// secret would sign JWTs with an empty HMAC key, making tokens forgeable.
	if c.Security.JWTSecret == "" {
		return errors.New("security.jwt_secret is required")
	}

	if c.App.Environment == "production" {
		if c.Cashi.APIKey == "" || c.Cashi.SecretKey == "" {
			return errors.New("cashi.api_key and cashi.secret_key are required in production")
		}
		if c.Security.JWTSecret == placeholderJWTSecret {
			return errors.New("security.jwt_secret must be changed in production")
		}
	}

	return nil
}

func (c *DBConfig) ConnectionString() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		c.Host, c.Port, c.User, c.Password, c.Name, c.SSLMode,
	)
}
```

- [ ] **Step 6: Run the config tests to verify they pass**

Run: `go test ./internal/config/ -count=1 -v`
Expected: PASS for all `TestLoad_*` tests (including every `TestLoad_Validation` subtest).

- [ ] **Step 7: Wire `cmd/api/main.go` to the new API**

In the import block, add `"flag"` (keep alphabetical order — between `"database/sql"` and `"fmt"`):

```go
import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net/http"
```

Replace:

```go
	cfg, err := config.Load()
```

with:

```go
	configPath := flag.String("config", ".config.toml", "path to the TOML config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
```

Replace:

```go
		Addr:         ":" + cfg.App.Port,
```

with:

```go
		Addr:         fmt.Sprintf(":%d", cfg.App.Port),
```

Replace:

```go
		logger.Infof("Server is running on port %s", cfg.App.Port)
```

with:

```go
		logger.Infof("Server is running on port %d", cfg.App.Port)
```

- [ ] **Step 8: Tidy modules and run the full suite**

Run:
```bash
go mod tidy
grep -n "godotenv\|BurntSushi" go.mod
go vet ./...
go test ./... -race -count=1
```
Expected: `go.mod` lists `github.com/BurntSushi/toml v1.6.0` as a direct requirement and no `godotenv`; `go vet` prints nothing; all tests pass (the existing 328 plus the new config tests).

- [ ] **Step 9: Check the API fails cleanly without a config file**

Run: `go run ./cmd/api -config /nonexistent.toml; echo "exit=$?"`
Expected: a JSON error log line containing `config file not found: /nonexistent.toml (copy .config.example.toml to create it)` and `exit=1`.

- [ ] **Step 10: Report (no commit)**

List changed files: `back/internal/config/config.go`, `back/internal/config/config_test.go`, `back/.config.example.toml`, `back/cmd/api/main.go`, `back/go.mod`, `back/go.sum`. Do not commit.

---

### Task 2: `cmd/configenv` CLI

**Files:**
- Create: `back/cmd/configenv/main.go`
- Create: `back/cmd/configenv/main_test.go`

**Interfaces:**
- Consumes (from Task 1): `config.Load(path string) (*config.Config, error)`; fields `cfg.DB.Host`, `cfg.DB.Port` (int), `cfg.DB.User`, `cfg.DB.Password`, `cfg.DB.Name`, `cfg.Cashi.SecretKey`; types `config.DBConfig`, `config.CashiConfig`.
- Produces (used by Task 3): the command `go run ./cmd/configenv [-config path]`, run from `back/`. On success stdout is exactly six lines `export NAME='value'` for `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`, `CASHI_SECRET_KEY`; on failure stdout is empty, the error goes to stderr, exit code 1.

- [ ] **Step 1: Write the failing tests `back/cmd/configenv/main_test.go`**

```go
package main

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/config"
)

func TestShellQuote(t *testing.T) {
	tests := map[string]string{
		"":              `''`,
		"plain":         `'plain'`,
		"it's":          `'it'\''s'`,
		"a b":           `'a b'`,
		`$HOME "x" \n`:  `'$HOME "x" \n'`,
		"''":            `''\'''\'''`,
	}
	for in, want := range tests {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestRender_Format(t *testing.T) {
	cfg := &config.Config{
		DB:    config.DBConfig{Host: "h", Port: 5432, User: "u", Password: "p", Name: "n"},
		Cashi: config.CashiConfig{SecretKey: "s"},
	}
	want := "export DB_HOST='h'\n" +
		"export DB_PORT='5432'\n" +
		"export DB_USER='u'\n" +
		"export DB_PASSWORD='p'\n" +
		"export DB_NAME='n'\n" +
		"export CASHI_SECRET_KEY='s'\n"
	if got := render(cfg); got != want {
		t.Errorf("render() =\n%s\nwant\n%s", got, want)
	}
}

// Proves the quoting survives a real shell: values full of shell
// metacharacters come back byte-for-byte after eval.
func TestRender_RoundTripThroughBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	cfg := &config.Config{
		DB: config.DBConfig{
			Host:     "db host",
			Port:     5432,
			User:     `u$USER`,
			Password: "p'a\"s$s `w` \\ ord\nline2",
			Name:     "",
		},
		Cashi: config.CashiConfig{SecretKey: `$(echo pwned)`},
	}
	script := render(cfg) +
		`printf '%s\0' "$DB_HOST" "$DB_PORT" "$DB_USER" "$DB_PASSWORD" "$DB_NAME" "$CASHI_SECRET_KEY"`

	out, err := exec.Command(bash, "-c", script).Output()
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	want := []string{cfg.DB.Host, "5432", cfg.DB.User, cfg.DB.Password, cfg.DB.Name, cfg.Cashi.SecretKey}
	if len(got) != len(want) {
		t.Fatalf("got %d values %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("value %d = %q, want %q", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/configenv/ -count=1`
Expected: FAIL — `undefined: shellQuote` / `undefined: render` (package has no non-test files yet; Go may report `no non-test Go files` — either counts as the expected failure).

- [ ] **Step 3: Write `back/cmd/configenv/main.go`**

```go
// Command configenv prints the backend config values that shell scripts
// need (scripts/*.sh, flow-test) as `export NAME='value'` lines. It uses
// the same loader and validation as the API, so scripts reject a bad
// config exactly like the server does. Usage from bash (run from back/):
//
//	config_env="$(go run ./cmd/configenv)"
//	eval "$config_env"
//
// The two steps matter: `eval "$(go run ./cmd/configenv)"` would swallow a
// failure, since eval of an empty string returns 0 and set -e never fires.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/akbarryyan/pg-aggregator-back/internal/config"
)

func main() {
	configPath := flag.String("config", ".config.toml", "path to the TOML config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configenv: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(render(cfg))
}

// render emits only the variables the scripts actually read, under their
// pre-TOML env var names so the scripts' bodies didn't need to change.
func render(cfg *config.Config) string {
	vars := []struct{ name, value string }{
		{"DB_HOST", cfg.DB.Host},
		{"DB_PORT", strconv.Itoa(cfg.DB.Port)},
		{"DB_USER", cfg.DB.User},
		{"DB_PASSWORD", cfg.DB.Password},
		{"DB_NAME", cfg.DB.Name},
		{"CASHI_SECRET_KEY", cfg.Cashi.SecretKey},
	}

	var b strings.Builder
	for _, v := range vars {
		fmt.Fprintf(&b, "export %s=%s\n", v.name, shellQuote(v.value))
	}
	return b.String()
}

// shellQuote wraps s in single quotes, inside which POSIX shells treat
// every character literally; an embedded ' is closed, escaped and
// reopened as '\''.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/configenv/ -count=1 -v`
Expected: PASS for `TestShellQuote`, `TestRender_Format`, `TestRender_RoundTripThroughBash`.

- [ ] **Step 5: Check the CLI end to end**

Run:
```bash
go run ./cmd/configenv -config .config.example.toml
go run ./cmd/configenv -config /nonexistent.toml; echo "exit=$?"
```
Expected: first command prints the six `export` lines with the example values (`export DB_HOST='localhost'` … `export CASHI_SECRET_KEY=''`). Second prints `configenv: config file not found: /nonexistent.toml (copy .config.example.toml to create it)` on stderr, nothing on stdout, and `exit=1`.

- [ ] **Step 6: Run vet and the full suite**

Run: `go vet ./... && go test ./... -race -count=1`
Expected: no vet output, all tests pass.

- [ ] **Step 7: Report (no commit)**

List changed files: `back/cmd/configenv/main.go`, `back/cmd/configenv/main_test.go`. Do not commit.

---

### Task 3: Move bash scripts and flow-test onto `configenv`

**Files:**
- Modify: `back/scripts/migrate.sh:7-15`
- Modify: `back/scripts/seed.sh:7-15`
- Modify: `back/scripts/db-create.sh:7-15`
- Modify: `flow-test/lib/common.sh:17-26`
- Modify: `flow-test/05_webhook_settle.sh:8`

**Interfaces:**
- Consumes (from Task 2): `go run ./cmd/configenv` run from `back/`, contract as described in Task 2 "Produces".
- Produces: nothing new for later tasks; scripts keep reading `$DB_HOST`, `$DB_PORT`, `$DB_USER`, `$DB_PASSWORD`, `$DB_NAME`, and flow-test keeps reading `$CASHI_SECRET_KEY`.

- [ ] **Step 1: Replace the `.env` block in all three back scripts**

In each of `back/scripts/migrate.sh`, `back/scripts/seed.sh`, `back/scripts/db-create.sh`, replace exactly:

```bash
if [[ ! -f .env ]]; then
  echo ".env file not found in $ROOT_DIR"
  exit 1
fi

set -a
. ./.env
set +a
```

with:

```bash
# Two steps on purpose: `eval "$(cmd)"` would swallow a configenv failure
# (eval of an empty string returns 0), so set -e would not stop the script.
config_env="$(go run ./cmd/configenv)"
eval "$config_env"
```

Leave everything else (including the `: "${DB_HOST:?...}"` guards that follow) unchanged. The scripts already `cd "$ROOT_DIR"` (= `back/`) before this block, so `configenv`'s default `.config.toml` path is correct.

- [ ] **Step 2: Replace the `back/.env` reader in `flow-test/lib/common.sh`**

Replace exactly:

```bash
# CASHI_SECRET_KEY belongs to back/ (it's the shared secret the real Cashi
# provider signs webhooks with). Rather than duplicating it into
# flow-test/.env — where it'd silently go stale after a rotation — read it
# straight from back/.env unless the user explicitly overrode it here.
BACK_ENV_FILE="$SCRIPT_DIR/../back/.env"
if [ -z "${CASHI_SECRET_KEY:-}" ] && [ -f "$BACK_ENV_FILE" ]; then
  CASHI_SECRET_KEY="$(grep -E '^CASHI_SECRET_KEY=' "$BACK_ENV_FILE" | tail -n1 | cut -d'=' -f2-)"
  export CASHI_SECRET_KEY
fi
```

with:

```bash
# CASHI_SECRET_KEY belongs to back/ (it's the shared secret the real Cashi
# provider signs webhooks with). Rather than duplicating it into
# flow-test/.env — where it'd silently go stale after a rotation — read it
# from back/.config.toml via back's configenv helper unless the user
# explicitly overrode it here. Only that one value is taken (in a subshell),
# so back's DB_* vars don't leak into the flow-test environment. A failure
# here only warns: sandbox runs don't need the secret, and
# 05_webhook_settle.sh has its own guard for production runs.
BACK_CONFIG_FILE="$SCRIPT_DIR/../back/.config.toml"
if [ -z "${CASHI_SECRET_KEY:-}" ] && [ -f "$BACK_CONFIG_FILE" ]; then
  if back_config_env="$(cd "$SCRIPT_DIR/../back" && go run ./cmd/configenv)"; then
    CASHI_SECRET_KEY="$(eval "$back_config_env" && printf '%s' "$CASHI_SECRET_KEY")"
    export CASHI_SECRET_KEY
  else
    echo "warning: could not read CASHI_SECRET_KEY from back/.config.toml (configenv failed, see above)" >&2
  fi
  unset back_config_env
fi
```

- [ ] **Step 3: Update the guard message in `flow-test/05_webhook_settle.sh`**

Replace:

```bash
: "${CASHI_SECRET_KEY:?CASHI_SECRET_KEY not found — expected it in back/.env, or set it manually in flow-test/.env}"
```

with:

```bash
: "${CASHI_SECRET_KEY:?CASHI_SECRET_KEY not found — expected cashi.secret_key in back/.config.toml, or set it manually in flow-test/.env}"
```

- [ ] **Step 4: Syntax-check every changed script**

Run (from workspace root):
```bash
for f in back/scripts/migrate.sh back/scripts/seed.sh back/scripts/db-create.sh flow-test/lib/common.sh flow-test/05_webhook_settle.sh; do bash -n "$f" && echo "ok $f"; done
grep -rn '\.env' back/scripts flow-test/lib flow-test/05_webhook_settle.sh
```
Expected: five `ok` lines. The grep prints only `flow-test/lib/common.sh` lines that reference `flow-test`'s own `$SCRIPT_DIR/.env` (lines 9 and 12) and the `flow-test/.env` mentions in comments/messages — no `back/.env`, no `./.env` in `back/scripts`.

- [ ] **Step 5: Verify a script stops on a missing config (no DB needed)**

Only if `back/.config.toml` does **not** exist yet (it is created in Task 5):

Run: `cd back && ./scripts/migrate.sh; echo "exit=$?"`
Expected: `configenv: config file not found: .config.toml (copy .config.example.toml to create it)` and a non-zero exit, with no `Running migrations/...` output — proving `set -e` caught the failure.

If `back/.config.toml` already exists, skip this step and note it in the report (the positive path is verified in Task 5).

- [ ] **Step 6: Verify the flow-test helper with a scratch config (no backend needed)**

Run (from workspace root):
```bash
tmp="$(mktemp -d)"
sed 's/^secret_key = ""/secret_key = "flowtest-secret"/' back/.config.example.toml > "$tmp/c.toml"
(cd back && config_env="$(go run ./cmd/configenv -config "$tmp/c.toml")" && eval "$config_env" && printf '%s\n' "$CASHI_SECRET_KEY")
rm -rf "$tmp"
```
Expected: prints `flowtest-secret`. (This exercises the exact capture/eval pattern used in `common.sh`; `common.sh` itself reads the real `back/.config.toml` and is run end to end in Task 5.)

- [ ] **Step 7: Report (no commit)**

List changed files: the five scripts above. Do not commit.

---

### Task 4: Docs, `.gitignore`, remove `.env.example`

**Files:**
- Modify: `back/.gitignore` (block under `# Environment variables`)
- Delete: `back/.env.example`
- Modify: `README.md`, `RUNNING.md`, `back/README.md`, `flow-test/README.md`

**Interfaces:**
- Consumes: file names and commands from Tasks 1–3 (`.config.toml`, `.config.example.toml`, `-config`, `go run ./cmd/configenv`, key names like `app.port`).
- Produces: nothing for later tasks.

- [ ] **Step 1: Ignore `.config.toml` in `back/.gitignore`**

Replace:

```
# Environment variables
.env
.env.local
.env.production
```

with:

```
# Local config (secrets). .env* kept so stale pre-TOML files never get committed.
.config.toml
.env
.env.local
.env.production
```

Verify: `cd back && git check-ignore -v .config.toml` prints a line pointing at `.gitignore` (this is a read-only git command — still no `git add`/commit).

- [ ] **Step 2: Delete `back/.env.example`**

Run: `rm back/.env.example`

- [ ] **Step 3: Update `README.md` (root)**

Replace:

```
cd back && cp .env.example .env && make migrate && make seed && make run
```

with:

```
cd back && cp .config.example.toml .config.toml && make migrate && make seed && make run
```

- [ ] **Step 4: Update `RUNNING.md`**

Apply these replacements (each `old` string is unique in the file):

1. `cp .env.example .env       # lalu edit sesuai kebutuhan (lihat catatan port di bawah)`
   → `cp .config.example.toml .config.toml   # lalu edit sesuai kebutuhan (lihat catatan port di bawah)`

2. ```
   > **Catatan port.** Default di `.env.example` adalah `APP_PORT=8080`. Di mesin
   > ini port 8080 sudah dipakai proses lain, jadi `back/.env` memakai
   > **`APP_PORT=8090`**. Tiga tempat ini harus konsisten:
   ```
   →
   ```
   > **Catatan port.** Default di `.config.example.toml` adalah `[app] port = 8080`.
   > Di mesin ini port 8080 sudah dipakai proses lain, jadi `back/.config.toml`
   > memakai **`port = 8090`**. Tiga tempat ini harus konsisten:
   ```

3. `> | `back/.env`        | `APP_PORT`, `APP_URL` | `8090`, `http://localhost:8090` |`
   → `> | `back/.config.toml` | `app.port`, `app.url` | `8090`, `http://localhost:8090` |`

4. Replace the whole section from the line `### Environment variable yang wajib benar` up to (not including) the next `---` line with:

   ```
   ### Config yang wajib benar (`back/.config.toml`)

   Semua key di `.config.example.toml` **wajib ada** — tidak ada nilai default.
   Key yang tidak dikenal (mis. typo) membuat backend dan `make migrate`/`make seed`
   gagal start dengan pesan yang menyebut nama key-nya. Nilai `""` boleh, kecuali
   untuk aturan di bawah. Script `make migrate`/`seed`/`db-create` membaca nilai
   `[db]` lewat `go run ./cmd/configenv`.

   | Key                                 | Kenapa penting                                                                |
   | ----------------------------------- | ----------------------------------------------------------------------------- |
   | `app.url`, `app.frontend_url`       | `config.Validate()` menolak start kalau kosong                                |
   | `[db]`                              | koneksi PostgreSQL; dipakai juga oleh `make migrate`/`make seed`              |
   | `security.jwt_secret`               | tidak boleh kosong; wajib diganti kalau `app.environment = "production"`      |
   | `cashi.api_key`, `cashi.secret_key` | hanya perlu valid untuk payment `production`; sandbox tidak menyentuh network |

   ```

5. `> jadi butuh `CASHI_API_KEY` / `CASHI_SECRET_KEY` yang valid di `back/.env`.`
   → `> jadi butuh `cashi.api_key` / `cashi.secret_key` yang valid di `back/.config.toml`.`

6. `> `CASHI_SECRET_KEY` dibaca otomatis dari `back/.env`, tidak perlu disalin.`
   → `> `CASHI_SECRET_KEY` dibaca otomatis dari `cashi.secret_key` di `back/.config.toml`, tidak perlu disalin.`

7. `port `APP_PORT` sudah dipakai proses lain. Cek `ss -ltnp \| grep 8090`, atau ganti `APP_PORT` (+ `APP_URL`, `NEXT_PUBLIC_API_URL`, `BASE_URL` flow-test).`
   → `port `app.port` sudah dipakai proses lain. Cek `ss -ltnp \| grep 8090`, atau ganti `app.port` (+ `app.url`, `NEXT_PUBLIC_API_URL`, `BASE_URL` flow-test).`

8. ``APP_URL` / `FRONTEND_URL` kosong di `.env` — `config.Validate()` menolak start.`
   → ``invalid config` (unknown/missing keys) atau `app.url` / `app.frontend_url` kosong di `.config.toml` — bandingkan dengan `.config.example.toml`.`

9. ``.env file not found` dari `make migrate``
   → ``config file not found` dari `make migrate`/`make run``

10. `pastikan `.env` sudah dicopy dari `.env.example`.`
    → `pastikan `.config.toml` sudah dicopy dari `.config.example.toml`.`

11. `atau `DB_*` salah.`
    → `atau section `[db]` di `.config.toml` salah.`

12. `dan `CASHI_SECRET_KEY` harus sama dengan yang di `back/.env`.`
    → `dan `CASHI_SECRET_KEY` harus sama dengan `cashi.secret_key` di `back/.config.toml`.`

Leave `cp .env.example .env` in the `flow-test` setup block and the `front/.env.local` line unchanged — those are flow-test's and the frontend's own files.

- [ ] **Step 5: Update `back/README.md`**

1. `├── cmd/api/              # Entry point (main.go): wiring DI manual + router setup`
   → keep the line, and add directly below it:
   `├── cmd/configenv/        # Cetak nilai config sebagai `export` untuk script shell`

2. `│   ├── config/           # Load & validasi .env`
   → `│   ├── config/           # Load & validasi .config.toml`

3. Replace:
   ````
   1. Copy environment file:
   ```bash
   cp .env.example .env
   ```

   2. Update `.env` dengan konfigurasi kamu (lihat tabel di bawah)
   ````
   with:
   ````
   1. Copy config file:
   ```bash
   cp .config.example.toml .config.toml
   ```

   2. Isi `.config.toml` dengan konfigurasi kamu (lihat tabel di bawah).
      Semua key wajib ada — tidak ada nilai default — dan key yang tidak
      dikenal ditolak. Path lain bisa dipakai lewat flag:
      `go run ./cmd/api -config /path/ke/config.toml`.
   ````

4. Replace the section from `### Environment Variables Penting` through the row `| `DB_*` | Koneksi PostgreSQL |` with:
   ```
   ### Config Penting (`.config.toml`)

   | Key | Keterangan |
   |---|---|
   | `app.url`, `app.frontend_url` | Wajib diisi — `config.Validate()` gagal start kalau kosong |
   | `security.jwt_secret` | Tidak boleh kosong; wajib diganti di production (`app.environment = "production"` menolak `change-this-secret`) |
   | `cashi.api_key` / `cashi.secret_key` | Wajib di production; sandbox merchant tidak butuh ini (mock, tanpa HTTP call) |
   | `[db]` | Koneksi PostgreSQL; script `make migrate`/`seed`/`db-create` membacanya lewat `go run ./cmd/configenv` |

   Error config (key hilang, key tidak dikenal, sintaks) hanya menyebut nama
   key dan nomor baris, tidak pernah nilainya, karena file ini berisi secret.
   ```

- [ ] **Step 6: Update `flow-test/README.md`**

1. `— otomatis dibaca dari `back/.env` tiap script jalan, jadi selalu sinkron`
   → `— otomatis dibaca dari `back/.config.toml` (lewat `go run ./cmd/configenv`) tiap script jalan, jadi selalu sinkron`

2. `> butuh `CASHI_API_KEY`/`CASHI_SECRET_KEY` valid di `back/.env`.`
   → `> butuh `cashi.api_key`/`cashi.secret_key` valid di `back/.config.toml`.`

- [ ] **Step 7: Sweep for stale references**

Run (from workspace root):
```bash
grep -rn 'back/\.env\|\.env\.example\|godotenv\|APP_PORT\|JWT_SECRET\|CASHI_API_KEY' README.md RUNNING.md back flow-test --include=*.md --include=*.sh --include=*.go --include=*.toml --include=*.yml --include=*.yaml --include=.gitignore | grep -v 'docs/superpowers/'
```
Expected: the only hits are `cp .env.example .env` lines that refer to **flow-test's own** `.env.example` (in `RUNNING.md` and `flow-test/README.md`). Fix any other hit.

- [ ] **Step 8: Report (no commit)**

List changed/deleted files: `back/.gitignore`, `back/.env.example` (deleted), `README.md`, `RUNNING.md`, `back/README.md`, `flow-test/README.md`. Do not commit.

---

### Task 5: Local migration and end-to-end verification

**Files:**
- Create (local only, gitignored): `back/.config.toml`

**Interfaces:**
- Consumes: everything from Tasks 1–4.
- Produces: a working local setup; verification evidence for the report.

- [ ] **Step 1: Ask the user before creating `back/.config.toml`**

`back/.env` holds real secrets (Cashi keys, JWT secret). Ask the user: "Should I create `back/.config.toml` from your existing `back/.env`, or will you create it yourself from `.config.example.toml`?" Stop until they answer.

- If they create it themselves: wait for confirmation, then go to Step 3.
- If they want it generated: go to Step 2.

- [ ] **Step 2 (only if approved): Generate `back/.config.toml` from `back/.env` without printing secrets**

Map each value using the table in spec §4 (`APP_PORT` → `app.port` as an integer, `DB_PORT` → `db.port` as an integer, `REDIS_PORT` → `redis.port` as an integer, everything else as a quoted string; `LOG_LEVEL` is dropped). Write the file with the Write tool, using `.config.example.toml`'s layout. Never echo the file or individual secret values to the terminal. Then run:

```bash
cd back && chmod 600 .config.toml && go run ./cmd/configenv > /dev/null && echo "config ok"
```
Expected: `config ok`. Leave `back/.env` in place (not deleted); tell the user it is now unused and can be removed by them.

- [ ] **Step 3: Static checks and full test suite**

Run (from `back/`): `go vet ./... && go test ./... -race -count=1`
Expected: no vet output; all tests pass. Record the pass count.

- [ ] **Step 4: Database scripts against the real config**

Run (from `back/`, PostgreSQL running):
```bash
make db-create
make migrate
make seed
```
Expected: `Database pg_aggregator already exists` (or `created`), `Migrations completed (applied: 0, skipped: 15)` (or similar), seed completes without error.

- [ ] **Step 5: API starts and answers**

Run `make run` in the background, then:
```bash
curl -s http://localhost:8090/api/v1/health
```
Expected: `{"status":"ok"}` (port `8090` per this workspace's config; use the configured `app.port`). The startup log contains `Server is running on port 8090`.

- [ ] **Step 6: flow-test sandbox run**

With the API still running, run (from `flow-test/`): `./run_all.sh`
Expected: all sandbox steps pass (health → admin login → API key → create payment → status `pending`), and no `warning: could not read CASHI_SECRET_KEY` line.

Stop the API afterwards.

- [ ] **Step 7: Negative check — typo key fails loudly**

Run (from `back/`):
```bash
tmp="$(mktemp -d)"
sed 's/^password = /paswrod = /' .config.example.toml > "$tmp/bad.toml"
go run ./cmd/api -config "$tmp/bad.toml"; echo "api exit=$?"
go run ./cmd/configenv -config "$tmp/bad.toml"; echo "configenv exit=$?"
rm -rf "$tmp"
```
Expected: both print `invalid config ...:` with `unknown keys: db.paswrod, redis.paswrod` and `missing keys: db.password, redis.password`, and both exit `1`. (The scripts use the same `configenv` code path, and Task 3 Step 5 showed they stop on a configenv failure.)

- [ ] **Step 8: Final report (no commit)**

Summarize for the user: all files changed across Tasks 1–5, test pass count, results of Steps 4–7, and that nothing was committed. Remind them `back/.env` is now unused.
