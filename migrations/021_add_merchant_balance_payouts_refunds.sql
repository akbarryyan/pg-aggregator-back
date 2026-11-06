-- Migration 021: merchant balance ledger, payouts, refunds.
-- One transaction; idempotent (safe to re-apply, including the backfill).
-- Hard-coded 70 below is the Cashi fee in basis points (internal/fee).
BEGIN;
-- Fail fast instead of queueing behind (and blocking) live traffic; a timeout
-- rolls the whole migration back and it can simply be retried.
SET LOCAL lock_timeout = '5s';

ALTER TABLE payments ADD COLUMN IF NOT EXISTS merchant_net_amount BIGINT NOT NULL DEFAULT 0;

-- 'refunded' joins the allowed statuses.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint
               WHERE conrelid = 'payments'::regclass AND conname = 'payments_status_valid') THEN
        ALTER TABLE payments DROP CONSTRAINT payments_status_valid;
    END IF;
    ALTER TABLE payments ADD CONSTRAINT payments_status_valid
        CHECK (status IN ('pending', 'paid', 'expired', 'failed', 'cancelled', 'refunded'));
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid = 'payments'::regclass AND conname = 'payments_merchant_net_non_negative') THEN
        ALTER TABLE payments ADD CONSTRAINT payments_merchant_net_non_negative CHECK (merchant_net_amount >= 0);
    END IF;
END $$;
COMMENT ON CONSTRAINT payments_status_valid ON payments IS
    'status is one of: pending, paid, expired, failed, cancelled, refunded (refunded is terminal; only paid -> refunded)';

