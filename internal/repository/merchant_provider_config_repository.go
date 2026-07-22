package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/google/uuid"
)

type MerchantProviderConfigRepository struct {
	db *sql.DB
}

func NewMerchantProviderConfigRepository(db *sql.DB) *MerchantProviderConfigRepository {
	return &MerchantProviderConfigRepository{db: db}
}

func (r *MerchantProviderConfigRepository) ListEnabledByMerchantAndPaymentMethod(ctx context.Context, merchantID uuid.UUID, paymentMethod string) ([]*provider.MerchantProviderConfig, error) {
	query := `
		SELECT
			id, merchant_id, provider_name, payment_method, priority, weight, failover_enabled, is_enabled, created_at, updated_at
		FROM merchant_provider_configs
		WHERE merchant_id = $1 AND payment_method = $2 AND is_enabled = true
		ORDER BY priority ASC, weight DESC, created_at ASC
	`

	rows, err := r.db.QueryContext(ctx, query, merchantID, paymentMethod)
	if err != nil {
		return nil, fmt.Errorf("failed to list merchant provider configs: %w", err)
	}
	defer rows.Close()

	var configs []*provider.MerchantProviderConfig
	for rows.Next() {
		cfg := &provider.MerchantProviderConfig{}
		if err := rows.Scan(
			&cfg.ID,
			&cfg.MerchantID,
			&cfg.ProviderName,
			&cfg.PaymentMethod,
			&cfg.Priority,
			&cfg.Weight,
			&cfg.FailoverEnabled,
			&cfg.IsEnabled,
			&cfg.CreatedAt,
			&cfg.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan merchant provider config: %w", err)
		}
		configs = append(configs, cfg)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate merchant provider configs: %w", err)
	}

	return configs, nil
}

func (r *MerchantProviderConfigRepository) ListByMerchant(ctx context.Context, merchantID uuid.UUID) ([]*provider.MerchantProviderConfig, error) {
	query := `
		SELECT
			id, merchant_id, provider_name, payment_method, priority, weight, failover_enabled, is_enabled, created_at, updated_at
		FROM merchant_provider_configs
		WHERE merchant_id = $1
		ORDER BY payment_method ASC, priority ASC, weight DESC, created_at ASC
	`

	rows, err := r.db.QueryContext(ctx, query, merchantID)
	if err != nil {
		return nil, fmt.Errorf("failed to list merchant provider configs: %w", err)
	}
	defer rows.Close()

	var configs []*provider.MerchantProviderConfig
	for rows.Next() {
		cfg := &provider.MerchantProviderConfig{}
		if err := rows.Scan(
			&cfg.ID,
			&cfg.MerchantID,
			&cfg.ProviderName,
			&cfg.PaymentMethod,
			&cfg.Priority,
			&cfg.Weight,
			&cfg.FailoverEnabled,
			&cfg.IsEnabled,
			&cfg.CreatedAt,
			&cfg.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan merchant provider config: %w", err)
		}
		configs = append(configs, cfg)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate merchant provider configs: %w", err)
	}

	return configs, nil
}

func (r *MerchantProviderConfigRepository) Upsert(ctx context.Context, merchantID uuid.UUID, providerName, paymentMethod string, priority, weight int, failoverEnabled, isEnabled bool) error {
	query := `
		INSERT INTO merchant_provider_configs (
			id, merchant_id, provider_name, payment_method, priority, weight, failover_enabled, is_enabled, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10
		)
		ON CONFLICT (merchant_id, payment_method, provider_name)
		DO UPDATE SET
			priority = EXCLUDED.priority,
			weight = EXCLUDED.weight,
			failover_enabled = EXCLUDED.failover_enabled,
			is_enabled = EXCLUDED.is_enabled,
			updated_at = EXCLUDED.updated_at
	`

	now := time.Now()
	_, err := r.db.ExecContext(
		ctx,
		query,
		uuid.New(),
		merchantID,
		providerName,
		paymentMethod,
		priority,
		weight,
		failoverEnabled,
		isEnabled,
		now,
		now,
	)
	if err != nil {
		return fmt.Errorf("failed to upsert merchant provider config: %w", err)
	}

	return nil
}

