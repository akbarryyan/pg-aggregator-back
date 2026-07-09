-- Migration: Create merchant provider configs table
-- Description: Stores provider routing preferences and fallback order per merchant

CREATE TABLE IF NOT EXISTS merchant_provider_configs (
    id UUID PRIMARY KEY,
    merchant_id UUID NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    provider_name VARCHAR(100) NOT NULL,
    payment_method VARCHAR(50) NOT NULL,
    priority INTEGER NOT NULL DEFAULT 1,
    weight INTEGER NOT NULL DEFAULT 100,
    failover_enabled BOOLEAN NOT NULL DEFAULT true,
    is_enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_merchant_provider_method UNIQUE (merchant_id, payment_method, provider_name),
    CONSTRAINT chk_merchant_provider_priority CHECK (priority > 0),
    CONSTRAINT chk_merchant_provider_weight CHECK (weight > 0)
);

CREATE INDEX idx_merchant_provider_configs_merchant_method
    ON merchant_provider_configs(merchant_id, payment_method, priority);
CREATE INDEX idx_merchant_provider_configs_provider_name
    ON merchant_provider_configs(provider_name);
CREATE INDEX idx_merchant_provider_configs_enabled
    ON merchant_provider_configs(is_enabled);

COMMENT ON TABLE merchant_provider_configs IS 'Stores enabled providers and fallback order per merchant and payment method';
COMMENT ON COLUMN merchant_provider_configs.priority IS 'Lower number means higher priority in routing order';
