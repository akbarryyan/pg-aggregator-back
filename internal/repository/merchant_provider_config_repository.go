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
