-- Migration: Create payments table
-- Description: Stores payment transactions and their status

CREATE TABLE IF NOT EXISTS payments (
    id UUID PRIMARY KEY,
    reference VARCHAR(100) NOT NULL UNIQUE,
    merchant_id UUID NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    amount BIGINT NOT NULL,
    currency VARCHAR(10) NOT NULL DEFAULT 'IDR',
    payment_method VARCHAR(50) NOT NULL,
    provider_name VARCHAR(100) NOT NULL,
    provider_reference VARCHAR(255),
    status VARCHAR(50) NOT NULL,
    description TEXT NOT NULL,
    customer_name VARCHAR(255),
    customer_email VARCHAR(255),
    qris_data TEXT,
    callback_url TEXT,
    expires_at TIMESTAMP NOT NULL,
    paid_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_payments_reference ON payments(reference);
CREATE INDEX IF NOT EXISTS idx_payments_merchant_id ON payments(merchant_id);
CREATE INDEX IF NOT EXISTS idx_payments_provider_reference ON payments(provider_reference);
CREATE INDEX IF NOT EXISTS idx_payments_status ON payments(status);
CREATE INDEX IF NOT EXISTS idx_payments_payment_method ON payments(payment_method);
CREATE INDEX IF NOT EXISTS idx_payments_created_at ON payments(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_payments_expires_at ON payments(expires_at);
CREATE INDEX IF NOT EXISTS idx_payments_merchant_status ON payments(merchant_id, status);

COMMENT ON TABLE payments IS 'Stores payment transactions';
COMMENT ON COLUMN payments.reference IS 'Internal unique payment reference';
COMMENT ON COLUMN payments.provider_reference IS 'Provider transaction ID';
COMMENT ON COLUMN payments.status IS 'Payment status (pending, paid, expired, failed, cancelled)';
COMMENT ON COLUMN payments.qris_data IS 'QRIS string data for QR code generation';
COMMENT ON COLUMN payments.expires_at IS 'Payment expiration timestamp';
COMMENT ON COLUMN payments.paid_at IS 'Timestamp when payment was confirmed paid';
