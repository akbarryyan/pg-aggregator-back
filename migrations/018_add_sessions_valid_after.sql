-- Token revocation: a JWT whose iat is before sessions_valid_after is rejected.
-- NULL means no cutoff, so nothing is invalidated when this migration runs.
-- Set on password change and on logout.
ALTER TABLE admins ADD COLUMN IF NOT EXISTS sessions_valid_after TIMESTAMPTZ;
ALTER TABLE merchant_users ADD COLUMN IF NOT EXISTS sessions_valid_after TIMESTAMPTZ;

COMMENT ON COLUMN admins.sessions_valid_after IS 'JWTs issued before this instant are rejected (logout / password change)';
COMMENT ON COLUMN merchant_users.sessions_valid_after IS 'JWTs issued before this instant are rejected (logout / password change)';
