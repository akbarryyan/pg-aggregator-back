package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/admin"
	"github.com/google/uuid"
)

type AdminRepository struct {
	db *sql.DB
}

func NewAdminRepository(db *sql.DB) *AdminRepository {
	return &AdminRepository{db: db}
}

func (r *AdminRepository) GetByEmail(ctx context.Context, email string) (*admin.Admin, error) {
	a := &admin.Admin{}

	query := `
		SELECT id, name, email, password_hash, is_active, last_login_at, created_at, updated_at
		FROM admins
		WHERE email = $1
	`

	err := r.db.QueryRowContext(ctx, query, email).Scan(
		&a.ID,
		&a.Name,
		&a.Email,
		&a.PasswordHash,
		&a.IsActive,
		&a.LastLoginAt,
		&a.CreatedAt,
		&a.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, admin.ErrAdminNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get admin by email: %w", err)
	}

	return a, nil
}

func (r *AdminRepository) GetByID(ctx context.Context, id uuid.UUID) (*admin.Admin, error) {
	a := &admin.Admin{}

	query := `
		SELECT id, name, email, password_hash, is_active, last_login_at, created_at, updated_at
		FROM admins
		WHERE id = $1
	`

	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&a.ID,
		&a.Name,
		&a.Email,
		&a.PasswordHash,
		&a.IsActive,
		&a.LastLoginAt,
		&a.CreatedAt,
		&a.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, admin.ErrAdminNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get admin by id: %w", err)
	}

	return a, nil
}

func (r *AdminRepository) UpdateLastLoginAt(ctx context.Context, id uuid.UUID, at time.Time) error {
	query := `
		UPDATE admins
		SET last_login_at = $1, updated_at = $2
		WHERE id = $3
	`
	_, err := r.db.ExecContext(ctx, query, at, at, id)
	if err != nil {
		return fmt.Errorf("failed to update admin last login: %w", err)
	}
	return nil
}

func (r *AdminRepository) UpdatePasswordHash(ctx context.Context, id uuid.UUID, passwordHash string) error {
	now := time.Now().UTC()
	query := `
		UPDATE admins
		SET password_hash = $1, updated_at = $2
		WHERE id = $3
	`
	result, err := r.db.ExecContext(ctx, query, passwordHash, now, id)
	if err != nil {
		return fmt.Errorf("failed to update admin password: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to read password update result: %w", err)
	}
	if rows == 0 {
		return admin.ErrAdminNotFound
	}
	return nil
}

func (r *AdminRepository) ExistsByEmailExceptID(ctx context.Context, email string, id uuid.UUID) (bool, error) {
	var exists bool
	query := `
		SELECT EXISTS(
			SELECT 1 FROM admins WHERE email = $1 AND id <> $2
		)
	`
	if err := r.db.QueryRowContext(ctx, query, email, id).Scan(&exists); err != nil {
		return false, fmt.Errorf("failed to check admin email uniqueness: %w", err)
	}
	return exists, nil
}

func (r *AdminRepository) UpdateProfile(ctx context.Context, id uuid.UUID, name, email string) (*admin.Admin, error) {
	now := time.Now().UTC()
	query := `
		UPDATE admins
		SET name = $1, email = $2, updated_at = $3
		WHERE id = $4
		RETURNING id, name, email, password_hash, is_active, last_login_at, created_at, updated_at
	`

	a := &admin.Admin{}
	err := r.db.QueryRowContext(ctx, query, name, email, now, id).Scan(
		&a.ID,
		&a.Name,
		&a.Email,
		&a.PasswordHash,
		&a.IsActive,
		&a.LastLoginAt,
		&a.CreatedAt,
		&a.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, admin.ErrAdminNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update admin profile: %w", err)
	}
	return a, nil
}
