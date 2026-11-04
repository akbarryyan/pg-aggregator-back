package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

type MerchantCallbackRepository struct {
	db *sql.DB
}

func NewMerchantCallbackRepository(db *sql.DB) *MerchantCallbackRepository {
	return &MerchantCallbackRepository{db: db}
}

type CallbackDeliveryRow struct {
	Delivery     merchant.CallbackDelivery
	MerchantName string
	Reference    string
}

func (r *MerchantCallbackRepository) Create(ctx context.Context, d *merchant.CallbackDelivery) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	now := time.Now().UTC()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	d.UpdatedAt = now
	if d.RequestPayload == nil {
		d.RequestPayload = map[string]interface{}{}
	}
	payload, err := json.Marshal(d.RequestPayload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	_, err = r.db.ExecContext(ctx, `
		INSERT INTO merchant_callback_deliveries (
			id, payment_id, merchant_id, event_type, target_url, request_payload,
			attempt_number, status, http_status, response_body, error_message,
			delivered_at, next_retry_at, created_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,
			$7,$8,$9,$10,$11,
			$12,$13,$14,$15
		)
	`,
		d.ID, d.PaymentID, d.MerchantID, d.EventType, d.TargetURL, payload,
		d.AttemptNumber, d.Status, d.HTTPStatus, d.ResponseBody, d.ErrorMessage,
		d.DeliveredAt, d.NextRetryAt, d.CreatedAt, d.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create callback delivery: %w", err)
	}
	return nil
}

func (r *MerchantCallbackRepository) UpdateResult(ctx context.Context, d *merchant.CallbackDelivery) error {
	d.UpdatedAt = time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE merchant_callback_deliveries
		SET status = $2,
		    http_status = $3,
		    response_body = $4,
		    error_message = $5,
		    delivered_at = $6,
		    next_retry_at = $7,
		    attempt_number = $8,
		    updated_at = $9
		WHERE id = $1
	`,
		d.ID, d.Status, d.HTTPStatus, d.ResponseBody, d.ErrorMessage,
		d.DeliveredAt, d.NextRetryAt, d.AttemptNumber, d.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to update callback delivery: %w", err)
	}
	return nil
}

func (r *MerchantCallbackRepository) GetByID(ctx context.Context, id uuid.UUID) (*merchant.CallbackDelivery, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, payment_id, merchant_id, event_type, target_url, request_payload,
		       attempt_number, status, http_status, response_body, error_message,
		       delivered_at, next_retry_at, created_at, updated_at
		FROM merchant_callback_deliveries
		WHERE id = $1
	`, id)
	d, err := scanCallbackDelivery(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("callback delivery not found")
		}
		return nil, err
	}
	return d, nil
}

func (r *MerchantCallbackRepository) ListFiltered(
	ctx context.Context,
	status string,
	merchantID *uuid.UUID,
	limit, offset int,
) ([]CallbackDeliveryRow, error) {
	where, args := callbackFilters(status, merchantID)
	query := `
		SELECT d.id, d.payment_id, d.merchant_id, d.event_type, d.target_url, d.request_payload,
		       d.attempt_number, d.status, d.http_status, d.response_body, d.error_message,
		       d.delivered_at, d.next_retry_at, d.created_at, d.updated_at,
		       COALESCE(m.business_name, m.name, '') AS merchant_name,
		       COALESCE(p.reference, '') AS payment_reference
		FROM merchant_callback_deliveries d
		LEFT JOIN merchants m ON m.id = d.merchant_id
		LEFT JOIN payments p ON p.id = d.payment_id
	` + where + `
		ORDER BY d.created_at DESC
		LIMIT $` + fmt.Sprintf("%d", len(args)+1) + ` OFFSET $` + fmt.Sprintf("%d", len(args)+2)

	args = append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list callback deliveries: %w", err)
	}
	defer rows.Close()

	items := make([]CallbackDeliveryRow, 0)
	for rows.Next() {
		item, err := scanCallbackDeliveryRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (r *MerchantCallbackRepository) CountFiltered(
	ctx context.Context,
	status string,
	merchantID *uuid.UUID,
) (int64, error) {
	where, args := callbackFilters(status, merchantID)
	query := `SELECT COUNT(*) FROM merchant_callback_deliveries d ` + where
	var total int64
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("failed to count callback deliveries: %w", err)
	}
	return total, nil
}

