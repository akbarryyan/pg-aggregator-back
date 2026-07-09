-- Migration: Add routing policy columns to merchant provider configs
-- Description: Adds weight and failover policy for merchant provider routing

ALTER TABLE merchant_provider_configs
    ADD COLUMN IF NOT EXISTS weight INTEGER NOT NULL DEFAULT 100,
    ADD COLUMN IF NOT EXISTS failover_enabled BOOLEAN NOT NULL DEFAULT true;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'chk_merchant_provider_weight'
    ) THEN
        ALTER TABLE merchant_provider_configs
            ADD CONSTRAINT chk_merchant_provider_weight CHECK (weight > 0);
    END IF;
END $$;