func (r *MerchantProviderConfigRepository) Delete(ctx context.Context, merchantID uuid.UUID, paymentMethod, providerName string) error {
	query := `
		DELETE FROM merchant_provider_configs
		WHERE merchant_id = $1 AND payment_method = $2 AND provider_name = $3
	`

	_, err := r.db.ExecContext(ctx, query, merchantID, paymentMethod, providerName)
	if err != nil {
		return fmt.Errorf("failed to delete merchant provider config: %w", err)
	}

	return nil
}

// RoutingRow joins merchant display name for admin routing views.
type RoutingRow struct {
	Config       *provider.MerchantProviderConfig
	MerchantName string
	MerchantEmail string
}

func (r *MerchantProviderConfigRepository) ListByProvider(ctx context.Context, providerName string, limit, offset int) ([]RoutingRow, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	query := `
		SELECT
			c.id, c.merchant_id, c.provider_name, c.payment_method, c.priority, c.weight,
			c.failover_enabled, c.is_enabled, c.created_at, c.updated_at,
			COALESCE(m.business_name, m.name, '') AS merchant_name,
			COALESCE(m.email, '') AS merchant_email
		FROM merchant_provider_configs c
		LEFT JOIN merchants m ON m.id = c.merchant_id
		WHERE LOWER(c.provider_name) = LOWER($1)
		ORDER BY c.priority ASC, m.business_name ASC
		LIMIT $2 OFFSET $3
	`

	rows, err := r.db.QueryContext(ctx, query, providerName, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list configs by provider: %w", err)
	}
	defer rows.Close()

	return scanRoutingRows(rows)
}

func (r *MerchantProviderConfigRepository) CountByProvider(ctx context.Context, providerName string) (int64, error) {
	var count int64
	query := `SELECT COUNT(*) FROM merchant_provider_configs WHERE LOWER(provider_name) = LOWER($1)`
	if err := r.db.QueryRowContext(ctx, query, providerName).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count configs by provider: %w", err)
	}
	return count, nil
}

func (r *MerchantProviderConfigRepository) ListAllRouting(ctx context.Context, limit, offset int) ([]RoutingRow, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	query := `
		SELECT
			c.id, c.merchant_id, c.provider_name, c.payment_method, c.priority, c.weight,
			c.failover_enabled, c.is_enabled, c.created_at, c.updated_at,
			COALESCE(m.business_name, m.name, '') AS merchant_name,
			COALESCE(m.email, '') AS merchant_email
		FROM merchant_provider_configs c
		LEFT JOIN merchants m ON m.id = c.merchant_id
		ORDER BY c.payment_method ASC, c.priority ASC, m.business_name ASC
		LIMIT $1 OFFSET $2
	`

	rows, err := r.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list routing configs: %w", err)
	}
	defer rows.Close()

	return scanRoutingRows(rows)
}

func (r *MerchantProviderConfigRepository) CountAllRouting(ctx context.Context) (int64, error) {
	var count int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM merchant_provider_configs`).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count routing configs: %w", err)
	}
	return count, nil
}

func scanRoutingRows(rows *sql.Rows) ([]RoutingRow, error) {
	var result []RoutingRow
	for rows.Next() {
		cfg := &provider.MerchantProviderConfig{}
		var merchantName, merchantEmail string
		if err := rows.Scan(
			&cfg.ID,
			&cfg.MerchantID,
			&cfg.ProviderName,
			&cfg.PaymentMethod,
			&cfg.Priority,
			&cfg.Weight,
			&cfg.FailoverEnabled,
			&cfg.IsEnabled,
			&cfg.CreatedAt,
			&cfg.UpdatedAt,
			&merchantName,
			&merchantEmail,
		); err != nil {
			return nil, fmt.Errorf("failed to scan routing row: %w", err)
		}
		result = append(result, RoutingRow{
			Config:        cfg,
			MerchantName:  merchantName,
			MerchantEmail: merchantEmail,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
