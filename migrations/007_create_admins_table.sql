-- Migration: Create admins table
-- Description: Stores platform admin accounts for admin panel authentication

CREATE TABLE IF NOT EXISTS admins (
    id UUID PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    email VARCHAR(255) NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT true,
    last_login_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_admins_email ON admins(email);
CREATE INDEX IF NOT EXISTS idx_admins_is_active ON admins(is_active);

COMMENT ON TABLE admins IS 'Platform administrator accounts';
COMMENT ON COLUMN admins.email IS 'Admin login email (unique)';
COMMENT ON COLUMN admins.password_hash IS 'bcrypt password hash';
COMMENT ON COLUMN admins.is_active IS 'Whether admin account can sign in';
COMMENT ON COLUMN admins.last_login_at IS 'Timestamp of last successful login';
