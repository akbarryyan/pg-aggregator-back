package config

import (
	"database/sql"
	"net/url"
	"os"
	"strconv"
	"testing"

	_ "github.com/lib/pq"
)

// The connection string the API builds must make PostgreSQL apply the
// statement and idle-in-transaction timeouts to every session.
func TestConnectionString_AppliesSessionTimeouts(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real PostgreSQL test")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	port := 5432
	if p := u.Port(); p != "" {
		port, err = strconv.Atoi(p)
		if err != nil {
			t.Fatalf("TEST_DATABASE_URL port %q: %v", p, err)
		}
	}
	if len(u.Path) < 2 {
		t.Fatalf("TEST_DATABASE_URL has no database name in its path")
	}
	pass, _ := u.User.Password()
	cfg := DBConfig{
		Host: u.Hostname(), Port: port, User: u.User.Username(), Password: pass,
		Name: u.Path[1:], SSLMode: "disable",
	}
	if m := u.Query().Get("sslmode"); m != "" {
		cfg.SSLMode = m
	}
	// A unix-socket URL puts the directory in the host query parameter.
	if h := u.Query().Get("host"); h != "" {
		cfg.Host = h
	}

	db, err := sql.Open("postgres", cfg.ConnectionString())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for param, want := range map[string]string{
		"statement_timeout":                   "30s",
		"idle_in_transaction_session_timeout": "1min",
	} {
		var got string
		if err := db.QueryRow("SHOW " + param).Scan(&got); err != nil {
			t.Fatalf("SHOW %s: %v", param, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", param, got, want)
		}
	}
}