ALTER TABLE merchants
    ADD COLUMN IF NOT EXISTS payout_bank_name VARCHAR(100),
    ADD COLUMN IF NOT EXISTS payout_account_number VARCHAR(50),
    ADD COLUMN IF NOT EXISTS payout_account_holder VARCHAR(150);

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid = 'merchants'::regclass AND conname = 'merchants_payout_account_all_or_none') THEN
        ALTER TABLE merchants ADD CONSTRAINT merchants_payout_account_all_or_none CHECK (
            (payout_bank_name IS NULL AND payout_account_number IS NULL AND payout_account_holder IS NULL)
            OR (payout_bank_name IS NOT NULL AND payout_account_number IS NOT NULL AND payout_account_holder IS NOT NULL));
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS payouts (
    id UUID PRIMARY KEY,
    merchant_id UUID NOT NULL REFERENCES merchants(id) ON DELETE RESTRICT,
    amount BIGINT NOT NULL CHECK (amount > 0),
    status VARCHAR(20) NOT NULL DEFAULT 'requested' CHECK (status IN ('requested', 'paid', 'rejected')),
    bank_name VARCHAR(100) NOT NULL,
    account_number VARCHAR(50) NOT NULL,
    account_holder VARCHAR(150) NOT NULL,
    requested_by UUID,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_by UUID,
    processed_at TIMESTAMPTZ,
    reference VARCHAR(200),
    reject_reason VARCHAR(500),
    CONSTRAINT payouts_processing_consistent CHECK (
        (status = 'requested' AND processed_at IS NULL AND reference IS NULL AND reject_reason IS NULL)
        OR (status = 'paid' AND processed_at IS NOT NULL AND reference IS NOT NULL AND reject_reason IS NULL)
        OR (status = 'rejected' AND processed_at IS NOT NULL AND reject_reason IS NOT NULL AND reference IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_payouts_one_open_per_merchant ON payouts (merchant_id) WHERE status = 'requested';
CREATE INDEX IF NOT EXISTS idx_payouts_status_requested_at ON payouts (status, requested_at);
CREATE INDEX IF NOT EXISTS idx_payouts_merchant ON payouts (merchant_id, requested_at DESC);

CREATE TABLE IF NOT EXISTS payment_refunds (
    id UUID PRIMARY KEY,
    payment_id UUID NOT NULL REFERENCES payments(id) ON DELETE RESTRICT,
    merchant_id UUID NOT NULL REFERENCES merchants(id) ON DELETE RESTRICT,
    amount BIGINT NOT NULL CHECK (amount > 0),
    reason VARCHAR(500) NOT NULL CHECK (length(btrim(reason)) > 0),
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_payment_refunds_payment ON payment_refunds (payment_id);

CREATE TABLE IF NOT EXISTS merchant_ledger (
    id UUID PRIMARY KEY,
    merchant_id UUID NOT NULL REFERENCES merchants(id) ON DELETE RESTRICT,
    entry_type VARCHAR(20) NOT NULL CHECK (entry_type IN
        ('payment_credit', 'refund_debit', 'payout_debit', 'payout_reversal', 'adjustment')),
    amount BIGINT NOT NULL CHECK (amount <> 0),
    payment_id UUID REFERENCES payments(id) ON DELETE RESTRICT,
    payout_id UUID REFERENCES payouts(id) ON DELETE RESTRICT,
    refund_id UUID REFERENCES payment_refunds(id) ON DELETE RESTRICT,
    note VARCHAR(500) NOT NULL DEFAULT '',
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT merchant_ledger_sign_matches_type CHECK (
        (entry_type IN ('payment_credit', 'payout_reversal') AND amount > 0)
        OR (entry_type IN ('refund_debit', 'payout_debit') AND amount < 0)
        OR entry_type = 'adjustment'),
    CONSTRAINT merchant_ledger_reference_matches_type CHECK (
        (entry_type = 'payment_credit' AND payment_id IS NOT NULL)
        OR (entry_type = 'refund_debit' AND payment_id IS NOT NULL AND refund_id IS NOT NULL)
        OR (entry_type IN ('payout_debit', 'payout_reversal') AND payout_id IS NOT NULL)
        OR entry_type = 'adjustment')
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_ledger_credit_per_payment ON merchant_ledger (payment_id) WHERE entry_type = 'payment_credit';
CREATE UNIQUE INDEX IF NOT EXISTS uq_ledger_debit_per_payout ON merchant_ledger (payout_id) WHERE entry_type = 'payout_debit';
CREATE UNIQUE INDEX IF NOT EXISTS uq_ledger_reversal_per_payout ON merchant_ledger (payout_id) WHERE entry_type = 'payout_reversal';
CREATE UNIQUE INDEX IF NOT EXISTS uq_ledger_debit_per_refund ON merchant_ledger (refund_id) WHERE entry_type = 'refund_debit';
CREATE INDEX IF NOT EXISTS idx_ledger_merchant_created ON merchant_ledger (merchant_id, created_at DESC, id DESC);

CREATE OR REPLACE FUNCTION merchant_ledger_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'merchant_ledger is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS merchant_ledger_no_update ON merchant_ledger;
CREATE TRIGGER merchant_ledger_no_update BEFORE UPDATE OR DELETE ON merchant_ledger
    FOR EACH ROW EXECUTE FUNCTION merchant_ledger_append_only();

-- TRUNCATE bypasses row triggers, so it gets its own statement-level one.
DROP TRIGGER IF EXISTS merchant_ledger_no_truncate ON merchant_ledger;
CREATE TRIGGER merchant_ledger_no_truncate BEFORE TRUNCATE ON merchant_ledger
    FOR EACH STATEMENT EXECUTE FUNCTION merchant_ledger_append_only();

-- BACKFILL BEGIN
-- Backfill: production payments already paid. fee_amount > 0 means the
-- customer paid the fee on top (net = amount); otherwise the merchant bears
-- it (net = amount - ceil(amount*70/10000)). Re-runnable: only rows whose net
-- is still 0 are updated and the unique index absorbs repeated credits.
UPDATE payments
   SET merchant_net_amount = CASE WHEN fee_amount > 0 THEN amount
                                  ELSE amount - ((amount * 70 + 9999) / 10000) END
 WHERE status = 'paid' AND environment = 'production' AND merchant_net_amount = 0;

INSERT INTO merchant_ledger (id, merchant_id, entry_type, amount, payment_id, note)
SELECT gen_random_uuid(), p.merchant_id, 'payment_credit', p.merchant_net_amount, p.id, 'backfill'
  FROM payments p
 WHERE p.status = 'paid' AND p.environment = 'production' AND p.merchant_net_amount > 0
ON CONFLICT (payment_id) WHERE entry_type = 'payment_credit' DO NOTHING;
-- BACKFILL END

COMMIT;
