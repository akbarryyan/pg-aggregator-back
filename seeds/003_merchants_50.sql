-- Seed: 50 demo merchants for admin list/pagination testing
-- Safe to re-run (upsert by email)

INSERT INTO merchants (
    id, name, email, phone, business_name, webhook_url, is_active, created_at, updated_at
) VALUES
(
    '33333333-3333-3333-3333-000000000001',
    'Andi Pratama',
    'merchant01@pg-aggregator.local',
    '081230000001',
    'Kopi Nusantara',
    'https://merchant-01.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '49 days',
    CURRENT_TIMESTAMP - INTERVAL '49 days'
),
(
    '33333333-3333-3333-3333-000000000002',
    'Budi Santoso',
    'merchant02@pg-aggregator.local',
    '081230000002',
    'Warung Sederhana',
    'https://merchant-02.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '48 days',
    CURRENT_TIMESTAMP - INTERVAL '48 days'
),
(
    '33333333-3333-3333-3333-000000000003',
    'Citra Lestari',
    'merchant03@pg-aggregator.local',
    '081230000003',
    'Fashion Citra',
    'https://merchant-03.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '47 days',
    CURRENT_TIMESTAMP - INTERVAL '47 days'
),
(
    '33333333-3333-3333-3333-000000000004',
    'Dewi Anggraini',
    'merchant04@pg-aggregator.local',
    '081230000004',
    'Toko Elektronik DA',
    'https://merchant-04.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '46 days',
    CURRENT_TIMESTAMP - INTERVAL '46 days'
),
(
    '33333333-3333-3333-3333-000000000005',
    'Eko Wijaya',
    'merchant05@pg-aggregator.local',
    '081230000005',
    'Bengkel Motor Eko',
    'https://merchant-05.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '45 days',
    CURRENT_TIMESTAMP - INTERVAL '45 days'
),
(
    '33333333-3333-3333-3333-000000000006',
    'Fajar Nugroho',
    'merchant06@pg-aggregator.local',
    '081230000006',
    'Fajar Fresh Mart',
    'https://merchant-06.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '44 days',
    CURRENT_TIMESTAMP - INTERVAL '44 days'
),
(
    '33333333-3333-3333-3333-000000000007',
    'Gita Maharani',
    'merchant07@pg-aggregator.local',
    '081230000007',
    'Salon Gita Beauty',
    'https://merchant-07.local/webhooks/payments',
    false,
    CURRENT_TIMESTAMP - INTERVAL '43 days',
    CURRENT_TIMESTAMP - INTERVAL '43 days'
),
(
    '33333333-3333-3333-3333-000000000008',
    'Hendra Gunawan',
    'merchant08@pg-aggregator.local',
    '081230000008',
    'Hendra Digital Store',
    'https://merchant-08.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '42 days',
    CURRENT_TIMESTAMP - INTERVAL '42 days'
),
(
    '33333333-3333-3333-3333-000000000009',
    'Indah Permata',
    'merchant09@pg-aggregator.local',
    '081230000009',
    'Boutique Indah',
    'https://merchant-09.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '41 days',
    CURRENT_TIMESTAMP - INTERVAL '41 days'
),
(
    '33333333-3333-3333-3333-000000000010',
    'Joko Susilo',
    'merchant10@pg-aggregator.local',
    '081230000010',
    'Restoran Jaya Rasa',
    'https://merchant-10.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '40 days',
    CURRENT_TIMESTAMP - INTERVAL '40 days'
),
(
    '33333333-3333-3333-3333-000000000011',
    'Kartika Sari',
    'merchant11@pg-aggregator.local',
    '081230000011',
    'Kartika Pharmacy',
    'https://merchant-11.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '39 days',
    CURRENT_TIMESTAMP - INTERVAL '39 days'
),
(
    '33333333-3333-3333-3333-000000000012',
    'Lukman Hakim',
    'merchant12@pg-aggregator.local',
    '081230000012',
    'Lukman Auto Parts',
    'https://merchant-12.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '38 days',
    CURRENT_TIMESTAMP - INTERVAL '38 days'
),
(
    '33333333-3333-3333-3333-000000000013',
    'Maya Putri',
    'merchant13@pg-aggregator.local',
    '081230000013',
    'Maya Cake House',
    'https://merchant-13.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '37 days',
    CURRENT_TIMESTAMP - INTERVAL '37 days'
),
(
    '33333333-3333-3333-3333-000000000014',
    'Nanda Firmansyah',
    'merchant14@pg-aggregator.local',
    '081230000014',
    'NF Gadget Center',
    'https://merchant-14.local/webhooks/payments',
    false,
    CURRENT_TIMESTAMP - INTERVAL '36 days',
    CURRENT_TIMESTAMP - INTERVAL '36 days'
),
(
    '33333333-3333-3333-3333-000000000015',
    'Oki Setiawan',
    'merchant15@pg-aggregator.local',
    '081230000015',
    'Oki Sport Shop',
    'https://merchant-15.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '35 days',
    CURRENT_TIMESTAMP - INTERVAL '35 days'
),
(
    '33333333-3333-3333-3333-000000000016',
    'Putri Ayu',
    'merchant16@pg-aggregator.local',
    '081230000016',
    'Putri Laundry Express',
    'https://merchant-16.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '34 days',
    CURRENT_TIMESTAMP - INTERVAL '34 days'
),
(
    '33333333-3333-3333-3333-000000000017',
    'Qori Ananda',
    'merchant17@pg-aggregator.local',
    '081230000017',
    'QA Print Solutions',
    'https://merchant-17.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '33 days',
    CURRENT_TIMESTAMP - INTERVAL '33 days'
),
(
    '33333333-3333-3333-3333-000000000018',
    'Rizky Maulana',
    'merchant18@pg-aggregator.local',
    '081230000018',
    'Rizky Computer',
    'https://merchant-18.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '32 days',
    CURRENT_TIMESTAMP - INTERVAL '32 days'
),
(
    '33333333-3333-3333-3333-000000000019',
    'Sinta Dewi',
    'merchant19@pg-aggregator.local',
    '081230000019',
    'Sinta Homemade',
    'https://merchant-19.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '31 days',
    CURRENT_TIMESTAMP - INTERVAL '31 days'
),
(
    '33333333-3333-3333-3333-000000000020',
    'Taufik Hidayat',
    'merchant20@pg-aggregator.local',
    '081230000020',
    'TH Outdoor Gear',
    'https://merchant-20.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '30 days',
    CURRENT_TIMESTAMP - INTERVAL '30 days'
),
(
    '33333333-3333-3333-3333-000000000021',
    'Umi Kalsum',
    'merchant21@pg-aggregator.local',
    '081230000021',
    'Umi Hijab Collection',
    'https://merchant-21.local/webhooks/payments',
    false,
    CURRENT_TIMESTAMP - INTERVAL '29 days',
    CURRENT_TIMESTAMP - INTERVAL '29 days'
),
(
    '33333333-3333-3333-3333-000000000022',
    'Vino Saputra',
    'merchant22@pg-aggregator.local',
    '081230000022',
    'Vino Coffee Roastery',
    'https://merchant-22.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '28 days',
    CURRENT_TIMESTAMP - INTERVAL '28 days'
),
(
    '33333333-3333-3333-3333-000000000023',
    'Wulan Dari',
    'merchant23@pg-aggregator.local',
    '081230000023',
    'Wulan Florist',
    'https://merchant-23.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '27 days',
    CURRENT_TIMESTAMP - INTERVAL '27 days'
),
(
    '33333333-3333-3333-3333-000000000024',
    'Xander Pratama',
    'merchant24@pg-aggregator.local',
    '081230000024',
    'Xander Tech Hub',
    'https://merchant-24.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '26 days',
    CURRENT_TIMESTAMP - INTERVAL '26 days'
),
(
    '33333333-3333-3333-3333-000000000025',
    'Yuni Astuti',
    'merchant25@pg-aggregator.local',
    '081230000025',
    'Yuni Baby Shop',
    'https://merchant-25.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '25 days',
    CURRENT_TIMESTAMP - INTERVAL '25 days'
),
(
    '33333333-3333-3333-3333-000000000026',
    'Zaki Rahman',
    'merchant26@pg-aggregator.local',
    '081230000026',
    'Zaki Frozen Food',
    'https://merchant-26.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '24 days',
    CURRENT_TIMESTAMP - INTERVAL '24 days'
),
(
    '33333333-3333-3333-3333-000000000027',
    'Alya Rahma',
    'merchant27@pg-aggregator.local',
    '081230000027',
    'Alya Skincare',
    'https://merchant-27.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '23 days',
    CURRENT_TIMESTAMP - INTERVAL '23 days'
),
(
    '33333333-3333-3333-3333-000000000028',
    'Bayu Aditya',
    'merchant28@pg-aggregator.local',
    '081230000028',
    'Bayu Pet Care',
    'https://merchant-28.local/webhooks/payments',
    false,
    CURRENT_TIMESTAMP - INTERVAL '22 days',
    CURRENT_TIMESTAMP - INTERVAL '22 days'
),
(
    '33333333-3333-3333-3333-000000000029',
    'Cahya Ningrum',
    'merchant29@pg-aggregator.local',
    '081230000029',
    'Cahya Bookstore',
    'https://merchant-29.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '21 days',
    CURRENT_TIMESTAMP - INTERVAL '21 days'
),
(
    '33333333-3333-3333-3333-000000000030',
    'Dimas Arya',
    'merchant30@pg-aggregator.local',
    '081230000030',
    'Dimas Mini Market',
    'https://merchant-30.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '20 days',
    CURRENT_TIMESTAMP - INTERVAL '20 days'
),
(
    '33333333-3333-3333-3333-000000000031',
    'Elsa Fitriani',
    'merchant31@pg-aggregator.local',
    '081230000031',
    'Elsa Craft Studio',
    'https://merchant-31.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '19 days',
    CURRENT_TIMESTAMP - INTERVAL '19 days'
),
(
    '33333333-3333-3333-3333-000000000032',
    'Farhan Akbar',
    'merchant32@pg-aggregator.local',
    '081230000032',
    'Farhan Tools',
    'https://merchant-32.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '18 days',
    CURRENT_TIMESTAMP - INTERVAL '18 days'
),
(
    '33333333-3333-3333-3333-000000000033',
    'Gina Oktaviani',
    'merchant33@pg-aggregator.local',
    '081230000033',
    'Gina Kitchenware',
    'https://merchant-33.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '17 days',
    CURRENT_TIMESTAMP - INTERVAL '17 days'
),
(
    '33333333-3333-3333-3333-000000000034',
    'Haris Abdullah',
    'merchant34@pg-aggregator.local',
    '081230000034',
    'Haris Fresh Fruit',
    'https://merchant-34.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '16 days',
    CURRENT_TIMESTAMP - INTERVAL '16 days'
),
(
    '33333333-3333-3333-3333-000000000035',
    'Ika Nuraini',
    'merchant35@pg-aggregator.local',
    '081230000035',
    'Ika Snack Corner',
    'https://merchant-35.local/webhooks/payments',
    false,
    CURRENT_TIMESTAMP - INTERVAL '15 days',
    CURRENT_TIMESTAMP - INTERVAL '15 days'
),
(
    '33333333-3333-3333-3333-000000000036',
    'Julian Prasetyo',
    'merchant36@pg-aggregator.local',
    '081230000036',
    'Julian Watch Store',
    'https://merchant-36.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '14 days',
    CURRENT_TIMESTAMP - INTERVAL '14 days'
),
(
    '33333333-3333-3333-3333-000000000037',
    'Kiki Amelia',
    'merchant37@pg-aggregator.local',
    '081230000037',
    'Kiki Fashion Kids',
    'https://merchant-37.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '13 days',
    CURRENT_TIMESTAMP - INTERVAL '13 days'
),
(
    '33333333-3333-3333-3333-000000000038',
    'Laras Wati',
    'merchant38@pg-aggregator.local',
    '081230000038',
    'Laras Organic Market',
    'https://merchant-38.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '12 days',
    CURRENT_TIMESTAMP - INTERVAL '12 days'
),
(
    '33333333-3333-3333-3333-000000000039',
    'Miko Hartono',
    'merchant39@pg-aggregator.local',
    '081230000039',
    'Miko Game Store',
    'https://merchant-39.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '11 days',
    CURRENT_TIMESTAMP - INTERVAL '11 days'
),
(
    '33333333-3333-3333-3333-000000000040',
    'Nadia Safira',
    'merchant40@pg-aggregator.local',
    '081230000040',
    'Nadia Perfume',
    'https://merchant-40.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '10 days',
    CURRENT_TIMESTAMP - INTERVAL '10 days'
),
(
    '33333333-3333-3333-3333-000000000041',
    'Omar Syahputra',
    'merchant41@pg-aggregator.local',
    '081230000041',
    'Omar Bike Shop',
    'https://merchant-41.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '9 days',
    CURRENT_TIMESTAMP - INTERVAL '9 days'
),
(
    '33333333-3333-3333-3333-000000000042',
    'Prima Yudha',
    'merchant42@pg-aggregator.local',
    '081230000042',
    'Prima Office Supply',
    'https://merchant-42.local/webhooks/payments',
    false,
    CURRENT_TIMESTAMP - INTERVAL '8 days',
    CURRENT_TIMESTAMP - INTERVAL '8 days'
),
(
    '33333333-3333-3333-3333-000000000043',
    'Rani Melati',
    'merchant43@pg-aggregator.local',
    '081230000043',
    'Rani Cake & Bakery',
    'https://merchant-43.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '7 days',
    CURRENT_TIMESTAMP - INTERVAL '7 days'
),
(
    '33333333-3333-3333-3333-000000000044',
    'Satria Wibowo',
    'merchant44@pg-aggregator.local',
    '081230000044',
    'Satria Outdoor',
    'https://merchant-44.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '6 days',
    CURRENT_TIMESTAMP - INTERVAL '6 days'
),
(
    '33333333-3333-3333-3333-000000000045',
    'Tania Kusuma',
    'merchant45@pg-aggregator.local',
    '081230000045',
    'Tania Accessories',
    'https://merchant-45.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '5 days',
    CURRENT_TIMESTAMP - INTERVAL '5 days'
),
(
    '33333333-3333-3333-3333-000000000046',
    'Ujang Mulyadi',
    'merchant46@pg-aggregator.local',
    '081230000046',
    'Ujang Seafood',
    'https://merchant-46.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '4 days',
    CURRENT_TIMESTAMP - INTERVAL '4 days'
),
(
    '33333333-3333-3333-3333-000000000047',
    'Vera Angelia',
    'merchant47@pg-aggregator.local',
    '081230000047',
    'Vera Home Decor',
    'https://merchant-47.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '3 days',
    CURRENT_TIMESTAMP - INTERVAL '3 days'
),
(
    '33333333-3333-3333-3333-000000000048',
    'Wahyu Ramadhan',
    'merchant48@pg-aggregator.local',
    '081230000048',
    'Wahyu Motor Parts',
    'https://merchant-48.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '2 days',
    CURRENT_TIMESTAMP - INTERVAL '2 days'
),
(
    '33333333-3333-3333-3333-000000000049',
    'Yasmine Putri',
    'merchant49@pg-aggregator.local',
    '081230000049',
    'Yasmine Beauty Lab',
    'https://merchant-49.local/webhooks/payments',
    false,
    CURRENT_TIMESTAMP - INTERVAL '1 days',
    CURRENT_TIMESTAMP - INTERVAL '1 days'
),
(
    '33333333-3333-3333-3333-000000000050',
    'Zainal Abidin',
    'merchant50@pg-aggregator.local',
    '081230000050',
    'Zainal Agro Supply',
    'https://merchant-50.local/webhooks/payments',
    true,
    CURRENT_TIMESTAMP - INTERVAL '0 days',
    CURRENT_TIMESTAMP - INTERVAL '0 days'
)

