package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/google/uuid"
)

type WebhookEventRepository struct {
	db *sql.DB
}

func NewWebhookEventRepository(db *sql.DB) *WebhookEventRepository {
	return &WebhookEventRepository{db: db}
}

func (r *WebhookEventRepository) Create(ctx context.Context, e *provider.WebhookEvent) error {
	rawPayloadJSON, err := json.Marshal(e.RawPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal raw payload: %w", err)
	}

	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}

	query := `
		INSERT INTO webhook_events (
			id, payment_id, provider_name, provider_reference, event_type,
			status, raw_payload, processed_at, is_processed, processing_error, created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, CURRENT_TIMESTAMP
		)
		RETURNING created_at
	`

	err = r.db.QueryRowContext(ctx, query,
		e.ID, e.PaymentID, e.ProviderName, e.ProviderReference, e.EventType,
		e.Status, rawPayloadJSON, e.ProcessedAt, e.IsProcessed, e.ProcessingError,
	).Scan(&e.CreatedAt)

	if err != nil {
		return fmt.Errorf("failed to create webhook event: %w", err)
	}

	return nil
}

// Finalize records the outcome of processing a previously created webhook event.
// It is called on every exit path of webhook processing, including rejected,
// duplicate, or unmatched webhooks, so raw payloads stay traceable for audit
// even when the payment cannot be resolved.
func (r *WebhookEventRepository) Finalize(
	ctx context.Context,
	id uuid.UUID,
	paymentID *uuid.UUID,
	providerReference, eventType, status string,
	isProcessed bool,
	processingError *string,
) error {
	query := `
		UPDATE webhook_events
		SET payment_id = $2,
			provider_reference = $3,
			event_type = $4,
			status = $5,
			is_processed = $6,
			processing_error = $7,
			processed_at = CASE WHEN $6 THEN CURRENT_TIMESTAMP ELSE processed_at END
		WHERE id = $1
	`

	_, err := r.db.ExecContext(ctx, query, id, paymentID, providerReference, eventType, status, isProcessed, processingError)
	if err != nil {
		return fmt.Errorf("failed to finalize webhook event: %w", err)
	}

	return nil
}

// ListAdmin returns recent webhook events for admin debugging.
// Raw payload is intentionally omitted from list queries for safety/size.
func (r *WebhookEventRepository) ListAdmin(ctx context.Context, limit, offset int) ([]*provider.WebhookEvent, error) {
	return r.ListAdminFiltered(ctx, "", "", nil, limit, offset)
}

func (r *WebhookEventRepository) ListAdminFiltered(
	ctx context.Context,
	status string,
	providerName string,
	isProcessed *bool,
	limit, offset int,
) ([]*provider.WebhookEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	query := `
		SELECT
			id, payment_id, provider_name, provider_reference, event_type,
			status, processed_at, is_processed, processing_error, created_at
		FROM webhook_events
		WHERE 1=1
	`
	args := make([]interface{}, 0, 5)
	argN := 1

	if status != "" {
		query += fmt.Sprintf(" AND status = $%d", argN)
		args = append(args, status)
		argN++
	}
	if providerName != "" {
		query += fmt.Sprintf(" AND LOWER(provider_name) = LOWER($%d)", argN)
		args = append(args, providerName)
		argN++
	}
	if isProcessed != nil {
		query += fmt.Sprintf(" AND is_processed = $%d", argN)
		args = append(args, *isProcessed)
		argN++
	}

	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", argN, argN+1)
	args = append(args, limit, offset)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list webhook events: %w", err)
	}
	defer rows.Close()

	var events []*provider.WebhookEvent
	for rows.Next() {
		e := &provider.WebhookEvent{}
		if err := rows.Scan(
			&e.ID,
			&e.PaymentID,
			&e.ProviderName,
			&e.ProviderReference,
			&e.EventType,
			&e.Status,
			&e.ProcessedAt,
			&e.IsProcessed,
			&e.ProcessingError,
			&e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan webhook event: %w", err)
		}
		events = append(events, e)
	}

	return events, nil
}

func (r *WebhookEventRepository) Count(ctx context.Context) (int64, error) {
	return r.CountFiltered(ctx, "", "", nil)
}

func (r *WebhookEventRepository) CountFiltered(
	ctx context.Context,
	status string,
	providerName string,
	isProcessed *bool,
) (int64, error) {
	query := `SELECT COUNT(*) FROM webhook_events WHERE 1=1`
	args := make([]interface{}, 0, 3)
	argN := 1

	if status != "" {
		query += fmt.Sprintf(" AND status = $%d", argN)
		args = append(args, status)
		argN++
	}
	if providerName != "" {
		query += fmt.Sprintf(" AND LOWER(provider_name) = LOWER($%d)", argN)
		args = append(args, providerName)
		argN++
	}
	if isProcessed != nil {
		query += fmt.Sprintf(" AND is_processed = $%d", argN)
		args = append(args, *isProcessed)
	}

	var count int64
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count webhook events: %w", err)
	}
	return count, nil
}

func (r *WebhookEventRepository) GetByID(ctx context.Context, id uuid.UUID) (*provider.WebhookEvent, error) {
	e := &provider.WebhookEvent{}
	var rawPayloadJSON []byte

	query := `
		SELECT
			id, payment_id, provider_name, provider_reference, event_type,
			status, raw_payload, processed_at, is_processed, processing_error, created_at
		FROM webhook_events
		WHERE id = $1
	`

	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&e.ID,
		&e.PaymentID,
		&e.ProviderName,
		&e.ProviderReference,
		&e.EventType,
		&e.Status,
		&rawPayloadJSON,
		&e.ProcessedAt,
		&e.IsProcessed,
		&e.ProcessingError,
		&e.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("webhook event not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get webhook event: %w", err)
	}

	if len(rawPayloadJSON) > 0 {
		_ = json.Unmarshal(rawPayloadJSON, &e.RawPayload)
	}
	if e.RawPayload == nil {
		e.RawPayload = map[string]interface{}{}
	}

	return e, nil
}

func (r *WebhookEventRepository) ListByPaymentID(ctx context.Context, paymentID uuid.UUID, limit int) ([]*provider.WebhookEvent, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT
			id, payment_id, provider_name, provider_reference, event_type,
			status, processed_at, is_processed, processing_error, created_at
		FROM webhook_events
		WHERE payment_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`

	rows, err := r.db.QueryContext(ctx, query, paymentID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list webhook events by payment: %w", err)
	}
	defer rows.Close()

	var events []*provider.WebhookEvent
	for rows.Next() {
		e := &provider.WebhookEvent{}
		if err := rows.Scan(
			&e.ID,
			&e.PaymentID,
			&e.ProviderName,
			&e.ProviderReference,
			&e.EventType,
			&e.Status,
			&e.ProcessedAt,
			&e.IsProcessed,
			&e.ProcessingError,
			&e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan webhook event: %w", err)
		}
		events = append(events, e)
	}

	return events, nil
}
