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
