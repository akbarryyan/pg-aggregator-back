-- Migration: Create merchant users table
-- Description: Login accounts for merchant dashboard (scoped to a merchant)

CREATE TABLE IF NOT EXISTS merchant_users (
    id UUID PRIMARY KEY,
    merchant_id UUID NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    name VARCHAR(150) NOT NULL,
    email VARCHAR(255) NOT NULL,
    password_hash TEXT NOT NULL,
    role VARCHAR(50) NOT NULL DEFAULT 'owner',
    is_active BOOLEAN NOT NULL DEFAULT true,
    last_login_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_merchant_users_email_lower
    ON merchant_users (LOWER(email));
CREATE INDEX IF NOT EXISTS idx_merchant_users_merchant_id
    ON merchant_users(merchant_id);

COMMENT ON TABLE merchant_users IS 'Merchant dashboard login users (JWT auth, not API keys)';
COMMENT ON COLUMN merchant_users.role IS 'owner | staff';
