package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/google/uuid"
)

type MerchantAPIKeyRepository struct {
	db *sql.DB
}

func NewMerchantAPIKeyRepository(db *sql.DB) *MerchantAPIKeyRepository {
	return &MerchantAPIKeyRepository{db: db}
}

func (r *MerchantAPIKeyRepository) Create(ctx context.Context, key *merchant.APIKey) error {
	if key.ID == uuid.Nil {
		key.ID = uuid.New()
	}
	now := time.Now().UTC()
	if key.CreatedAt.IsZero() {
		key.CreatedAt = now
	}
	key.UpdatedAt = now

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO merchant_api_keys (
			id, merchant_id, name, key_prefix, key_hash, is_active,
			last_used_at, revoked_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`,
		key.ID, key.MerchantID, key.Name, key.KeyPrefix, key.KeyHash, key.IsActive,
		key.LastUsedAt, key.RevokedAt, key.CreatedAt, key.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create api key: %w", err)
	}
	return nil
}

func (r *MerchantAPIKeyRepository) ListByMerchant(ctx context.Context, merchantID uuid.UUID) ([]*merchant.APIKey, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, merchant_id, name, key_prefix, key_hash, is_active,
		       last_used_at, revoked_at, created_at, updated_at
		FROM merchant_api_keys
		WHERE merchant_id = $1
		ORDER BY created_at DESC
	`, merchantID)
	if err != nil {
		return nil, fmt.Errorf("failed to list api keys: %w", err)
	}
	defer rows.Close()

	items := make([]*merchant.APIKey, 0)
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, k)
	}
	return items, rows.Err()
}

func (r *MerchantAPIKeyRepository) GetByID(ctx context.Context, id uuid.UUID) (*merchant.APIKey, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, merchant_id, name, key_prefix, key_hash, is_active,
		       last_used_at, revoked_at, created_at, updated_at
		FROM merchant_api_keys
		WHERE id = $1
	`, id)
	k, err := scanAPIKey(row)
	if err == sql.ErrNoRows {
		return nil, merchant.ErrAPIKeyNotFound
	}
	if err != nil {
		return nil, err
	}
	return k, nil
}

func (r *MerchantAPIKeyRepository) GetByHash(ctx context.Context, keyHash string) (*merchant.APIKey, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, merchant_id, name, key_prefix, key_hash, is_active,
		       last_used_at, revoked_at, created_at, updated_at
		FROM merchant_api_keys
		WHERE key_hash = $1
	`, keyHash)
	k, err := scanAPIKey(row)
	if err == sql.ErrNoRows {
		return nil, merchant.ErrAPIKeyNotFound
	}
	if err != nil {
		return nil, err
	}
	return k, nil
}

func (r *MerchantAPIKeyRepository) Delete(ctx context.Context, id, merchantID uuid.UUID) error {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM merchant_api_keys
		WHERE id = $1 AND merchant_id = $2
	`, id, merchantID)
	if err != nil {
		return fmt.Errorf("failed to delete api key: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return merchant.ErrAPIKeyNotFound
	}
	return nil
}

// DeleteByMerchantAndName removes any existing key for merchant+environment (sandbox/production).
func (r *MerchantAPIKeyRepository) DeleteByMerchantAndName(ctx context.Context, merchantID uuid.UUID, name string) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM merchant_api_keys
		WHERE merchant_id = $1 AND name = $2
	`, merchantID, name)
	if err != nil {
		return 0, fmt.Errorf("failed to delete api keys by env: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (r *MerchantAPIKeyRepository) GetByMerchantAndName(ctx context.Context, merchantID uuid.UUID, name string) (*merchant.APIKey, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, merchant_id, name, key_prefix, key_hash, is_active,
		       last_used_at, revoked_at, created_at, updated_at
		FROM merchant_api_keys
		WHERE merchant_id = $1 AND name = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, merchantID, name)
	k, err := scanAPIKey(row)
	if err == sql.ErrNoRows {
		return nil, merchant.ErrAPIKeyNotFound
	}
	if err != nil {
		return nil, err
	}
	return k, nil
}

func (r *MerchantAPIKeyRepository) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE merchant_api_keys
		SET last_used_at = $2, updated_at = $2
		WHERE id = $1
	`, id, now)
	return err
}

type apiKeyScanner interface {
	Scan(dest ...interface{}) error
}

func scanAPIKey(row apiKeyScanner) (*merchant.APIKey, error) {
	var (
		k         merchant.APIKey
		lastUsed  sql.NullTime
		revokedAt sql.NullTime
	)
	err := row.Scan(
		&k.ID, &k.MerchantID, &k.Name, &k.KeyPrefix, &k.KeyHash, &k.IsActive,
		&lastUsed, &revokedAt, &k.CreatedAt, &k.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if lastUsed.Valid {
		t := lastUsed.Time
		k.LastUsedAt = &t
	}
	if revokedAt.Valid {
		t := revokedAt.Time
		k.RevokedAt = &t
	}
	return &k, nil
}
