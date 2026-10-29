package config

import (
	"net/netip"
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

[rate_limit]
trusted_proxies = []

[rate_limit.auth]
requests_per_minute = 5
burst               = 4

[rate_limit.public]
requests_per_minute = 120
burst               = 30

[rate_limit.sensitive]
requests_per_minute = 10
burst               = 6
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

	rl := cfg.RateLimit
	if len(rl.TrustedProxies) != 0 {
		t.Errorf("trusted proxies = %d entries, want 0", len(rl.TrustedProxies))
	}
	if rl.Auth != (RateLimitRule{RequestsPerMinute: 5, Burst: 4}) ||
		rl.Public != (RateLimitRule{RequestsPerMinute: 120, Burst: 30}) ||
		rl.Sensitive != (RateLimitRule{RequestsPerMinute: 10, Burst: 6}) {
		t.Errorf("rate limit rules = %+v", rl)
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
		{
			name:    "rate limit requests_per_minute zero",
			edits:   []edit{{`requests_per_minute = 5`, `requests_per_minute = 0`}},
			wantErr: "rate_limit.auth.requests_per_minute must be greater than 0",
		},
		{
			name:    "rate limit burst zero",
			edits:   []edit{{`burst               = 30`, `burst               = 0`}},
			wantErr: "rate_limit.public.burst must be at least 1",
		},
		{
			name:    "rate limit burst negative",
			edits:   []edit{{`burst               = 6`, `burst               = -1`}},
			wantErr: "rate_limit.sensitive.burst must be at least 1",
		},
		{
			name:    "invalid trusted proxy CIDR",
			edits:   []edit{{`trusted_proxies = []`, `trusted_proxies = ["10.89.0.2/32", "not-a-cidr"]`}},
			wantErr: "rate_limit.trusted_proxies[1] is not a valid CIDR",
		},
		{
			name:  "valid trusted proxies (IPv4 and IPv6)",
			edits: []edit{{`trusted_proxies = []`, `trusted_proxies = ["10.89.0.2/32", "2001:db8::/32"]`}},
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
		// decode error passed through from the toml library via %w — the one
		// path Load doesn't sanitize itself, so guard it against upgrades
		"type mismatch": replaceOnce(t, secrets, `port         = 8090`, `port         = "LEAKCHECK-port"`),
		// trusted_proxies errors are reported by index only
		"invalid trusted proxy": replaceOnce(t, secrets, `trusted_proxies = []`, `trusted_proxies = ["LEAKCHECK-cidr"]`),
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
	if cfg.RateLimit.Auth != (RateLimitRule{RequestsPerMinute: 5, Burst: 5}) ||
		cfg.RateLimit.Public != (RateLimitRule{RequestsPerMinute: 120, Burst: 30}) ||
		cfg.RateLimit.Sensitive != (RateLimitRule{RequestsPerMinute: 10, Burst: 5}) {
		t.Errorf("example rate limits = %+v; want auth 5/5, public 120/30, sensitive 10/5", cfg.RateLimit)
	}
}

func TestLoad_MissingRateLimitSubTable(t *testing.T) {
	content := replaceOnce(t, validTOML, "[rate_limit.auth]\nrequests_per_minute = 5\nburst               = 4\n", "")
	_, err := Load(writeConfig(t, content))
	if err == nil {
		t.Fatal("expected missing-key error")
	}
	want := "missing keys: rate_limit.auth.burst, rate_limit.auth.requests_per_minute"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err, want)
	}
}

func TestRateLimitConfig_TrustedProxyPrefixes(t *testing.T) {
	rl := RateLimitConfig{TrustedProxies: []string{"10.89.0.2/32", "2001:db8::/32"}}
	got, err := rl.TrustedProxyPrefixes()
	if err != nil {
		t.Fatalf("TrustedProxyPrefixes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d prefixes, want 2", len(got))
	}
	if !got[0].Contains(netip.MustParseAddr("10.89.0.2")) || got[0].Contains(netip.MustParseAddr("10.89.0.3")) {
		t.Errorf("prefix 0 = %v, want exactly 10.89.0.2", got[0])
	}
	if !got[1].Contains(netip.MustParseAddr("2001:db8::1")) {
		t.Errorf("prefix 1 = %v, want 2001:db8::/32", got[1])
	}

	// A bare IP is not a CIDR; the error names the index, never the value.
	_, err = RateLimitConfig{TrustedProxies: []string{"10.89.0.2"}}.TrustedProxyPrefixes()
	if err == nil || err.Error() != "rate_limit.trusted_proxies[0] is not a valid CIDR" {
		t.Errorf("bare IP error = %v", err)
	}

	// Host bits set (10.89.0.2/24) would trust a whole subnet by typo.
	_, err = RateLimitConfig{TrustedProxies: []string{"10.89.0.0/24", "10.89.0.2/24"}}.TrustedProxyPrefixes()
	if err == nil || err.Error() != "rate_limit.trusted_proxies[1] is not a valid CIDR" {
		t.Errorf("non-canonical CIDR error = %v", err)
	}
}
