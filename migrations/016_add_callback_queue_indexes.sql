-- Partial indexes for the async merchant-callback queue (the worker polls every
-- 5 seconds). Both stay tiny because finished rows (success, skipped, and
-- failed rows with no retry scheduled) are excluded.
-- Plain CREATE INDEX (not CONCURRENTLY): the table is small and this keeps the
-- file runnable inside psql -f as a single script.

-- RequeueDueFailed: failed deliveries whose retry time has come.
CREATE INDEX IF NOT EXISTS idx_merchant_callback_deliveries_retry_due
    ON merchant_callback_deliveries (next_retry_at)
    WHERE status = 'failed' AND next_retry_at IS NOT NULL;

-- ClaimDue: rows waiting to be sent, plus 'sending' rows that may be stale.
CREATE INDEX IF NOT EXISTS idx_merchant_callback_deliveries_queue
    ON merchant_callback_deliveries (created_at)
    WHERE status IN ('pending', 'sending');

COMMENT ON COLUMN merchant_callback_deliveries.status IS 'pending | sending | success | failed | skipped';
