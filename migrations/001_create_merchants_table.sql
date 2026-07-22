-- Migration: Create merchants table
-- Description: Stores merchant/business information

CREATE TABLE IF NOT EXISTS merchants (
    id UUID PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    email VARCHAR(255) NOT NULL UNIQUE,
    phone VARCHAR(50),
    business_name VARCHAR(255) NOT NULL,
    webhook_url TEXT,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_merchants_email ON merchants(email);
CREATE INDEX IF NOT EXISTS idx_merchants_is_active ON merchants(is_active);
CREATE INDEX IF NOT EXISTS idx_merchants_created_at ON merchants(created_at);

COMMENT ON TABLE merchants IS 'Stores merchant and business information';
COMMENT ON COLUMN merchants.id IS 'Unique merchant identifier (UUID)';
COMMENT ON COLUMN merchants.email IS 'Merchant email address (unique)';
COMMENT ON COLUMN merchants.webhook_url IS 'URL for merchant callback notifications';
COMMENT ON COLUMN merchants.is_active IS 'Whether merchant account is active';
