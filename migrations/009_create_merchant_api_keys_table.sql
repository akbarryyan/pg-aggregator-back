-- Migration: Create merchant API keys table
-- Description: Hashed API keys for merchant → platform integration auth

CREATE TABLE IF NOT EXISTS merchant_api_keys (
    id UUID PRIMARY KEY,
    merchant_id UUID NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL DEFAULT 'Default',
    key_prefix VARCHAR(32) NOT NULL,
    key_hash VARCHAR(64) NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT true,
    last_used_at TIMESTAMP,
    revoked_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_merchant_api_keys_key_hash
    ON merchant_api_keys(key_hash);
CREATE INDEX IF NOT EXISTS idx_merchant_api_keys_merchant_id
    ON merchant_api_keys(merchant_id);
CREATE INDEX IF NOT EXISTS idx_merchant_api_keys_prefix
    ON merchant_api_keys(key_prefix);

COMMENT ON TABLE merchant_api_keys IS 'Merchant integration API keys (secret shown once; only hash stored)';
COMMENT ON COLUMN merchant_api_keys.key_prefix IS 'Non-secret prefix for display, e.g. pk_live_ab12cd';
COMMENT ON COLUMN merchant_api_keys.key_hash IS 'SHA-256 hex of full API key secret';
