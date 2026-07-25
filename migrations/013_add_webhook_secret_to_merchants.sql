-- Migration: Add webhook_secret to merchants
-- Used to HMAC-SHA256 sign outbound merchant callback payloads (payment
-- status webhooks), mirroring how we verify inbound Cashi webhooks.
-- Nullable: generated lazily on first use, not backfilled for existing
-- merchants — see PaymentService.EnsureMerchantWebhookSecret.

ALTER TABLE merchants ADD COLUMN IF NOT EXISTS webhook_secret VARCHAR(255);

COMMENT ON COLUMN merchants.webhook_secret IS 'Shared secret for HMAC-SHA256 signing of outbound payment webhooks (X-PG-Signature header). Generated lazily, not shown in general merchant API responses.';
