package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

var apiKeyCols = []string{
	"id", "merchant_id", "name", "key_prefix", "key_hash", "is_active",
	"last_used_at", "revoked_at", "created_at", "updated_at",
}

func apiKeyRow(k *merchant.APIKey) []driver.Value {
	var lastUsed, revokedAt driver.Value
	if k.LastUsedAt != nil {
		lastUsed = *k.LastUsedAt
	}
	if k.RevokedAt != nil {
		revokedAt = *k.RevokedAt
	}
	return []driver.Value{
		k.ID, k.MerchantID, k.Name, k.KeyPrefix, k.KeyHash, k.IsActive,
		lastUsed, revokedAt, k.CreatedAt, k.UpdatedAt,
	}
}

func sampleAPIKey() *merchant.APIKey {
	now := time.Now()
	return &merchant.APIKey{
		ID:         uuid.New(),
		MerchantID: uuid.New(),
		Name:       "production",
		KeyPrefix:  "pk_live_",
		KeyHash:    "deadbeefdeadbeefdeadbeefdeadbeef",
		IsActive:   true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// TestMerchantAPIKeyRepository_GetByHash_Found is the exact query
// MerchantAPIKeyService.Authenticate relies on for every API-key-authed
// request — a regression here means merchant API auth breaks entirely.
func TestMerchantAPIKeyRepository_GetByHash_Found(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantAPIKeyRepository(db)
	k := sampleAPIKey()

	mock.ExpectQuery(`SELECT .* FROM merchant_api_keys\s+WHERE key_hash = \$1`).
		WithArgs(k.KeyHash).
		WillReturnRows(sqlmock.NewRows(apiKeyCols).AddRow(apiKeyRow(k)...))

	got, err := repo.GetByHash(context.Background(), k.KeyHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.MerchantID != k.MerchantID {
		t.Errorf("expected merchant_id %s, got %s", k.MerchantID, got.MerchantID)
	}
	if !got.IsActive {
		t.Errorf("expected active key")
	}
}

func TestMerchantAPIKeyRepository_GetByHash_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantAPIKeyRepository(db)

	mock.ExpectQuery(`SELECT .* FROM merchant_api_keys\s+WHERE key_hash = \$1`).
		WithArgs("unknown-hash").
		WillReturnError(sql.ErrNoRows)

	_, err := repo.GetByHash(context.Background(), "unknown-hash")
	if !errors.Is(err, merchant.ErrAPIKeyNotFound) {
		t.Fatalf("expected ErrAPIKeyNotFound, got %v", err)
	}
}

func TestMerchantAPIKeyRepository_GetByHash_RevokedKeyStillReturned(t *testing.T) {
	// GetByHash itself doesn't filter is_active/revoked_at — that's the
	// service layer's job (Authenticate checks IsActive/RevokedAt after
	// fetching). Pin this down so a future "helpful" WHERE is_active=true
	// added here doesn't silently change Authenticate's revoked-key error
	// message from "revoked" to "invalid".
	db, mock := newMockDB(t)
	repo := NewMerchantAPIKeyRepository(db)
	k := sampleAPIKey()
	k.IsActive = false
	now := time.Now()
	k.RevokedAt = &now

	mock.ExpectQuery(`SELECT .* FROM merchant_api_keys\s+WHERE key_hash = \$1`).
		WithArgs(k.KeyHash).
		WillReturnRows(sqlmock.NewRows(apiKeyCols).AddRow(apiKeyRow(k)...))

	got, err := repo.GetByHash(context.Background(), k.KeyHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.IsActive {
		t.Errorf("expected inactive key to still be returned (not filtered) for service-layer error mapping")
	}
	if got.RevokedAt == nil {
		t.Errorf("expected revoked_at to be populated")
	}
}

func TestMerchantAPIKeyRepository_Create(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantAPIKeyRepository(db)
	k := &merchant.APIKey{
		MerchantID: uuid.New(),
		Name:       "sandbox",
		KeyPrefix:  "pk_test_",
		KeyHash:    "abc123",
		IsActive:   true,
	}

	mock.ExpectExec(`INSERT INTO merchant_api_keys`).
		WithArgs(
			sqlmock.AnyArg(), k.MerchantID, k.Name, k.KeyPrefix, k.KeyHash, k.IsActive,
			k.LastUsedAt, k.RevokedAt, sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	if err := repo.Create(context.Background(), k); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if k.ID == uuid.Nil {
		t.Errorf("expected repository to generate an ID")
	}
	if k.CreatedAt.IsZero() || k.UpdatedAt.IsZero() {
		t.Errorf("expected timestamps to be set")
	}
}

func TestMerchantAPIKeyRepository_Delete_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantAPIKeyRepository(db)
	id, merchantID := uuid.New(), uuid.New()

	mock.ExpectExec(`DELETE FROM merchant_api_keys`).
		WithArgs(id, merchantID).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.Delete(context.Background(), id, merchantID)
	if !errors.Is(err, merchant.ErrAPIKeyNotFound) {
		t.Fatalf("expected ErrAPIKeyNotFound when no rows affected, got %v", err)
	}
}

// TestMerchantAPIKeyRepository_Delete_ScopedToMerchant verifies the delete
// query is scoped by merchant_id, not just key id — without this, one
// merchant could delete another merchant's API key by guessing its UUID.
func TestMerchantAPIKeyRepository_Delete_ScopedToMerchant(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantAPIKeyRepository(db)
	id, merchantID := uuid.New(), uuid.New()

	mock.ExpectExec(`DELETE FROM merchant_api_keys\s+WHERE id = \$1 AND merchant_id = \$2`).
		WithArgs(id, merchantID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.Delete(context.Background(), id, merchantID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
