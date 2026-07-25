-- Migration: Trace payments back to the payment link that spawned them.
-- Nullable + ON DELETE SET NULL so deactivating/deleting a link never
-- cascades into historical payment records.

ALTER TABLE payments ADD COLUMN IF NOT EXISTS payment_link_id UUID REFERENCES payment_links(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_payments_payment_link_id ON payments(payment_link_id);

COMMENT ON COLUMN payments.payment_link_id IS 'Set when this one-time payment was spawned by a customer checking out through a Payment Link; NULL for directly-created payments';
