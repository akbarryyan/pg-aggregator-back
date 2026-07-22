-- Seed: Default platform admin
-- Email: admin@pg-aggregator.local
-- Password: Admin123!
-- password_hash generated with bcrypt DefaultCost

INSERT INTO admins (
    id, name, email, password_hash, is_active, last_login_at, created_at, updated_at
) VALUES (
    'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
    'Platform Admin',
    'admin@pg-aggregator.local',
    '$2a$10$2KeoyP7u4/U6KCLgo1OC5.5gfd4RRteGr1JX6i0X4dMluP1HC6ZDi',
    true,
    NULL,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
)
ON CONFLICT (email) DO UPDATE SET
    name = EXCLUDED.name,
    password_hash = EXCLUDED.password_hash,
    is_active = EXCLUDED.is_active,
    updated_at = CURRENT_TIMESTAMP;
