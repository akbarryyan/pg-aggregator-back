package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/idempotency"
	"github.com/google/uuid"
)

type IdempotencyRepository struct {
	db *sql.DB
}

func NewIdempotencyRepository(db *sql.DB) *IdempotencyRepository {
	return &IdempotencyRepository{db: db}
}

// Claim tries to take (merchant, environment, key). It returns (rec, true) when
// this caller now owns the key, (existing, false) when someone else does, and
// (nil, false) when the conflicting row vanished before it could be read (the
// caller retries). A row is taken over when it is older than idempotency.TTL, or
// when it never received a payment and is older than idempotency.AbandonedAfter.
func (r *IdempotencyRepository) Claim(
	ctx context.Context, merchantID uuid.UUID, environment, key, requestHash string, now time.Time,
) (*idempotency.Record, bool, error) {
	id := uuid.New()
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO idempotency_keys (id, merchant_id, environment, idempotency_key, request_hash, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (merchant_id, environment, idempotency_key) DO UPDATE
		SET id = EXCLUDED.id, request_hash = EXCLUDED.request_hash,
		    payment_id = NULL, created_at = EXCLUDED.created_at
		WHERE idempotency_keys.created_at < $7
		   OR (idempotency_keys.payment_id IS NULL AND idempotency_keys.created_at < $8)
		RETURNING id
	`, id, merchantID, environment, key, requestHash, now,
		now.Add(-idempotency.TTL), now.Add(-idempotency.AbandonedAfter)).Scan(&id)
	if err == nil {
		return &idempotency.Record{ID: id, MerchantID: merchantID, Environment: environment,
			Key: key, RequestHash: requestHash, CreatedAt: now}, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("failed to claim idempotency key: %w", err)
	}

	rec := &idempotency.Record{}
	var paymentID uuid.NullUUID
	err = r.db.QueryRowContext(ctx, `
		SELECT id, merchant_id, environment, idempotency_key, request_hash, payment_id, created_at
		FROM idempotency_keys
		WHERE merchant_id = $1 AND environment = $2 AND idempotency_key = $3
	`, merchantID, environment, key).Scan(
		&rec.ID, &rec.MerchantID, &rec.Environment, &rec.Key, &rec.RequestHash, &paymentID, &rec.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to read idempotency key: %w", err)
	}
	if paymentID.Valid {
		rec.PaymentID = &paymentID.UUID
	}
	return rec, false, nil
}

// Complete links the claim to the payment it produced. It returns
// idempotency.ErrClaimLost when no row has that id any more (the claim was
// taken over after the abandon window), so the caller can say so loudly.
func (r *IdempotencyRepository) Complete(ctx context.Context, id, paymentID uuid.UUID) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE idempotency_keys SET payment_id = $2 WHERE id = $1`, id, paymentID)
	if err != nil {
		return fmt.Errorf("failed to complete idempotency key: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to complete idempotency key: %w", err)
	}
	if n == 0 {
		return idempotency.ErrClaimLost
	}
	return nil
}

// Release drops a claim whose payment was never created, so a retry can run.
func (r *IdempotencyRepository) Release(ctx context.Context, id uuid.UUID) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE id = $1 AND payment_id IS NULL`, id); err != nil {
		return fmt.Errorf("failed to release idempotency key: %w", err)
	}
	return nil
}

func (r *IdempotencyRepository) PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("failed to purge idempotency keys: %w", err)
	}
	return res.RowsAffected()
}
