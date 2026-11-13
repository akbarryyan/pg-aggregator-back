-- Migration 023: activity log (audit trail of admin / merchant user actions).
-- Append-only from the application's side, no foreign keys so rows outlive
-- their actors. Idempotent, safe while the api is running.
BEGIN;

CREATE TABLE IF NOT EXISTS activity_logs (
    id           UUID PRIMARY KEY,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    actor_type   VARCHAR(20) NOT NULL CHECK (actor_type IN ('admin', 'merchant_user', 'anonymous')),
    actor_id     UUID,
    actor_email  VARCHAR(255) NOT NULL DEFAULT '',
    merchant_id  UUID,
    action       VARCHAR(100) NOT NULL,
    method       VARCHAR(10) NOT NULL,
    route        VARCHAR(255) NOT NULL,
    target_id    VARCHAR(100) NOT NULL DEFAULT '',
    status_code  INT NOT NULL,
    ip           VARCHAR(64) NOT NULL DEFAULT '',
    user_agent   VARCHAR(500) NOT NULL DEFAULT '',
    duration_ms  INT NOT NULL DEFAULT 0,
    details      JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS idx_activity_logs_created_at ON activity_logs (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_activity_logs_actor ON activity_logs (actor_type, actor_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_activity_logs_merchant ON activity_logs (merchant_id, created_at DESC) WHERE merchant_id IS NOT NULL;

COMMIT;
