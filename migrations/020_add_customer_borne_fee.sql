-- Migration 020: customer-borne provider fee.
-- merchants.fee_bearer decides who pays the provider's percentage fee; the
-- default keeps today's behaviour for every existing merchant.
-- payments.fee_amount is the fee added on top of payments.amount (the
-- merchant's nominal); the customer pays amount + fee_amount. Existing and
-- in-flight payments get 0, so their total equals amount.
BEGIN;

ALTER TABLE merchants ADD COLUMN IF NOT EXISTS fee_bearer VARCHAR(20) NOT NULL DEFAULT 'merchant';
ALTER TABLE payments ADD COLUMN IF NOT EXISTS fee_amount BIGINT NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'merchants'::regclass AND conname = 'merchants_fee_bearer_valid'
    ) THEN
        ALTER TABLE merchants ADD CONSTRAINT merchants_fee_bearer_valid
            CHECK (fee_bearer IN ('merchant', 'customer'));
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'payments'::regclass AND conname = 'payments_fee_amount_non_negative'
    ) THEN
        ALTER TABLE payments ADD CONSTRAINT payments_fee_amount_non_negative
            CHECK (fee_amount >= 0);
    END IF;
END $$;

COMMENT ON COLUMN merchants.fee_bearer IS 'merchant: customer pays the nominal; customer: customer pays nominal + provider fee';
COMMENT ON COLUMN payments.fee_amount IS 'Provider fee added on top of amount; customer pays amount + fee_amount';

COMMIT;