func (r *MerchantCallbackRepository) ListByPayment(ctx context.Context, paymentID uuid.UUID) ([]merchant.CallbackDelivery, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, payment_id, merchant_id, event_type, target_url, request_payload,
		       attempt_number, status, http_status, response_body, error_message,
		       delivered_at, next_retry_at, created_at, updated_at
		FROM merchant_callback_deliveries
		WHERE payment_id = $1
		ORDER BY created_at DESC
	`, paymentID)
	if err != nil {
		return nil, fmt.Errorf("failed to list callbacks by payment: %w", err)
	}
	defer rows.Close()

	items := make([]merchant.CallbackDelivery, 0)
	for rows.Next() {
		d, err := scanCallbackDelivery(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *d)
	}
	return items, rows.Err()
}

func callbackFilters(status string, merchantID *uuid.UUID) (string, []interface{}) {
	clauses := make([]string, 0, 2)
	args := make([]interface{}, 0, 2)
	n := 1
	if status != "" {
		clauses = append(clauses, fmt.Sprintf("d.status = $%d", n))
		args = append(args, status)
		n++
	}
	if merchantID != nil {
		clauses = append(clauses, fmt.Sprintf("d.merchant_id = $%d", n))
		args = append(args, *merchantID)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

type scannable interface {
	Scan(dest ...interface{}) error
}

func scanCallbackDelivery(row scannable) (*merchant.CallbackDelivery, error) {
	var (
		d            merchant.CallbackDelivery
		payloadRaw   []byte
		httpStatus   sql.NullInt64
		responseBody sql.NullString
		errorMsg     sql.NullString
		deliveredAt  sql.NullTime
		nextRetry    sql.NullTime
	)
	err := row.Scan(
		&d.ID, &d.PaymentID, &d.MerchantID, &d.EventType, &d.TargetURL, &payloadRaw,
		&d.AttemptNumber, &d.Status, &httpStatus, &responseBody, &errorMsg,
		&deliveredAt, &nextRetry, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if len(payloadRaw) > 0 {
		_ = json.Unmarshal(payloadRaw, &d.RequestPayload)
	}
	if d.RequestPayload == nil {
		d.RequestPayload = map[string]interface{}{}
	}
	if httpStatus.Valid {
		v := int(httpStatus.Int64)
		d.HTTPStatus = &v
	}
	if responseBody.Valid {
		v := responseBody.String
		d.ResponseBody = &v
	}
	if errorMsg.Valid {
		v := errorMsg.String
		d.ErrorMessage = &v
	}
	if deliveredAt.Valid {
		v := deliveredAt.Time
		d.DeliveredAt = &v
	}
	if nextRetry.Valid {
		v := nextRetry.Time
		d.NextRetryAt = &v
	}
	return &d, nil
}

func scanCallbackDeliveryRow(rows *sql.Rows) (*CallbackDeliveryRow, error) {
	var (
		d            merchant.CallbackDelivery
		payloadRaw   []byte
		httpStatus   sql.NullInt64
		responseBody sql.NullString
		errorMsg     sql.NullString
		deliveredAt  sql.NullTime
		nextRetry    sql.NullTime
		merchantName string
		reference    string
	)
	err := rows.Scan(
		&d.ID, &d.PaymentID, &d.MerchantID, &d.EventType, &d.TargetURL, &payloadRaw,
		&d.AttemptNumber, &d.Status, &httpStatus, &responseBody, &errorMsg,
		&deliveredAt, &nextRetry, &d.CreatedAt, &d.UpdatedAt,
		&merchantName, &reference,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan callback delivery row: %w", err)
	}
	if len(payloadRaw) > 0 {
		_ = json.Unmarshal(payloadRaw, &d.RequestPayload)
	}
	if d.RequestPayload == nil {
		d.RequestPayload = map[string]interface{}{}
	}
	if httpStatus.Valid {
		v := int(httpStatus.Int64)
		d.HTTPStatus = &v
	}
	if responseBody.Valid {
		v := responseBody.String
		d.ResponseBody = &v
	}
	if errorMsg.Valid {
		v := errorMsg.String
		d.ErrorMessage = &v
	}
	if deliveredAt.Valid {
		v := deliveredAt.Time
		d.DeliveredAt = &v
	}
	if nextRetry.Valid {
		v := nextRetry.Time
		d.NextRetryAt = &v
	}
	return &CallbackDeliveryRow{
		Delivery:     d,
		MerchantName: merchantName,
		Reference:    reference,
	}, nil
}

// ClaimDue atomically takes up to limit deliveries to send: pending ones,
// plus 'sending' ones whose worker evidently died (untouched for staleAfter).
// FOR UPDATE SKIP LOCKED means two workers never claim the same row.
func (r *MerchantCallbackRepository) ClaimDue(ctx context.Context, now time.Time, limit int, staleAfter time.Duration) ([]merchant.CallbackDelivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
		UPDATE merchant_callback_deliveries
		SET status = $1, updated_at = $2
		WHERE id IN (
			SELECT id FROM merchant_callback_deliveries
			WHERE status = $3 OR (status = $1 AND updated_at <= $4)
			ORDER BY created_at
			LIMIT $5
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, payment_id, merchant_id, event_type, target_url, request_payload,
		          attempt_number, status, http_status, response_body, error_message,
		          delivered_at, next_retry_at, created_at, updated_at
	`, merchant.CallbackStatusSending, now, merchant.CallbackStatusPending, now.Add(-staleAfter), limit)
	if err != nil {
		return nil, fmt.Errorf("failed to claim callback deliveries: %w", err)
	}
	defer rows.Close()

	items := make([]merchant.CallbackDelivery, 0)
	for rows.Next() {
		d, err := scanCallbackDelivery(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *d)
	}
	return items, rows.Err()
}

