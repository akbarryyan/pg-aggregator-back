package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/paymentlink"
	"github.com/google/uuid"
)

type PaymentLinkRepository struct {
	db *sql.DB
}

func NewPaymentLinkRepository(db *sql.DB) *PaymentLinkRepository {
	return &PaymentLinkRepository{db: db}
}

const paymentLinkSelectCols = `
			id, merchant_id, slug, title, description, amount_type, amount,
			currency, min_amount, max_amount, is_active, expires_at,
			environment, created_at, updated_at
`

func scanPaymentLink(scanner interface {
	Scan(dest ...interface{}) error
}, l *paymentlink.PaymentLink) error {
	var description sql.NullString
	err := scanner.Scan(
		&l.ID, &l.MerchantID, &l.Slug, &l.Title, &description, &l.AmountType, &l.Amount,
		&l.Currency, &l.MinAmount, &l.MaxAmount, &l.IsActive, &l.ExpiresAt,
		&l.Environment, &l.CreatedAt, &l.UpdatedAt,
	)
	if err != nil {
		return err
	}
	l.Description = description.String
	return nil
}

func (r *PaymentLinkRepository) Create(ctx context.Context, l *paymentlink.PaymentLink) error {
	query := `
		INSERT INTO payment_links (
			id, merchant_id, slug, title, description, amount_type, amount,
			currency, min_amount, max_amount, is_active, expires_at, environment,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
		)
		RETURNING id, created_at, updated_at
	`

	err := r.db.QueryRowContext(ctx, query,
		l.ID, l.MerchantID, l.Slug, l.Title, l.Description, l.AmountType, l.Amount,
		l.Currency, l.MinAmount, l.MaxAmount, l.IsActive, l.ExpiresAt, l.Environment,
		l.CreatedAt, l.UpdatedAt,
	).Scan(&l.ID, &l.CreatedAt, &l.UpdatedAt)

	if err != nil {
		return fmt.Errorf("failed to create payment link: %w", err)
	}

	return nil
}

func (r *PaymentLinkRepository) GetByID(ctx context.Context, id uuid.UUID) (*paymentlink.PaymentLink, error) {
	l := &paymentlink.PaymentLink{}
	query := `SELECT ` + paymentLinkSelectCols + ` FROM payment_links WHERE id = $1`
	err := scanPaymentLink(r.db.QueryRowContext(ctx, query, id), l)
	if err == sql.ErrNoRows {
		return nil, paymentlink.ErrPaymentLinkNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get payment link: %w", err)
	}
	return l, nil
}

func (r *PaymentLinkRepository) GetBySlug(ctx context.Context, slug string) (*paymentlink.PaymentLink, error) {
	l := &paymentlink.PaymentLink{}
	query := `SELECT ` + paymentLinkSelectCols + ` FROM payment_links WHERE slug = $1`
	err := scanPaymentLink(r.db.QueryRowContext(ctx, query, slug), l)
	if err == sql.ErrNoRows {
		return nil, paymentlink.ErrPaymentLinkNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get payment link by slug: %w", err)
	}
	return l, nil
}

func (r *PaymentLinkRepository) List(
	ctx context.Context,
	merchantID uuid.UUID,
	environment string,
	isActive *bool,
	limit, offset int,
) ([]*paymentlink.PaymentLink, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	query := `SELECT ` + paymentLinkSelectCols + ` FROM payment_links WHERE merchant_id = $1`
	args := []interface{}{merchantID}
	argN := 2

	if environment != "" {
		query += fmt.Sprintf(" AND environment = $%d", argN)
		args = append(args, environment)
		argN++
	}
	if isActive != nil {
		query += fmt.Sprintf(" AND is_active = $%d", argN)
		args = append(args, *isActive)
		argN++
	}

	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", argN, argN+1)
	args = append(args, limit, offset)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list payment links: %w", err)
	}
	defer rows.Close()

	var links []*paymentlink.PaymentLink
	for rows.Next() {
		l := &paymentlink.PaymentLink{}
		if err := scanPaymentLink(rows, l); err != nil {
			return nil, fmt.Errorf("failed to scan payment link: %w", err)
		}
		links = append(links, l)
	}
	return links, nil
}

func (r *PaymentLinkRepository) Count(
	ctx context.Context,
	merchantID uuid.UUID,
	environment string,
	isActive *bool,
) (int, error) {
	query := `SELECT COUNT(*) FROM payment_links WHERE merchant_id = $1`
	args := []interface{}{merchantID}
	argN := 2

	if environment != "" {
		query += fmt.Sprintf(" AND environment = $%d", argN)
		args = append(args, environment)
		argN++
	}
	if isActive != nil {
		query += fmt.Sprintf(" AND is_active = $%d", argN)
		args = append(args, *isActive)
	}

	var count int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count payment links: %w", err)
	}
	return count, nil
}

// Update persists the mutable fields of a payment link (title, description,
// is_active, expires_at, min_amount, max_amount). amount_type/amount are
// immutable after creation and are never touched here.
func (r *PaymentLinkRepository) Update(ctx context.Context, l *paymentlink.PaymentLink) error {
	query := `
		UPDATE payment_links
		SET title = $1, description = $2, is_active = $3, expires_at = $4,
		    min_amount = $5, max_amount = $6, updated_at = $7
		WHERE id = $8
		RETURNING updated_at
	`
	err := r.db.QueryRowContext(ctx, query,
		l.Title, l.Description, l.IsActive, l.ExpiresAt, l.MinAmount, l.MaxAmount, l.UpdatedAt, l.ID,
	).Scan(&l.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to update payment link: %w", err)
	}
	return nil
}

func (r *PaymentLinkRepository) SetActive(ctx context.Context, id uuid.UUID, isActive bool) error {
	query := `UPDATE payment_links SET is_active = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`
	_, err := r.db.ExecContext(ctx, query, isActive, id)
	if err != nil {
		return fmt.Errorf("failed to update payment link active status: %w", err)
	}
	return nil
}
