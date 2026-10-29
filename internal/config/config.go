package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"reflect"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	App       AppConfig       `toml:"app"`
	DB        DBConfig        `toml:"db"`
	Redis     RedisConfig     `toml:"redis"`
	Cashi     CashiConfig     `toml:"cashi"`
	Security  SecurityConfig  `toml:"security"`
	RateLimit RateLimitConfig `toml:"rate_limit"`
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

// RateLimitConfig holds the per-client-IP limiter budgets and the reverse
// proxies whose X-Forwarded-For header may name the client IP.
type RateLimitConfig struct {
	// TrustedProxies lists CIDRs of reverse proxies (e.g. Caddy) allowed to
	// report the client IP via X-Forwarded-For. Empty means the header is
	// never trusted and the TCP peer address is used.
	TrustedProxies []string      `toml:"trusted_proxies"`
	Auth           RateLimitRule `toml:"auth"`
	Public         RateLimitRule `toml:"public"`
	Sensitive      RateLimitRule `toml:"sensitive"`
}

type RateLimitRule struct {
	RequestsPerMinute int `toml:"requests_per_minute"`
	Burst             int `toml:"burst"`
}

// TrustedProxyPrefixes parses TrustedProxies. Errors name the entry's index,
// never its value, like every other config error. A CIDR with host bits set
// (e.g. 10.89.0.2/24) is rejected because it almost certainly is a typo that
// would trust a whole subnet.
func (r RateLimitConfig) TrustedProxyPrefixes() ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(r.TrustedProxies))
	for i, raw := range r.TrustedProxies {
		p, err := netip.ParsePrefix(raw)
		if err != nil || p != p.Masked() {
			return nil, fmt.Errorf("rate_limit.trusted_proxies[%d] is not a valid CIDR", i)
		}
		prefixes = append(prefixes, p)
	}
	return prefixes, nil
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

	if _, err := c.RateLimit.TrustedProxyPrefixes(); err != nil {
		return err
	}

	for _, r := range []struct {
		name string
		rule RateLimitRule
	}{
		{"auth", c.RateLimit.Auth},
		{"public", c.RateLimit.Public},
		{"sensitive", c.RateLimit.Sensitive},
	} {
		if r.rule.RequestsPerMinute <= 0 {
			return fmt.Errorf("rate_limit.%s.requests_per_minute must be greater than 0", r.name)
		}
		// A zero burst makes the limiter reject every request — login would
		// silently stop working instead of failing at startup.
		if r.rule.Burst < 1 {
			return fmt.Errorf("rate_limit.%s.burst must be at least 1", r.name)
		}
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
