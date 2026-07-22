-- Migration: Create webhook events table
-- Description: Stores raw provider webhook payloads for audit and debugging

CREATE TABLE IF NOT EXISTS webhook_events (
    id UUID PRIMARY KEY,
    payment_id UUID REFERENCES payments(id) ON DELETE CASCADE,
    provider_name VARCHAR(100) NOT NULL,
    provider_reference VARCHAR(255) NOT NULL,
    event_type VARCHAR(100) NOT NULL,
    status VARCHAR(50) NOT NULL,
    raw_payload JSONB NOT NULL,
    processed_at TIMESTAMP,
    is_processed BOOLEAN NOT NULL DEFAULT false,
    processing_error TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_webhook_events_payment_id ON webhook_events(payment_id);
CREATE INDEX IF NOT EXISTS idx_webhook_events_provider_reference ON webhook_events(provider_reference);
CREATE INDEX IF NOT EXISTS idx_webhook_events_created_at ON webhook_events(created_at DESC);

COMMENT ON TABLE webhook_events IS 'Stores raw webhook payloads received from payment providers for audit/debugging';
COMMENT ON COLUMN webhook_events.raw_payload IS 'Raw webhook payload as received from provider, before normalization';
