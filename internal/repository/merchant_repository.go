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

func (r *MerchantRepository) GetByID(ctx context.Context, id uuid.UUID) (*merchant.Merchant, error) {
	m := &merchant.Merchant{}

	query := `
		SELECT id, name, email, phone, business_name, webhook_url, is_active, created_at, updated_at
		FROM merchants
		WHERE id = $1
	`

	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&m.ID, &m.Name, &m.Email, &m.Phone, &m.BusinessName, &m.WebhookURL, &m.IsActive, &m.CreatedAt, &m.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, merchant.ErrMerchantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get merchant: %w", err)
	}

	return m, nil
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
