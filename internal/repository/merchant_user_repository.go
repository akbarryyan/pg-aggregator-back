package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/google/uuid"
)

type MerchantUserRepository struct {
	db *sql.DB
}

func NewMerchantUserRepository(db *sql.DB) *MerchantUserRepository {
	return &MerchantUserRepository{db: db}
}

func (r *MerchantUserRepository) Create(ctx context.Context, u *merchant.User) (*merchant.User, error) {
	u.ID = uuid.New()
	now := time.Now().UTC()
	u.CreatedAt = now
	u.UpdatedAt = now

	query := `
		INSERT INTO merchant_users (id, merchant_id, name, email, password_hash, role, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at
	`
	err := r.db.QueryRowContext(ctx, query,
		u.ID, u.MerchantID, u.Name, u.Email, u.PasswordHash, u.Role, u.IsActive, u.CreatedAt, u.UpdatedAt,
	).Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create merchant user: %w", err)
	}
	return u, nil
}

func (r *MerchantUserRepository) GetByEmail(ctx context.Context, email string) (*merchant.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	row := r.db.QueryRowContext(ctx, `
		SELECT id, merchant_id, name, email, password_hash, role, is_active,
		       last_login_at, created_at, updated_at
		FROM merchant_users
		WHERE LOWER(email) = $1
	`, email)
	return scanMerchantUser(row)
}

func (r *MerchantUserRepository) GetByID(ctx context.Context, id uuid.UUID) (*merchant.User, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, merchant_id, name, email, password_hash, role, is_active,
		       last_login_at, created_at, updated_at
		FROM merchant_users
		WHERE id = $1
	`, id)
	return scanMerchantUser(row)
}

func (r *MerchantUserRepository) UpdateLastLoginAt(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE merchant_users SET last_login_at = $2, updated_at = $2 WHERE id = $1
	`, id, at)
	return err
}

func (r *MerchantUserRepository) UpdateProfile(ctx context.Context, id uuid.UUID, name, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE merchant_users
		SET name = $2, email = $3, updated_at = $4
		WHERE id = $1
	`, id, name, email, now)
	if err != nil {
		return fmt.Errorf("failed to update merchant user profile: %w", err)
	}
	return nil
}

func (r *MerchantUserRepository) UpdatePasswordHash(ctx context.Context, id uuid.UUID, hash string) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE merchant_users SET password_hash = $2, updated_at = $3 WHERE id = $1
	`, id, hash, now)
	return err
}

type merchantUserScanner interface {
	Scan(dest ...interface{}) error
}

func scanMerchantUser(row merchantUserScanner) (*merchant.User, error) {
	var (
		u         merchant.User
		lastLogin sql.NullTime
	)
	err := row.Scan(
		&u.ID, &u.MerchantID, &u.Name, &u.Email, &u.PasswordHash, &u.Role, &u.IsActive,
		&lastLogin, &u.CreatedAt, &u.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, merchant.ErrMerchantUserNotFound
	}
	if err != nil {
		return nil, err
	}
	if lastLogin.Valid {
		t := lastLogin.Time
		u.LastLoginAt = &t
	}
	return &u, nil
}
