package main

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/config"
)

func TestShellQuote(t *testing.T) {
	tests := map[string]string{
		"":             `''`,
		"plain":        `'plain'`,
		"it's":         `'it'\''s'`,
		"a b":          `'a b'`,
		`$HOME "x" \n`: `'$HOME "x" \n'`,
		"''":           `''\'''\'''`,
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
		App:   config.AppConfig{Environment: "development"},
	}
	want := "export DB_HOST='h'\n" +
		"export DB_PORT='5432'\n" +
		"export DB_USER='u'\n" +
		"export DB_PASSWORD='p'\n" +
		"export DB_NAME='n'\n" +
		"export CASHI_SECRET_KEY='s'\n" +
		"export APP_ENVIRONMENT='development'\n"
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
		App:   config.AppConfig{Environment: "production"},
	}
	script := render(cfg) +
		`printf '%s\0' "$DB_HOST" "$DB_PORT" "$DB_USER" "$DB_PASSWORD" "$DB_NAME" "$CASHI_SECRET_KEY" "$APP_ENVIRONMENT"`

	out, err := exec.Command(bash, "-c", script).Output()
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	want := []string{cfg.DB.Host, "5432", cfg.DB.User, cfg.DB.Password, cfg.DB.Name, cfg.Cashi.SecretKey, cfg.App.Environment}
	if len(got) != len(want) {
		t.Fatalf("got %d values %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("value %d = %q, want %q", i, got[i], want[i])
		}
	}
}
