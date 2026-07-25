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

// ListDueForRetry returns failed deliveries whose next_retry_at has passed.
// Deliveries with next_retry_at = NULL (retry cap already reached, see
// executeCallbackDelivery) are excluded automatically since NULL <= $1 is
// never true in SQL.
func (r *MerchantCallbackRepository) ListDueForRetry(ctx context.Context, before time.Time, limit int) ([]merchant.CallbackDelivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, payment_id, merchant_id, event_type, target_url, request_payload,
		       attempt_number, status, http_status, response_body, error_message,
		       delivered_at, next_retry_at, created_at, updated_at
		FROM merchant_callback_deliveries
		WHERE status = $1 AND next_retry_at IS NOT NULL AND next_retry_at <= $2
		ORDER BY next_retry_at ASC
		LIMIT $3
	`, merchant.CallbackStatusFailed, before, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list due callback retries: %w", err)
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
		d           merchant.CallbackDelivery
		payloadRaw  []byte
		httpStatus  sql.NullInt64
		responseBody sql.NullString
		errorMsg    sql.NullString
		deliveredAt sql.NullTime
		nextRetry   sql.NullTime
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
