-- Seed: Initial merchant and provider routing data

INSERT INTO merchants (
    id, name, email, phone, business_name, webhook_url, is_active, created_at, updated_at
) VALUES (
    '11111111-1111-1111-1111-111111111111',
    'Demo Merchant Owner',
    'merchant.demo@pg-aggregator.local',
    '081234567890',
    'Demo Merchant Store',
    'https://merchant-demo.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
)
ON CONFLICT (email) DO UPDATE SET
    name = EXCLUDED.name,
    phone = EXCLUDED.phone,
    business_name = EXCLUDED.business_name,
    webhook_url = EXCLUDED.webhook_url,
    is_active = EXCLUDED.is_active,
    updated_at = CURRENT_TIMESTAMP;

INSERT INTO merchant_provider_configs (
    id, merchant_id, provider_name, payment_method, priority, weight, failover_enabled, is_enabled, created_at, updated_at
) VALUES (
    '22222222-2222-2222-2222-222222222221',
    '11111111-1111-1111-1111-111111111111',
    'klikqris',
    'qris',
    1,
    100,
    true,
    true,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
)
ON CONFLICT (merchant_id, payment_method, provider_name) DO UPDATE SET
    priority = EXCLUDED.priority,
    weight = EXCLUDED.weight,
    failover_enabled = EXCLUDED.failover_enabled,
    is_enabled = EXCLUDED.is_enabled,
    updated_at = CURRENT_TIMESTAMP;
