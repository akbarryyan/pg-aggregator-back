package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/admin"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/google/uuid"
)

// Real-PostgreSQL checks for migration 018 and the revocation queries
// (opt-in via TEST_DATABASE_URL, like payment_repository_pg_test.go).

func insertPGAdmin(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := db.Exec(`INSERT INTO admins (id, name, email, password_hash) VALUES ($1, 'A', $2, 'h')`,
		id, id.String()+"@example.com"); err != nil {
		t.Fatalf("insert admin: %v", err)
	}
	return id
}

func insertPGMerchantUser(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	mid := uuid.New()
	if _, err := db.Exec(`INSERT INTO merchants (id, name, business_name, email) VALUES ($1, 'M', 'B', $2)`,
		mid, mid.String()+"@example.com"); err != nil {
		t.Fatalf("insert merchant: %v", err)
	}
	uid := uuid.New()
	if _, err := db.Exec(`INSERT INTO merchant_users (id, merchant_id, name, email, password_hash) VALUES ($1, $2, 'U', $3, 'h')`,
		uid, mid, uid.String()+"@example.com"); err != nil {
		t.Fatalf("insert merchant user: %v", err)
	}
	return uid
}

func TestPG_AdminSessionsValidAfter(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewAdminRepository(db)
	ctx := context.Background()
	id := insertPGAdmin(t, db)

	a, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.SessionsValidAfter != nil {
		t.Fatalf("new admin must have no cutoff, got %v", a.SessionsValidAfter)
	}

	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if err := repo.RevokeSessions(ctx, id, at); err != nil {
		t.Fatalf("RevokeSessions: %v", err)
	}
	a, _ = repo.GetByID(ctx, id)
	if a.SessionsValidAfter == nil || !a.SessionsValidAfter.Equal(at) {
		t.Fatalf("GetByID cutoff = %v, want %v", a.SessionsValidAfter, at)
	}
	byEmail, err := repo.GetByEmail(ctx, a.Email)
	if err != nil || byEmail.SessionsValidAfter == nil {
		t.Fatalf("GetByEmail cutoff missing: %v %v", byEmail, err)
	}

	before := time.Now().UTC().Add(-time.Second)
	if err := repo.UpdatePasswordHash(ctx, id, "newhash"); err != nil {
		t.Fatal(err)
	}
	a, _ = repo.GetByID(ctx, id)
	if a.SessionsValidAfter == nil || a.SessionsValidAfter.Before(before) {
		t.Fatalf("UpdatePasswordHash must stamp cutoff, got %v", a.SessionsValidAfter)
	}

	updated, err := repo.UpdateProfile(ctx, id, "New", a.Email)
	if err != nil || updated.SessionsValidAfter == nil {
		t.Fatalf("UpdateProfile RETURNING must include cutoff: %v %v", updated, err)
	}

	if err := repo.RevokeSessions(ctx, uuid.New(), at); err != admin.ErrAdminNotFound {
		t.Fatalf("unknown admin: err = %v, want ErrAdminNotFound", err)
	}
}

func TestPG_MerchantUserSessionsValidAfter(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewMerchantUserRepository(db)
	ctx := context.Background()
	id := insertPGMerchantUser(t, db)

	u, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if u.SessionsValidAfter != nil {
		t.Fatalf("new user must have no cutoff, got %v", u.SessionsValidAfter)
	}

	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if err := repo.RevokeSessions(ctx, id, at); err != nil {
		t.Fatalf("RevokeSessions: %v", err)
	}
	u, _ = repo.GetByID(ctx, id)
	if u.SessionsValidAfter == nil || !u.SessionsValidAfter.Equal(at) {
		t.Fatalf("GetByID cutoff = %v, want %v", u.SessionsValidAfter, at)
	}
	byEmail, err := repo.GetByEmail(ctx, u.Email)
	if err != nil || byEmail.SessionsValidAfter == nil {
		t.Fatalf("GetByEmail cutoff missing: %v %v", byEmail, err)
	}

	before := time.Now().UTC().Add(-time.Second)
	if err := repo.UpdatePasswordHash(ctx, id, "newhash"); err != nil {
		t.Fatal(err)
	}
	u, _ = repo.GetByID(ctx, id)
	if u.SessionsValidAfter == nil || u.SessionsValidAfter.Before(before) {
		t.Fatalf("UpdatePasswordHash must stamp cutoff, got %v", u.SessionsValidAfter)
	}

	if err := repo.RevokeSessions(ctx, uuid.New(), at); err != merchant.ErrMerchantUserNotFound {
		t.Fatalf("unknown user: err = %v, want ErrMerchantUserNotFound", err)
	}
}
