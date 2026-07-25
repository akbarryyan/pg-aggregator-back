-- Migration: Convert naive TIMESTAMP columns to TIMESTAMPTZ
-- Bug: all timestamp columns were "timestamp without time zone". The API
-- server runs with system timezone Asia/Jakarta (UTC+7), so time.Now()
-- writes local wall-clock digits (e.g. 22:09) with no offset attached.
-- On read, the Go driver has no offset to go on and labels the value UTC,
-- so JSON responses carry a "Z" suffix on what is actually WIB time. The
-- frontend then correctly converts that (mislabeled) UTC instant to the
-- browser's local timezone, adding a second +07:00 shift on top of the
-- first — e.g. a payment created 24 Jul 22:09 WIB displayed as
-- 25 Jul 05:09. TIMESTAMPTZ removes the ambiguity: Postgres stores the
-- absolute instant, so every existing naive value is reinterpreted here
-- as Asia/Jakarta wall-clock time (matching how it was actually written).

BEGIN;

ALTER TABLE merchants
    ALTER COLUMN created_at TYPE TIMESTAMPTZ USING created_at AT TIME ZONE 'Asia/Jakarta',
    ALTER COLUMN updated_at TYPE TIMESTAMPTZ USING updated_at AT TIME ZONE 'Asia/Jakarta';

ALTER TABLE payments
    ALTER COLUMN expires_at TYPE TIMESTAMPTZ USING expires_at AT TIME ZONE 'Asia/Jakarta',
    ALTER COLUMN paid_at    TYPE TIMESTAMPTZ USING paid_at    AT TIME ZONE 'Asia/Jakarta',
    ALTER COLUMN created_at TYPE TIMESTAMPTZ USING created_at AT TIME ZONE 'Asia/Jakarta',
    ALTER COLUMN updated_at TYPE TIMESTAMPTZ USING updated_at AT TIME ZONE 'Asia/Jakarta';

ALTER TABLE merchant_provider_configs
    ALTER COLUMN created_at TYPE TIMESTAMPTZ USING created_at AT TIME ZONE 'Asia/Jakarta',
    ALTER COLUMN updated_at TYPE TIMESTAMPTZ USING updated_at AT TIME ZONE 'Asia/Jakarta';

ALTER TABLE webhook_events
    ALTER COLUMN processed_at TYPE TIMESTAMPTZ USING processed_at AT TIME ZONE 'Asia/Jakarta',
    ALTER COLUMN created_at   TYPE TIMESTAMPTZ USING created_at   AT TIME ZONE 'Asia/Jakarta';

ALTER TABLE admins
    ALTER COLUMN last_login_at TYPE TIMESTAMPTZ USING last_login_at AT TIME ZONE 'Asia/Jakarta',
    ALTER COLUMN created_at    TYPE TIMESTAMPTZ USING created_at    AT TIME ZONE 'Asia/Jakarta',
    ALTER COLUMN updated_at    TYPE TIMESTAMPTZ USING updated_at    AT TIME ZONE 'Asia/Jakarta';

ALTER TABLE merchant_callback_deliveries
    ALTER COLUMN delivered_at  TYPE TIMESTAMPTZ USING delivered_at  AT TIME ZONE 'Asia/Jakarta',
    ALTER COLUMN next_retry_at TYPE TIMESTAMPTZ USING next_retry_at AT TIME ZONE 'Asia/Jakarta',
    ALTER COLUMN created_at    TYPE TIMESTAMPTZ USING created_at    AT TIME ZONE 'Asia/Jakarta',
    ALTER COLUMN updated_at    TYPE TIMESTAMPTZ USING updated_at    AT TIME ZONE 'Asia/Jakarta';

ALTER TABLE merchant_api_keys
    ALTER COLUMN last_used_at TYPE TIMESTAMPTZ USING last_used_at AT TIME ZONE 'Asia/Jakarta',
    ALTER COLUMN revoked_at   TYPE TIMESTAMPTZ USING revoked_at   AT TIME ZONE 'Asia/Jakarta',
    ALTER COLUMN created_at   TYPE TIMESTAMPTZ USING created_at   AT TIME ZONE 'Asia/Jakarta',
    ALTER COLUMN updated_at   TYPE TIMESTAMPTZ USING updated_at   AT TIME ZONE 'Asia/Jakarta';

ALTER TABLE merchant_users
    ALTER COLUMN last_login_at TYPE TIMESTAMPTZ USING last_login_at AT TIME ZONE 'Asia/Jakarta',
    ALTER COLUMN created_at    TYPE TIMESTAMPTZ USING created_at    AT TIME ZONE 'Asia/Jakarta',
    ALTER COLUMN updated_at    TYPE TIMESTAMPTZ USING updated_at    AT TIME ZONE 'Asia/Jakarta';

COMMIT;