// RequeueDueFailed turns each failed delivery whose retry time has come into
// a fresh pending attempt (attempt_number + 1, history kept) and clears the
// old row's schedule so it is never picked up twice — one statement, so
// concurrent workers cannot both requeue the same failure.
func (r *MerchantCallbackRepository) RequeueDueFailed(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 50
	}
	res, err := r.db.ExecContext(ctx, `
		WITH due AS (
			SELECT id FROM merchant_callback_deliveries
			WHERE status = $1 AND next_retry_at IS NOT NULL AND next_retry_at <= $2
			ORDER BY next_retry_at
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		), cleared AS (
			UPDATE merchant_callback_deliveries d
			SET next_retry_at = NULL, updated_at = $2
			FROM due
			WHERE d.id = due.id
			RETURNING d.payment_id, d.merchant_id, d.event_type, d.target_url, d.request_payload, d.attempt_number
		)
		INSERT INTO merchant_callback_deliveries (
			id, payment_id, merchant_id, event_type, target_url, request_payload,
			attempt_number, status, created_at, updated_at
		)
		SELECT gen_random_uuid(), payment_id, merchant_id, event_type, target_url, request_payload,
		       attempt_number + 1, $4, $2, $2
		FROM cleared
	`, merchant.CallbackStatusFailed, now, limit, merchant.CallbackStatusPending)
	if err != nil {
		return 0, fmt.Errorf("failed to requeue failed callbacks: %w", err)
	}
	return res.RowsAffected()
}

// ClearRetrySchedule stops the worker from auto-requeueing a failed delivery
// (used before a manual retry sends its own new attempt). It only touches a
// failed row that still has a schedule, and only that column, so it can never
// overwrite a status or result written concurrently. Reports whether a row
// changed.
func (r *MerchantCallbackRepository) ClearRetrySchedule(ctx context.Context, id uuid.UUID, now time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE merchant_callback_deliveries
		SET next_retry_at = NULL, updated_at = $2
		WHERE id = $1 AND status = $3 AND next_retry_at IS NOT NULL
	`, id, now, merchant.CallbackStatusFailed)
	if err != nil {
		return false, fmt.Errorf("failed to clear callback retry schedule: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to clear callback retry schedule: %w", err)
	}
	return n > 0, nil
}

// ReleaseClaimed hands claimed-but-unsent deliveries back to 'pending' (a
// worker stopping early: batch budget spent, or shutdown) so they are sent on
// the next tick instead of after the stale window. Only rows still 'sending'
// change, so a row another worker or a manual retry has since finished is
// left alone. Reports how many rows were released.
func (r *MerchantCallbackRepository) ReleaseClaimed(ctx context.Context, ids []uuid.UUID, now time.Time) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	// As strings: pq.Array can't encode uuid.UUID (a [16]byte) elements.
	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = id.String()
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE merchant_callback_deliveries
		SET status = $3, updated_at = $2
		WHERE id = ANY($1::uuid[]) AND status = $4
	`, pq.Array(strs), now, merchant.CallbackStatusPending, merchant.CallbackStatusSending)
	if err != nil {
		return 0, fmt.Errorf("failed to release claimed callbacks: %w", err)
	}
	return res.RowsAffected()
}
