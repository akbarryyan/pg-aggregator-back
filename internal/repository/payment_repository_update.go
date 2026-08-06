package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
)

func (r *PaymentRepository) UpdateStatus(ctx context.Context, id uuid.UUID, newStatus string, paidAt *time.Time) error {
	p, err := r.GetByID(ctx, id)
	if err != nil {
		return err
	}

	if !payment.CanTransitionTo(p.Status, newStatus) {
		return payment.ErrInvalidStatusTransition
	}

	query := `
		UPDATE payments
		SET status = $1, paid_at = $2, updated_at = $3
		WHERE id = $4
	`

	_, err = r.db.ExecContext(ctx, query, newStatus, paidAt, time.Now(), id)
	if err != nil {
		return fmt.Errorf("failed to update payment status: %w", err)
	}

	return nil
}

func (r *PaymentRepository) Update(ctx context.Context, p *payment.Payment) error {
	p.UpdatedAt = time.Now()

	query := `
		UPDATE payments
		SET 
			status = $1,
			provider_reference = $2,
			qris_data = $3,
			paid_at = $4,
			updated_at = $5
		WHERE id = $6
	`

	_, err := r.db.ExecContext(ctx, query,
		p.Status, p.ProviderReference, p.QRISData, p.PaidAt, p.UpdatedAt, p.ID,
	)

	if err != nil {
		return fmt.Errorf("failed to update payment: %w", err)
	}

	return nil
}

func (r *PaymentRepository) UpdateProviderReference(ctx context.Context, id uuid.UUID, providerReference string) error {
	query := `
		UPDATE payments
		SET provider_reference = $1, updated_at = $2
		WHERE id = $3
	`

	_, err := r.db.ExecContext(ctx, query, providerReference, time.Now(), id)
	if err != nil {
		return fmt.Errorf("failed to update provider reference: %w", err)
	}

	return nil
}

func (r *PaymentRepository) UpdateQRISData(ctx context.Context, id uuid.UUID, qrisData string) error {
	query := `
		UPDATE payments
		SET qris_data = $1, updated_at = $2
		WHERE id = $3
	`

	_, err := r.db.ExecContext(ctx, query, qrisData, time.Now(), id)
	if err != nil {
		return fmt.Errorf("failed to update QRIS data: %w", err)
	}

	return nil
}

func (r *PaymentRepository) CountByStatus(ctx context.Context, merchantID uuid.UUID, status string) (int64, error) {
	var count int64
	query := `SELECT COUNT(*) FROM payments WHERE merchant_id = $1 AND status = $2`
	err := r.db.QueryRowContext(ctx, query, merchantID, status).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count payments: %w", err)
	}
	return count, nil
}

func (r *PaymentRepository) GetTotalAmount(ctx context.Context, merchantID uuid.UUID, status string) (int64, error) {
	var total int64
	query := `SELECT COALESCE(SUM(amount), 0) FROM payments WHERE merchant_id = $1 AND status = $2`
	err := r.db.QueryRowContext(ctx, query, merchantID, status).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("failed to get total amount: %w", err)
	}
	return total, nil
}

