package service

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

// newServicePGDB opens a throwaway schema built from the migrations. Opt-in
// via TEST_DATABASE_URL (same convention as the repository PG tests).
func newServicePGDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real PostgreSQL test")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	schema := "pgtest_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		admin.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(f), err)
		}
	}
	return db
}

func TestPG_AdminCreateMerchant_CreatesMerchantAndOwnerLogin(t *testing.T) {
	db := newServicePGDB(t)
	merchantRepo := repository.NewMerchantRepository(db)
	userRepo := repository.NewMerchantUserRepository(db)
	svc := NewAdminMerchantService(merchantRepo, nil, userRepo, nil)
	const pw = "s3cret-pass"

	resp, err := svc.CreateMerchant(context.Background(), &merchant.CreateMerchantRequest{
		Name: " Acme Owner ", Email: " Owner@Example.COM ", BusinessName: "Acme", Password: pw,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// merchant is readable through the repository (phone must be '' not NULL)
	m, err := merchantRepo.GetByID(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("merchant not readable: %v", err)
	}
	if m.Email != "owner@example.com" || m.Phone != "" {
		t.Errorf("unexpected merchant: email=%q phone=%q", m.Email, m.Phone)
	}

	u, err := userRepo.GetByEmail(context.Background(), "owner@example.com")
	if err != nil {
		t.Fatalf("owner user missing: %v", err)
	}
	if u.MerchantID != resp.ID || u.Role != "owner" || !u.IsActive || u.Name != "Acme Owner" {
		t.Errorf("unexpected user: %+v", u)
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(pw)) != nil {
		t.Error("stored hash does not match the password (login would fail)")
	}

	// the same e-mail again is a conflict and creates nothing
	_, err = svc.CreateMerchant(context.Background(), &merchant.CreateMerchantRequest{
		Name: "Other", Email: "owner@example.com", BusinessName: "Other", Password: pw,
	})
	if err != merchant.ErrMerchantAlreadyExists {
		t.Fatalf("want ErrMerchantAlreadyExists, got %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM merchants`).Scan(&n); err != nil || n != 1 {
		t.Errorf("merchants count = %d err=%v, want 1", n, err)
	}
}
