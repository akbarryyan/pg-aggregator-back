package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
)

type MerchantRepository struct {
	db *sql.DB
}

func NewMerchantRepository(db *sql.DB) *MerchantRepository {
	return &MerchantRepository{db: db}
}

func (r *MerchantRepository) Create(ctx context.Context, req *merchant.CreateMerchantRequest) (*merchant.Merchant, error) {
	m := &merchant.Merchant{
		ID:           uuid.New(),
		Name:         req.Name,
		Email:        req.Email,
		Phone:        req.Phone,
		BusinessName: req.BusinessName,
		WebhookURL:   req.WebhookURL,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	query := `
		INSERT INTO merchants (id, name, email, phone, business_name, webhook_url, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at
	`

	err := r.db.QueryRowContext(ctx, query,
		m.ID, m.Name, m.Email, m.Phone, m.BusinessName, m.WebhookURL, m.IsActive, m.CreatedAt, m.UpdatedAt,
	).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt)

	if err != nil {
		return nil, fmt.Errorf("failed to create merchant: %w", err)
	}

	return m, nil
}

func (r *MerchantRepository) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM merchants WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete merchant: %w", err)
	}
	return nil
}

func (r *MerchantRepository) GetByID(ctx context.Context, id uuid.UUID) (*merchant.Merchant, error) {
	m := &merchant.Merchant{}

	query := `
		SELECT id, name, email, phone, business_name, webhook_url, webhook_secret, is_active, created_at, updated_at
		FROM merchants
		WHERE id = $1
	`

	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&m.ID, &m.Name, &m.Email, &m.Phone, &m.BusinessName, &m.WebhookURL, &m.WebhookSecret, &m.IsActive, &m.CreatedAt, &m.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, merchant.ErrMerchantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get merchant: %w", err)
	}

	return m, nil
}

// SetWebhookSecret persists the HMAC signing secret for outbound payment
// webhooks. See PaymentService.EnsureMerchantWebhookSecret for generation.
func (r *MerchantRepository) SetWebhookSecret(ctx context.Context, id uuid.UUID, secret string) error {
	query := `UPDATE merchants SET webhook_secret = $1, updated_at = $2 WHERE id = $3`
	_, err := r.db.ExecContext(ctx, query, secret, time.Now(), id)
	if err != nil {
		return fmt.Errorf("failed to set merchant webhook secret: %w", err)
	}
	return nil
}

func (r *MerchantRepository) GetByEmail(ctx context.Context, email string) (*merchant.Merchant, error) {
	m := &merchant.Merchant{}

	query := `
		SELECT id, name, email, phone, business_name, webhook_url, is_active, created_at, updated_at
		FROM merchants
		WHERE email = $1
	`

	err := r.db.QueryRowContext(ctx, query, email).Scan(
		&m.ID, &m.Name, &m.Email, &m.Phone, &m.BusinessName, &m.WebhookURL, &m.IsActive, &m.CreatedAt, &m.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, merchant.ErrMerchantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get merchant by email: %w", err)
	}

	return m, nil
}

func (r *MerchantRepository) Update(ctx context.Context, id uuid.UUID, req *merchant.UpdateMerchantRequest) (*merchant.Merchant, error) {
	m, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if req.Name != nil {
		m.Name = *req.Name
	}
	if req.Phone != nil {
		m.Phone = *req.Phone
	}
	if req.BusinessName != nil {
		m.BusinessName = *req.BusinessName
	}
	if req.WebhookURL != nil {
		m.WebhookURL = req.WebhookURL
	}

	m.UpdatedAt = time.Now()

	query := `
		UPDATE merchants
		SET name = $1, phone = $2, business_name = $3, webhook_url = $4, updated_at = $5
		WHERE id = $6
		RETURNING updated_at
	`

	err = r.db.QueryRowContext(ctx, query,
		m.Name, m.Phone, m.BusinessName, m.WebhookURL, m.UpdatedAt, m.ID,
	).Scan(&m.UpdatedAt)

	if err != nil {
		return nil, fmt.Errorf("failed to update merchant: %w", err)
	}

	return m, nil
}

func (r *MerchantRepository) SetActive(ctx context.Context, id uuid.UUID, isActive bool) error {
	query := `UPDATE merchants SET is_active = $1, updated_at = $2 WHERE id = $3`
	_, err := r.db.ExecContext(ctx, query, isActive, time.Now(), id)
	if err != nil {
		return fmt.Errorf("failed to update merchant active status: %w", err)
	}
	return nil
}

func (r *MerchantRepository) List(ctx context.Context, limit, offset int) ([]*merchant.Merchant, error) {
	return r.ListAdmin(ctx, "", nil, limit, offset)
}

func (r *MerchantRepository) ListAdmin(
	ctx context.Context,
	search string,
	isActive *bool,
	limit, offset int,
) ([]*merchant.Merchant, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	query := `
		SELECT id, name, email, phone, business_name, webhook_url, is_active, created_at, updated_at
		FROM merchants
		WHERE 1=1
	`
	args := make([]interface{}, 0, 4)
	argN := 1

	if isActive != nil {
		query += fmt.Sprintf(" AND is_active = $%d", argN)
		args = append(args, *isActive)
		argN++
	}
	if search != "" {
		query += fmt.Sprintf(` AND (
			name ILIKE $%d OR
			email ILIKE $%d OR
			COALESCE(phone, '') ILIKE $%d OR
			business_name ILIKE $%d
		)`, argN, argN, argN, argN)
		args = append(args, "%"+search+"%")
		argN++
	}

	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", argN, argN+1)
	args = append(args, limit, offset)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list merchants: %w", err)
	}
	defer rows.Close()

	var merchants []*merchant.Merchant
	for rows.Next() {
		m := &merchant.Merchant{}
		if err := rows.Scan(
			&m.ID, &m.Name, &m.Email, &m.Phone, &m.BusinessName, &m.WebhookURL, &m.IsActive, &m.CreatedAt, &m.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan merchant: %w", err)
		}
		merchants = append(merchants, m)
	}

	return merchants, nil
}

func (r *MerchantRepository) Count(ctx context.Context) (int64, error) {
	return r.CountAdmin(ctx, "", nil)
}

func (r *MerchantRepository) CountAdmin(ctx context.Context, search string, isActive *bool) (int64, error) {
	query := `SELECT COUNT(*) FROM merchants WHERE 1=1`
	args := make([]interface{}, 0, 2)
	argN := 1

	if isActive != nil {
		query += fmt.Sprintf(" AND is_active = $%d", argN)
		args = append(args, *isActive)
		argN++
	}
	if search != "" {
		query += fmt.Sprintf(` AND (
			name ILIKE $%d OR
			email ILIKE $%d OR
			COALESCE(phone, '') ILIKE $%d OR
			business_name ILIKE $%d
		)`, argN, argN, argN, argN)
		args = append(args, "%"+search+"%")
	}

	var count int64
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count merchants: %w", err)
	}
	return count, nil
}
