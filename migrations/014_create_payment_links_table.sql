-- Migration: Create payment_links table
-- Reusable, multi-use payment link templates. A link itself is never a
-- payment — it's a config that spawns a new one-time Payment (with its own
-- freshly-generated QRIS, valid ~10 minutes) each time a customer checks out
-- through it. Creating a link is DB-only; no provider/Cashi call happens
-- until a customer actually initiates checkout (see PaymentLinkService).

CREATE TABLE IF NOT EXISTS payment_links (
    id UUID PRIMARY KEY,
    merchant_id UUID NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    slug VARCHAR(64) NOT NULL UNIQUE,
    title VARCHAR(255) NOT NULL,
    description TEXT,
    amount_type VARCHAR(10) NOT NULL,
    amount BIGINT,
    currency VARCHAR(10) NOT NULL DEFAULT 'IDR',
    min_amount BIGINT,
    max_amount BIGINT,
    is_active BOOLEAN NOT NULL DEFAULT true,
    expires_at TIMESTAMPTZ,
    environment VARCHAR(20) NOT NULL DEFAULT 'production',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_payment_links_amount_type CHECK (amount_type IN ('fixed', 'open')),
    CONSTRAINT chk_payment_links_amount_matches_type CHECK (
        (amount_type = 'fixed' AND amount IS NOT NULL AND amount > 0)
        OR (amount_type = 'open' AND amount IS NULL)
    ),
    CONSTRAINT chk_payment_links_min_max CHECK (
        min_amount IS NULL OR max_amount IS NULL OR min_amount <= max_amount
    )
);

CREATE INDEX IF NOT EXISTS idx_payment_links_merchant_id ON payment_links(merchant_id);
CREATE INDEX IF NOT EXISTS idx_payment_links_merchant_active ON payment_links(merchant_id, is_active);
CREATE INDEX IF NOT EXISTS idx_payment_links_created_at ON payment_links(created_at DESC);

COMMENT ON TABLE payment_links IS 'Reusable payment link templates; each checkout spawns a new one-time payment in the payments table';
COMMENT ON COLUMN payment_links.slug IS 'Public URL segment, e.g. frontend /l/{slug}; server-generated, not user-chosen';
COMMENT ON COLUMN payment_links.amount_type IS 'fixed: amount is set by merchant at creation; open: customer enters amount at checkout, bounded by min_amount/max_amount (and platform-wide Cashi limits: 2,000-10,000,000 IDR)';
COMMENT ON COLUMN payment_links.amount IS 'Required and >0 when amount_type=fixed; must be NULL when amount_type=open (see chk_payment_links_amount_matches_type)';
COMMENT ON COLUMN payment_links.min_amount IS 'Optional per-link floor for open-amount checkout; NULL falls back to platform default (2,000 IDR, Cashi minimum)';
COMMENT ON COLUMN payment_links.max_amount IS 'Optional per-link ceiling for open-amount checkout; NULL falls back to platform default (10,000,000 IDR, Cashi maximum)';
COMMENT ON COLUMN payment_links.environment IS 'sandbox|production — which environment payments spawned from this link are created in';
