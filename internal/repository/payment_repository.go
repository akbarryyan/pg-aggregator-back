package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"pg-aggregator/internal/domain/payment"
)

type PaymentRepository struct {
	db *sql.DB
}

func NewPaymentRepository(db *sql.DB) *PaymentRepository {
	return &PaymentRepository{db: db}
}

func (r *PaymentRepository) Create(ctx context.Context, p *payment.Payment) error {
	query := `
		INSERT INTO payments (
			id, reference, merchant_id, amount, currency, payment_method,
			provider_name, provider_reference, status, description,
			customer_name, customer_email, qris_data, callback_url,
			expires_at, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17
		)
		RETURNING id, created_at, updated_at
	`

	err := r.db.QueryRowContext(ctx, query,
		p.ID, p.Reference, p.MerchantID, p.Amount, p.Currency, p.PaymentMethod,
		p.ProviderName, p.ProviderReference, p.Status, p.Description,
		p.CustomerName, p.CustomerEmail, p.QRISData, p.CallbackURL,
		p.ExpiresAt, p.CreatedAt, p.UpdatedAt,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)

	if err != nil {
		return fmt.Errorf("failed to create payment: %w", err)
	}

	return nil
}

func (r *PaymentRepository) GetByID(ctx context.Context, id uuid.UUID) (*payment.Payment, error) {
	p := &payment.Payment{}

	query := `
		SELECT 
			id, reference, merchant_id, amount, currency, payment_method,
			provider_name, provider_reference, status, description,
			customer_name, customer_email, qris_data, callback_url,
			expires_at, paid_at, created_at, updated_at
		FROM payments
		WHERE id = $1
	`

	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&p.ID, &p.Reference, &p.MerchantID, &p.Amount, &p.Currency, &p.PaymentMethod,
		&p.ProviderName, &p.ProviderReference, &p.Status, &p.Description,
		&p.CustomerName, &p.CustomerEmail, &p.QRISData, &p.CallbackURL,
		&p.ExpiresAt, &p.PaidAt, &p.CreatedAt, &p.UpdatedAt,
	)

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

	query := `
		SELECT 
			id, reference, merchant_id, amount, currency, payment_method,
			provider_name, provider_reference, status, description,
			customer_name, customer_email, qris_data, callback_url,
			expires_at, paid_at, created_at, updated_at
		FROM payments
		WHERE reference = $1
	`

	err := r.db.QueryRowContext(ctx, query, reference).Scan(
		&p.ID, &p.Reference, &p.MerchantID, &p.Amount, &p.Currency, &p.PaymentMethod,
		&p.ProviderName, &p.ProviderReference, &p.Status, &p.Description,
		&p.CustomerName, &p.CustomerEmail, &p.QRISData, &p.CallbackURL,
		&p.ExpiresAt, &p.PaidAt, &p.CreatedAt, &p.UpdatedAt,
	)

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

	query := `
		SELECT 
			id, reference, merchant_id, amount, currency, payment_method,
			provider_name, provider_reference, status, description,
			customer_name, customer_email, qris_data, callback_url,
			expires_at, paid_at, created_at, updated_at
		FROM payments
		WHERE provider_reference = $1
	`

	err := r.db.QueryRowContext(ctx, query, providerReference).Scan(
		&p.ID, &p.Reference, &p.MerchantID, &p.Amount, &p.Currency, &p.PaymentMethod,
		&p.ProviderName, &p.ProviderReference, &p.Status, &p.Description,
		&p.CustomerName, &p.CustomerEmail, &p.QRISData, &p.CallbackURL,
		&p.ExpiresAt, &p.PaidAt, &p.CreatedAt, &p.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, payment.ErrPaymentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get payment by provider reference: %w", err)
	}

	return p, nil
}

func (r *PaymentRepository) List(ctx context.Context, limit, offset int) ([]*payment.Payment, error) {
	query := `
		SELECT 
			id, reference, merchant_id, amount, currency, payment_method,
			provider_name, provider_reference, status, description,
			customer_name, customer_email, qris_data, callback_url,
			expires_at, paid_at, created_at, updated_at
		FROM payments
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2
	`

	rows, err := r.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list payments: %w", err)
	}
	defer rows.Close()

	var payments []*payment.Payment
	for rows.Next() {
		p := &payment.Payment{}
		err := rows.Scan(
			&p.ID, &p.Reference, &p.MerchantID, &p.Amount, &p.Currency, &p.PaymentMethod,
			&p.ProviderName, &p.ProviderReference, &p.Status, &p.Description,
			&p.CustomerName, &p.CustomerEmail, &p.QRISData, &p.CallbackURL,
			&p.ExpiresAt, &p.PaidAt, &p.CreatedAt, &p.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan payment: %w", err)
		}
		payments = append(payments, p)
	}

	return payments, nil
}

func (r *PaymentRepository) ListByMerchant(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]*payment.Payment, error) {
	query := `
		SELECT 
			id, reference, merchant_id, amount, currency, payment_method,
			provider_name, provider_reference, status, description,
			customer_name, customer_email, qris_data, callback_url,
			expires_at, paid_at, created_at, updated_at
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
		err := rows.Scan(
			&p.ID, &p.Reference, &p.MerchantID, &p.Amount, &p.Currency, &p.PaymentMethod,
			&p.ProviderName, &p.ProviderReference, &p.Status, &p.Description,
			&p.CustomerName, &p.CustomerEmail, &p.QRISData, &p.CallbackURL,
			&p.ExpiresAt, &p.PaidAt, &p.CreatedAt, &p.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan payment: %w", err)
		}
		payments = append(payments, p)
	}

	return payments, nil
}