func (r *PaymentRepository) CountAll(ctx context.Context) (int64, error) {
	var count int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM payments`).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count all payments: %w", err)
	}
	return count, nil
}

func (r *PaymentRepository) CountAllByStatus(ctx context.Context, status string) (int64, error) {
	var count int64
	query := `SELECT COUNT(*) FROM payments WHERE status = $1`
	if err := r.db.QueryRowContext(ctx, query, status).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count payments by status: %w", err)
	}
	return count, nil
}

func (r *PaymentRepository) SumPaidAmount(ctx context.Context) (int64, error) {
	var total int64
	query := `SELECT COALESCE(SUM(amount), 0) FROM payments WHERE status = $1`
	if err := r.db.QueryRowContext(ctx, query, payment.StatusPaid).Scan(&total); err != nil {
		return 0, fmt.Errorf("failed to sum paid payments: %w", err)
	}
	return total, nil
}

// StatusBreakdown is a one-query summary of payment counts per status (plus
// total paid amount), optionally scoped by merchant/environment. Exists so
// callers building a dashboard summary don't have to issue one List+Count
// round-trip per status (see AdminPaymentService.StatusBreakdown).
type StatusBreakdown struct {
	Total      int64
	Paid       int64
	Pending    int64
	Failed     int64
	Expired    int64
	Cancelled  int64
	PaidAmount int64
}

// StatusBreakdownFiltered returns one row of counts per status (+ paid
// amount) for payments optionally scoped by merchant and/or environment.
func (r *PaymentRepository) StatusBreakdownFiltered(
	ctx context.Context,
	merchantID *uuid.UUID,
	environment string,
) (*StatusBreakdown, error) {
	query := `
		SELECT
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE status = 'paid') AS paid,
			COUNT(*) FILTER (WHERE status = 'pending') AS pending,
			COUNT(*) FILTER (WHERE status = 'failed') AS failed,
			COUNT(*) FILTER (WHERE status = 'expired') AS expired,
			COUNT(*) FILTER (WHERE status = 'cancelled') AS cancelled,
			COALESCE(SUM(amount) FILTER (WHERE status = 'paid'), 0) AS paid_amount
		FROM payments
		WHERE 1=1
	`
	args := make([]interface{}, 0, 2)
	argN := 1
	if merchantID != nil {
		query += fmt.Sprintf(" AND merchant_id = $%d", argN)
		args = append(args, *merchantID)
		argN++
	}
	if environment != "" {
		query += fmt.Sprintf(" AND environment = $%d", argN)
		args = append(args, payment.NormalizeEnvironment(environment))
	}

	var s StatusBreakdown
	err := r.db.QueryRowContext(ctx, query, args...).Scan(
		&s.Total, &s.Paid, &s.Pending, &s.Failed, &s.Expired, &s.Cancelled, &s.PaidAmount,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get status breakdown: %w", err)
	}
	return &s, nil
}

// DailyPaymentStat is one day of payment aggregates for dashboard charts.
type DailyPaymentStat struct {
	Day        time.Time
	Total      int64
	Paid       int64
	Pending    int64
	Failed     int64
	Expired    int64
	Cancelled  int64
	PaidAmount int64
}

func (r *PaymentRepository) DailyStats(ctx context.Context, days int) ([]DailyPaymentStat, error) {
	return r.DailyStatsFiltered(ctx, days, nil, "")
}

// DailyStatsFiltered returns daily aggregates, optionally scoped by merchant and environment.
func (r *PaymentRepository) DailyStatsFiltered(
	ctx context.Context,
	days int,
	merchantID *uuid.UUID,
	environment string,
) ([]DailyPaymentStat, error) {
	if days <= 0 {
		days = 14
	}
	if days > 90 {
		days = 90
	}

	query := `
		SELECT
			DATE(created_at) AS day,
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE status = 'paid') AS paid,
			COUNT(*) FILTER (WHERE status = 'pending') AS pending,
			COUNT(*) FILTER (WHERE status = 'failed') AS failed,
			COUNT(*) FILTER (WHERE status = 'expired') AS expired,
			COUNT(*) FILTER (WHERE status = 'cancelled') AS cancelled,
			COALESCE(SUM(amount) FILTER (WHERE status = 'paid'), 0) AS paid_amount
		FROM payments
		WHERE created_at >= (CURRENT_DATE - ($1::int - 1) * INTERVAL '1 day')
	`
	args := []interface{}{days}
	argN := 2
	if merchantID != nil {
		query += fmt.Sprintf(" AND merchant_id = $%d", argN)
		args = append(args, *merchantID)
		argN++
	}
	if environment != "" {
		query += fmt.Sprintf(" AND environment = $%d", argN)
		args = append(args, payment.NormalizeEnvironment(environment))
	}
	query += `
		GROUP BY DATE(created_at)
		ORDER BY day ASC
	`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query daily payment stats: %w", err)
	}
	defer rows.Close()

	var stats []DailyPaymentStat
	for rows.Next() {
		var s DailyPaymentStat
		if err := rows.Scan(
			&s.Day,
			&s.Total,
			&s.Paid,
			&s.Pending,
			&s.Failed,
			&s.Expired,
			&s.Cancelled,
			&s.PaidAmount,
		); err != nil {
			return nil, fmt.Errorf("failed to scan daily payment stats: %w", err)
		}
		stats = append(stats, s)
	}
	return stats, nil
}

// AdminPaymentRow is a payment row with optional merchant display name for admin lists.
type AdminPaymentRow struct {
	Payment      *payment.Payment
	MerchantName string
}

func (r *PaymentRepository) ListAdmin(
	ctx context.Context,
	status string,
	search string,
	merchantID *uuid.UUID,
	dateFrom, dateTo *time.Time,
	environment string,
	limit, offset int,
) ([]AdminPaymentRow, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	query := `
		SELECT
			p.id, p.reference, p.merchant_id, p.amount, p.currency, p.payment_method,
			p.provider_name, p.provider_reference, p.status, p.description,
			p.customer_name, p.customer_email, p.qris_data, p.callback_url,
			p.environment, p.expires_at, p.paid_at, p.created_at, p.updated_at,
			COALESCE(m.business_name, m.name, '') AS merchant_name
		FROM payments p
		LEFT JOIN merchants m ON m.id = p.merchant_id
		WHERE 1=1
	`
	args := make([]interface{}, 0, 10)
	argN := 1

	if status != "" {
		query += fmt.Sprintf(" AND p.status = $%d", argN)
		args = append(args, status)
		argN++
	}
	if merchantID != nil {
		query += fmt.Sprintf(" AND p.merchant_id = $%d", argN)
		args = append(args, *merchantID)
		argN++
	}
	if search != "" {
		query += fmt.Sprintf(` AND (
			p.reference ILIKE $%d OR
			COALESCE(p.provider_reference, '') ILIKE $%d OR
			COALESCE(p.customer_name, '') ILIKE $%d OR
			COALESCE(p.customer_email, '') ILIKE $%d OR
			COALESCE(m.business_name, '') ILIKE $%d
		)`, argN, argN, argN, argN, argN)
		args = append(args, "%"+search+"%")
		argN++
	}
	if dateFrom != nil {
		query += fmt.Sprintf(" AND p.created_at >= $%d", argN)
		args = append(args, *dateFrom)
		argN++
	}
	if dateTo != nil {
		query += fmt.Sprintf(" AND p.created_at < $%d", argN)
		args = append(args, *dateTo)
		argN++
	}
	if environment != "" {
		query += fmt.Sprintf(" AND p.environment = $%d", argN)
		args = append(args, payment.NormalizeEnvironment(environment))
		argN++
	}

	query += fmt.Sprintf(" ORDER BY p.created_at DESC LIMIT $%d OFFSET $%d", argN, argN+1)
	args = append(args, limit, offset)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list admin payments: %w", err)
	}
	defer rows.Close()

	var result []AdminPaymentRow
	for rows.Next() {
		p := &payment.Payment{}
		var merchantName string
		if err := rows.Scan(
			&p.ID, &p.Reference, &p.MerchantID, &p.Amount, &p.Currency, &p.PaymentMethod,
			&p.ProviderName, &p.ProviderReference, &p.Status, &p.Description,
			&p.CustomerName, &p.CustomerEmail, &p.QRISData, &p.CallbackURL,
			&p.Environment, &p.ExpiresAt, &p.PaidAt, &p.CreatedAt, &p.UpdatedAt,
			&merchantName,
		); err != nil {
			return nil, fmt.Errorf("failed to scan admin payment: %w", err)
		}
		result = append(result, AdminPaymentRow{Payment: p, MerchantName: merchantName})
	}

	return result, nil
}

func (r *PaymentRepository) CountAdmin(
	ctx context.Context,
	status string,
	search string,
	merchantID *uuid.UUID,
	dateFrom, dateTo *time.Time,
	environment string,
) (int64, error) {
	query := `
		SELECT COUNT(*)
		FROM payments p
		LEFT JOIN merchants m ON m.id = p.merchant_id
		WHERE 1=1
	`
	args := make([]interface{}, 0, 8)
	argN := 1

	if status != "" {
		query += fmt.Sprintf(" AND p.status = $%d", argN)
		args = append(args, status)
		argN++
	}
	if merchantID != nil {
		query += fmt.Sprintf(" AND p.merchant_id = $%d", argN)
		args = append(args, *merchantID)
		argN++
	}
	if search != "" {
		query += fmt.Sprintf(` AND (
			p.reference ILIKE $%d OR
			COALESCE(p.provider_reference, '') ILIKE $%d OR
			COALESCE(p.customer_name, '') ILIKE $%d OR
			COALESCE(p.customer_email, '') ILIKE $%d OR
			COALESCE(m.business_name, '') ILIKE $%d
		)`, argN, argN, argN, argN, argN)
		args = append(args, "%"+search+"%")
		argN++
	}
	if dateFrom != nil {
		query += fmt.Sprintf(" AND p.created_at >= $%d", argN)
		args = append(args, *dateFrom)
		argN++
	}
	if dateTo != nil {
		query += fmt.Sprintf(" AND p.created_at < $%d", argN)
		args = append(args, *dateTo)
		argN++
	}
	if environment != "" {
		query += fmt.Sprintf(" AND p.environment = $%d", argN)
		args = append(args, payment.NormalizeEnvironment(environment))
	}

	var count int64
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count admin payments: %w", err)
	}
	return count, nil
}
