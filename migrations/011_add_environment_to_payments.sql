-- Migration: Add environment column to payments
-- sandbox | production — isolates merchant dashboard data and API key creates

ALTER TABLE payments
    ADD COLUMN IF NOT EXISTS environment VARCHAR(20) NOT NULL DEFAULT 'production';

CREATE INDEX IF NOT EXISTS idx_payments_merchant_environment
    ON payments (merchant_id, environment);

CREATE INDEX IF NOT EXISTS idx_payments_environment_created_at
    ON payments (environment, created_at DESC);

COMMENT ON COLUMN payments.environment IS 'sandbox | production; set from API key or merchant dashboard switch';

-- Existing rows stay production (default already applied)
UPDATE payments SET environment = 'production' WHERE environment IS NULL OR environment = '';
