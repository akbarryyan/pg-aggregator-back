-- Migration 019: money-integrity constraints on payments.
-- Runs in one transaction: if existing data violates a constraint the whole
-- file rolls back and nothing changes (DEPLOY.md 4.4 lists the queries that
-- must return no rows before applying). Idempotent: safe to re-apply.
-- Lookups in pg_constraint are scoped by conrelid, never by name alone: names
-- are only unique per table, and tests apply this file in several schemas.
BEGIN;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'payments'::regclass AND conname = 'payments_amount_positive'
    ) THEN
        ALTER TABLE payments ADD CONSTRAINT payments_amount_positive CHECK (amount > 0);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'payments'::regclass AND conname = 'payments_status_valid'
    ) THEN
        ALTER TABLE payments ADD CONSTRAINT payments_status_valid
            CHECK (status IN ('pending', 'paid', 'expired', 'failed', 'cancelled'));
    END IF;
END $$;

-- Two payments must never share a provider reference: references are unique
-- per provider. Today the lookup (GetByProviderReference) is by
-- provider_reference alone, which is safe because the reference formats of
-- the two adapters cannot collide.
CREATE UNIQUE INDEX IF NOT EXISTS uq_payments_provider_reference
    ON payments (provider_name, provider_reference)
    WHERE provider_reference IS NOT NULL;

-- Deleting a merchant must not silently delete its payment history.
-- confdeltype: 'c' = CASCADE, 'r' = RESTRICT. Other tables (API keys, users,
-- provider configs, payment links, idempotency keys) keep ON DELETE CASCADE.
DO $$
DECLARE
    fk_name text;
BEGIN
    SELECT c.conname INTO fk_name
    FROM pg_constraint c
    WHERE c.conrelid = 'payments'::regclass
      AND c.confrelid = 'merchants'::regclass
      AND c.contype = 'f'
      AND c.confdeltype <> 'r';

    IF fk_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE payments DROP CONSTRAINT %I', fk_name);
        ALTER TABLE payments ADD CONSTRAINT payments_merchant_id_fkey
            FOREIGN KEY (merchant_id) REFERENCES merchants(id) ON DELETE RESTRICT;
    END IF;
END $$;

COMMENT ON CONSTRAINT payments_amount_positive ON payments IS 'Payments are always for a positive amount';
COMMENT ON CONSTRAINT payments_status_valid ON payments IS 'Must match the status constants in internal/domain/payment';

COMMIT;
