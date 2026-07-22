-- Seed: Demo merchant dashboard user
-- Email: merchant.demo@pg-aggregator.local
-- Password: Merchant123!
-- Linked to demo merchant 11111111-1111-1111-1111-111111111111

INSERT INTO merchant_users (
    id, merchant_id, name, email, password_hash, role, is_active,
    last_login_at, created_at, updated_at
) VALUES (
    'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
    '11111111-1111-1111-1111-111111111111',
    'Demo Merchant Owner',
    'merchant.demo@pg-aggregator.local',
    '$2a$10$mi4QUGW7S9k28Wah83YggeuypkKZj6O3ZZjE7kYShRuujbbWnr5Zu',
    'owner',
    true,
    NULL,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    email = EXCLUDED.email,
    password_hash = EXCLUDED.password_hash,
    is_active = EXCLUDED.is_active,
    updated_at = CURRENT_TIMESTAMP;
