-- Migration: Create merchant callback deliveries table
-- Description: Tracks outbound webhook deliveries from platform to merchant endpoints

CREATE TABLE IF NOT EXISTS merchant_callback_deliveries (
    id UUID PRIMARY KEY,
    payment_id UUID NOT NULL REFERENCES payments(id) ON DELETE CASCADE,
    merchant_id UUID NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    event_type VARCHAR(100) NOT NULL,
    target_url TEXT NOT NULL,
    request_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    attempt_number INT NOT NULL DEFAULT 1,
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    http_status INT,
    response_body TEXT,
    error_message TEXT,
    delivered_at TIMESTAMP,
    next_retry_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_merchant_callback_deliveries_payment_id
    ON merchant_callback_deliveries(payment_id);
CREATE INDEX IF NOT EXISTS idx_merchant_callback_deliveries_merchant_id
    ON merchant_callback_deliveries(merchant_id);
CREATE INDEX IF NOT EXISTS idx_merchant_callback_deliveries_status
    ON merchant_callback_deliveries(status);
CREATE INDEX IF NOT EXISTS idx_merchant_callback_deliveries_created_at
    ON merchant_callback_deliveries(created_at DESC);

COMMENT ON TABLE merchant_callback_deliveries IS 'Outbound callback attempts to merchant webhook_url / payment callback_url';
COMMENT ON COLUMN merchant_callback_deliveries.status IS 'pending | success | failed | skipped';
COMMENT ON COLUMN merchant_callback_deliveries.event_type IS 'payment.paid | payment.expired | payment.failed | etc.';