ON CONFLICT (email) DO UPDATE SET
    name = EXCLUDED.name,
    phone = EXCLUDED.phone,
    business_name = EXCLUDED.business_name,
    webhook_url = EXCLUDED.webhook_url,
    is_active = EXCLUDED.is_active,
    updated_at = CURRENT_TIMESTAMP;

-- Optional provider config for first 10 seeded merchants (Cashi QRIS)
INSERT INTO merchant_provider_configs (
    id, merchant_id, provider_name, payment_method, priority, weight, failover_enabled, is_enabled, created_at, updated_at
) VALUES
(
    '44444444-4444-4444-4444-000000000001',
    '33333333-3333-3333-3333-000000000001',
    'cashi',
    'qris',
    1,
    100,
    true,
    true,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
),
(
    '44444444-4444-4444-4444-000000000002',
    '33333333-3333-3333-3333-000000000002',
    'cashi',
    'qris',
    1,
    100,
    true,
    true,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
),
(
    '44444444-4444-4444-4444-000000000003',
    '33333333-3333-3333-3333-000000000003',
    'cashi',
    'qris',
    1,
    100,
    true,
    true,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
),
(
    '44444444-4444-4444-4444-000000000004',
    '33333333-3333-3333-3333-000000000004',
    'cashi',
    'qris',
    1,
    100,
    true,
    true,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
),
(
    '44444444-4444-4444-4444-000000000005',
    '33333333-3333-3333-3333-000000000005',
    'cashi',
    'qris',
    1,
    100,
    true,
    true,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
),
(
    '44444444-4444-4444-4444-000000000006',
    '33333333-3333-3333-3333-000000000006',
    'cashi',
    'qris',
    1,
    100,
    true,
    true,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
),
(
    '44444444-4444-4444-4444-000000000007',
    '33333333-3333-3333-3333-000000000007',
    'cashi',
    'qris',
    1,
    100,
    true,
    true,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
),
(
    '44444444-4444-4444-4444-000000000008',
    '33333333-3333-3333-3333-000000000008',
    'cashi',
    'qris',
    1,
    100,
    true,
    true,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
),
(
    '44444444-4444-4444-4444-000000000009',
    '33333333-3333-3333-3333-000000000009',
    'cashi',
    'qris',
    1,
    100,
    true,
    true,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
),
(
    '44444444-4444-4444-4444-000000000010',
    '33333333-3333-3333-3333-000000000010',
    'cashi',
    'qris',
    1,
    100,
    true,
    true,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
)

ON CONFLICT (id) DO UPDATE SET
    merchant_id = EXCLUDED.merchant_id,
    provider_name = EXCLUDED.provider_name,
    payment_method = EXCLUDED.payment_method,
    priority = EXCLUDED.priority,
    weight = EXCLUDED.weight,
    failover_enabled = EXCLUDED.failover_enabled,
    is_enabled = EXCLUDED.is_enabled,
    updated_at = CURRENT_TIMESTAMP;
