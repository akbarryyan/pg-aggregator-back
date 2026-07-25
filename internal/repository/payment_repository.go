package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/google/uuid"
)

type PaymentRepository struct {
	db *sql.DB
}

func NewPaymentRepository(db *sql.DB) *PaymentRepository {
	return &PaymentRepository{db: db}
}

const paymentSelectCols = `
			id, reference, merchant_id, amount, currency, payment_method,
			provider_name, provider_reference, status, description,
			customer_name, customer_email, qris_data, callback_url,
			environment, payment_link_id, expires_at, paid_at, created_at, updated_at
`

func scanPayment(scanner interface {
	Scan(dest ...interface{}) error
}, p *payment.Payment) error {
	return scanner.Scan(
		&p.ID, &p.Reference, &p.MerchantID, &p.Amount, &p.Currency, &p.PaymentMethod,
		&p.ProviderName, &p.ProviderReference, &p.Status, &p.Description,
		&p.CustomerName, &p.CustomerEmail, &p.QRISData, &p.CallbackURL,
		&p.Environment, &p.PaymentLinkID, &p.ExpiresAt, &p.PaidAt, &p.CreatedAt, &p.UpdatedAt,
	)
}

func (r *PaymentRepository) Create(ctx context.Context, p *payment.Payment) error {
	if p.Environment == "" {
		p.Environment = payment.EnvironmentProduction
	}
	query := `
		INSERT INTO payments (
			id, reference, merchant_id, amount, currency, payment_method,
			provider_name, provider_reference, status, description,
			customer_name, customer_email, qris_data, callback_url,
			environment, payment_link_id, expires_at, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19
		)
		RETURNING id, created_at, updated_at
	`

	err := r.db.QueryRowContext(ctx, query,
		p.ID, p.Reference, p.MerchantID, p.Amount, p.Currency, p.PaymentMethod,
		p.ProviderName, p.ProviderReference, p.Status, p.Description,
		p.CustomerName, p.CustomerEmail, p.QRISData, p.CallbackURL,
		p.Environment, p.PaymentLinkID, p.ExpiresAt, p.CreatedAt, p.UpdatedAt,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)

	if err != nil {
		return fmt.Errorf("failed to create payment: %w", err)
	}

	return nil
}

func (r *PaymentRepository) GetByID(ctx context.Context, id uuid.UUID) (*payment.Payment, error) {
	p := &payment.Payment{}
	query := `SELECT ` + paymentSelectCols + ` FROM payments WHERE id = $1`
	err := scanPayment(r.db.QueryRowContext(ctx, query, id), p)
	if err == sql.ErrNoRows {
		return nil, payment.ErrPaymentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get payment: %w", err)
	}
	return p, nil
}

func (r *PaymentRepository) GetByReference(ctx context.Context, reference string) (*payment.Payment, error) {
	p := &payment.Payment{}
	query := `SELECT ` + paymentSelectCols + ` FROM payments WHERE reference = $1`
	err := scanPayment(r.db.QueryRowContext(ctx, query, reference), p)
	if err == sql.ErrNoRows {
		return nil, payment.ErrPaymentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get payment by reference: %w", err)
	}
	return p, nil
}

func (r *PaymentRepository) GetByProviderReference(ctx context.Context, providerReference string) (*payment.Payment, error) {
	p := &payment.Payment{}
	query := `SELECT ` + paymentSelectCols + ` FROM payments WHERE provider_reference = $1`
	err := scanPayment(r.db.QueryRowContext(ctx, query, providerReference), p)
	if err == sql.ErrNoRows {
		return nil, payment.ErrPaymentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get payment by provider reference: %w", err)
	}
	return p, nil
}

func (r *PaymentRepository) List(ctx context.Context, limit, offset int) ([]*payment.Payment, error) {
	query := `SELECT ` + paymentSelectCols + ` FROM payments ORDER BY created_at DESC LIMIT $1 OFFSET $2`
	rows, err := r.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list payments: %w", err)
	}
	defer rows.Close()

	var payments []*payment.Payment
	for rows.Next() {
		p := &payment.Payment{}
		if err := scanPayment(rows, p); err != nil {
			return nil, fmt.Errorf("failed to scan payment: %w", err)
		}
		payments = append(payments, p)
	}
	return payments, nil
}

// ListExpiredPending returns pending payments whose expires_at has already
// passed, oldest deadline first. Used by the expiry job — filtering by
// status+expires_at in SQL (instead of scanning the most recent N payments
// of any status) so it still finds old stragglers once payment volume grows.
func (r *PaymentRepository) ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]*payment.Payment, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT ` + paymentSelectCols + `
		FROM payments
		WHERE status = $1 AND expires_at < $2
		ORDER BY expires_at ASC
		LIMIT $3
	`
	rows, err := r.db.QueryContext(ctx, query, payment.StatusPending, before, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list expired pending payments: %w", err)
	}
	defer rows.Close()

	var payments []*payment.Payment
	for rows.Next() {
		p := &payment.Payment{}
		if err := scanPayment(rows, p); err != nil {
			return nil, fmt.Errorf("failed to scan payment: %w", err)
		}
		payments = append(payments, p)
	}
	return payments, nil
}

func (r *PaymentRepository) ListByMerchant(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]*payment.Payment, error) {
	query := `SELECT ` + paymentSelectCols + `
		FROM payments
		WHERE merchant_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`
	rows, err := r.db.QueryContext(ctx, query, merchantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list payments by merchant: %w", err)
	}
	defer rows.Close()

	var payments []*payment.Payment
	for rows.Next() {
		p := &payment.Payment{}
		if err := scanPayment(rows, p); err != nil {
			return nil, fmt.Errorf("failed to scan payment: %w", err)
		}
		payments = append(payments, p)
	}
	return payments, nil
}

// ListByPaymentLinkID returns payments spawned by checking out through a
// given Payment Link, most recent first — used by the link's detail page.
func (r *PaymentRepository) ListByPaymentLinkID(ctx context.Context, linkID uuid.UUID, limit, offset int) ([]*payment.Payment, error) {
	query := `SELECT ` + paymentSelectCols + `
		FROM payments
		WHERE payment_link_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`
	rows, err := r.db.QueryContext(ctx, query, linkID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list payments by payment link: %w", err)
	}
	defer rows.Close()

	var payments []*payment.Payment
	for rows.Next() {
		p := &payment.Payment{}
		if err := scanPayment(rows, p); err != nil {
			return nil, fmt.Errorf("failed to scan payment: %w", err)
		}
		payments = append(payments, p)
	}
	return payments, nil
}

// CountByPaymentLinkID returns the total number of payments spawned by a
// given Payment Link, for pagination on the link's detail page.
func (r *PaymentRepository) CountByPaymentLinkID(ctx context.Context, linkID uuid.UUID) (int, error) {
	var count int
	query := `SELECT COUNT(*) FROM payments WHERE payment_link_id = $1`
	if err := r.db.QueryRowContext(ctx, query, linkID).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count payments by payment link: %w", err)
	}
	return count, nil
}
