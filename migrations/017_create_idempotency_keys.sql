-- Idempotency claims for merchant payment creation (Idempotency-Key header).
-- A claim is inserted BEFORE the provider is called, so two concurrent requests
-- with the same key cannot both create a payment. payment_id stays NULL until
-- the payment exists. Rows older than 24h are ignored and purged hourly.
CREATE TABLE IF NOT EXISTS idempotency_keys (
    id              UUID PRIMARY KEY,
    merchant_id     UUID NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    environment     VARCHAR(20) NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    request_hash    CHAR(64) NOT NULL,
    payment_id      UUID REFERENCES payments(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_idempotency_scope UNIQUE (merchant_id, environment, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_idempotency_keys_created_at ON idempotency_keys (created_at);
