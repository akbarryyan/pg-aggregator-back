-- Migration: Make webhook_events.payment_id nullable
-- Description: Raw webhook payloads must be recorded even when the payment
-- cannot be resolved yet (e.g. invalid signature, unknown provider reference),
-- so payment_id can no longer be required at insert time.
-- Safe to re-run: DROP NOT NULL is a no-op when the column is already nullable.

ALTER TABLE webhook_events ALTER COLUMN payment_id DROP NOT NULL;
